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

package starttest

import (
	"slices"
	"sync"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// RecordingManager is a ctrl.Manager that records every runnable added to
// it, controllers included (the builder adds each through the manager it is
// given), before passing it on.
type RecordingManager struct {
	ctrl.Manager
	mu    sync.Mutex
	added []manager.Runnable
}

// Record wraps mgr.
func Record(mgr ctrl.Manager) *RecordingManager { return &RecordingManager{Manager: mgr} }

// Add records r and adds it to the wrapped manager.
func (m *RecordingManager) Add(r manager.Runnable) error {
	m.mu.Lock()
	m.added = append(m.added, r)
	m.mu.Unlock()
	return m.Manager.Add(r)
}

// Runnables is everything added so far.
func (m *RecordingManager) Runnables() []manager.Runnable {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.added)
}

// LeaderOnly is every recorded runnable controller-runtime starts only on
// the leader: those whose NeedLeaderElection is true, and those that do not
// declare it (runnable_group.go puts them behind the lease).
func (m *RecordingManager) LeaderOnly() []manager.Runnable {
	var out []manager.Runnable
	for _, r := range m.Runnables() {
		if le, ok := r.(manager.LeaderElectionRunnable); !ok || le.NeedLeaderElection() {
			out = append(out, r)
		}
	}
	return out
}
