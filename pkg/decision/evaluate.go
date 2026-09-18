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
	"context"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/release"
)

// Evaluate runs the §8.2 checklist against every release in rels for one
// Target, in the order: protocol enabled, availability (skipped when
// o.UserInvoked), size, quality-in-profile + MinFormatScore, language,
// sample, blocklist + already-imported, queue preference, then the
// UpgradableSpecification table via p.UpgradeDecision. Every applicable
// check runs (nothing short-circuits except an unparseable title), so a
// release can carry more than one Rejection -- matching the real
// DownloadDecision's Rejections list, which Approved/TemporarilyRejected
// are then derived from exactly as Radarr's DownloadDecision does
// (Disagreement 6).
func Evaluate(ctx context.Context, t Target, p quality.Profile, cat *catalogue.Catalogue, rels []common.ReleaseInfo, o Options) []Decision {
	out := make([]Decision, 0, len(rels))
	for _, rel := range rels {
		out = append(out, evaluateOne(ctx, t, p, cat, rel, o))
	}
	return out
}

func evaluateOne(ctx context.Context, t Target, p quality.Profile, cat *catalogue.Catalogue, rel common.ReleaseInfo, o Options) Decision {
	parsed, err := release.Parse(rel.Title, release.Options{Kind: t.Kind})
	if err != nil {
		logging.FromContext(ctx).Debug("decision: release title did not parse", "title", rel.Title, "err", err)
		return Decision{
			Release:    rel,
			Rejections: []common.Rejection{newRejection(ReasonUnableToParse, "%v", err)},
		}
	}
	parsed.ApplyTo(&rel)

	ic := catalogue.ItemContext{OriginalLanguage: t.OriginalLanguage, IndexerFlags: rel.IndexerFlags, ReleaseType: parsed.ReleaseType}
	score, matched := p.Score(ctx, cat, parsed, ic)
	rel.FormatScore = int32(score)
	rel.MatchedFormats = matched

	var rejections []common.Rejection
	add := func(r *common.Rejection) {
		if r != nil {
			rejections = append(rejections, *r)
		}
	}

	add(protocolRejection(rel, o))
	add(availabilityRejection(t, o))
	rejections = append(rejections, sizeRejections(t, p, parsed, rel)...)
	rejections = append(rejections, qualityRejections(p, rel, score)...)
	add(languageRejection(t, p, parsed))
	add(sampleRejection(rel))
	rejections = append(rejections, blocklistAndHistoryRejections(t, rel)...)

	candidate := quality.Candidate{Quality: rel.Quality, Revision: rel.Revision, FormatScore: score}
	add(queueRejection(p, t, candidate))
	add(upgradeRejection(p, t, candidate))

	d := Decision{
		Release:             rel,
		Parsed:              parsed,
		Approved:            len(rejections) == 0,
		TemporarilyRejected: len(rejections) > 0 && allTemporary(rejections),
		Rejections:          rejections,
		Score:               score,
		Matched:             matched,
	}
	if d.Approved {
		d.Rank = buildRankKey(p, o, t, parsed, rel, score)
	}
	return d
}

func allTemporary(rejections []common.Rejection) bool {
	for _, r := range rejections {
		if r.Type != common.RejectionTemporary {
			return false
		}
	}
	return true
}

// buildRankKey is completed in Step 12 (primary keys) and Step 13
// (secondary keys); this step only needs QualityIndex to exist.
func buildRankKey(p quality.Profile, o Options, t Target, parsed *release.ParsedRelease, rel common.ReleaseInfo, score int) RankKey {
	idx, _ := p.Index(rel.Quality)
	return RankKey{QualityIndex: idx}
}
