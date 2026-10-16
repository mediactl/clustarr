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
	"context"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/release"
)

// withPartSets adds, to a movie search's decisions, each set of the movie's
// separately posted parts as one more decision (decision.PartSets): the
// set's release judged as the film by the same evaluator, carrying its
// parts' rejections; rels gains the set's release, carrying the first
// block the release index holds on one of its parts. The parts stay as
// they were. Any other kind's answer is returned unchanged.
func (w *Worker) withPartSets(
	ctx context.Context, t decision.Target, p quality.Profile, ds []decision.Decision, rels []schema.Release, o decision.Options,
) ([]decision.Decision, []schema.Release) {
	if t.Kind != commonv1.MediaKindMovie {
		return ds, rels
	}
	blocked := map[string]*schema.ReleaseBlock{}
	for i := range rels {
		if rels[i].Blocked != nil {
			blocked[releaseKey(rels[i].Info)] = rels[i].Blocked
		}
	}
	// A whole post the release index blocks is no grab, so it hides no
	// parts (decision.PartSets prefers a whole post to its parts).
	candidates := make([]decision.Decision, 0, len(ds))
	for _, d := range ds {
		if _, _, part := release.PartOf(d.Release.Title); part || blocked[releaseKey(d.Release)] == nil {
			candidates = append(candidates, d)
		}
	}
	for _, s := range decision.PartSets(candidates) {
		judged := w.evaluate()(ctx, t, p, w.Catalogue, []commonv1.ReleaseInfo{s.Release()}, o)
		if len(judged) != 1 {
			continue
		}
		d := s.Judge(judged[0])
		ds = append(ds, d)
		r := schema.Release{Info: d.Release}
		for _, m := range s.Members {
			if b := blocked[releaseKey(m.Release)]; b != nil {
				cp := *b
				r.Blocked = &cp
				break
			}
		}
		rels = append(rels, r)
	}
	return ds, rels
}
