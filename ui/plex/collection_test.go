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
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata/extended"
	"github.com/mediactl/clustarr/ui/plex"
	"github.com/mediactl/clustarr/ui/projection"
)

const (
	bttfCollectionPlex = "5ec2eb574592b6004137f444"
	bttfTMDBKey        = "tmdb-collection-264"
)

// bttfMovies are the three Back to the Future films, stored out of order,
// each in TMDB collection 264 with the Plex id the gateway learned.
func bttfMovies(plexID string) []client.Object {
	movie := func(n int, title string, year int32) *catalogv1.Movie {
		return &catalogv1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: "bttf-" + digit(int32(n)), Namespace: "default", UID: types.UID("b0000000-0000-0000-0000-00000000000" + digit(int32(n)))},
			Status: catalogv1.MovieStatus{Metadata: &catalogv1.MovieMetadata{
				Title: title, Year: year,
				Collection: &catalogv1.CollectionRef{TmdbID: 264, Name: "Back to the Future Collection", PlexID: plexID},
			}},
		}
	}
	return []client.Object{
		movie(3, "Back to the Future Part III", 1990),
		movie(1, "Back to the Future", 1985),
		movie(2, "Back to the Future Part II", 1989),
	}
}

// collectionDoc is the extended document the gateway writes for every
// member movie.
func collectionDoc(context.Context, commonv1.MediaKind, types.UID) (extended.Doc, bool, error) {
	return extended.Doc{Collection: &extended.Collection{
		Summary: "Marty McFly and Doc Brown travel through time.",
		Poster:  "https://image.tmdb.org/t/p/w500/poster.jpg",
		Art:     "https://image.tmdb.org/t/p/original/backdrop.jpg",
	}}, true, nil
}

func collectionHandler(t *testing.T, plexGUIDs bool, objs ...client.Object) http.Handler {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
	return plex.Handler(plex.Options{
		ExternalURL: "https://clustarr.example",
		PlexGUIDs:   plexGUIDs,
		Extended:    collectionDoc,
		PhotoURL:    func(src string) string { return "https://clustarr.example/art/search?u=" + src },
		Index:       func(ctx context.Context) (*projection.Index, error) { return projection.BuildIndex(ctx, c) },
	})
}

type rootResponse struct {
	MediaProvider struct {
		Types []struct {
			Type int `json:"type"`
		} `json:"Types"`
		Feature []struct {
			Type string `json:"type"`
			Key  string `json:"key"`
		} `json:"Feature"`
	} `json:"MediaProvider"`
}

func readRoot(t *testing.T, h http.Handler, path string) (types []int, features map[string]string) {
	t.Helper()
	rec := getJSON(t, h, path)
	require.Equal(t, http.StatusOK, rec.Code)
	var out rootResponse
	require.NoError(t, decodeJSON(rec.Body.Bytes(), &out))
	features = map[string]string{}
	for _, f := range out.MediaProvider.Feature {
		features[f.Type] = f.Key
	}
	for _, ty := range out.MediaProvider.Types {
		types = append(types, ty.Type)
	}
	return types, features
}

// The movies provider declares the collection feature; both providers declare match, which Plex's provider docs make
// required. Collections are movie-only ("currently only supported in movie
// libraries"), so the tv provider declares none.
func TestTheRootsDeclareMatchAndTheMoviesRootCollections(t *testing.T) {
	h := collectionHandler(t, true)

	// Not type 18: PMS 1.43.4 refuses a provider declaring it ("The
	// provider supports unsupported metadata types", 2026-10-06), though
	// Plex's docs list it; the collection feature alone is accepted.
	types, features := readRoot(t, h, "/plex/movies")
	assert.ElementsMatch(t, []int{1}, types)
	assert.Equal(t, "/library/metadata/matches", features["match"])
	assert.Equal(t, "/library/metadata", features["metadata"])
	assert.Equal(t, "/library/collections", features["collection"])

	types, features = readRoot(t, h, "/plex/tv")
	assert.ElementsMatch(t, []int{2, 3, 4}, types)
	assert.Equal(t, "/library/metadata/matches", features["match"])
	assert.NotContains(t, features, "collection")
}

type collectionEntry struct {
	Guid    string `json:"guid"`
	Key     string `json:"key"`
	Tag     string `json:"tag"`
	Summary string `json:"summary"`
	Thumb   string `json:"thumb"`
	Art     string `json:"art"`
}

func movieCollection(t *testing.T, h http.Handler, uid string) collectionEntry {
	t.Helper()
	rec := getJSON(t, h, "/plex/movies/library/metadata/"+uid)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out struct {
		MediaContainer struct {
			Metadata []struct {
				Collection []collectionEntry `json:"Collection"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	require.NoError(t, decodeJSON(rec.Body.Bytes(), &out))
	require.Len(t, out.MediaContainer.Metadata, 1)
	require.Len(t, out.MediaContainer.Metadata[0].Collection, 1)
	return out.MediaContainer.Metadata[0].Collection[0]
}

// A movie names its collection with the collection's plex:// GUID, a key
// the collection route answers, and the summary and artwork from the
// movie's extended document.
func TestAMoviesCollectionIsLinkedWithItsPlexGUID(t *testing.T) {
	h := collectionHandler(t, true, bttfMovies(bttfCollectionPlex)...)
	got := movieCollection(t, h, "b0000000-0000-0000-0000-000000000001")
	assert.Equal(t, collectionEntry{
		Guid:    "plex://collection/" + bttfCollectionPlex,
		Key:     "/library/collections/" + bttfCollectionPlex + "/children",
		Tag:     "Back to the Future Collection",
		Summary: "Marty McFly and Doc Brown travel through time.",
		Thumb:   "https://clustarr.example/art/search?u=https://image.tmdb.org/t/p/w500/poster.jpg",
		Art:     "https://clustarr.example/art/search?u=https://image.tmdb.org/t/p/original/backdrop.jpg",
	}, got)
}

// A collection Plex does not know, or the plex:// GUIDs switched off, is
// answered under the movies provider's own scheme by its TMDB id.
func TestACollectionWithoutAPlexIDIsClustarrs(t *testing.T) {
	for name, h := range map[string]http.Handler{
		"no plex id":     collectionHandler(t, true, bttfMovies("")...),
		"plex guids off": collectionHandler(t, false, bttfMovies(bttfCollectionPlex)...),
	} {
		t.Run(name, func(t *testing.T) {
			got := movieCollection(t, h, "b0000000-0000-0000-0000-000000000001")
			assert.Equal(t, plex.MoviesIdentifier+"://collection/"+bttfTMDBKey, got.Guid)
			assert.Equal(t, "/library/collections/"+bttfTMDBKey+"/children", got.Key)
		})
	}
}

type collectionMetadata struct {
	RatingKey  string `json:"ratingKey"`
	Key        string `json:"key"`
	Guid       string `json:"guid"`
	Type       string `json:"type"`
	Title      string `json:"title"`
	Summary    string `json:"summary"`
	Thumb      string `json:"thumb"`
	Art        string `json:"art"`
	ChildCount int    `json:"childCount"`
	MinYear    int    `json:"minYear"`
	MaxYear    int    `json:"maxYear"`
}

// The collection route answers the collection itself, by the Plex id PMS
// holds it under or by clustarr's own key.
func TestTheCollectionRouteAnswersTheCollection(t *testing.T) {
	h := collectionHandler(t, true, bttfMovies(bttfCollectionPlex)...)
	for _, key := range []string{bttfCollectionPlex, bttfTMDBKey} {
		t.Run(key, func(t *testing.T) {
			rec := getJSON(t, h, "/plex/movies/library/collections/"+key)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var out struct {
				MediaContainer struct {
					Identifier string               `json:"identifier"`
					Size       int                  `json:"size"`
					Metadata   []collectionMetadata `json:"Metadata"`
				} `json:"MediaContainer"`
			}
			require.NoError(t, decodeJSON(rec.Body.Bytes(), &out))
			assert.Equal(t, plex.MoviesIdentifier, out.MediaContainer.Identifier)
			require.Len(t, out.MediaContainer.Metadata, 1)
			assert.Equal(t, collectionMetadata{
				RatingKey:  bttfCollectionPlex,
				Key:        "/library/collections/" + bttfCollectionPlex + "/children",
				Guid:       "plex://collection/" + bttfCollectionPlex,
				Type:       "collection",
				Title:      "Back to the Future Collection",
				Summary:    "Marty McFly and Doc Brown travel through time.",
				Thumb:      "https://clustarr.example/art/search?u=https://image.tmdb.org/t/p/w500/poster.jpg",
				Art:        "https://clustarr.example/art/search?u=https://image.tmdb.org/t/p/original/backdrop.jpg",
				ChildCount: 3,
				MinYear:    1985,
				MaxYear:    1990,
			}, out.MediaContainer.Metadata[0])
		})
	}
}

// A collection's children are its movies in the library, oldest first,
// paged as PMS asks.
func TestACollectionsChildrenAreItsMovies(t *testing.T) {
	h := collectionHandler(t, true, bttfMovies(bttfCollectionPlex)...)
	type page struct {
		MediaContainer struct {
			Size      int `json:"size"`
			TotalSize int `json:"totalSize"`
			Metadata  []struct {
				Type  string `json:"type"`
				Title string `json:"title"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	rec := getJSON(t, h, "/plex/movies/library/collections/"+bttfCollectionPlex+"/children")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var all page
	require.NoError(t, decodeJSON(rec.Body.Bytes(), &all))
	require.Equal(t, 3, all.MediaContainer.TotalSize)
	titles := []string{}
	for _, m := range all.MediaContainer.Metadata {
		assert.Equal(t, "movie", m.Type)
		titles = append(titles, m.Title)
	}
	assert.Equal(t, []string{"Back to the Future", "Back to the Future Part II", "Back to the Future Part III"}, titles)

	rec = getJSON(t, h, "/plex/movies/library/collections/"+bttfTMDBKey+"/children?X-Plex-Container-Start=1&X-Plex-Container-Size=1")
	var one page
	require.NoError(t, decodeJSON(rec.Body.Bytes(), &one))
	assert.Equal(t, 3, one.MediaContainer.TotalSize)
	require.Len(t, one.MediaContainer.Metadata, 1)
	assert.Equal(t, "Back to the Future Part II", one.MediaContainer.Metadata[0].Title)
}

func TestAnUnknownCollectionIsNotFound(t *testing.T) {
	h := collectionHandler(t, true, bttfMovies(bttfCollectionPlex)...)
	for _, path := range []string{
		"/plex/movies/library/collections/tmdb-collection-999",
		"/plex/movies/library/collections/000000000000000000000000/children",
		"/plex/movies/library/collections/not-a-key",
		"/plex/tv/library/collections/" + bttfCollectionPlex,
	} {
		rec := getJSON(t, h, path)
		assert.Equal(t, http.StatusNotFound, rec.Code, path)
	}
}

// A collection match (type 18) finds the collection by its plex:// GUID or
// by name; another root answers none.
func TestACollectionMatchesByGUIDOrName(t *testing.T) {
	h := collectionHandler(t, true, bttfMovies(bttfCollectionPlex)...)
	type matched struct {
		MediaContainer struct {
			Metadata []collectionMetadata `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	for name, body := range map[string]map[string]any{
		"plex guid": {"type": 18, "guid": "plex://collection/" + bttfCollectionPlex},
		"own guid":  {"type": 18, "guid": plex.MoviesIdentifier + "://collection/" + bttfTMDBKey},
		"name":      {"type": 18, "title": "back to the future collection"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := postJSON(t, h, "/plex/movies/library/metadata/matches", body)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			var out matched
			require.NoError(t, decodeJSON(rec.Body.Bytes(), &out))
			require.Len(t, out.MediaContainer.Metadata, 1)
			assert.Equal(t, "plex://collection/"+bttfCollectionPlex, out.MediaContainer.Metadata[0].Guid)
			assert.Equal(t, 3, out.MediaContainer.Metadata[0].ChildCount)
		})
	}

	rec := postJSON(t, h, "/plex/tv/library/metadata/matches", map[string]any{"type": 18, "title": "Back to the Future Collection"})
	require.Equal(t, http.StatusOK, rec.Code)
	var none matched
	require.NoError(t, decodeJSON(rec.Body.Bytes(), &none))
	assert.Empty(t, none.MediaContainer.Metadata)
}
