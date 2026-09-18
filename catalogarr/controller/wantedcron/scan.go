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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// eligibleNamespaces returns, sorted, every namespace holding at least one
// item this sweep should wake the search workers for.
//
// An item counts when both hold:
//
//   - Its phase is Wanted or CutoffUnmet -- the two "something is still
//     missing" states. Every other phase is either already handled
//     (Downloading, Delayed, Imported), deliberately out of scope
//     (Unmonitored) or not yet actionable (Pending, Unavailable, Unaired).
//   - Its per-item backoff has elapsed, by the same Backoff/NextEligible the
//     search worker applies. Waking a namespace whose every item the search
//     worker would immediately skip costs a message, a consumer slot and a
//     full List for nothing.
//
// It returns namespaces rather than items because §5's handoff is one
// WantedScan per namespace; see the package doc.
func eligibleNamespaces(movies []catalogv1alpha1.Movie, episodes []catalogv1alpha1.Episode, now time.Time) []string {
	seen := map[string]struct{}{}
	for i := range movies {
		m := &movies[i]
		if _, ok := seen[m.Namespace]; ok {
			continue
		}
		if movieWanted(m.Status.Phase) && Eligible(searchAttempts(m.Status.SearchAttempts, m.Status.LastSearchedAt), now) {
			seen[m.Namespace] = struct{}{}
		}
	}
	for i := range episodes {
		ep := &episodes[i]
		if _, ok := seen[ep.Namespace]; ok {
			continue
		}
		if episodeWanted(ep.Status.Phase) && Eligible(searchAttempts(ep.Status.SearchAttempts, ep.Status.LastSearchedAt), now) {
			seen[ep.Namespace] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for ns := range seen {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

func movieWanted(p catalogv1alpha1.MoviePhase) bool {
	return p == catalogv1alpha1.MoviePhaseWanted || p == catalogv1alpha1.MoviePhaseCutoffUnmet
}

func episodeWanted(p catalogv1alpha1.EpisodePhase) bool {
	return p == catalogv1alpha1.EpisodePhaseWanted || p == catalogv1alpha1.EpisodePhaseCutoffUnmet
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
