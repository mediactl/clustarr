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

package searchplan

import "sync"

// DonorBudget is leader-local: at most DonorSearchesPerSweep first-time
// live donor searches per namespace per wantedcron period (§7.3, P4). A
// donor -- an old DVD rip -- seldom comes by RSS, so an item's first donor
// search is live; a pool of them must never hit an indexer at once
// (CLAUDE.md, 2026-10-06). wantedcron refills it at each sweep; a new
// leader starts empty and waits for the next sweep, as the old WantedScan
// expansion did.
type DonorBudget struct {
	mu   sync.Mutex
	left map[string]int
}

// NewDonorBudget is an empty budget.
func NewDonorBudget() *DonorBudget { return &DonorBudget{left: map[string]int{}} }

// Refill sets namespace's budget to n.
func (b *DonorBudget) Refill(namespace string, n int) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.left[namespace] = n
}

// Room reports namespace's budget has a search left.
func (b *DonorBudget) Room(namespace string) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.left[namespace] > 0
}

// Take spends one of namespace's searches, false when none is left.
func (b *DonorBudget) Take(namespace string) bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.left[namespace] <= 0 {
		return false
	}
	b.left[namespace]--
	return true
}
