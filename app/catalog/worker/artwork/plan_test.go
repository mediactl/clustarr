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

package artwork_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/artwork"
	"github.com/mediactl/clustarr/pkg/overlay"
)

func profile(name, ns string, sel map[string]string, badges ...catalogv1alpha1.RatingSource) catalogv1alpha1.OverlayProfile {
	p := catalogv1alpha1.OverlayProfile{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
	if sel != nil {
		p.Spec.Selector = &metav1.LabelSelector{MatchLabels: sel}
	}
	for _, b := range badges {
		p.Spec.Badges = append(p.Spec.Badges, catalogv1alpha1.OverlayBadge{Source: b})
	}
	return p
}

func movieItem(ns string, lbls map[string]string, ratings ...catalogv1alpha1.Rating) artwork.Item {
	m := &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: ns, Labels: lbls}}
	if len(ratings) > 0 {
		m.Status.Metadata = &catalogv1alpha1.MovieMetadata{Ratings: ratings}
	}
	it, err := artwork.ItemOf(m)
	if err != nil {
		panic(err)
	}
	return it
}

func TestWinnerIsTheLowestNamedMatchingProfile(t *testing.T) {
	crit := map[string]string{"overlay": "critics"}
	it := movieItem("films", crit)
	profiles := []catalogv1alpha1.OverlayProfile{
		profile("zeta", "films", crit),
		profile("alpha", "films", crit),
		profile("aaa-other-ns", "tv", crit),     // another namespace never selects
		profile("aa-no-selector", "films", nil), // nil selects nothing
		profile("a-other-label", "films", map[string]string{"overlay": "audience"}),
	}
	w := artwork.Winner(profiles, it)
	require.NotNil(t, w)
	assert.Equal(t, "alpha", w.Name)

	// Deleting profiles step aside.
	now := metav1.Now()
	profiles[1].DeletionTimestamp = &now
	profiles[1].Finalizers = []string{"x"}
	assert.Equal(t, "zeta", artwork.Winner(profiles, it).Name)

	assert.Nil(t, artwork.Winner(profiles[2:], it))
}

func TestSelectsHonoursKinds(t *testing.T) {
	crit := map[string]string{"overlay": "critics"}
	moviesOnly := profile("movies", "films", crit)
	moviesOnly.Spec.Kinds = []commonv1.MediaKind{commonv1.MediaKindMovie}
	seriesOnly := profile("series", "films", crit)
	seriesOnly.Spec.Kinds = []commonv1.MediaKind{commonv1.MediaKindSeries}
	both := profile("both", "films", crit)

	m := movieItem("films", crit)
	s, err := artwork.ItemOf(&catalogv1alpha1.Series{ObjectMeta: metav1.ObjectMeta{Name: "lost", Namespace: "films", Labels: crit}})
	require.NoError(t, err)

	assert.True(t, artwork.Selects(&moviesOnly, m))
	assert.False(t, artwork.Selects(&moviesOnly, s))
	assert.True(t, artwork.Selects(&seriesOnly, s))
	assert.False(t, artwork.Selects(&seriesOnly, m))
	assert.True(t, artwork.Selects(&both, m), "unset kinds means both")
	assert.True(t, artwork.Selects(&both, s), "unset kinds means both")

	bad := profile("bad", "films", nil)
	bad.Spec.Selector = &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "x", Operator: "Bogus"}}}
	assert.False(t, artwork.Selects(&bad, m), "a malformed selector selects nothing rather than everything")
}

func TestItemOfRefusesKindsWithoutAnOverlay(t *testing.T) {
	_, err := artwork.ItemOf(&catalogv1alpha1.Album{})
	require.ErrorIs(t, err, artwork.ErrNoOverlay)
	assert.True(t, artwork.Overlaid(commonv1.MediaKindMovie))
	assert.True(t, artwork.Overlaid(commonv1.MediaKindSeries))
	assert.False(t, artwork.Overlaid(commonv1.MediaKindEpisode))
}

func TestBadgesOmitSourcesWithoutARating(t *testing.T) {
	p := profile("p", "films", nil, catalogv1alpha1.RatingSourceMetacritic, catalogv1alpha1.RatingSourceTMDB, catalogv1alpha1.RatingSourceIMDb)
	badges := artwork.Badges(p.Spec, []catalogv1alpha1.Rating{
		{Source: catalogv1alpha1.RatingSourceIMDb, ValueCentis: 0}, // FormatScore omits a zero
		{Source: catalogv1alpha1.RatingSourceTMDB, ValueCentis: 781, Votes: 12000},
		{Source: catalogv1alpha1.RatingSourceTrakt, ValueCentis: 800}, // not a badge of this profile
	})
	require.Len(t, badges, 1, "metacritic has no rating and imdb's zero is omitted")
	assert.Equal(t, overlay.SourceTMDB, badges[0].Source)
	assert.Equal(t, "7.8", badges[0].Score)
	assert.NotNil(t, badges[0].Logo)

	assert.Empty(t, artwork.Badges(p.Spec, nil))
}

func TestBadgesKeepTheProfilesOrder(t *testing.T) {
	p := profile("p", "films", nil, catalogv1alpha1.RatingSourceIMDb, catalogv1alpha1.RatingSourceMetacritic)
	badges := artwork.Badges(p.Spec, []catalogv1alpha1.Rating{
		{Source: catalogv1alpha1.RatingSourceMetacritic, ValueCentis: 7600},
		{Source: catalogv1alpha1.RatingSourceIMDb, ValueCentis: 830},
	})
	require.Len(t, badges, 2)
	assert.Equal(t, []string{overlay.SourceIMDb, overlay.SourceMetacritic}, []string{badges[0].Source, badges[1].Source})
	assert.Equal(t, "76", badges[1].Score)
}

func TestUnsetBadgesMeanOneMetacriticBadge(t *testing.T) {
	var spec catalogv1alpha1.OverlayProfileSpec
	assert.Equal(t, []catalogv1alpha1.OverlayBadge{{Source: catalogv1alpha1.RatingSourceMetacritic}}, artwork.EffectiveBadges(spec))
	badges := artwork.Badges(spec, []catalogv1alpha1.Rating{{Source: catalogv1alpha1.RatingSourceMetacritic, ValueCentis: 9100}})
	require.Len(t, badges, 1)
	assert.Equal(t, "91", badges[0].Score)
}

func TestInputsDigest(t *testing.T) {
	r1 := catalogv1alpha1.Rating{Source: catalogv1alpha1.RatingSourceTMDB, ValueCentis: 781, Votes: 10}
	r2 := catalogv1alpha1.Rating{Source: catalogv1alpha1.RatingSourceIMDb, ValueCentis: 830, Votes: 20}
	base := artwork.InputsDigest("orig", "hash", []catalogv1alpha1.Rating{r1, r2})
	assert.Len(t, base, 64, "hex SHA-256")

	assert.Equal(t, base, artwork.InputsDigest("orig", "hash", []catalogv1alpha1.Rating{r2, r1}), "ratings are sorted by source")
	assert.NotEqual(t, base, artwork.InputsDigest("orig2", "hash", []catalogv1alpha1.Rating{r1, r2}), "the original's digest")
	assert.NotEqual(t, base, artwork.InputsDigest("orig", "hash2", []catalogv1alpha1.Rating{r1, r2}), "the profile hash")
	moved := r1
	moved.ValueCentis = 782
	assert.NotEqual(t, base, artwork.InputsDigest("orig", "hash", []catalogv1alpha1.Rating{moved, r2}), "a rating's value")
	assert.NotEqual(t, base, artwork.InputsDigest("orig", "hash", []catalogv1alpha1.Rating{r1}), "a rating's presence")

	// Votes are never drawn, so a vote count ticking over on a metadata
	// refresh must not cost a render.
	voted := r1
	voted.Votes = 99999
	assert.Equal(t, base, artwork.InputsDigest("orig", "hash", []catalogv1alpha1.Rating{voted, r2}))
}

func TestPlan(t *testing.T) {
	crit := map[string]string{"overlay": "critics"}
	rated := catalogv1alpha1.Rating{Source: catalogv1alpha1.RatingSourceTMDB, ValueCentis: 781}
	p := profile("critics", "films", crit, catalogv1alpha1.RatingSourceTMDB)
	profiles := []catalogv1alpha1.OverlayProfile{p}

	t.Run("no profile selects the item", func(t *testing.T) {
		w := artwork.Plan(movieItem("films", nil, rated), profiles, "orig")
		assert.Nil(t, w.Profile)
		assert.Empty(t, w.InputsDigest)
	})
	t.Run("no badge has a rating", func(t *testing.T) {
		w := artwork.Plan(movieItem("films", crit), profiles, "orig")
		assert.Nil(t, w.Profile, "no badges means no overlay (spec §C.6 step 4)")
	})
	t.Run("no original poster", func(t *testing.T) {
		w := artwork.Plan(movieItem("films", crit, rated), profiles, "")
		assert.Nil(t, w.Profile)
	})
	t.Run("renders", func(t *testing.T) {
		it := movieItem("films", crit, rated)
		w := artwork.Plan(it, profiles, "orig")
		require.NotNil(t, w.Profile)
		assert.Equal(t, "critics", w.Profile.Name)
		require.Len(t, w.Badges, 1)
		assert.Equal(t, artwork.InputsDigest("orig", artwork.ProfileHash(p.Spec), it.Ratings), w.InputsDigest)

		assert.False(t, w.RecordedBy(nil))
		assert.True(t, w.RecordedBy(&catalogv1alpha1.OverlayEntry{ProfileRef: "critics", RenderedFrom: w.InputsDigest, Digest: "x"}))
		assert.False(t, w.RecordedBy(&catalogv1alpha1.OverlayEntry{ProfileRef: "other", RenderedFrom: w.InputsDigest}))
		assert.False(t, w.RecordedBy(&catalogv1alpha1.OverlayEntry{ProfileRef: "critics", RenderedFrom: "stale"}))
	})
	t.Run("none is recorded by no overlay", func(t *testing.T) {
		var none artwork.Want
		assert.True(t, none.RecordedBy(nil))
		assert.False(t, none.RecordedBy(&catalogv1alpha1.OverlayEntry{ProfileRef: "critics"}))
	})
}
