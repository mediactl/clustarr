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

package search

import (
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
)

// MaxResults is the hard cap on Search.status.results
// (+kubebuilder:validation:MaxItems=200 in api/catalog/v1alpha1/search_types.go).
// A patch carrying more entries than this is rejected by the apiserver, so
// RankAndCap, not the caller, is the last line of defence.
const MaxResults = 200

// RankAndCap orders ds best-first and projects it onto the API type, keeping
// at most limit entries (limit <= 0, or larger than MaxResults, means
// MaxResults) and stamping Rank as the 1-based position in the final list.
//
// The comparator is pkg/decision.Rank -- docs/research/quality.md §6.2's
// DownloadDecisionComparer, ported once in the decision engine. This function
// deliberately does not reimplement any part of it: Evaluate already filled
// each Decision's RankKey (quality tier, revision policy, format score,
// preferred-protocol match, pack size, size delta) from the profile and the
// target, and Rank adds indexer priority, indexer flags and seeders/age from
// Options and the release itself. Duplicating that chain here would silently
// diverge from the engine the automatic grab path uses.
//
// Approved releases sort ahead of every rejected one: a rejected Decision has
// a zero RankKey (Evaluate only fills it when Approved), so they are split
// out first and appended afterwards in arrival order rather than interleaved
// by a meaningless key. That ordering is what makes status.results readable
// in the UI -- grabbable releases first, then the ones explaining why the
// rest were not.
func RankAndCap(ds []decision.Decision, o decision.Options, limit int) []catalogv1alpha1.ReleaseDecision {
	approved := make([]decision.Decision, 0, len(ds))
	rejected := make([]decision.Decision, 0, len(ds))
	for _, d := range ds {
		if d.Approved {
			approved = append(approved, d)
			continue
		}
		rejected = append(rejected, d)
	}

	ordered := append(decision.Rank(approved, o), rejected...)

	n := limit
	if n <= 0 || n > MaxResults {
		n = MaxResults
	}
	if n > len(ordered) {
		n = len(ordered)
	}

	out := make([]catalogv1alpha1.ReleaseDecision, n)
	for i := range out {
		out[i] = catalogv1alpha1.ReleaseDecision{
			ReleaseInfo:         ordered[i].Release,
			Approved:            ordered[i].Approved,
			TemporarilyRejected: ordered[i].TemporarilyRejected,
			Rejections:          ordered[i].Rejections,
			Rank:                int32(i + 1),
		}
	}
	return out
}
