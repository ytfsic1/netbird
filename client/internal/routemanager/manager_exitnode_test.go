package routemanager

import (
	"net/netip"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/netbirdio/netbird/client/internal/routeselector"
	"github.com/netbirdio/netbird/route"
)

func TestUpdateRouteSelectorFromManagement_ExitNodeSelectionUpdates(t *testing.T) {
	m := &DefaultManager{
		routeSelector: routeselector.NewRouteSelector(),
	}

	netID := route.NetID("exit-node-1")

	// Management initially doesn't auto-apply the exit node.
	r1 := &route.Route{
		ID:            "r1",
		NetID:         netID,
		Network:       netip.MustParsePrefix("0.0.0.0/0"),
		Peer:          "peer-1",
		SkipAutoApply: true,
	}
	clientRoutes := route.HAMap{
		r1.GetHAUniqueID(): {r1},
	}

	m.updateRouteSelectorFromManagement(clientRoutes)
	require.False(t, m.routeSelector.IsSelected(netID))
	require.False(t, m.routeSelector.HasUserSelectionForRoute(netID), "management selection must not be treated as user selection")

	// Management later enables auto-apply for the same exit node.
	r2 := r1.Copy()
	r2.SkipAutoApply = false
	clientRoutes2 := route.HAMap{
		r2.GetHAUniqueID(): {r2},
	}

	m.updateRouteSelectorFromManagement(clientRoutes2)
	require.True(t, m.routeSelector.IsSelected(netID), "selector should follow management updates when there is no user selection")
	require.False(t, m.routeSelector.HasUserSelectionForRoute(netID), "management selection must not be treated as user selection")
}

func TestUpdateRouteSelectorFromManagement_UserOverrideWins(t *testing.T) {
	m := &DefaultManager{
		routeSelector: routeselector.NewRouteSelector(),
	}

	netID := route.NetID("exit-node-1")

	// Management selects the exit node.
	r1 := &route.Route{
		ID:            "r1",
		NetID:         netID,
		Network:       netip.MustParsePrefix("0.0.0.0/0"),
		Peer:          "peer-1",
		SkipAutoApply: false,
	}
	clientRoutes := route.HAMap{
		r1.GetHAUniqueID(): {r1},
	}

	m.updateRouteSelectorFromManagement(clientRoutes)
	require.True(t, m.routeSelector.IsSelected(netID))
	require.False(t, m.routeSelector.HasUserSelectionForRoute(netID))

	// User explicitly deselects the route (e.g., via UI).
	err := m.routeSelector.DeselectRoutes([]route.NetID{netID}, []route.NetID{netID})
	require.NoError(t, err)
	require.False(t, m.routeSelector.IsSelected(netID))
	require.True(t, m.routeSelector.HasUserSelectionForRoute(netID))

	// Management still wants it selected; user selection should be respected.
	m.updateRouteSelectorFromManagement(clientRoutes)
	require.False(t, m.routeSelector.IsSelected(netID))
	require.True(t, m.routeSelector.HasUserSelectionForRoute(netID))
}
