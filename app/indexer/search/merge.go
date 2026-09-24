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
	"slices"
	"sort"
	"strings"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// indexerResult is one indexer's contribution to the merge.
//
// Priority is Indexer.spec.priority (1..50, lower wins) and is the FIRST
// tiebreak, ahead of seeders: spec §6.2 keeps the best (priority, seeders).
type indexerResult struct {
	Name     string
	Priority int32
	Releases []schema.Release
}

// dedupeKey is exactly the two keys spec §6.2 names and no third. Infohash
// first, so the same torrent offered by three indexers collapses to one;
// then (indexer, guid), which is per-indexer by construction and therefore
// only catches an indexer that repeated itself inside one page.
//
// By design (gap-fix ruling R-2): a usenet release offered by two indexers
// has no infohash and two distinct guids, so it is NOT collapsed. A
// title+size key was considered and rejected -- a wrong dedupe silently loses
// releases, which is worse than a duplicate that pkg/decision ranks sanely
// downstream.
func dedupeKey(r schema.Release) string {
	if h := strings.ToLower(strings.TrimSpace(r.Info.InfoHash)); h != "" {
		return "hash:" + h
	}
	return "guid:" + r.Info.IndexerRef + "\x00" + r.Info.GUID
}

// mergeReleases collapses duplicates, orders the survivors and caps the set.
//
// The order decides WHICH releases survive the cap, not how they are ranked:
// catalogarr re-scores everything through pkg/decision. It is fully
// deterministic -- ties fall back to arrival sequence -- so the same inputs
// always truncate the same way.
//
// Each survivor carries spec §6.2's alsoOn provenance in Info.AlsoOn: the
// OTHER indexers that offered the same release, by object name, sorted and
// capped at commonv1.MaxAlsoOn. Info.IndexerRef is the source the merge kept
// and is never repeated there. Only an infohash collapse can name another
// indexer -- the (indexer, guid) key is per-indexer by construction -- so a
// usenet release, which dedupeKey never collapses across indexers, always
// has an empty AlsoOn.
func mergeReleases(results []indexerResult, limit int) ([]schema.Release, bool) {
	type entry struct {
		rel      schema.Release
		priority int32
		seeders  int32
		seq      int
		sources  []string // every indexer that offered this key
	}
	best := make(map[string]*entry)
	order := make([]string, 0, limit)
	seq := 0
	for _, res := range results {
		for _, r := range res.Releases {
			seq++
			k := dedupeKey(r)
			cur := &entry{rel: r, priority: res.Priority, seeders: seedersOf(r), seq: seq}
			prev, ok := best[k]
			if !ok {
				cur.sources = []string{r.Info.IndexerRef}
				best[k] = cur
				order = append(order, k)
				continue
			}
			sources := prev.sources
			if !slices.Contains(sources, r.Info.IndexerRef) {
				sources = append(sources, r.Info.IndexerRef)
			}
			prev.sources = sources
			if cur.priority < prev.priority ||
				(cur.priority == prev.priority && cur.seeders > prev.seeders) {
				// Keep the winner's slot in the original order: the survivor
				// changes, its position does not, so the merge stays stable.
				cur.seq = prev.seq
				cur.sources = sources
				best[k] = cur
			}
		}
	}
	merged := make([]*entry, 0, len(order))
	for _, k := range order {
		merged = append(merged, best[k])
	}
	sort.SliceStable(merged, func(i, j int) bool {
		a, b := merged[i], merged[j]
		if a.priority != b.priority {
			return a.priority < b.priority
		}
		if a.seeders != b.seeders {
			return a.seeders > b.seeders
		}
		return a.seq < b.seq
	})
	truncated := false
	if limit > 0 && len(merged) > limit {
		merged, truncated = merged[:limit], true
	}
	out := make([]schema.Release, 0, len(merged))
	for _, e := range merged {
		r := e.rel
		r.Info.AlsoOn = alsoOn(e.sources, r.Info.IndexerRef)
		out = append(out, r)
	}
	return out, truncated
}

// alsoOn is sources without kept, deduplicated, sorted by name so the same
// merge always renders the same list, and truncated to commonv1.MaxAlsoOn --
// the CRD's MaxItems, past which the apiserver would reject the whole
// Search.status or Download.spec that carries this release. An empty result
// is nil, so the omitempty field is absent rather than [].
func alsoOn(sources []string, kept string) []string {
	var out []string
	for _, s := range sources {
		if s != "" && s != kept && !slices.Contains(out, s) {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	if len(out) > commonv1.MaxAlsoOn {
		out = out[:commonv1.MaxAlsoOn]
	}
	return out
}

// seedersOf reads the seeder count, treating "the indexer reported none" as
// zero for ranking. Seeders is a *int32 because a usenet release has no
// seeders at all, which is different from a torrent with none.
func seedersOf(r schema.Release) int32 {
	if r.Info.Seeders == nil {
		return 0
	}
	return *r.Info.Seeders
}
