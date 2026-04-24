package routeselector

import (
	"encoding/json"
	"fmt"
	"runtime"
	"slices"
	"sync"

	"github.com/hashicorp/go-multierror"
	"golang.org/x/exp/maps"

	"github.com/netbirdio/netbird/client/errors"
	"github.com/netbirdio/netbird/route"
)

const (
	exitNodeCIDR = "0.0.0.0/0"
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
		if rs.managedRoutes == nil {
			rs.managedRoutes = map[route.NetID]struct{}{}
		}
		maps.Clear(rs.deselectedRoutes)
		maps.Clear(rs.selectedRoutes)
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
	if rs.managedRoutes == nil {
		rs.managedRoutes = map[route.NetID]struct{}{}
	}
	maps.Clear(rs.deselectedRoutes)
	maps.Clear(rs.selectedRoutes)
	// User intent overrides all managed state.
	maps.Clear(rs.managedRoutes)
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
	if rs.managedRoutes == nil {
		rs.managedRoutes = map[route.NetID]struct{}{}
	}
	maps.Clear(rs.deselectedRoutes)
	maps.Clear(rs.selectedRoutes)
	// User intent overrides all managed state.
	maps.Clear(rs.managedRoutes)
}

// IsSelected checks if a specific route is selected.
func (rs *RouteSelector) IsSelected(routeID route.NetID) bool {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	if rs.deselectAll {
		return false
	}

	_, deselected := rs.deselectedRoutes[routeID]
	isSelected := !deselected
	return isSelected
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
		netID := id.NetID()
		_, deselected := rs.deselectedRoutes[netID]
		if !deselected {
			filtered[id] = rt
		}
	}
	return filtered
}

// HasUserSelectionForRoute returns true if the user has explicitly selected or deselected this specific route
func (rs *RouteSelector) HasUserSelectionForRoute(routeID route.NetID) bool {
	rs.mu.RLock()
	defer rs.mu.RUnlock()

	if rs.deselectAll {
		return true
	}

	if _, managed := rs.managedRoutes[routeID]; managed {
		return false
	}

	_, selected := rs.selectedRoutes[routeID]
	_, deselected := rs.deselectedRoutes[routeID]
	return selected || deselected
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
		if rs.isDeselected(netID) {
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

func (rs *RouteSelector) isDeselected(netID route.NetID) bool {
	_, deselected := rs.deselectedRoutes[netID]
	return deselected || rs.deselectAll
}

func isExitNode(rt []*route.Route) bool {
	return len(rt) > 0 && rt[0].Network.String() == exitNodeCIDR
}

func (rs *RouteSelector) applyExitNodeFilter(
	id route.HAUniqueID,
	netID route.NetID,
	rt []*route.Route,
	out route.HAMap,
) {

	if rs.hasUserSelections() {
		// user made explicit selects/deselects
		if rs.IsSelected(netID) {
			out[id] = rt
		}
		return
	}

	// no explicit selections: only include routes marked !SkipAutoApply (=AutoApply)
	sel := collectSelected(rt)
	if len(sel) > 0 {
		out[id] = sel
	}
}

func (rs *RouteSelector) hasUserSelections() bool {
	return len(rs.selectedRoutes) > 0 || len(rs.deselectedRoutes) > 0
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
