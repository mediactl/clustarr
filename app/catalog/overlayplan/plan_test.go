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

package overlayplan_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/overlayplan"
	"github.com/mediactl/clustarr/pkg/dubs"
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

func movieItem(ns string, lbls map[string]string, ratings ...catalogv1alpha1.Rating) overlayplan.Item {
	m := &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: ns, Labels: lbls}}
	if len(ratings) > 0 {
		m.Status.Metadata = &catalogv1alpha1.MovieMetadata{Ratings: ratings}
	}
	it, err := overlayplan.ItemOf(context.Background(), m)
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
	w := overlayplan.Winner(profiles, it)
	require.NotNil(t, w)
	assert.Equal(t, "alpha", w.Name)

	// Deleting profiles step aside.
	now := metav1.Now()
	profiles[1].DeletionTimestamp = &now
	profiles[1].Finalizers = []string{"x"}
	assert.Equal(t, "zeta", overlayplan.Winner(profiles, it).Name)

	assert.Nil(t, overlayplan.Winner(profiles[2:], it))
}

func TestSelectsHonoursKinds(t *testing.T) {
	crit := map[string]string{"overlay": "critics"}
	moviesOnly := profile("movies", "films", crit)
	moviesOnly.Spec.Kinds = []commonv1.MediaKind{commonv1.MediaKindMovie}
	seriesOnly := profile("series", "films", crit)
	seriesOnly.Spec.Kinds = []commonv1.MediaKind{commonv1.MediaKindSeries}
	both := profile("both", "films", crit)

	m := movieItem("films", crit)
	s, err := overlayplan.ItemOf(context.Background(), &catalogv1alpha1.Series{ObjectMeta: metav1.ObjectMeta{Name: "lost", Namespace: "films", Labels: crit}})
	require.NoError(t, err)

	assert.True(t, overlayplan.Selects(&moviesOnly, m))
	assert.False(t, overlayplan.Selects(&moviesOnly, s))
	assert.True(t, overlayplan.Selects(&seriesOnly, s))
	assert.False(t, overlayplan.Selects(&seriesOnly, m))
	assert.True(t, overlayplan.Selects(&both, m), "unset kinds means both")
	assert.True(t, overlayplan.Selects(&both, s), "unset kinds means both")

	bad := profile("bad", "films", nil)
	bad.Spec.Selector = &metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "x", Operator: "Bogus"}}}
	assert.False(t, overlayplan.Selects(&bad, m), "a malformed selector selects nothing rather than everything")
}

func TestItemOfRefusesKindsWithoutAnOverlay(t *testing.T) {
	_, err := overlayplan.ItemOf(context.Background(), &catalogv1alpha1.Album{})
	require.ErrorIs(t, err, overlayplan.ErrNoOverlay)
	assert.True(t, overlayplan.Overlaid(commonv1.MediaKindMovie))
	assert.True(t, overlayplan.Overlaid(commonv1.MediaKindSeries))
	assert.False(t, overlayplan.Overlaid(commonv1.MediaKindEpisode))
}

func TestBadgesOmitSourcesWithoutARating(t *testing.T) {
	p := profile("p", "films", nil, catalogv1alpha1.RatingSourceMetacritic, catalogv1alpha1.RatingSourceTMDB, catalogv1alpha1.RatingSourceIMDb)
	badges := overlayplan.Badges(p.Spec, []catalogv1alpha1.Rating{
		{Source: catalogv1alpha1.RatingSourceIMDb, ValueCentis: 0}, // FormatScore omits a zero
		{Source: catalogv1alpha1.RatingSourceTMDB, ValueCentis: 781, Votes: 12000},
		{Source: catalogv1alpha1.RatingSourceTrakt, ValueCentis: 800}, // not a badge of this profile
	})
	require.Len(t, badges, 1, "metacritic has no rating and imdb's zero is omitted")
	assert.Equal(t, overlay.SourceTMDB, badges[0].Source)
	assert.Equal(t, "7.8", badges[0].Score)
	assert.NotNil(t, badges[0].Logo)

	assert.Empty(t, overlayplan.Badges(p.Spec, nil))
}

func TestBadgesKeepTheProfilesOrder(t *testing.T) {
	p := profile("p", "films", nil, catalogv1alpha1.RatingSourceIMDb, catalogv1alpha1.RatingSourceMetacritic)
	badges := overlayplan.Badges(p.Spec, []catalogv1alpha1.Rating{
		{Source: catalogv1alpha1.RatingSourceMetacritic, ValueCentis: 7600},
		{Source: catalogv1alpha1.RatingSourceIMDb, ValueCentis: 830},
	})
	require.Len(t, badges, 2)
	assert.Equal(t, []string{overlay.SourceIMDb, overlay.SourceMetacritic}, []string{badges[0].Source, badges[1].Source})
	assert.Equal(t, "76", badges[1].Score)
}

func TestUnsetBadgesMeanOneMetacriticBadge(t *testing.T) {
	var spec catalogv1alpha1.OverlayProfileSpec
	assert.Equal(t, []catalogv1alpha1.OverlayBadge{{Source: catalogv1alpha1.RatingSourceMetacritic}}, overlayplan.EffectiveBadges(spec))
	badges := overlayplan.Badges(spec, []catalogv1alpha1.Rating{{Source: catalogv1alpha1.RatingSourceMetacritic, ValueCentis: 9100}})
	require.Len(t, badges, 1)
	assert.Equal(t, "91", badges[0].Score)
}

func TestInputsDigest(t *testing.T) {
	r1 := catalogv1alpha1.Rating{Source: catalogv1alpha1.RatingSourceTMDB, ValueCentis: 781, Votes: 10}
	r2 := catalogv1alpha1.Rating{Source: catalogv1alpha1.RatingSourceIMDb, ValueCentis: 830, Votes: 20}
	base := overlayplan.InputsDigest("orig", "hash", []catalogv1alpha1.Rating{r1, r2}, false)
	assert.Len(t, base, 64, "hex SHA-256")

	assert.Equal(t, base, overlayplan.InputsDigest("orig", "hash", []catalogv1alpha1.Rating{r2, r1}, false), "ratings are sorted by source")
	assert.NotEqual(t, base, overlayplan.InputsDigest("orig2", "hash", []catalogv1alpha1.Rating{r1, r2}, false), "the original's digest")
	assert.NotEqual(t, base, overlayplan.InputsDigest("orig", "hash2", []catalogv1alpha1.Rating{r1, r2}, false), "the profile hash")
	moved := r1
	moved.ValueCentis = 782
	assert.NotEqual(t, base, overlayplan.InputsDigest("orig", "hash", []catalogv1alpha1.Rating{moved, r2}, false), "a rating's value")
	assert.NotEqual(t, base, overlayplan.InputsDigest("orig", "hash", []catalogv1alpha1.Rating{r1}, false), "a rating's presence")

	// Votes are never drawn, so a vote count ticking over on a metadata
	// refresh must not cost a render.
	voted := r1
	voted.Votes = 99999
	assert.Equal(t, base, overlayplan.InputsDigest("orig", "hash", []catalogv1alpha1.Rating{voted, r2}, false))
}

// TestRenderVersionMovesTheDigest: a renderer that draws differently from
// the same inputs must re-render every stored overlay, and the only way the
// renderer learns an overlay is stale is its Clustarr-Rendered-From no
// longer matching. InputsDigest is the current version's; bumping
// RenderVersion changes every digest.
func TestRenderVersionMovesTheDigest(t *testing.T) {
	ratings := []catalogv1alpha1.Rating{{Source: catalogv1alpha1.RatingSourceTMDB, ValueCentis: 781}}
	current := overlayplan.InputsDigest("orig", "hash", ratings, false)
	assert.Equal(t, current, overlayplan.InputsDigestAt(overlayplan.RenderVersion, "orig", "hash", ratings, false))
	assert.NotEqual(t, current, overlayplan.InputsDigestAt(overlayplan.RenderVersion+1, "orig", "hash", ratings, false),
		"a bumped RenderVersion must change the digest")
	assert.NotEqual(t, current, overlayplan.InputsDigestAt(overlayplan.RenderVersion-1, "orig", "hash", ratings, false),
		"an overlay drawn by the previous renderer is stale")
}

func TestPlan(t *testing.T) {
	crit := map[string]string{"overlay": "critics"}
	rated := catalogv1alpha1.Rating{Source: catalogv1alpha1.RatingSourceTMDB, ValueCentis: 781}
	p := profile("critics", "films", crit, catalogv1alpha1.RatingSourceTMDB)
	profiles := []catalogv1alpha1.OverlayProfile{p}

	t.Run("no profile selects the item", func(t *testing.T) {
		w := overlayplan.Plan(movieItem("films", nil, rated), profiles, "orig")
		assert.Nil(t, w.Profile)
		assert.Empty(t, w.InputsDigest)
	})
	t.Run("no badge has a rating", func(t *testing.T) {
		w := overlayplan.Plan(movieItem("films", crit), profiles, "orig")
		assert.Nil(t, w.Profile, "no badges means no overlay (spec §C.6 step 4)")
	})
	t.Run("no original poster", func(t *testing.T) {
		w := overlayplan.Plan(movieItem("films", crit, rated), profiles, "")
		assert.Nil(t, w.Profile)
	})
	t.Run("renders", func(t *testing.T) {
		it := movieItem("films", crit, rated)
		w := overlayplan.Plan(it, profiles, "orig")
		require.NotNil(t, w.Profile)
		assert.Equal(t, "critics", w.Profile.Name)
		require.Len(t, w.Badges, 1)
		assert.Equal(t, overlayplan.InputsDigest("orig", overlayplan.ProfileHash(p.Spec), it.Ratings, false), w.InputsDigest)

		assert.False(t, w.RecordedBy(nil))
		assert.True(t, w.RecordedBy(&catalogv1alpha1.OverlayEntry{ProfileRef: "critics", RenderedFrom: w.InputsDigest, Digest: "x"}))
		assert.False(t, w.RecordedBy(&catalogv1alpha1.OverlayEntry{ProfileRef: "other", RenderedFrom: w.InputsDigest}))
		assert.False(t, w.RecordedBy(&catalogv1alpha1.OverlayEntry{ProfileRef: "critics", RenderedFrom: "stale"}))
	})
	t.Run("none is recorded by no overlay", func(t *testing.T) {
		var none overlayplan.Want
		assert.True(t, none.RecordedBy(nil))
		assert.False(t, none.RecordedBy(&catalogv1alpha1.OverlayEntry{ProfileRef: "critics"}))
	})
}

// animeItem is an anime Series, or an anime Movie, read through the
// MAL-Dubs index ix as a process carries it: the route every caller of
// ItemOf takes to an item's dub.
func animeItem(t *testing.T, ix *dubs.Index, kind commonv1.MediaKind, mal string, ratings ...catalogv1alpha1.Rating) overlayplan.Item {
	t.Helper()
	meta := metav1.ObjectMeta{Name: "eighty-six", Namespace: "anime", Labels: map[string]string{"overlay": "critics"}}
	ids := map[string]string{}
	if mal != "" {
		ids["mal"] = mal
	}
	var obj client.Object
	switch kind {
	case commonv1.MediaKindMovie:
		obj = &catalogv1alpha1.Movie{ObjectMeta: meta, Status: catalogv1alpha1.MovieStatus{
			Classification: &catalogv1alpha1.MovieClassification{Anime: true},
			Metadata:       &catalogv1alpha1.MovieMetadata{ExternalIDs: ids, Ratings: ratings},
		}}
	default:
		obj = &catalogv1alpha1.Series{
			ObjectMeta: meta,
			Spec:       catalogv1alpha1.SeriesSpec{SeriesType: catalogv1alpha1.SeriesTypeAnime},
			Status:     catalogv1alpha1.SeriesStatus{Metadata: &catalogv1alpha1.SeriesMetadata{ExternalIDs: ids, Ratings: ratings}},
		}
	}
	it, err := overlayplan.ItemOf(dubs.WithIndex(context.Background(), ix), obj)
	require.NoError(t, err)
	return it
}

func sources(badges []overlay.Badge) []string {
	out := make([]string, 0, len(badges))
	for _, b := range badges {
		out = append(out, b.Source)
	}
	return out
}

// A dubbed anime's poster carries the dub mark (owner, 2026-11-17): alone
// when no badge has a rating -- no anime on talos has a Metacritic score, so
// that is how nearly every one carries it -- and after the rating badges
// otherwise. Only the list's "dubbed" marks: a dub still being made, an
// anime the list does not name and one with no MAL id carry none.
func TestADubbedAnimeCarriesTheDubMark(t *testing.T) {
	ix := dubs.NewIndex()
	ix.Set(dubs.Record{Dubbed: []int{41457}, Incomplete: []int{56352}})
	profiles := []catalogv1alpha1.OverlayProfile{profile("critics", "anime", map[string]string{"overlay": "critics"})}
	rated := catalogv1alpha1.Rating{Source: catalogv1alpha1.RatingSourceMetacritic, ValueCentis: 7800}

	for _, kind := range []commonv1.MediaKind{commonv1.MediaKindSeries, commonv1.MediaKindMovie} {
		t.Run(string(kind), func(t *testing.T) {
			alone := overlayplan.Plan(animeItem(t, ix, kind, "41457"), profiles, "orig")
			require.NotNil(t, alone.Profile, "the mark stands without a rated badge")
			require.Equal(t, []string{overlay.SourceDub}, sources(alone.Badges))
			assert.Equal(t, overlay.DubText, alone.Badges[0].Score)
			assert.Nil(t, alone.Badges[0].Logo)

			both := overlayplan.Plan(animeItem(t, ix, kind, "41457", rated), profiles, "orig")
			assert.Equal(t, []string{overlay.SourceMetacritic, overlay.SourceDub}, sources(both.Badges),
				"the rating keeps the corner it has on every other poster")
			assert.NotEqual(t, alone.InputsDigest, both.InputsDigest)

			for name, mal := range map[string]string{"a dub still being made": "56352", "not on the list": "53802", "no MAL id": ""} {
				assert.Nil(t, overlayplan.Plan(animeItem(t, ix, kind, mal), profiles, "orig").Profile, name)
				w := overlayplan.Plan(animeItem(t, ix, kind, mal, rated), profiles, "orig")
				assert.Equal(t, []string{overlay.SourceMetacritic}, sources(w.Badges), name)
			}

			// No profile selects it, or there is no poster: no overlay,
			// dubbed or not.
			assert.Nil(t, overlayplan.Plan(animeItem(t, ix, kind, "41457"), nil, "orig").Profile)
			assert.Nil(t, overlayplan.Plan(animeItem(t, ix, kind, "41457"), profiles, "").Profile)
		})
	}

	// Anything but an anime is not the list's to mark, whatever its ids.
	drama := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "lost", Namespace: "anime", Labels: map[string]string{"overlay": "critics"}},
		Status:     catalogv1alpha1.SeriesStatus{Metadata: &catalogv1alpha1.SeriesMetadata{ExternalIDs: map[string]string{"mal": "41457"}}},
	}
	it, err := overlayplan.ItemOf(dubs.WithIndex(context.Background(), ix), drama)
	require.NoError(t, err)
	assert.False(t, it.Dubbed)
	assert.Nil(t, overlayplan.Plan(it, profiles, "orig").Profile)
}

// The mark is an input of the render: gaining or losing it moves the
// digest, so the poster is drawn again. A poster without one keeps the
// digest it had before the mark existed -- the literal below is that
// digest -- so shipping the mark re-rendered no poster that does not carry
// it.
func TestTheDubMarkMovesOnlyAMarkedPostersDigest(t *testing.T) {
	ratings := []catalogv1alpha1.Rating{{Source: catalogv1alpha1.RatingSourceMetacritic, ValueCentis: 7600}}
	plain := overlayplan.InputsDigest("orig", "hash", ratings, false)
	assert.NotEqual(t, plain, overlayplan.InputsDigest("orig", "hash", ratings, true))
	assert.Equal(t, "4245fba79a70c574033daac98d1c1c015322324b2bb5e869a3653b0e2beceb52", plain)
}

// An anime read before the process has the list is not "not dubbed": its
// dub is unread, which every caller of Plan checks before it acts, and it
// stops being so at the first list read. Nothing else is ever unread.
func TestAnAnimesDubIsUnreadUntilTheListIs(t *testing.T) {
	ix := dubs.NewIndex()
	it := animeItem(t, ix, commonv1.MediaKindSeries, "41457")
	assert.True(t, it.DubUnread)
	assert.False(t, it.Dubbed)
	assert.False(t, movieItem("films", nil).DubUnread, "a process with no index, and no anime")

	ix.Set(dubs.Record{Dubbed: []int{41457}})
	it = animeItem(t, ix, commonv1.MediaKindSeries, "41457")
	assert.False(t, it.DubUnread)
	assert.True(t, it.Dubbed)
}
