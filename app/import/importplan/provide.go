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

package importplan

import (
	"cmp"
	"slices"
	"strings"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// maxEpisodes is an entry's episode cap (DownloadEntry.Episodes' MaxItems,
// grabplan's maxCovered).
const maxEpisodes = 200

// provide makes a Series' pack account for the files it holds (owner,
// 2026-11-27: "we should download and extract *both* packs to satisfy the
// season. Each pack will account for the files it provides"). An entry
// names the episodes its release was grabbed for, read from the release's
// title; the inspection is the first to know which episodes its files are.
// Where the two differ the entry's episodes become:
//
//   - the ones it was grabbed for and holds, when it holds some of them;
//   - the ones its files are, when it holds none of them.
//
// The episodes it was grabbed for and does not hold are Freed: no longer
// covered by a live grab, so the planner takes another release for them,
// and each granted a search (the downloads stage) so one is looked for.
//
// On talos three usenet packs of How a Realist Hero Rebuilt the Kingdom
// named "S01" were grabbed in turn for episodes 14 to 26, TVDB's one
// season of 26: each held episodes 1 to 13, was held at import over the
// files those episodes already had, and went on covering 14 to 26 for its
// 24 hours, while the part that held them ranked fourth.
//
// It reads the first inspection only, before any execute has moved a file
// out of the payload (a later inspection lists what is left), every file
// of which is attributed: a file nobody could attribute may be any episode
// the entry names, and is a person's to say. A person's target, a donor
// and a set are left as they are, and so is an entry that would name more
// episodes than an entry can.
func (p *planner) provide() {
	e := &p.in.Entry
	r := p.in.Inspect
	if p.in.OwnerKind != commonv1.MediaKindSeries || p.donor() || e.Set != nil || p.sum.Target != "" ||
		p.in.Execute != nil || r == nil || r.Inspect == nil || len(e.Episodes) == 0 {
		return
	}
	held := map[string]bool{}
	for _, f := range r.Inspect.Files {
		switch {
		case f.Sample:
			continue
		case f.Proposed == nil || MediaKindOf(f.Proposed.Kind) != commonv1.MediaKindEpisode:
			return
		}
		for _, k := range keysOf(f) {
			held[k] = true
		}
	}
	if len(held) == 0 {
		return
	}
	var kept, freed []schema.ItemRef
	for _, t := range p.in.Targets {
		switch {
		case !strings.EqualFold(t.Kind, "Episode"):
			return
		case held[t.Name]:
			kept = append(kept, t)
		default:
			freed = append(freed, t)
		}
	}
	if len(freed) == 0 {
		return
	}
	names := make([]string, 0, len(held))
	if len(kept) > 0 {
		for _, t := range kept {
			names = append(names, t.Name)
		}
	} else {
		for k := range held {
			names = append(names, k)
		}
	}
	if len(names) > maxEpisodes {
		return
	}
	episodes := make([]catalogv1alpha1.EpisodeNumber, 0, len(names))
	for _, n := range names {
		num, ok := p.in.Numbers[n]
		if !ok {
			return
		}
		episodes = append(episodes, num)
	}
	slices.SortFunc(episodes, func(a, b catalogv1alpha1.EpisodeNumber) int {
		return cmp.Or(cmp.Compare(a.Season, b.Season), cmp.Compare(a.Number, b.Number))
	})
	p.dec.Episodes, p.dec.Freed = slices.Compact(episodes), freed
}
