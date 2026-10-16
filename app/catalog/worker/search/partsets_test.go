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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// A movie search's answer carries each set of posted parts as one more
// candidate (decision.PartSets) -- judged as the film, its parts listed,
// blocked when the release index blocks a part -- beside the parts
// themselves; an episode search's never does.
func TestASearchAnswerCarriesAMoviesPartsAsOneCandidate(t *testing.T) {
	cat := catalogue.LoadedCatalogue()
	profiles, errs := quality.BuiltinProfiles(cat)
	require.Empty(t, errs)
	p := profiles["hd-bluray-web"]
	w := &Worker{Catalogue: cat}
	opts := decision.Options{ProtocolsEnabled: map[string]bool{"usenet": true}}
	part := func(guid, title string) schema.Release {
		return schema.Release{Info: commonv1.ReleaseInfo{
			GUID: guid, IndexerRef: "nzbgeek", Protocol: commonv1.ProtocolUsenet, Title: title, SizeBytes: 4 << 30,
		}}
	}
	answer := func(blockPart2 bool) []schema.Release {
		rels := []schema.Release{
			part("g1", "56.Up.2012.Part1.1080p.BluRay.x264-USURY"),
			part("g2", "56.Up.2012.Part2.1080p.BluRay.x264-USURY"),
			part("g3", "56.Up.2012.Part3.1080p.BluRay.x264-USURY"),
		}
		if blockPart2 {
			rels[1].Blocked = &schema.ReleaseBlock{Reason: "missingArticles"}
		}
		return rels
	}
	ctx := context.Background()
	candidates := func(tg decision.Target, rels []schema.Release) []schema.ScoredRelease {
		ds := decision.EvaluateRelease(ctx, tg, p, cat, releaseInfos(rels), opts)
		ds, rels = w.withPartSets(ctx, tg, p, ds, rels, opts)
		return RankScored(ds, opts, rels, schema.MaxSearchCandidates)
	}
	film := decision.Target{
		Kind: commonv1.MediaKindMovie, Monitored: true, Available: true, OriginalLanguageTag: "en", RuntimeMinutes: 144,
		Identity: decision.Identity{Titles: []string{"56 Up"}, Year: 2012},
	}

	got := candidates(film, answer(false))
	require.Len(t, got, 4, "three parts and the set")
	set := got[0]
	assert.True(t, set.Decision.Approved, "the set ranks first, approved: %v", set.Decision.Rejections)
	assert.Equal(t, "56.Up.2012.1080p.BluRay.x264-USURY", set.Decision.Title)
	assert.Len(t, set.Decision.Parts, 3)
	assert.Nil(t, set.Blocked)
	for _, c := range got[1:] {
		assert.False(t, c.Decision.Approved, "%s alone is no film", c.Decision.Title)
	}

	got = candidates(film, answer(true))
	require.Len(t, got, 4)
	for _, c := range got {
		if len(c.Decision.Parts) > 0 {
			require.NotNil(t, c.Blocked, "a set one of whose parts is blocked is blocked")
			assert.Equal(t, "missingArticles", c.Blocked.Reason)
		}
	}

	episode := film
	episode.Kind = commonv1.MediaKindEpisode
	assert.Len(t, candidates(episode, answer(false)), 3, "no set for an episode")
}
