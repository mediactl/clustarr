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
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
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
func RankAndCap(ds []decision.Decision, o decision.Options, limit int) []commonv1.ReleaseDecision {
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

	out := make([]commonv1.ReleaseDecision, n)
	for i := range out {
		out[i] = commonv1.ReleaseDecision{
			ReleaseInfo:         ordered[i].Release,
			Approved:            ordered[i].Approved,
			TemporarilyRejected: ordered[i].TemporarilyRejected,
			Rejections:          ordered[i].Rejections,
			Rank:                int32(i + 1),
		}
	}
	return out
}

// RankScored is RankAndCap for a search record (ADR-0019 §7.4): the
// decisions best-first, approved ahead of rejected, at most limit, each
// with what the manager's grab planner re-checks it by -- its parsed
// coverage, the release index's block state from the answer (rels), its
// format score and its indexer's priority.
func RankScored(ds []decision.Decision, o decision.Options, rels []schema.Release, limit int) []schema.ScoredRelease {
	ranked := RankAndCap(ds, o, limit)
	byRelease := make(map[string]*decision.Decision, len(ds))
	for i := range ds {
		byRelease[releaseKey(ds[i].Release)] = &ds[i]
	}
	blocked := make(map[string]*schema.ReleaseBlock, len(rels))
	for i := range rels {
		if rels[i].Blocked != nil {
			blocked[releaseKey(rels[i].Info)] = rels[i].Blocked
		}
	}
	out := make([]schema.ScoredRelease, 0, len(ranked))
	for _, rd := range ranked {
		sr := schema.ScoredRelease{Decision: rd, Score: rd.FormatScore, IndexerPriority: priorityOf(o, rd.IndexerRef)}
		if d := byRelease[releaseKey(rd.ReleaseInfo)]; d != nil {
			sr.Score = int32(d.Score) //nolint:gosec // a format score fits
			sr.Parsed = parsedFacts(d)
		}
		if b := blocked[releaseKey(rd.ReleaseInfo)]; b != nil {
			cp := *b
			sr.Blocked = &cp
		}
		out = append(out, sr)
	}
	return out
}

// releaseKey identifies a release within one answer.
func releaseKey(r commonv1.ReleaseInfo) string {
	return r.IndexerRef + "\x00" + r.GUID + "\x00" + r.Title
}

// priorityOf is an indexer's priority as the ranking read it (25 when the
// task named none).
func priorityOf(o decision.Options, ref string) int32 {
	if p, ok := o.IndexerPriority[ref]; ok {
		return int32(p) //nolint:gosec // 1..50
	}
	return 25
}

// parsedFacts is what a decision's parsed title covers.
func parsedFacts(d *decision.Decision) schema.ParsedFacts {
	p := d.Parsed
	if p == nil {
		return schema.ParsedFacts{}
	}
	f := schema.ParsedFacts{
		Seasons: int32s(p.Seasons), Episodes: int32s(p.Episodes), Absolute: int32s(p.Absolute),
		FullSeason: p.FullSeason, MultiSeason: p.MultiSeason,
	}
	if p.AirDate != nil {
		f.AirDate = p.AirDate.UTC().Format(time.DateOnly)
	}
	if p.Comic != nil {
		f.Issue = p.Comic.Issue
	}
	return f
}

func int32s(ns []int) []int32 {
	if len(ns) == 0 {
		return nil
	}
	out := make([]int32, len(ns))
	for i, n := range ns {
		out[i] = int32(n) //nolint:gosec // season and episode numbers fit
	}
	return out
}
