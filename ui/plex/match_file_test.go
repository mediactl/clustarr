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
	"net/http"
	"path"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
)

// plexEngine renders paths the way importarr names files under a Plex
// RootFolder, so these fixtures are what a real library holds rather than
// strings shaped like the answer.
var plexEngine = naming.NewEngine(naming.Config{Dialect: naming.DialectPlex})

// movieRel is a movie's path relative to its RootFolder:
// "<folder>/<file>.mkv".
func movieRel(t *testing.T, m *catalogv1.Movie) string {
	t.Helper()
	c := naming.Context{
		Kind:   commonv1.MediaKindMovie,
		Title:  m.Status.Metadata.Title,
		Year:   int(m.Status.Metadata.Year),
		TmdbID: strconv.FormatInt(m.Spec.TmdbID, 10),
	}
	folder, err := plexEngine.MovieFolder(c)
	require.NoError(t, err)
	file, err := plexEngine.MovieFile(c)
	require.NoError(t, err)
	return path.Join(folder, file+".mkv")
}

// episodeRel is an episode file's path relative to its RootFolder:
// "<series>/<season>/<file>.mkv", covering every episode number in eps.
func episodeRel(t *testing.T, s *catalogv1.Series, season int32, eps ...int32) string {
	t.Helper()
	nums := make([]int, len(eps))
	for i, e := range eps {
		nums[i] = int(e)
	}
	c := naming.Context{
		Kind:        commonv1.MediaKindEpisode,
		SeriesTitle: s.Status.Metadata.Title,
		SeriesYear:  int(s.Status.Metadata.Year),
		Title:       s.Status.Metadata.Title,
		Year:        int(s.Status.Metadata.Year),
		TvdbID:      strconv.FormatInt(s.Spec.TvdbID, 10),
		Season:      int(season),
		Episodes:    nums,
	}
	seriesFolder, err := plexEngine.SeriesFolder(c)
	require.NoError(t, err)
	seasonFolder, err := plexEngine.SeasonFolder(c)
	require.NoError(t, err)
	file, err := plexEngine.EpisodeFile(c)
	require.NoError(t, err)
	return path.Join(seriesFolder, seasonFolder, file+".mkv")
}

func mediaFile(name string, kind commonv1.MediaKind, item string, p string, keys ...string) *catalogv1.MediaFile {
	return &catalogv1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: catalogv1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: kind, Name: item, Keys: keys},
			Path:     p,
		},
	}
}

// matchedRatingKeys returns the ratingKeys of a match response, in order.
func matchedRatingKeys(t *testing.T, body []byte) []string {
	t.Helper()
	var resp struct {
		MediaContainer struct {
			Metadata []struct {
				RatingKey string `json:"ratingKey"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	require.NoError(t, decodeJSON(body, &resp))
	var out []string
	for _, m := range resp.MediaContainer.Metadata {
		out = append(out, m.RatingKey)
	}
	return out
}

// TestMatchMovieByFileBeatsTitle is the remake the title rule cannot tell
// apart: two "Skyfall Protocol"s and no year in the request. The file
// decides.
func TestMatchMovieByFileBeatsTitle(t *testing.T) {
	orig, remake := fixtureMovie(), fixtureMovieRemake()
	rel := movieRel(t, remake)
	h := newTestHandler(t, externalURLFixture, orig, remake,
		mediaFile("remake-file", commonv1.MediaKindMovie, remake.Name, "/data/media/movies/"+rel))

	for _, sent := range []string{rel, "movies/" + rel} {
		rec := postJSON(t, h, "/plex/movies/library/metadata/matches", map[string]any{
			"type": 1, "title": "Skyfall Protocol", "filename": sent,
		})
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, []string{string(remakeMovieUID)}, matchedRatingKeys(t, rec.Body.Bytes()), "filename %q", sent)
	}
}

// TestMatchByFileFallsThrough covers every way rule 0 has no answer: the
// title rule then runs exactly as before.
func TestMatchByFileFallsThrough(t *testing.T) {
	orig, remake := fixtureMovie(), fixtureMovieRemake()
	rel := movieRel(t, remake)
	series, episodes := fixtureSeriesAndEpisodes()
	objs := []client.Object{
		orig, remake, series,
		// The same relative path under two RootFolders, backing two movies.
		mediaFile("a", commonv1.MediaKindMovie, orig.Name, "/data/media/movies/"+rel),
		mediaFile("b", commonv1.MediaKindMovie, remake.Name, "/data/media/movies-4k/"+rel),
		// An episode file never answers a movie request.
		mediaFile("ep", commonv1.MediaKindEpisode, episodes[0].Name, "/data/media/tv/Show/Season 01/x.mkv"),
		// A file whose item is gone.
		mediaFile("orphan", commonv1.MediaKindMovie, "deleted-movie", "/data/media/movies/Gone (2001)/Gone (2001).mkv"),
	}
	for _, e := range episodes {
		objs = append(objs, e)
	}
	h := newTestHandler(t, externalURLFixture, objs...)

	for name, filename := range map[string]string{
		"shared by two roots": rel,
		"episode file":        "Show/Season 01/x.mkv",
		"orphaned file":       "Gone (2001)/Gone (2001).mkv",
		"traversal":           "../../" + rel,
		"absolute":            "/data/media/movies/" + rel,
		"empty":               "",
		"dot":                 ".",
	} {
		t.Run(name, func(t *testing.T) {
			rec := postJSON(t, h, "/plex/movies/library/metadata/matches", map[string]any{
				"type": 1, "title": "Skyfall Protocol", "year": 2015, "filename": filename,
			})
			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, []string{string(movieUID)}, matchedRatingKeys(t, rec.Body.Bytes()),
				"the title rule answers, as it did before rule 0")
		})
	}
}

// TestMatchShowSeasonEpisodeByFile: Plex sends the first episode's file
// for a show or a season; for an episode, its own. A two-episode file is
// one Series, and type 4 picks the episode by the request's numbers.
func TestMatchShowSeasonEpisodeByFile(t *testing.T) {
	series, episodes := fixtureSeriesAndEpisodes()
	// episodes are S01E01..S01E03, S02E01..S02E03 in order.
	single := episodeRel(t, series, 1, 1)
	double := episodeRel(t, series, 2, 1, 2)
	objs := []client.Object{
		series,
		mediaFile("s01e01", commonv1.MediaKindEpisode, episodes[0].Name, "/data/media/tv/"+single),
		mediaFile("s02e0102", commonv1.MediaKindEpisode, episodes[3].Name, "/data/media/tv/"+double, episodes[4].Name),
	}
	for _, e := range episodes {
		objs = append(objs, e)
	}
	h := newTestHandler(t, externalURLFixture, objs...)

	// A wrong title proves the file, not the title, decided.
	rec := postJSON(t, h, "/plex/tv/library/metadata/matches", map[string]any{
		"type": 2, "title": "Not The Title", "filename": double,
	})
	require.Equal(t, []string{string(seriesUID)}, matchedRatingKeys(t, rec.Body.Bytes()), "show")

	rec = postJSON(t, h, "/plex/tv/library/metadata/matches", map[string]any{
		"type": 3, "parentTitle": "Not The Title", "index": 2, "filename": double,
	})
	require.Len(t, matchedRatingKeys(t, rec.Body.Bytes()), 1, "season")

	rec = postJSON(t, h, "/plex/tv/library/metadata/matches", map[string]any{
		"type": 4, "grandparentTitle": "Not The Title", "parentIndex": 2, "index": 2, "filename": double,
	})
	require.Equal(t, []string{string(episodes[4].UID)}, matchedRatingKeys(t, rec.Body.Bytes()), "episode of a two-episode file")

	rec = postJSON(t, h, "/plex/tv/library/metadata/matches", map[string]any{
		"type": 4, "grandparentTitle": "Not The Title", "filename": single,
	})
	require.Equal(t, []string{string(episodes[0].UID)}, matchedRatingKeys(t, rec.Body.Bytes()), "the file's only episode")
}

// TestMatchByFileNeverPicksBetweenTwoItems: the same relative path backs a
// different item under each of two RootFolders. Rule 0 must answer nothing,
// so a title that matches neither returns nothing -- preferring either file
// would return an item.
func TestMatchByFileNeverPicksBetweenTwoItems(t *testing.T) {
	orig, remake := fixtureMovie(), fixtureMovieRemake()
	rel := movieRel(t, remake)
	series, episodes := fixtureSeriesAndEpisodes()
	other := series.DeepCopy()
	other.Name, other.UID = "harborview-2", "55555555-5555-5555-5555-555555555555"
	otherEp := episodes[0].DeepCopy()
	otherEp.Name, otherEp.UID = "harborview-2-s01e01", "66666666-6666-6666-6666-666666666666"
	otherEp.OwnerReferences[0].Name, otherEp.OwnerReferences[0].UID = other.Name, other.UID
	epRel := episodeRel(t, series, 1, 1)

	objs := []client.Object{
		orig, remake, series, other, otherEp,
		mediaFile("a", commonv1.MediaKindMovie, orig.Name, "/data/media/movies/"+rel),
		mediaFile("b", commonv1.MediaKindMovie, remake.Name, "/data/media/movies-4k/"+rel),
		mediaFile("c", commonv1.MediaKindEpisode, episodes[0].Name, "/data/media/tv/"+epRel),
		mediaFile("d", commonv1.MediaKindEpisode, otherEp.Name, "/data/media/tv-4k/"+epRel),
	}
	for _, e := range episodes {
		objs = append(objs, e)
	}
	h := newTestHandler(t, externalURLFixture, objs...)

	rec := postJSON(t, h, "/plex/movies/library/metadata/matches", map[string]any{
		"type": 1, "title": "Nothing Like This", "filename": rel,
	})
	require.Empty(t, matchedRatingKeys(t, rec.Body.Bytes()), "two movies share the path")
	rec = postJSON(t, h, "/plex/tv/library/metadata/matches", map[string]any{
		"type": 2, "title": "Nothing Like This", "filename": epRel,
	})
	require.Empty(t, matchedRatingKeys(t, rec.Body.Bytes()), "two series share the path")
}

// TestAnEpisodeFileNeverAnswersAMovieRequest: a Movie and an Episode share
// a name, so without the kind filter the episode's file would resolve to
// the movie.
func TestAnEpisodeFileNeverAnswersAMovieRequest(t *testing.T) {
	m := fixtureMovie()
	series, episodes := fixtureSeriesAndEpisodes()
	ep := episodes[0].DeepCopy()
	ep.Name = m.Name
	objs := []client.Object{
		m, series, ep,
		mediaFile("ep", commonv1.MediaKindEpisode, ep.Name, "/data/media/tv/Show/Season 01/x.mkv"),
	}
	h := newTestHandler(t, externalURLFixture, objs...)
	rec := postJSON(t, h, "/plex/movies/library/metadata/matches", map[string]any{
		"type": 1, "title": "Nothing Like This", "filename": "Show/Season 01/x.mkv",
	})
	require.Empty(t, matchedRatingKeys(t, rec.Body.Bytes()))
}

// TestFixMatchListsTheFilesItemFirstAndStillSearchesTheTitle: Plex's "Fix
// Match" (manual=1) is how a user overrides a match, so the file's item
// leads the list but the title search still offers the others.
func TestFixMatchListsTheFilesItemFirstAndStillSearchesTheTitle(t *testing.T) {
	orig, remake := fixtureMovie(), fixtureMovieRemake()
	rel := movieRel(t, remake)
	h := newTestHandler(t, externalURLFixture, orig, remake,
		mediaFile("remake-file", commonv1.MediaKindMovie, remake.Name, "/data/media/movies/"+rel))
	rec := postJSON(t, h, "/plex/movies/library/metadata/matches", map[string]any{
		"type": 1, "title": "Skyfall Protocol", "filename": rel, "manual": 1,
	})
	require.Equal(t, []string{string(remakeMovieUID), string(movieUID)}, matchedRatingKeys(t, rec.Body.Bytes()),
		"the file's item first, the other title after, no duplicate")

	series, episodes := fixtureSeriesAndEpisodes()
	epRel := episodeRel(t, series, 1, 1)
	objs := []client.Object{series, mediaFile("s01e01", commonv1.MediaKindEpisode, episodes[0].Name, "/data/media/tv/"+epRel)}
	for _, e := range episodes {
		objs = append(objs, e)
	}
	h = newTestHandler(t, externalURLFixture, objs...)
	rec = postJSON(t, h, "/plex/tv/library/metadata/matches", map[string]any{
		"type": 2, "title": "Harborview", "filename": epRel, "manual": 1,
	})
	require.Equal(t, []string{string(seriesUID)}, matchedRatingKeys(t, rec.Body.Bytes()), "the same show is not listed twice")
}
