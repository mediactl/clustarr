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

package agent

import (
	"context"
	"testing"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/test/starttest"
)

// recordingManager records every runnable a domain adds.
type recordingManager struct {
	ctrl.Manager
	added []manager.Runnable
}

func (m *recordingManager) Add(r manager.Runnable) error {
	m.added = append(m.added, r)
	return m.Manager.Add(r)
}

// TestAgentsRegisterNoLeaderOnlyRunnable (§5.15): no domain adds a runnable
// that waits for a lease. An agent takes none, so controller-runtime's "not
// electing counts as elected" would run it on every replica -- which is why
// every leader-only runnable lives in the manager (R9).
func TestAgentsRegisterNoLeaderOnlyRunnable(t *testing.T) {
	env := starttest.StartEnv(t)
	createEngineClients(t, env.Config)
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(context.Background(), events.Default()))
	for _, d := range Domains() {
		t.Run(d, func(t *testing.T) {
			if d == "import" || d == "caption" {
				requireFFmpeg(t)
			}
			o := domainOptions(t, d)
			mgr, err := ctrl.NewManager(env.Config, managerOptions(o))
			require.NoError(t, err)
			rec := &recordingManager{Manager: mgr}
			reg, err := domains[d](context.Background(), rec, bus, o)
			require.NoError(t, err)
			if reg.Close != nil {
				t.Cleanup(func() { _ = reg.Close() })
			}
			assert.NotEmpty(t, rec.added, "%s adds no runnable: this test would prove nothing", d)
			for _, r := range rec.added {
				if le, ok := r.(manager.LeaderElectionRunnable); ok {
					assert.False(t, le.NeedLeaderElection(), "%s adds %T, which waits for a lease no agent takes (R9)", d, r)
				}
			}
		})
	}
}
