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

package wantedcron

import (
	"sort"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// eligibleNamespaces returns, sorted, every namespace holding at least one
// candidate this sweep should wake the search workers for: one that is
// wanted (Candidate.Reason -- missing, or below its cutoff) and whose
// per-item backoff has elapsed, by the same Backoff/NextEligible the search
// worker applies (Candidate.Due). Waking a namespace whose every item the
// search worker would immediately skip costs a message, a consumer slot and
// a full List for nothing.
//
// It returns namespaces rather than items because §5's handoff is one
// WantedScan per namespace; see the package doc.
func eligibleNamespaces(cands []Candidate, now time.Time) []string {
	seen := map[string]struct{}{}
	for _, c := range cands {
		if _, ok := seen[c.Namespace]; ok {
			continue
		}
		if c.Due(now, true) {
			seen[c.Namespace] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for ns := range seen {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

// searchAttempts folds status.lastSearchedAt into status.searchAttempts.
//
// The two overlap: Attempts.Latest is the structured record and
// LastSearchedAt is the flat one, and a writer may reasonably set only the
// latter (the Search CR's interactive path, for one, has no attempt count to
// increment). Taking the later of the two means an item searched five minutes
// ago is never re-searched just because the write went to the other field.
func searchAttempts(a commonv1.Attempts, lastSearchedAt *metav1.Time) commonv1.Attempts {
	if lastSearchedAt == nil {
		return a
	}
	if a.Latest == nil || lastSearchedAt.After(a.Latest.Time) {
		a.Latest = lastSearchedAt
	}
	return a
}
