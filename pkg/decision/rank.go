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

package decision

import (
	"sort"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Rank stable-sorts a copy of ds best-first: QualityIndex asc, Revision
// desc (only if PreferRevision), FormatScore desc, PreferredProtocolMatch
// desc, EpisodeCount desc, indexer priority asc (from o.IndexerPriority),
// indexer-flag score desc, seeders/age desc, then size (Disagreement 7).
// Callers typically pass only the Approved decisions; ds with a zero
// RankKey (never Evaluated as Approved) sort by that zero value like any
// other -- Rank does not filter Rejected decisions itself.
func Rank(ds []Decision, o Options) []Decision {
	out := make([]Decision, len(ds))
	copy(out, ds)
	sort.SliceStable(out, func(i, j int) bool { return less(out[i], out[j], o) })
	return out
}

func less(a, b Decision, o Options) bool {
	if a.Rank.QualityIndex != b.Rank.QualityIndex {
		return a.Rank.QualityIndex < b.Rank.QualityIndex
	}
	if a.Rank.PreferRevision {
		if c := compareRevision(a.Rank.Revision, b.Rank.Revision); c != 0 {
			return c > 0 // higher revision (Real, then Version) wins
		}
	}
	if a.Rank.FormatScore != b.Rank.FormatScore {
		return a.Rank.FormatScore > b.Rank.FormatScore
	}
	return false // Step 13 appends the remaining keys here
}

// compareRevision orders by Real, then Version -- Revision.CompareTo,
// docs/research/quality.md §7.1.
func compareRevision(x, y common.Revision) int {
	if x.Real != y.Real {
		if x.Real > y.Real {
			return 1
		}
		return -1
	}
	if x.Version != y.Version {
		if x.Version > y.Version {
			return 1
		}
		return -1
	}
	return 0
}
