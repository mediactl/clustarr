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

package remediation

import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// staleWindow bounds the applied-status memo (§3.7).
const staleWindow = 30 * time.Second

// transientSteps is the per-key transient backoff: 30 s, 1 m, 2 m, then 5 m.
var transientSteps = []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute}

// keyState is one key's leader-local memory (§3.6, §3.7): its backoffs, the
// applied-status memo and the effect failures remembered for the next pass.
// controller-runtime never runs one key twice at once, so a keyState is
// touched by one pass at a time.
type keyState struct {
	transientN, conflictN int
	memoUID               types.UID
	memo                  *catalogv1alpha1.MediaFileStatus // normalised
	memoAt                time.Time
	effectFailures        map[PlannerName]outcome
}

type keyStates struct {
	mu sync.Mutex
	m  map[types.NamespacedName]*keyState
}

func (k *keyStates) get(nn types.NamespacedName) *keyState {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.m == nil {
		k.m = map[types.NamespacedName]*keyState{}
	}
	s, ok := k.m[nn]
	if !ok {
		s = &keyState{effectFailures: map[PlannerName]outcome{}}
		k.m[nn] = s
	}
	return s
}

func (k *keyStates) forget(nn types.NamespacedName) {
	k.mu.Lock()
	defer k.mu.Unlock()
	delete(k.m, nn)
}

// stale reports whether cached differs from the memo of this process's
// last landed apply, younger than staleWindow; a caught-up or expired memo
// is dropped.
func (s *keyState) stale(uid types.UID, cached *catalogv1alpha1.MediaFileStatus, now time.Time) bool {
	if s.memo == nil || s.memoUID != uid {
		return false
	}
	if now.Sub(s.memoAt) >= staleWindow || equality.Semantic.DeepEqual(normalize(cached), s.memo) {
		s.memo = nil
		return false
	}
	return true
}

func (s *keyState) remember(uid types.UID, applied *catalogv1alpha1.MediaFileStatus, now time.Time) {
	s.memoUID, s.memo, s.memoAt = uid, normalize(applied), now
}

// transientBackoff is 30 s, 1 m, 2 m, then 5 m.
func (s *keyState) transientBackoff() time.Duration {
	d := transientSteps[min(s.transientN, len(transientSteps)-1)]
	s.transientN++
	return d
}

// conflictBackoff is 1 s doubling to 30 s.
func (s *keyState) conflictBackoff() time.Duration {
	d := min(time.Second<<min(s.conflictN, 5), 30*time.Second)
	s.conflictN++
	return d
}

func (s *keyState) clean() { s.transientN, s.conflictN = 0, 0 }
