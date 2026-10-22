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

package plex_test

import (
	"context"
	"sync"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata/extended"
	"github.com/mediactl/clustarr/ui/plex"
	"github.com/mediactl/clustarr/ui/projection"
)

// photoURL stands in for the ui's signed /art/search proxy.
func photoURL(src string) string {
	return externalURLFixture + "/art/search?src=" + url.QueryEscape(src)
}

// newFullHandler is newTestHandler with the extended-metadata read and the
// photo proxy wired, as ui/routes.go wires them.
func newFullHandler(t *testing.T, ext func(context.Context, commonv1.MediaKind, types.UID) (extended.Doc, bool, error), objs ...client.Object) http.Handler {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
	return plex.Handler(plex.Options{
		ExternalURL: externalURLFixture,
		Index:       func(ctx context.Context) (*projection.Index, error) { return projection.BuildIndex(ctx, c) },
		Extended:    ext,
		PhotoURL:    photoURL,
	})
}

func docs(m map[types.UID]extended.Doc) func(context.Context, commonv1.MediaKind, types.UID) (extended.Doc, bool, error) {
	return func(_ context.Context, _ commonv1.MediaKind, uid types.UID) (extended.Doc, bool, error) {
		d, ok := m[uid]
		return d, ok, nil
	}
}

// metadataOf GETs one item and returns its Metadata object as a map, so a
// test asserts on the JSON Plex receives.
func metadataOf(t *testing.T, h http.Handler, path string) map[string]any {
	t.Helper()
	rec := getJSON(t, h, path)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var body struct {
		MediaContainer struct {
			Metadata []map[string]any `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	require.NoError(t, decodeJSON(rec.Body.Bytes(), &body))
	require.Len(t, body.MediaContainer.Metadata, 1)
	return body.MediaContainer.Metadata[0]
}

func TestMovieEmitsTheFullMetadataFields(t *testing.T) {
	m := fixtureMovie()
	m.Status.Metadata.Tagline = "Nothing is what it seems."
	m.Status.Metadata.Studios = []string{"Film4", "The Bureau"}
	m.Status.Metadata.Countries = []string{"United Kingdom"}
	m.Status.Metadata.Adult = true
	m.Status.Metadata.Certification = "15"
	remake := fixtureMovieRemake() // tmdb 9001, in the catalog
	h := newFullHandler(t, docs(map[types.UID]extended.Doc{m.UID: {
		Role:     []extended.Person{{Name: "Tom Cullen", Character: "Russell", Order: 0, Photo: "https://image.tmdb.org/t/p/w185/tom.jpg"}},
		Director: []extended.Person{{Name: "Andrew Haigh", Job: "Director"}},
		Writer:   []extended.Person{{Name: "Andrew Haigh", Job: "Screenplay"}},
		Producer: []extended.Person{{Name: "Tristan Goligher", Job: "Producer"}},
		Similar:  []extended.Similar{{Title: "Skyfall Protocol", Year: 1999, TmdbID: 9001}, {Title: "Badlands", Year: 1973, TmdbID: 3133}},
	}}), m, remake)

	md := metadataOf(t, h, "/plex/movies/library/metadata/"+string(m.UID))
	assert.Equal(t, "Nothing is what it seems.", md["tagline"])
	assert.Equal(t, "Film4", md["studio"])
	assert.Equal(t, []any{map[string]any{"tag": "Film4"}, map[string]any{"tag": "The Bureau"}}, md["Studio"])
	assert.Equal(t, []any{map[string]any{"tag": "United Kingdom"}}, md["Country"])
	assert.Equal(t, true, md["isAdult"])
	assert.Equal(t, "15", md["contentRating"])
	assert.Equal(t, []any{map[string]any{
		"tag": "Tom Cullen", "role": "Russell", "thumb": photoURL("https://image.tmdb.org/t/p/w185/tom.jpg"),
	}}, md["Role"])
	assert.Equal(t, []any{map[string]any{"tag": "Andrew Haigh", "role": "Director"}}, md["Director"])
	assert.Equal(t, []any{map[string]any{"tag": "Andrew Haigh", "role": "Screenplay"}}, md["Writer"])
	assert.Equal(t, []any{map[string]any{"tag": "Tristan Goligher", "role": "Producer"}}, md["Producer"])
	assert.Equal(t, []any{
		map[string]any{"guid": "tv.plex.agents.custom.clustarr.movies://movie/" + string(remake.UID), "tag": "Skyfall Protocol"},
		map[string]any{"guid": "tmdb://3133", "tag": "Badlands"},
	}, md["Similar"])
	for _, img := range md["Image"].([]any) {
		assert.Equal(t, "Skyfall Protocol", img.(map[string]any)["alt"])
	}
}

func TestShowEmitsNetworksAndItsStoredSeasonType(t *testing.T) {
	s, eps := fixtureSeriesAndEpisodes()
	s.Status.Metadata.Networks = []string{"AMC", "AMC+"}
	s.Status.Metadata.Studios = []string{"Harbor Films"}
	s.Status.Metadata.SeasonTypes = []catalogv1.SeasonTypeRef{{ID: "official", Name: "Aired Order"}, {ID: "dvd", Name: "DVD Order"}}
	objs := []client.Object{s}
	for _, e := range eps {
		objs = append(objs, e)
	}
	h := newFullHandler(t, docs(map[types.UID]extended.Doc{s.UID: {Role: []extended.Person{{Name: "Jane Doe", Character: "Mayor"}}}}), objs...)

	md := metadataOf(t, h, "/plex/tv/library/metadata/"+string(s.UID))
	assert.Equal(t, []any{map[string]any{"tag": "AMC"}, map[string]any{"tag": "AMC+"}}, md["Network"])
	assert.Equal(t, "Harbor Films", md["studio"])
	assert.Equal(t, []any{map[string]any{"id": "official", "source": "tvdb", "tag": "Aired Order", "title": "TheTVDB (Aired Order)"}}, md["SeasonType"],
		"only the order clustarr stores for the series")
	assert.Equal(t, []any{map[string]any{"tag": "Jane Doe", "role": "Mayor"}}, md["Role"])
}

func TestSeasonUsesItsOwnPosterAndSpecialsIsNamed(t *testing.T) {
	s, eps := fixtureSeriesAndEpisodes()
	s.Status.Seasons = append(s.Status.Seasons, catalogv1.SeasonStatus{Number: 0})
	s.Status.Metadata.SeasonImages = []catalogv1.SeasonImage{{Season: 1, Type: catalogv1.ImageTypePoster, URL: "https://artworks.thetvdb.com/s1.jpg"}}
	objs := []client.Object{s}
	for _, e := range eps {
		objs = append(objs, e)
	}
	h := newFullHandler(t, nil, objs...)

	md := metadataOf(t, h, "/plex/tv/library/metadata/"+plex.SeasonKey(s.UID, 1))
	assert.Equal(t, photoURL("https://artworks.thetvdb.com/s1.jpg"), md["thumb"])
	assert.NotEmpty(t, md["parentArt"])
	assert.NotEqual(t, md["thumb"], md["parentThumb"], "the season's own poster, not the series'")

	md = metadataOf(t, h, "/plex/tv/library/metadata/"+plex.SeasonKey(s.UID, 0))
	assert.Equal(t, "Specials", md["title"])
	assert.Equal(t, md["parentThumb"], md["thumb"], "no season poster: the series'")
}

func TestEpisodeEmitsItsStillAndInheritedFields(t *testing.T) {
	s, eps := fixtureSeriesAndEpisodes()
	eps[0].Status.Images = []catalogv1.Image{{Type: catalogv1.ImageTypeScreenshot, URL: "https://artworks.thetvdb.com/e1.jpg"}}
	objs := []client.Object{s}
	for _, e := range eps {
		objs = append(objs, e)
	}
	h := newFullHandler(t, nil, objs...)

	md := metadataOf(t, h, "/plex/tv/library/metadata/"+string(eps[0].UID))
	still := photoURL("https://artworks.thetvdb.com/e1.jpg")
	assert.Equal(t, still, md["thumb"])
	assert.Equal(t, []any{map[string]any{"type": "snapshot", "url": still, "alt": eps[0].Status.Title}}, md["Image"])
	assert.NotEmpty(t, md["parentArt"])
	assert.Equal(t, md["parentArt"], md["grandparentArt"])
	assert.Equal(t, "TV-14", md["contentRating"], "the series' rating")
}

// An episode answers its own TVDB id in Guid[], and its guest cast and
// crew from its extended document (written by the metadata gateway's
// series refresh, 2026-10-07).
func TestEpisodeEmitsItsTVDBGuidAndItsPeople(t *testing.T) {
	s, eps := fixtureSeriesAndEpisodes()
	eps[0].Status.TvdbID = 295294
	objs := []client.Object{s}
	for _, e := range eps {
		objs = append(objs, e)
	}
	asked := map[commonv1.MediaKind]int{}
	ext := func(_ context.Context, kind commonv1.MediaKind, uid types.UID) (extended.Doc, bool, error) {
		asked[kind]++
		if uid != eps[0].UID {
			return extended.Doc{}, false, nil
		}
		return extended.Doc{
			Role:     []extended.Person{{Name: "Noel Clarke", Character: "Mickey Smith"}},
			Director: []extended.Person{{Name: "Keith Boak", Job: "Director"}},
			Writer:   []extended.Person{{Name: "Russell T Davies", Job: "Writer"}},
		}, true, nil
	}
	h := newFullHandler(t, ext, objs...)

	md := metadataOf(t, h, "/plex/tv/library/metadata/"+string(eps[0].UID))
	assert.Equal(t, []any{map[string]any{"id": "tvdb://295294"}}, md["Guid"])
	assert.Equal(t, []any{map[string]any{"tag": "Noel Clarke", "role": "Mickey Smith"}}, md["Role"])
	assert.Equal(t, []any{map[string]any{"tag": "Keith Boak", "role": "Director"}}, md["Director"])
	assert.Equal(t, []any{map[string]any{"tag": "Russell T Davies", "role": "Writer"}}, md["Writer"])
	assert.Equal(t, 1, asked[commonv1.MediaKindEpisode])

	other := metadataOf(t, h, "/plex/tv/library/metadata/"+string(eps[1].UID))
	assert.NotContains(t, other, "Guid", "an episode with no TVDB id has none")
	assert.NotContains(t, other, "Role")
}

// PMS refreshes a show by matching the show and then each season, and
// GETting each with includeChildren=1: a season's episodes come from that
// answer alone, and PMS asks for no episode by itself (talos, 2026-10-22,
// PMS's debug log), so not one of 11,083 episodes held its crew. An
// episode's guest cast and crew are on its season's children, and on the
// paged /children and /grandchildren listings too.
func TestEpisodesCarryTheirPeopleWhereverTheirSeasonListsThem(t *testing.T) {
	s, eps := fixtureSeriesAndEpisodes()
	objs := []client.Object{s}
	for _, e := range eps {
		objs = append(objs, e)
	}
	people := extended.Doc{
		Role:     []extended.Person{{Name: "Noel Clarke", Character: "Mickey Smith"}},
		Director: []extended.Person{{Name: "Keith Boak", Job: "Director"}},
		Writer:   []extended.Person{{Name: "Russell T Davies", Job: "Writer"}},
	}
	h := newFullHandler(t, docs(map[types.UID]extended.Doc{eps[0].UID: people}), objs...)
	season := plex.SeasonKey(s.UID, eps[0].Spec.SeasonNumber)

	assertPeople := func(t *testing.T, where string, items []any) {
		t.Helper()
		var found bool
		for _, it := range items {
			md := it.(map[string]any)
			if md["ratingKey"] != plex.RatingKey(eps[0].UID) {
				assert.NotContains(t, md, "Director", "%s: an episode with no document has no people", where)
				continue
			}
			found = true
			assert.Equal(t, []any{map[string]any{"tag": "Noel Clarke", "role": "Mickey Smith"}}, md["Role"], where)
			assert.Equal(t, []any{map[string]any{"tag": "Keith Boak", "role": "Director"}}, md["Director"], where)
			assert.Equal(t, []any{map[string]any{"tag": "Russell T Davies", "role": "Writer"}}, md["Writer"], where)
		}
		assert.True(t, found, "%s lists the episode", where)
	}

	withChildren := metadataOf(t, h, "/plex/tv/library/metadata/"+season+"?includeChildren=1")
	children, ok := withChildren["Children"].(map[string]any)
	require.True(t, ok, "the season answers its Children")
	assertPeople(t, "includeChildren=1", children["Metadata"].([]any))

	for _, path := range []string{
		"/plex/tv/library/metadata/" + season + "/children",
		"/plex/tv/library/metadata/" + plex.RatingKey(s.UID) + "/grandchildren",
	} {
		rec := getJSON(t, h, path)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var body struct {
			MediaContainer struct {
				Metadata []any `json:"Metadata"`
			} `json:"MediaContainer"`
		}
		require.NoError(t, decodeJSON(rec.Body.Bytes(), &body))
		assertPeople(t, path, body.MediaContainer.Metadata)
	}
}

func TestNoExtendedDocMeansNoPeopleAndAnErrorIsNotAFailure(t *testing.T) {
	m := fixtureMovie()
	for name, ext := range map[string]func(context.Context, commonv1.MediaKind, types.UID) (extended.Doc, bool, error){
		"absent": docs(nil),
		"error": func(context.Context, commonv1.MediaKind, types.UID) (extended.Doc, bool, error) {
			return extended.Doc{}, false, errors.New("nats: timeout")
		},
		"unwired": nil,
	} {
		t.Run(name, func(t *testing.T) {
			md := metadataOf(t, newFullHandler(t, ext, m), "/plex/movies/library/metadata/"+string(m.UID))
			assert.NotContains(t, md, "Role")
			assert.NotContains(t, md, "Similar")
		})
	}
}

// The extended document is read under a deadline of its own: a stalled
// bus costs Plex its people, never the whole metadata request.
func TestTheExtendedReadHasADeadlineOfItsOwn(t *testing.T) {
	m := fixtureMovie()
	var deadline time.Duration
	ext := func(ctx context.Context, _ commonv1.MediaKind, _ types.UID) (extended.Doc, bool, error) {
		dl, ok := ctx.Deadline()
		if ok {
			deadline = time.Until(dl)
		}
		return extended.Doc{}, false, context.DeadlineExceeded
	}
	md := metadataOf(t, newFullHandler(t, ext, m), "/plex/movies/library/metadata/"+string(m.UID))
	assert.NotEmpty(t, md["title"], "the rest of the response still answers")
	assert.Positive(t, deadline, "the read carries a deadline")
	assert.LessOrEqual(t, deadline, plex.ExtendedReadTimeout)
}

// TestOriginalLanguageFieldsOnlyWhenAskedInAnotherLanguage: Your Name is
// Japanese; asked in English, Plex gets the Japanese title, genres and
// images as the original-language fields, and asked in Japanese it gets
// none of them.
func TestOriginalLanguageFieldsOnlyWhenAskedInAnotherLanguage(t *testing.T) {
	m := fixtureMovie()
	m.Status.Metadata.OriginalLanguage = "ja"
	m.Status.Metadata.OriginalTitle = "君の名は。"
	m.Status.Metadata.Genres = []string{"Animation", "Drama"}
	m.Status.Metadata.OriginalGenres = []string{"アニメーション", "ドラマ"}
	m.Status.Metadata.Images = append(m.Status.Metadata.Images,
		catalogv1.Image{Type: catalogv1.ImageTypePoster, URL: "https://image.tmdb.org/t/p/w500/ja.jpg", Language: "ja"})
	m.Status.Metadata.Certifications = []catalogv1.Certification{{Country: "JP", Rating: "G"}, {Country: "US", Rating: "PG"}}
	m.Status.Metadata.Certification = "PG"
	h := newFullHandler(t, nil, m)
	path := "/plex/movies/library/metadata/" + string(m.UID)

	md := metadataOf(t, h, path+"?X-Plex-Language=en-US")
	assert.Equal(t, "君の名は。", md["originalTitle"])
	assert.Equal(t, []any{
		map[string]any{"tag": "Animation", "originalTag": "アニメーション"},
		map[string]any{"tag": "Drama", "originalTag": "ドラマ"},
	}, md["Genre"])
	assert.Equal(t, []any{map[string]any{"type": "coverPoster", "url": photoURL("https://image.tmdb.org/t/p/w500/ja.jpg"), "alt": "君の名は。"}}, md["OriginalImage"])
	assert.Equal(t, "PG", md["contentRating"])

	md = metadataOf(t, h, path+"?X-Plex-Language=ja-JP")
	assert.NotContains(t, md, "originalTitle")
	assert.NotContains(t, md, "OriginalImage")
	assert.Equal(t, "jp/G", md["contentRating"], "the language's country when no X-Plex-Country")
}

// TestMatchHonoursResponseCustomizationInTheBody: Plex sends includeFields
// and includeElements in a match request's JSON body.
func TestMatchHonoursResponseCustomizationInTheBody(t *testing.T) {
	s, eps := fixtureSeriesAndEpisodes()
	objs := []client.Object{s}
	for _, e := range eps {
		objs = append(objs, e)
	}
	h := newFullHandler(t, nil, objs...)
	rec := postJSON(t, h, "/plex/tv/library/metadata/matches", map[string]any{
		"type": 3, "parentTitle": "Harborview", "index": 1, "includeChildren": 1,
		"includeElements": "Metadata,Children", "includeFields": "guid,title,index",
	})
	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		MediaContainer struct {
			Metadata []map[string]any `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	require.NoError(t, decodeJSON(rec.Body.Bytes(), &body))
	season := body.MediaContainer.Metadata[0]
	assert.ElementsMatch(t, []string{"guid", "title", "index", "ratingKey", "key", "type", "Children"}, keys(season))
	child := season["Children"].(map[string]any)["Metadata"].([]any)[0].(map[string]any)
	assert.ElementsMatch(t, []string{"guid", "title", "index", "ratingKey", "key", "type"}, keys(child))
}

// TestAnEpisodeOrderClustarrDoesNotStoreHasNoSeasons: the protocol says no
// season data for an order the provider has none for.
func TestAnEpisodeOrderClustarrDoesNotStoreHasNoSeasons(t *testing.T) {
	s, eps := fixtureSeriesAndEpisodes() // official, the default
	objs := []client.Object{s}
	for _, e := range eps {
		objs = append(objs, e)
	}
	h := newFullHandler(t, nil, objs...)
	path := "/plex/tv/library/metadata/" + string(s.UID)

	md := metadataOf(t, h, path+"?includeChildren=1&episodeOrder=official")
	assert.NotEmpty(t, md["Children"].(map[string]any)["Metadata"])
	md = metadataOf(t, h, path+"?includeChildren=1&episodeOrder=dvd")
	assert.Empty(t, md["Children"].(map[string]any)["Metadata"])

	rec := getJSON(t, h, path+"/children?episodeOrder=dvd")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `0`, string(mustField(t, rec.Body.Bytes(), "totalSize")))
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func mustField(t *testing.T, body []byte, field string) []byte {
	t.Helper()
	var c struct {
		MediaContainer map[string]json.RawMessage `json:"MediaContainer"`
	}
	require.NoError(t, json.Unmarshal(body, &c))
	return c.MediaContainer[field]
}

// TestShowThemeIsPlexsOwnThemeByTVDBID: Plex's own TV theme host serves an
// MP3 per TVDB id (verified reachable 2026-09-30), the one source of theme
// music there is.
func TestShowThemeIsPlexsOwnThemeByTVDBID(t *testing.T) {
	s, _ := fixtureSeriesAndEpisodes()
	md := metadataOf(t, newFullHandler(t, nil, s), "/plex/tv/library/metadata/"+string(s.UID))
	assert.Equal(t, "https://tvthemes.plexapp.com/298762.mp3", md["theme"])
}

// An episodeOrder clustarr never advertises -- a stale id from the show's
// previous agent, or Plex's own -- is answered with the stored order's
// seasons, not none: only an order clustarr knows and does not store is.
func TestAnEpisodeOrderClustarrDoesNotKnowGetsTheStoredSeasons(t *testing.T) {
	s, eps := fixtureSeriesAndEpisodes()
	objs := []client.Object{s}
	for _, e := range eps {
		objs = append(objs, e)
	}
	h := newFullHandler(t, nil, objs...)
	md := metadataOf(t, h, "/plex/tv/library/metadata/"+string(s.UID)+"?includeChildren=1&episodeOrder=tvdbAiring")
	assert.NotEmpty(t, md["Children"].(map[string]any)["Metadata"])
}

// A season named by its provider is titled by that name (TheTVDB's "Book 1:
// Water", "Steel Ball Run"): the series' own order's, else the aired
// order's, as its poster is chosen; an unnamed season stays "Season N", and
// an episode's parent carries the same title.
func TestSeasonIsTitledByItsOwnName(t *testing.T) {
	s, eps := fixtureSeriesAndEpisodes()
	s.Status.Metadata.SeasonTitles = []catalogv1.SeasonTitle{
		{Season: 1, Title: "Book 1: Water"},
		{Season: 1, Title: "Book One (DVD)", Order: "dvd"},
		{Season: 2, Title: "Book 2: Earth", Order: "official"},
	}
	objs := []client.Object{s}
	for _, e := range eps {
		objs = append(objs, e)
	}
	h := newFullHandler(t, nil, objs...)
	md := metadataOf(t, h, "/plex/tv/library/metadata/"+plex.SeasonKey(s.UID, 1))
	assert.Equal(t, "Book 1: Water", md["title"], "the aired order's title, the series' order")
	assert.EqualValues(t, 1, md["index"], "the number stays the index")
	md = metadataOf(t, h, "/plex/tv/library/metadata/"+plex.SeasonKey(s.UID, 2))
	assert.Equal(t, "Book 2: Earth", md["title"])
	md = metadataOf(t, h, "/plex/tv/library/metadata/"+string(eps[0].UID))
	assert.Equal(t, "Book 1: Water", md["parentTitle"])

	s.Spec.EpisodeOrder = catalogv1.EpisodeOrderDVD
	h = newFullHandler(t, nil, objs...)
	md = metadataOf(t, h, "/plex/tv/library/metadata/"+plex.SeasonKey(s.UID, 1))
	assert.Equal(t, "Book One (DVD)", md["title"], "the series' own order's title")
	md = metadataOf(t, h, "/plex/tv/library/metadata/"+plex.SeasonKey(s.UID, 2))
	assert.Equal(t, "Book 2: Earth", md["title"], "no DVD title: the aired order's")
}

// An image set's overrides (spec.seasonArtwork) are what a season and an
// episode show: the season's poster and background and the episode's
// thumbnail, through the photo proxy, ahead of TheTVDB's; a season or
// episode the set does not name keeps TheTVDB's.
func TestSeasonAndEpisodeTakeTheImageSetsOverrides(t *testing.T) {
	s, eps := fixtureSeriesAndEpisodes()
	s.Status.Metadata.SeasonImages = []catalogv1.SeasonImage{
		{Season: 1, Type: catalogv1.ImageTypePoster, URL: "https://artworks.thetvdb.com/s1.jpg"},
		{Season: 2, Type: catalogv1.ImageTypePoster, URL: "https://artworks.thetvdb.com/s2.jpg"},
	}
	one := int32(1)
	s.Spec.SeasonArtwork = []catalogv1.SeasonArtworkOverride{
		{Season: 1, Type: catalogv1.ImageTypePoster, URL: "https://api.mediux.pro/assets/s1"},
		{Season: 1, Type: catalogv1.ImageTypeFanart, URL: "https://api.mediux.pro/assets/s1-bg"},
		{Season: 1, Episode: &one, Type: catalogv1.ImageTypeScreenshot, URL: "https://api.mediux.pro/assets/e1"},
	}
	objs := []client.Object{s}
	for _, e := range eps {
		objs = append(objs, e)
	}
	h := newFullHandler(t, nil, objs...)
	md := metadataOf(t, h, "/plex/tv/library/metadata/"+plex.SeasonKey(s.UID, 1))
	assert.Equal(t, photoURL("https://api.mediux.pro/assets/s1"), md["thumb"], "the set's season poster, not TheTVDB's")
	assert.Equal(t, photoURL("https://api.mediux.pro/assets/s1-bg"), md["art"], "the set's season background, not the series'")
	md = metadataOf(t, h, "/plex/tv/library/metadata/"+plex.SeasonKey(s.UID, 2))
	assert.Equal(t, photoURL("https://artworks.thetvdb.com/s2.jpg"), md["thumb"], "a season the set does not name keeps TheTVDB's")
	assert.NotEqual(t, photoURL("https://api.mediux.pro/assets/s1-bg"), md["art"])
	var first *catalogv1.Episode
	for _, e := range eps {
		if e.Spec.SeasonNumber == 1 && e.Spec.EpisodeNumber == 1 {
			first = e
		}
	}
	if assert.NotNil(t, first, "the fixture has S01E01") {
		md = metadataOf(t, h, "/plex/tv/library/metadata/"+string(first.UID))
		assert.Equal(t, photoURL("https://api.mediux.pro/assets/e1"), md["thumb"], "the set's episode thumbnail")
		assert.Equal(t, photoURL("https://api.mediux.pro/assets/s1"), md["parentThumb"], "its season's poster from the set")
	}
}

// An episode whose own extended document lists no cast carries its show's
// cast (TheTVDB names an episode's guest stars and crew alone; Cyberpunk:
// Edgerunners' episodes list producers only, 2026-10-21); one with its own
// keeps it. The show's document is read once for a season's listing.
func TestEpisodeWithoutItsOwnCastCarriesTheShows(t *testing.T) {
	s, eps := fixtureSeriesAndEpisodes()
	objs := []client.Object{s}
	for _, e := range eps {
		objs = append(objs, e)
	}
	seriesCast := []extended.Person{{Name: "KENN", Character: "David Martinez"}, {Name: "Aoi Yuki", Character: "Lucy"}}
	d := map[types.UID]extended.Doc{
		s.UID:      {Role: seriesCast},
		eps[0].UID: {Producer: []extended.Person{{Name: "Rafal Jaki", Job: "Executive Producer"}}},
		eps[1].UID: {Role: []extended.Person{{Name: "Guest", Character: "Someone"}}},
	}
	reads := map[commonv1.MediaKind]int{}
	var mu sync.Mutex
	read := docs(d)
	counting := func(ctx context.Context, kind commonv1.MediaKind, uid types.UID) (extended.Doc, bool, error) {
		mu.Lock()
		reads[kind]++
		mu.Unlock()
		return read(ctx, kind, uid)
	}
	h := newFullHandler(t, counting, objs...)
	md := metadataOf(t, h, "/plex/tv/library/metadata/"+string(eps[0].UID))
	assert.Equal(t, []any{map[string]any{"tag": "KENN", "role": "David Martinez"}, map[string]any{"tag": "Aoi Yuki", "role": "Lucy"}}, md["Role"],
		"the show's cast on an episode with none of its own")
	assert.Equal(t, []any{map[string]any{"tag": "Rafal Jaki", "role": "Executive Producer"}}, md["Producer"], "its own producers stay")
	md = metadataOf(t, h, "/plex/tv/library/metadata/"+string(eps[1].UID))
	assert.Equal(t, []any{map[string]any{"tag": "Guest", "role": "Someone"}}, md["Role"], "an episode's own cast is its own")

	mu.Lock()
	reads = map[commonv1.MediaKind]int{}
	mu.Unlock()
	season := metadataOf(t, h, "/plex/tv/library/metadata/"+plex.SeasonKey(s.UID, 1)+"?includeChildren=1")
	children := season["Children"].(map[string]any)["Metadata"].([]any)
	assert.GreaterOrEqual(t, len(children), 2)
	for _, c := range children {
		assert.NotEmpty(t, c.(map[string]any)["Role"], "every listed episode carries a cast")
	}
	mu.Lock()
	assert.Equal(t, 1, reads[commonv1.MediaKindSeries], "the show's document is read once for the listing")
	mu.Unlock()
}
