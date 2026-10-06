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

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/ui/plex"
	"github.com/mediactl/clustarr/ui/projection"
)

const (
	moviePlex   = "5d776b83fb0d55001f56a04b"
	showPlex    = "5d9c086c7d06d9001ffd27aa"
	season1Plex = "5d9c09de08fddd001f2afb4c"
	ep1Plex     = "5d9c127e4eefaa001f6449c2"
)

// plexObjects are the fixtures with Plex ids: the movie, the show, its
// season 1 and its s01e01 -- s01e02 onward and season 2 have none, a
// partly resolved show.
func plexObjects() []client.Object {
	m := fixtureMovie()
	if m.Status.Metadata.ExternalIDs == nil {
		m.Status.Metadata.ExternalIDs = map[string]string{}
	}
	m.Status.Metadata.ExternalIDs["plex"] = moviePlex
	s, eps := fixtureSeriesAndEpisodes()
	s.Status.Metadata.ExternalIDs["plex"] = showPlex
	s.Status.Metadata.PlexSeasons = []catalogv1.PlexSeasonRef{{Number: 1, ID: season1Plex}}
	eps[0].Status.PlexID = ep1Plex
	objs := []client.Object{m, s}
	for _, e := range eps {
		objs = append(objs, e)
	}
	return objs
}

func plexGUIDHandler(t *testing.T, on bool, objs ...client.Object) http.Handler {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
	return plex.Handler(plex.Options{
		ExternalURL: "https://clustarr.example",
		PlexGUIDs:   on,
		Index:       func(ctx context.Context) (*projection.Index, error) { return projection.BuildIndex(ctx, c) },
	})
}

type guids struct {
	RatingKey       string `json:"ratingKey"`
	Guid            string `json:"guid"`
	ParentGuid      string `json:"parentGuid"`
	GrandparentGuid string `json:"grandparentGuid"`
	// Guids claims the Guid[] array, which encoding/json would otherwise
	// match case-insensitively onto guid.
	Guids []struct {
		ID string `json:"id"`
	} `json:"Guid"`
}

func firstGUIDs(t *testing.T, body []byte) guids {
	t.Helper()
	var out struct {
		MediaContainer struct {
			Metadata []guids `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	require.NoError(t, decodeJSON(body, &out))
	require.NotEmpty(t, out.MediaContainer.Metadata, string(body))
	return out.MediaContainer.Metadata[0]
}

func TestAMovieIsAnsweredWithItsPlexGUID(t *testing.T) {
	h := plexGUIDHandler(t, true, plexObjects()...)
	rec := getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID))
	require.Equal(t, http.StatusOK, rec.Code)
	g := firstGUIDs(t, rec.Body.Bytes())
	require.Equal(t, "plex://movie/"+moviePlex, g.Guid)
	require.Equal(t, string(movieUID), g.RatingKey, "the ratingKey stays clustarr's")

	rec = postJSON(t, h, "/plex/movies/library/metadata/matches", map[string]any{"type": 1, "title": "Skyfall Protocol", "year": 2015})
	require.Equal(t, "plex://movie/"+moviePlex, firstGUIDs(t, rec.Body.Bytes()).Guid, "a match result too")
}

func TestWithPlexGUIDsOffEveryGUIDIsClustarrs(t *testing.T) {
	h := plexGUIDHandler(t, false, plexObjects()...)
	g := firstGUIDs(t, getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID)).Body.Bytes())
	require.Equal(t, plex.GUID(plex.MoviesIdentifier, "movie", string(movieUID)), g.Guid)
}

func TestAnEpisodeAndItsParentsAreAnsweredWithTheirPlexGUIDs(t *testing.T) {
	h := plexGUIDHandler(t, true, plexObjects()...)
	g := firstGUIDs(t, getJSON(t, h, "/plex/tv/library/metadata/"+string(episodeUID(1))).Body.Bytes())
	require.Equal(t, "plex://episode/"+ep1Plex, g.Guid)
	require.Equal(t, "plex://season/"+season1Plex, g.ParentGuid)
	require.Equal(t, "plex://show/"+showPlex, g.GrandparentGuid)

	g = firstGUIDs(t, getJSON(t, h, "/plex/tv/library/metadata/"+string(seriesUID)+"-s01").Body.Bytes())
	require.Equal(t, "plex://season/"+season1Plex, g.Guid)
	require.Equal(t, "plex://show/"+showPlex, g.ParentGuid)
}

// A show only partly known to Plex: an episode and a season without a Plex
// id keep clustarr's GUIDs beside siblings that have theirs.
func TestAPartlyResolvedShowMixesGUIDs(t *testing.T) {
	h := plexGUIDHandler(t, true, plexObjects()...)
	g := firstGUIDs(t, getJSON(t, h, "/plex/tv/library/metadata/"+string(episodeUID(4))).Body.Bytes()) // s02e01
	require.Equal(t, plex.GUID(plex.TVIdentifier, "episode", string(episodeUID(4))), g.Guid)
	require.Equal(t, plex.GUID(plex.TVIdentifier, "season", string(seriesUID)+"-s02"), g.ParentGuid)
	require.Equal(t, "plex://show/"+showPlex, g.GrandparentGuid)
}

// A malformed Plex id in status (hand-edited, uppercase, short) is never
// published: the item keeps clustarr's own GUID.
func TestAMalformedPlexIDKeepsClustarrsGUID(t *testing.T) {
	m := fixtureMovie()
	m.Status.Metadata.ExternalIDs = map[string]string{"plex": "5D776B83FB0D55001F56A04B"}
	h := plexGUIDHandler(t, true, m)
	g := firstGUIDs(t, getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID)).Body.Bytes())
	require.Equal(t, plex.GUID(plex.MoviesIdentifier, "movie", string(movieUID)), g.Guid)
}

// PMS fetches an item it holds under a plex:// GUID by the Plex id (spike
// 3: GET .../library/metadata/5d776b83fb0d55001f56a04b). Every ratingKey
// route resolves one, with the flag on or off.
func TestEveryRouteResolvesAPlexID(t *testing.T) {
	for _, on := range []bool{true, false} {
		h := plexGUIDHandler(t, on, plexObjects()...)
		for _, path := range []string{
			"/plex/movies/library/metadata/" + moviePlex,
			"/plex/movies/library/metadata/" + moviePlex + "/images",
			"/plex/tv/library/metadata/" + showPlex,
			"/plex/tv/library/metadata/" + showPlex + "/children",
			"/plex/tv/library/metadata/" + showPlex + "/grandchildren",
			"/plex/tv/library/metadata/" + season1Plex,
			"/plex/tv/library/metadata/" + season1Plex + "/children",
			"/plex/tv/library/metadata/" + ep1Plex,
		} {
			rec := getJSON(t, h, path)
			require.Equal(t, http.StatusOK, rec.Code, "%s (plexGUIDs=%t): %s", path, on, rec.Body.String())
		}
		g := firstGUIDs(t, getJSON(t, h, "/plex/movies/library/metadata/"+moviePlex).Body.Bytes())
		require.Equal(t, string(movieUID), g.RatingKey)
	}
}

// A Plex id names one type, and only the root declaring it answers, as
// with a clustarr ratingKey.
func TestThePlexIDOfAnotherRootsTypeIsNotFound(t *testing.T) {
	h := plexGUIDHandler(t, true, plexObjects()...)
	for _, path := range []string{
		"/plex/movies/library/metadata/" + showPlex,
		"/plex/movies/library/metadata/" + ep1Plex,
		"/plex/tv/library/metadata/" + moviePlex,
		"/plex/tv/library/metadata/" + moviePlex + "/children",
		"/plex/tv/library/metadata/000000000000000000000000",
	} {
		require.Equal(t, http.StatusNotFound, getJSON(t, h, path).Code, path)
	}
}

func TestAMatchByAPlexGUIDHint(t *testing.T) {
	h := plexGUIDHandler(t, true, plexObjects()...)
	rec := postJSON(t, h, "/plex/movies/library/metadata/matches", map[string]any{"type": 1, "guid": "plex://movie/" + moviePlex})
	require.Equal(t, string(movieUID), firstGUIDs(t, rec.Body.Bytes()).RatingKey)
	rec = postJSON(t, h, "/plex/tv/library/metadata/matches", map[string]any{"type": 2, "guid": "plex://show/" + showPlex})
	require.Equal(t, string(seriesUID), firstGUIDs(t, rec.Body.Bytes()).RatingKey)
	// A show's id hinted to the movies root matches nothing by guid.
	rec = postJSON(t, h, "/plex/movies/library/metadata/matches", map[string]any{"type": 1, "guid": "plex://show/" + showPlex})
	var out struct {
		MediaContainer struct {
			Metadata []guids `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	require.NoError(t, decodeJSON(rec.Body.Bytes(), &out))
	require.Empty(t, out.MediaContainer.Metadata)
}
