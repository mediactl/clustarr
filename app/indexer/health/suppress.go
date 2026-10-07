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

package health

import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"
)

// SuppressFor bounds the local suppression after a failure (ADR-0019 §7.5,
// P87): an indexer is skipped for at most this long, or until the manager's
// verdict names the failure, whichever is first.
const SuppressFor = 30 * time.Second

// Suppress is the agent's local suppression of indexers that just failed.
// The zero value is ready to use; a nil *Suppress suppresses nothing.
type Suppress struct {
	mu sync.Mutex
	// failed is each suppressed indexer's failure instant.
	failed map[types.UID]time.Time
}

// Fail suppresses uid from now for SuppressFor.
func (s *Suppress) Fail(uid types.UID, now time.Time) {
	if s == nil || uid == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed == nil {
		s.failed = map[types.UID]time.Time{}
	}
	s.failed[uid] = now
	// Bound the map by the live entries: an indexer that never queries
	// again would otherwise keep its entry for the process's life.
	for u, at := range s.failed {
		if !now.Before(at.Add(SuppressFor)) {
			delete(s.failed, u)
		}
	}
}

// Suppressed reports whether uid is still suppressed at now.
func (s *Suppress) Suppressed(uid types.UID, now time.Time) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.failed[uid]
	if !ok {
		return false
	}
	if !now.Before(at.Add(SuppressFor)) {
		delete(s.failed, uid)
		return false
	}
	return true
}

// Decided ends uid's suppression when the manager's verdict covers it:
// lastFailureAt is the Indexer's status.lastFailureAt, which the controller
// stamps from the record's failure time at the second (metav1.Time's
// precision), so a verdict at or after the suppressed failure's second has
// seen it. Whatever the verdict was -- a backoff window or none -- the cache's
// status.disabledUntil now decides, and local suppression has nothing left
// to bound.
func (s *Suppress) Decided(uid types.UID, lastFailureAt time.Time) {
	if s == nil || lastFailureAt.IsZero() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if at, ok := s.failed[uid]; ok && !at.Truncate(time.Second).After(lastFailureAt) {
		delete(s.failed, uid)
	}
}
