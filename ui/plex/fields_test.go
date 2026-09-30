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
	"errors"
	"net/http"
	"net/url"
	"testing"

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
