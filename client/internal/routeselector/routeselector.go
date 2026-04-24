package routeselector

import (
	"encoding/json"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/hashicorp/go-multierror"

	"github.com/netbirdio/netbird/client/errors"
	"github.com/netbirdio/netbird/route"
)

type RouteSelector struct {
	mu               sync.RWMutex
	deselectedRoutes map[route.NetID]struct{}
	selectedRoutes   map[route.NetID]struct{}
	// managedRoutes tracks routes whose selection state is controlled by the management server.
	// These entries must not be treated as user selections (see HasUserSelectionForRoute).
	managedRoutes map[route.NetID]struct{}
	deselectAll   bool
}

func NewRouteSelector() *RouteSelector {
	return &RouteSelector{
		deselectedRoutes: map[route.NetID]struct{}{},
		selectedRoutes:   map[route.NetID]struct{}{},
		managedRoutes:    map[route.NetID]struct{}{},
		deselectAll:      false,
	}
}

// SelectRoutes updates the selected routes based on the provided route IDs.
func (rs *RouteSelector) SelectRoutes(routes []route.NetID, appendRoute bool, allRoutes []route.NetID) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	if !appendRoute || rs.deselectAll {
		if rs.deselectedRoutes == nil {
			rs.deselectedRoutes = map[route.NetID]struct{}{}
		}
		if rs.selectedRoutes == nil {
			rs.selectedRoutes = map[route.NetID]struct{}{}
		}
		clear(rs.deselectedRoutes)
		clear(rs.selectedRoutes)
		if rs.managedRoutes == nil {
			rs.managedRoutes = map[route.NetID]struct{}{}
		}
		// This is an explicit (non-append) selection; treat all provided routes as user-controlled.
		for _, r := range allRoutes {
			delete(rs.managedRoutes, r)
		}
		for _, r := range allRoutes {
			rs.deselectedRoutes[r] = struct{}{}
		}
	}

	var err *multierror.Error
	for _, route := range routes {
		if !slices.Contains(allRoutes, route) {
			err = multierror.Append(err, fmt.Errorf("route '%s' is not available", route))
			continue
		}
		// Explicit user selection overrides management selection.
		delete(rs.managedRoutes, route)
		delete(rs.deselectedRoutes, route)
		rs.selectedRoutes[route] = struct{}{}
	}

	rs.deselectAll = false

	return errors.FormatErrorOrNil(err)
}

// SelectAllRoutes sets the selector to select all routes.
func (rs *RouteSelector) SelectAllRoutes() {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	rs.deselectAll = false
	if rs.deselectedRoutes == nil {
		rs.deselectedRoutes = map[route.NetID]struct{}{}
	}
	if rs.selectedRoutes == nil {
		rs.selectedRoutes = map[route.NetID]struct{}{}
	}
	clear(rs.deselectedRoutes)
	clear(rs.selectedRoutes)
	if rs.managedRoutes == nil {
		rs.managedRoutes = map[route.NetID]struct{}{}
	}
	// User intent overrides all managed state.
	clear(rs.managedRoutes)
}

// DeselectRoutes removes specific routes from the selection.
func (rs *RouteSelector) DeselectRoutes(routes []route.NetID, allRoutes []route.NetID) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	if rs.deselectAll {
		return nil
	}

	var err *multierror.Error
	for _, route := range routes {
		if !slices.Contains(allRoutes, route) {
			err = multierror.Append(err, fmt.Errorf("route '%s' is not available", route))
			continue
		}
		// Explicit user deselection overrides management selection.
		delete(rs.managedRoutes, route)
		rs.deselectedRoutes[route] = struct{}{}
		delete(rs.selectedRoutes, route)
	}

	return errors.FormatErrorOrNil(err)
}

// DeselectAllRoutes deselects all routes, effectively disabling route selection.
func (rs *RouteSelector) DeselectAllRoutes() {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	rs.deselectAll = true
	if rs.deselectedRoutes == nil {
		rs.deselectedRoutes = map[route.NetID]struct{}{}
	}
	if rs.selectedRoutes == nil {
		rs.selectedRoutes = map[route.NetID]struct{}{}
	}
	clear(rs.deselectedRoutes)
	clear(rs.selectedRoutes)
	if rs.managedRoutes == nil {
		rs.managedRoutes = map[route.NetID]struct{}{}
	}
	// User intent overrides all managed state.
	clear(rs.managedRoutes)
}

// IsSelected checks if a specific route is selected.
func (rs *RouteSelector) IsSelected(routeID route.NetID) bool {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	return rs.isSelectedLocked(routeID)
}

// FilterSelected removes unselected routes from the provided map.
func (rs *RouteSelector) FilterSelected(routes route.HAMap) route.HAMap {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	if rs.deselectAll {
		return route.HAMap{}
	}

	filtered := route.HAMap{}
	for id, rt := range routes {
		if !rs.isDeselectedLocked(id.NetID()) {
			filtered[id] = rt
		}
	}
	return filtered
}

// HasUserSelectionForRoute returns true if the user has explicitly selected or deselected this route.
// Intended for exit-node code paths: a v6 exit-node pair (e.g. "MyExit-v6") with no explicit state of
// its own inherits its v4 base's state, so legacy persisted selections that predate v6 pairing
// transparently apply to the synthesized v6 entry.
func (rs *RouteSelector) HasUserSelectionForRoute(routeID route.NetID) bool {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	return rs.hasUserSelectionForRouteLocked(rs.effectiveNetID(routeID))
}

func (rs *RouteSelector) FilterSelectedExitNodes(routes route.HAMap) route.HAMap {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	if rs.deselectAll {
		return route.HAMap{}
	}

	filtered := make(route.HAMap, len(routes))
	for id, rt := range routes {
		netID := id.NetID()
		if rs.isDeselectedLocked(netID) {
			continue
		}

		if !isExitNode(rt) {
			filtered[id] = rt
			continue
		}

		rs.applyExitNodeFilter(id, netID, rt, filtered)
	}

	return filtered
}

// effectiveNetID returns the v4 base for a "-v6" exit pair entry that has no explicit
// state of its own, so selections made on the v4 entry govern the v6 entry automatically.
// Only call this from exit-node-specific code paths: applying it to a non-exit "-v6" route
// would make it inherit unrelated v4 state. Must be called with rs.mu held.
func (rs *RouteSelector) effectiveNetID(id route.NetID) route.NetID {
	name := string(id)
	if !strings.HasSuffix(name, route.V6ExitSuffix) {
		return id
	}
	if _, ok := rs.selectedRoutes[id]; ok {
		return id
	}
	if _, ok := rs.deselectedRoutes[id]; ok {
		return id
	}
	return route.NetID(strings.TrimSuffix(name, route.V6ExitSuffix))
}

func (rs *RouteSelector) isSelectedLocked(routeID route.NetID) bool {
	if rs.deselectAll {
		return false
	}
	_, deselected := rs.deselectedRoutes[routeID]
	return !deselected
}

func (rs *RouteSelector) isDeselectedLocked(netID route.NetID) bool {
	if rs.deselectAll {
		return true
	}
	_, deselected := rs.deselectedRoutes[netID]
	return deselected
}

func (rs *RouteSelector) hasUserSelectionForRouteLocked(routeID route.NetID) bool {
	_, selected := rs.selectedRoutes[routeID]
	_, deselected := rs.deselectedRoutes[routeID]
	return selected || deselected
}

func isExitNode(rt []*route.Route) bool {
	return len(rt) > 0 && (route.IsV4DefaultRoute(rt[0].Network) || route.IsV6DefaultRoute(rt[0].Network))
}

func (rs *RouteSelector) applyExitNodeFilter(
	id route.HAUniqueID,
	netID route.NetID,
	rt []*route.Route,
	out route.HAMap,
) {
	// Exit-node path: apply the v4/v6 pair mirror so a deselect on the v4 base also
	// drops the synthesized v6 entry that lacks its own explicit state.
	effective := rs.effectiveNetID(netID)
	if rs.hasUserSelectionForRouteLocked(effective) {
		if rs.isSelectedLocked(effective) {
			out[id] = rt
		}
		return
	}

	// no explicit selection for this route: defer to management's SkipAutoApply flag
	sel := collectSelected(rt)
	if len(sel) > 0 {
		out[id] = sel
	}
}

func collectSelected(rt []*route.Route) []*route.Route {
	var sel []*route.Route
	for _, r := range rt {
		if !r.SkipAutoApply {
			sel = append(sel, r)
		}
	}
	return sel
}

// SetManagedRoutesSelection applies a selection state coming from the management server.
// Managed selection state must not be treated as a user selection. User operations (SelectRoutes/DeselectRoutes)
// always override management selections.
//
// The selector's "selected by default unless explicitly deselected" semantics are preserved by
// marking all managed routes as deselected first, then removing the desired managed selections.
func (rs *RouteSelector) SetManagedRoutesSelection(selected []route.NetID, allRoutes []route.NetID) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	// Respect explicit "deselect all" user intent.
	if rs.deselectAll {
		return nil
	}

	if rs.deselectedRoutes == nil {
		rs.deselectedRoutes = map[route.NetID]struct{}{}
	}
	if rs.selectedRoutes == nil {
		rs.selectedRoutes = map[route.NetID]struct{}{}
	}
	if rs.managedRoutes == nil {
		rs.managedRoutes = map[route.NetID]struct{}{}
	}

	// Prepare a quick lookup of selected IDs.
	selectedSet := map[route.NetID]struct{}{}
	for _, id := range selected {
		selectedSet[id] = struct{}{}
	}

	var err *multierror.Error
	for _, id := range selected {
		if !slices.Contains(allRoutes, id) {
			err = multierror.Append(err, fmt.Errorf("route '%s' is not available", id))
		}
	}

	for _, id := range allRoutes {
		// Explicitly compute "is user-controlled" under lock.
		_, wasManaged := rs.managedRoutes[id]
		_, isSelected := rs.selectedRoutes[id]
		_, isDeselected := rs.deselectedRoutes[id]
		userControlled := (isSelected || isDeselected) && !wasManaged
		if userControlled {
			continue
		}

		rs.managedRoutes[id] = struct{}{}

		// Default to deselected for managed routes, we'll un-deselect those that are selected.
		rs.deselectedRoutes[id] = struct{}{}
		delete(rs.selectedRoutes, id)

		if _, ok := selectedSet[id]; ok {
			delete(rs.deselectedRoutes, id)
			rs.selectedRoutes[id] = struct{}{}
		}
	}

	return errors.FormatErrorOrNil(err)
}

// MarshalJSON implements the json.Marshaler interface
func (rs *RouteSelector) MarshalJSON() ([]byte, error) {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	return json.Marshal(struct {
		SelectedRoutes   map[route.NetID]struct{} `json:"selected_routes"`
		DeselectedRoutes map[route.NetID]struct{} `json:"deselected_routes"`
		ManagedRoutes    map[route.NetID]struct{} `json:"managed_routes"`
		DeselectAll      bool                     `json:"deselect_all"`
	}{
		SelectedRoutes:   rs.selectedRoutes,
		DeselectedRoutes: rs.deselectedRoutes,
		ManagedRoutes:    rs.managedRoutes,
		DeselectAll:      rs.deselectAll,
	})
}

// UnmarshalJSON implements the json.Unmarshaler interface
// If the JSON is empty or null, it will initialize like a NewRouteSelector.
func (rs *RouteSelector) UnmarshalJSON(data []byte) error {
	rs.mu.Lock()
	defer rs.mu.Unlock()

	// Check for null or empty JSON
	if len(data) == 0 || string(data) == "null" {
		rs.deselectedRoutes = map[route.NetID]struct{}{}
		rs.selectedRoutes = map[route.NetID]struct{}{}
		rs.managedRoutes = map[route.NetID]struct{}{}
		rs.deselectAll = false
		return nil
	}

	var temp struct {
		SelectedRoutes   map[route.NetID]struct{} `json:"selected_routes"`
		DeselectedRoutes map[route.NetID]struct{} `json:"deselected_routes"`
		ManagedRoutes    map[route.NetID]struct{} `json:"managed_routes"`
		DeselectAll      bool                     `json:"deselect_all"`
	}

	if err := json.Unmarshal(data, &temp); err != nil {
		return err
	}

	rs.selectedRoutes = temp.SelectedRoutes
	rs.deselectedRoutes = temp.DeselectedRoutes
	rs.managedRoutes = temp.ManagedRoutes
	rs.deselectAll = temp.DeselectAll

	if rs.deselectedRoutes == nil {
		rs.deselectedRoutes = map[route.NetID]struct{}{}
	}
	if rs.selectedRoutes == nil {
		rs.selectedRoutes = map[route.NetID]struct{}{}
	}
	if rs.managedRoutes == nil {
		// Android migration: old selector state stored management-driven selection in selected/deselected maps
		// but had no way to differentiate it from user selections. On Android we treat that legacy state as managed
		// so that management updates can override it.
		//
		// This is safe for JetBird/SartoriNet because we don't expose exit node selection in the UI yet.
		rs.managedRoutes = map[route.NetID]struct{}{}
		if runtime.GOOS == "android" {
			for k := range rs.selectedRoutes {
				rs.managedRoutes[k] = struct{}{}
			}
			for k := range rs.deselectedRoutes {
				rs.managedRoutes[k] = struct{}{}
			}
		}
	}

	return nil
}
