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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/artwork"
	"github.com/mediactl/clustarr/app/catalog/itemread"
)

const (
	textPoster      = "https://img.example/poster-en.jpg"
	textlessPoster  = "https://img.example/poster-textless.jpg"
	textlessPoster2 = "https://img.example/poster-textless-2.jpg"
	jaLogo          = "https://img.example/logo-ja.png"
	enLogo          = "https://img.example/logo-en.png"
	setPoster       = "https://mediux.example/set-poster.jpg"
)

// animeImages is an anime's metadata as the providers list it: the titled
// poster first, then one without a language, and the logos with the original
// language's first.
func animeImages() []catalogv1alpha1.Image {
	return []catalogv1alpha1.Image{
		{Type: catalogv1alpha1.ImageTypePoster, URL: textPoster, Language: "en"},
		{Type: catalogv1alpha1.ImageTypePoster, URL: textlessPoster},
		{Type: catalogv1alpha1.ImageTypePoster, URL: textlessPoster2},
		{Type: catalogv1alpha1.ImageTypeLogo, URL: jaLogo, Language: "ja"},
		{Type: catalogv1alpha1.ImageTypeLogo, URL: enLogo, Language: "en"},
		{Type: catalogv1alpha1.ImageTypeFanart, URL: "https://img.example/fanart.jpg"},
	}
}

func animeSeries(images []catalogv1alpha1.Image) *catalogv1alpha1.Series {
	return &catalogv1alpha1.Series{
		Spec:   catalogv1alpha1.SeriesSpec{SeriesType: catalogv1alpha1.SeriesTypeAnime},
		Status: catalogv1alpha1.SeriesStatus{Metadata: &catalogv1alpha1.SeriesMetadata{Images: images, Language: "en"}},
	}
}

func animeMovie(images []catalogv1alpha1.Image) *catalogv1alpha1.Movie {
	return &catalogv1alpha1.Movie{Status: catalogv1alpha1.MovieStatus{
		Metadata:       &catalogv1alpha1.MovieMetadata{Images: images, Language: "en"},
		Classification: &catalogv1alpha1.MovieClassification{Anime: true},
	}}
}

func sourcesOf(t *testing.T, obj client.Object) map[catalogv1alpha1.ImageType]artwork.Source {
	t.Helper()
	it, err := artwork.ItemOf(obj)
	require.NoError(t, err)
	return it.Sources()
}

// An anime takes the posters listed without a language, in order, and its
// English logo, named on the poster's source so the fetcher draws it on the
// first that carries no text, with the poster ResolveSources alone would
// take as the one to fall back to (owner, 2026-11-17).
func TestAnAnimeTakesTheTextlessPostersAndItsEnglishLogo(t *testing.T) {
	for name, obj := range map[string]client.Object{"series": animeSeries(animeImages()), "movie": animeMovie(animeImages())} {
		got := sourcesOf(t, obj)
		assert.Equal(t, artwork.Source{
			URL: textlessPoster, Kind: catalogv1alpha1.ArtworkSourceProvider, Logo: enLogo,
			Candidates: []string{textlessPoster2},
			Plain:      &artwork.Source{URL: textPoster, Kind: catalogv1alpha1.ArtworkSourceProvider, Language: "en"},
		}, got[catalogv1alpha1.ImageTypePoster], name)
		assert.Equal(t, artwork.Source{URL: enLogo, Kind: catalogv1alpha1.ArtworkSourceProvider, Language: "en"},
			got[catalogv1alpha1.ImageTypeLogo], name)
		assert.Equal(t, "https://img.example/fanart.jpg", got[catalogv1alpha1.ImageTypeFanart].URL, name)
	}
}

// The predicate is itemread.Anime's: ItemOf restates it rather than import
// the readers, and this holds the two together.
func TestTitledPosterFollowsTheAnimePredicate(t *testing.T) {
	standard := animeSeries(animeImages())
	standard.Spec.SeriesType = catalogv1alpha1.SeriesTypeStandard
	film := animeMovie(animeImages())
	film.Status.Classification = &catalogv1alpha1.MovieClassification{Anime: false}
	unclassified := animeMovie(animeImages())
	unclassified.Status.Classification = nil
	for name, obj := range map[string]client.Object{
		"anime series": animeSeries(animeImages()), "standard series": standard,
		"anime film": animeMovie(animeImages()), "live-action film": film, "unclassified film": unclassified,
	} {
		it, err := artwork.ItemOf(obj)
		require.NoError(t, err)
		assert.Equal(t, itemread.Anime(obj), it.TitledPoster, name)
		if !itemread.Anime(obj) {
			got := it.Sources()
			assert.Equal(t, textPoster, got[catalogv1alpha1.ImageTypePoster].URL, name)
			assert.Empty(t, got[catalogv1alpha1.ImageTypePoster].Logo, name)
			assert.Equal(t, jaLogo, got[catalogv1alpha1.ImageTypeLogo].URL, name)
		}
	}
}

// A textless poster without its logo names nothing, and a logo without a
// textless poster would be drawn over the poster's own title: either one
// missing leaves the item the poster it had.
func TestAnAnimeWithoutBothKeepsItsPoster(t *testing.T) {
	noLogo := []catalogv1alpha1.Image{
		{Type: catalogv1alpha1.ImageTypePoster, URL: textPoster, Language: "en"},
		{Type: catalogv1alpha1.ImageTypePoster, URL: textlessPoster},
		{Type: catalogv1alpha1.ImageTypeLogo, URL: jaLogo, Language: "ja"},
	}
	noTextless := []catalogv1alpha1.Image{
		{Type: catalogv1alpha1.ImageTypePoster, URL: textPoster, Language: "en"},
		{Type: catalogv1alpha1.ImageTypePoster, URL: "https://img.example/poster-ja.jpg", Language: "ja"},
		{Type: catalogv1alpha1.ImageTypeLogo, URL: enLogo, Language: "en"},
	}
	unfetchable := []catalogv1alpha1.Image{
		{Type: catalogv1alpha1.ImageTypePoster, URL: textPoster, Language: "en"},
		{Type: catalogv1alpha1.ImageTypePoster, URL: "/relative/textless.jpg"},
		{Type: catalogv1alpha1.ImageTypeLogo, URL: enLogo, Language: "en"},
	}
	for name, images := range map[string][]catalogv1alpha1.Image{"no English logo": noLogo, "no textless poster": noTextless, "unfetchable textless poster": unfetchable} {
		got := sourcesOf(t, animeSeries(images))
		assert.Equal(t, textPoster, got[catalogv1alpha1.ImageTypePoster].URL, name)
		assert.Empty(t, got[catalogv1alpha1.ImageTypePoster].Logo, name)
	}
}

// The owner's own poster wins -- a spec.artwork override, or a pinned image
// set -- and a set the mediux provider chose by itself yields: its poster
// carries the set maker's title.
func TestTheOwnersPosterBeatsTheTitledOneAndAChosenSetYields(t *testing.T) {
	set := &catalogv1alpha1.ImageSetStatus{Artwork: []catalogv1alpha1.ArtworkOverride{
		{Type: catalogv1alpha1.ImageTypePoster, URL: setPoster},
		{Type: catalogv1alpha1.ImageTypeFanart, URL: "https://mediux.example/set-fanart.jpg"},
	}}

	chosen := animeSeries(animeImages())
	chosen.Status.ImageSet = set
	got := sourcesOf(t, chosen)
	assert.Equal(t, textlessPoster, got[catalogv1alpha1.ImageTypePoster].URL, "a chosen set's poster yields")
	assert.Equal(t, enLogo, got[catalogv1alpha1.ImageTypePoster].Logo)
	assert.Equal(t, &artwork.Source{URL: setPoster, Kind: catalogv1alpha1.ArtworkSourceCustom}, got[catalogv1alpha1.ImageTypePoster].Plain,
		"and is what the item falls back to when every textless poster carries text")
	assert.Equal(t, "https://mediux.example/set-fanart.jpg", got[catalogv1alpha1.ImageTypeFanart].URL, "its other images stay")

	pinned := animeSeries(animeImages())
	pinned.Status.ImageSet, pinned.Spec.ImageSetID = set, "28422"
	got = sourcesOf(t, pinned)
	assert.Equal(t, artwork.Source{URL: setPoster, Kind: catalogv1alpha1.ArtworkSourceCustom}, got[catalogv1alpha1.ImageTypePoster], "a pinned set's poster stays")

	own := animeMovie(animeImages())
	own.Status.ImageSet = set
	own.Spec.Artwork = []catalogv1alpha1.ArtworkOverride{{Type: catalogv1alpha1.ImageTypePoster, URL: "https://owner.example/poster.jpg"}}
	got = sourcesOf(t, own)
	assert.Equal(t, artwork.Source{URL: "https://owner.example/poster.jpg", Kind: catalogv1alpha1.ArtworkSourceCustom}, got[catalogv1alpha1.ImageTypePoster])

	// An override of another type is no poster of the owner's, and the
	// owner's own logo stays the item's logo while the provider's is drawn.
	ownLogo := animeMovie(animeImages())
	ownLogo.Spec.Artwork = []catalogv1alpha1.ArtworkOverride{{Type: catalogv1alpha1.ImageTypeLogo, URL: "https://owner.example/logo.png"}}
	got = sourcesOf(t, ownLogo)
	assert.Equal(t, enLogo, got[catalogv1alpha1.ImageTypePoster].Logo)
	assert.Equal(t, "https://owner.example/logo.png", got[catalogv1alpha1.ImageTypeLogo].URL)
}

// The logo is in the language the item's titles are in, by primary subtag,
// and English when the metadata names none; the posters are capped.
func TestTheLogoIsInTheTitlesLanguageAndThePostersAreCapped(t *testing.T) {
	images := animeImages()
	posters, logo, ok := artwork.Titled(images, "ja")
	require.True(t, ok)
	assert.Equal(t, jaLogo, logo.URL)
	require.Len(t, posters, 2)
	assert.Equal(t, []string{textlessPoster, textlessPoster2}, []string{posters[0].URL, posters[1].URL})
	_, logo, ok = artwork.Titled(images, "en-US")
	require.True(t, ok)
	assert.Equal(t, enLogo, logo.URL)
	_, logo, ok = artwork.Titled(images, "")
	require.True(t, ok)
	assert.Equal(t, enLogo, logo.URL)
	_, _, ok = artwork.Titled(images, "de")
	assert.False(t, ok)

	many := []catalogv1alpha1.Image{{Type: catalogv1alpha1.ImageTypeLogo, URL: enLogo, Language: "en"}}
	for i := range artwork.MaxTitleCandidates + 4 {
		many = append(many, catalogv1alpha1.Image{Type: catalogv1alpha1.ImageTypePoster, URL: fmt.Sprintf("https://img.example/p%d.jpg", i)})
	}
	posters, _, ok = artwork.Titled(many, "en")
	require.True(t, ok)
	assert.Len(t, posters, artwork.MaxTitleCandidates)
}

// The mark names the titled source's inputs, not which poster the fetcher
// ended up storing: it moves with the logo, any candidate and the plain
// poster, and a source with no logo has none.
func TestTheTitleMarkMovesWithEveryInput(t *testing.T) {
	base := artwork.Source{
		URL: textlessPoster, Kind: catalogv1alpha1.ArtworkSourceProvider, Logo: enLogo, Candidates: []string{textlessPoster2},
		Plain: &artwork.Source{URL: textPoster, Kind: catalogv1alpha1.ArtworkSourceProvider},
	}
	mark := base.TitleMark()
	require.NotEmpty(t, mark)
	assert.True(t, strings.HasPrefix(mark, artwork.TitleVersion+" "))
	assert.Equal(t, mark, base.TitleMark(), "stable")
	assert.Empty(t, artwork.Source{URL: textlessPoster}.TitleMark())

	for name, change := range map[string]func(s *artwork.Source){
		"the logo":         func(s *artwork.Source) { s.Logo = jaLogo },
		"the first poster": func(s *artwork.Source) { s.URL = textlessPoster2 },
		"a candidate":      func(s *artwork.Source) { s.Candidates = nil },
		"the plain poster": func(s *artwork.Source) {
			s.Plain = &artwork.Source{URL: setPoster, Kind: catalogv1alpha1.ArtworkSourceProvider}
		},
		"the plain's source": func(s *artwork.Source) {
			s.Plain = &artwork.Source{URL: textPoster, Kind: catalogv1alpha1.ArtworkSourceCustom}
		},
		"no plain poster": func(s *artwork.Source) { s.Plain = nil },
	} {
		moved := base
		change(&moved)
		assert.NotEqual(t, mark, moved.TitleMark(), name)
	}
}

// The plan judges a titled source by its mark alone: whichever poster the
// fetcher stored under the source's mark satisfies it -- a candidate with
// the logo, or the plain poster from another URL and kind -- and anything
// stored under another mark, or none, is fetched.
func TestPlanFetchJudgesATitledSourceByItsMark(t *testing.T) {
	now := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	src := artwork.Source{
		URL: textlessPoster, Kind: catalogv1alpha1.ArtworkSourceProvider, Logo: enLogo, Candidates: []string{textlessPoster2},
		Plain: &artwork.Source{URL: setPoster, Kind: catalogv1alpha1.ArtworkSourceCustom},
	}
	titled := map[catalogv1alpha1.ImageType]artwork.Source{catalogv1alpha1.ImageTypePoster: src}
	plain := map[catalogv1alpha1.ImageType]artwork.Source{
		catalogv1alpha1.ImageTypePoster: {URL: textlessPoster, Kind: catalogv1alpha1.ArtworkSourceProvider},
	}
	obj := func(url string, kind catalogv1alpha1.ArtworkSource, title string) map[catalogv1alpha1.ImageType]artwork.Object {
		return map[catalogv1alpha1.ImageType]artwork.Object{catalogv1alpha1.ImageTypePoster: {
			Present: true, Current: true, Source: kind, SourceURL: url, UpdatedAt: metav1.NewTime(now), Title: title,
		}}
	}
	provider, custom := catalogv1alpha1.ArtworkSourceProvider, catalogv1alpha1.ArtworkSourceCustom
	mark := src.TitleMark()

	for name, o := range map[string]map[catalogv1alpha1.ImageType]artwork.Object{
		"the first poster, titled":   obj(textlessPoster, provider, mark),
		"a later candidate, titled":  obj(textlessPoster2, provider, mark),
		"the plain poster, as it is": obj(setPoster, custom, mark),
	} {
		assert.True(t, artwork.PlanFetch(titled, o, nil, now).Empty(), name)
	}
	assert.True(t, artwork.PlanFetch(plain, obj(textlessPoster, provider, ""), nil, now).Empty(), "no logo wanted, none recorded")

	other := src
	other.Logo = jaLogo
	for name, o := range map[string]map[catalogv1alpha1.ImageType]artwork.Object{
		"stored before titles":  obj(textlessPoster, provider, ""),
		"another source's mark": obj(textlessPoster, provider, other.TitleMark()),
		"an older version":      obj(textlessPoster, provider, "0 "+strings.TrimPrefix(mark, artwork.TitleVersion+" ")),
	} {
		plan := artwork.PlanFetch(titled, o, nil, now)
		require.Len(t, plan.Fetch, 1, name)
		f := plan.Fetch[0]
		assert.Equal(t, []string{textlessPoster, enLogo}, []string{f.URL, f.Logo}, name)
		assert.Equal(t, []string{textlessPoster2}, f.Candidates, name)
		require.NotNil(t, f.Plain, name)
		assert.Equal(t, []string{setPoster, string(custom)}, []string{f.Plain.URL, f.Plain.Kind}, name)
	}
	plan := artwork.PlanFetch(plain, obj(textlessPoster, provider, mark), nil, now)
	require.Len(t, plan.Fetch, 1, "a mark its source no longer has")
	assert.Empty(t, plan.Fetch[0].Logo)

	// A resting failure is the first poster's, as for any source.
	failed := map[catalogv1alpha1.ImageType]artwork.Failure{catalogv1alpha1.ImageTypePoster: {URL: textlessPoster, At: now.Add(-time.Hour)}}
	rest := artwork.PlanFetch(titled, obj(textlessPoster, provider, ""), failed, now)
	assert.True(t, rest.Empty())
	assert.Equal(t, now.Add(-time.Hour).Add(artwork.FailureRetry), rest.Due)

	// What the plan names is its hash: another logo or candidate is a new
	// task.
	a := artwork.PlanFetch(titled, obj("", provider, ""), nil, now)
	b := artwork.PlanFetch(map[catalogv1alpha1.ImageType]artwork.Source{catalogv1alpha1.ImageTypePoster: other}, obj("", provider, ""), nil, now)
	fewer := src
	fewer.Candidates = nil
	c := artwork.PlanFetch(map[catalogv1alpha1.ImageType]artwork.Source{catalogv1alpha1.ImageTypePoster: fewer}, obj("", provider, ""), nil, now)
	assert.NotEqual(t, a.Hash(), b.Hash())
	assert.NotEqual(t, a.Hash(), c.Hash())
}
