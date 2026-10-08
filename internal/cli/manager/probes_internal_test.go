/*
Copyright 2026 The Clustarr Authors.

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with this program.  If not, see <https://www.gnu.org/licenses/>.
*/

package manager

import (
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mediactl/clustarr/pkg/busconn"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/test/starttest"
)

// TestTheBusLivenessCheckIsRegistered (split §3.3 as amended 2026-10-07,
// S1): the manager's liveness set is exactly ping and bus, so a wedged bus
// -- handlers that ignore their context holding every slot past its budget
// -- restarts the pod; readiness is ping, jetstream and the cache sync.
func TestTheBusLivenessCheckIsRegistered(t *testing.T) {
	bus, nc, err := busconn.Connect(starttest.EmbeddedNATS(t), "manager")
	require.NoError(t, err)
	t.Cleanup(func() { _ = bus.Close(); nc.Close() })

	// A manager is built but never started: nothing here reaches the
	// apiserver.
	mgr, err := ctrl.NewManager(&rest.Config{Host: "https://127.0.0.1:1"}, ctrl.Options{
		Metrics: managerOptions(testOptions()).Metrics, HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)
	rec := newRecordingManager(mgr)
	ready, live, err := processChecks(rec, nc, bus)
	require.NoError(t, err)
	require.NoError(t, k8s.AddProbes(rec, ready, live))
	assert.Equal(t, []string{"ping", "bus"}, rec.healthz)
	assert.ElementsMatch(t, []string{"ping", "jetstream", "cache"}, rec.readyz)
	req := httptest.NewRequest("GET", "/healthz", nil)
	assert.NoError(t, live.Get("bus")(req), "a bus nothing has wedged is live")
	assert.NoError(t, ready.Get("jetstream")(req), "a connected bus is ready")
	assert.Error(t, ready.Get("cache")(req), "a manager that never started has not synced")

	// Without NATS there is no bus to wedge.
	_, live, err = processChecks(newRecordingManager(mgr), nil, nil)
	require.NoError(t, err)
	assert.Empty(t, live.Names())
}
