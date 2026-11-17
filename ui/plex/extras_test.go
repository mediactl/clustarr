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
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/trailers"
	"github.com/mediactl/clustarr/ui/plex"
	"github.com/mediactl/clustarr/ui/projection"
)

// gateway is the metadata gateway answering rpc.catalog.metadata.extras
// with a response recorded from Plex's metadata service (Arrival,
// 2026-10-06, two of its 44 extras), recording the Plex id it was asked for.
type gateway struct {
	calls atomic.Int32
	asked atomic.Value
	err   error
	items []plex.Extra
}

func newGateway(t *testing.T, err error) *gateway {
	t.Helper()
	body, readErr := os.ReadFile("../../test/data/metadata/plex/extras_" + moviePlex + ".json")
	require.NoError(t, readErr)
	var recorded struct {
		MediaContainer struct {
			Metadata []plex.Extra `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	require.NoError(t, json.Unmarshal(body, &recorded))
	return &gateway{err: err, items: recorded.MediaContainer.Metadata}
}

func (g *gateway) Extras(_ context.Context, plexID string) ([]plex.Extra, error) {
	g.calls.Add(1)
	g.asked.Store(plexID)
	if g.err != nil {
		return nil, g.err
	}
	return g.items, nil
}

func extrasHandler(t *testing.T, extras func(context.Context, string) ([]plex.Extra, error), objs ...client.Object) http.Handler {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
	return plex.Handler(plex.Options{
		ExternalURL: "https://clustarr.example",
		Extras:      extras,
		Index:       func(ctx context.Context) (*projection.Index, error) { return projection.BuildIndex(ctx, c) },
	})
}

func movieWithPlexID(id string) *catalogv1.Movie {
	m := fixtureMovie()
	m.Status.Metadata.ExternalIDs = map[string]string{"plex": id}
	return m
}

type extrasResponse struct {
	MediaContainer struct {
		Identifier string `json:"identifier"`
		Size       int    `json:"size"`
		TotalSize  int    `json:"totalSize"`
		Offset     int    `json:"offset"`
		Metadata   []struct {
			Title     string `json:"title"`
			Type      string `json:"type"`
			Subtype   string `json:"subtype"`
			ExtraType int    `json:"extraType"`
			Media     []struct {
				URL string `json:"url"`
			} `json:"Media"`
		} `json:"Metadata"`
	} `json:"MediaContainer"`
}

// PMS asks a custom provider for an item's extras on every refresh, a
// route Plex's provider docs never mention. It read clustarr's 404 as "no
// extras" and deleted the Internet Video Archive trailers it held for the
// item (kind-cluster-plex, 2026-10-06). The route answers Plex's own extras
// for the item's Plex id, IVA URLs and all, as the metadata gateway holds
// them.
func TestExtrasArePlexsOwnForTheItemsPlexID(t *testing.T) {
	gw := newGateway(t, nil)
	h := extrasHandler(t, gw.Extras, movieWithPlexID(moviePlex))

	rec := getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID)+"/extras")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out extrasResponse
	require.NoError(t, decodeJSON(rec.Body.Bytes(), &out))
	mc := out.MediaContainer
	assert.Equal(t, plex.MoviesIdentifier, mc.Identifier)
	assert.Equal(t, 2, mc.Size)
	assert.Equal(t, 2, mc.TotalSize)
	require.Len(t, mc.Metadata, 2)
	assert.Equal(t, "clip", mc.Metadata[0].Type)
	assert.Equal(t, "trailer", mc.Metadata[0].Subtype)
	assert.Equal(t, 1, mc.Metadata[0].ExtraType)
	require.NotEmpty(t, mc.Metadata[0].Media)
	assert.Contains(t, mc.Metadata[0].Media[0].URL, "video.internetvideoarchive.net")
	assert.Equal(t, moviePlex, gw.asked.Load())
}

// PMS reaches an item it holds under its plex:// GUID by the Plex id.
func TestExtrasResolveAPlexIDRatingKey(t *testing.T) {
	gw := newGateway(t, nil)
	h := extrasHandler(t, gw.Extras, movieWithPlexID(moviePlex))
	rec := getJSON(t, h, "/plex/movies/library/metadata/"+moviePlex+"/extras")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

func TestExtrasPageAsPMSAsks(t *testing.T) {
	gw := newGateway(t, nil)
	h := extrasHandler(t, gw.Extras, movieWithPlexID(moviePlex))
	rec := getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID)+"/extras?X-Plex-Container-Start=1&X-Plex-Container-Size=50")
	var out extrasResponse
	require.NoError(t, decodeJSON(rec.Body.Bytes(), &out))
	assert.Equal(t, 1, out.MediaContainer.Size)
	assert.Equal(t, 2, out.MediaContainer.TotalSize)
	assert.Equal(t, 1, out.MediaContainer.Offset)
	require.Len(t, out.MediaContainer.Metadata, 1)
}

// An item Plex does not know has no extras of Plex's to keep, and the
// gateway is not asked.
func TestExtrasOfAnItemWithoutAPlexIDAreNone(t *testing.T) {
	gw := newGateway(t, nil)
	h := extrasHandler(t, gw.Extras, fixtureMovie())
	rec := getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID)+"/extras")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"MediaContainer":{"offset":0,"totalSize":0,"identifier":"`+plex.MoviesIdentifier+`","size":0,"Metadata":[]}}`, rec.Body.String())
	assert.Zero(t, gw.calls.Load())
}

// When the gateway cannot answer, the route answers an error, never 404 or
// an empty list: both read to PMS as "no extras", and it deletes the ones
// it has.
func TestExtrasAreAnErrorWhenTheGatewayCannotAnswer(t *testing.T) {
	gw := newGateway(t, errors.New("nats: no responders available for request"))
	h := extrasHandler(t, gw.Extras, movieWithPlexID(moviePlex))
	rec := getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID)+"/extras")
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestExtrasWithoutAGatewayAreUnavailable(t *testing.T) {
	h := extrasHandler(t, nil, movieWithPlexID(moviePlex))
	rec := getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID)+"/extras")
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestExtrasOfAnUnknownItemAreNotFound(t *testing.T) {
	h := extrasHandler(t, nil)
	rec := getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID)+"/extras")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// Plex's metadata service keeps most TV trailers on a season and PMS stores
// none for a season, so a show answers its own extras and then each
// season's in season order, specials left out, each clip once (talos,
// 2026-11-17: Succession has none on the show and five on its fourth
// season; 390 of 541 shows had no trailer).
func TestAShowsExtrasAreItsOwnAndItsSeasons(t *testing.T) {
	const showPlex, s1, s2, special = "5d9c08ed46115600200ade5e", "5d9c08ed46115600200ade61", "5d9c08ed46115600200ade62", "5d9c08ed46115600200ade60"
	clip := func(guid string) plex.Extra {
		return plex.Extra(`{"guid":"plex://clip/` + guid + `","type":"clip","subtype":"trailer","extraType":1,"title":"` + guid + `"}`)
	}
	held := map[string][]plex.Extra{
		showPlex: {},
		s2:       {clip("season-two"), clip("shared")},
		s1:       {clip("season-one"), clip("shared")},
		special:  {clip("special")},
	}
	var asked []string
	extras := func(_ context.Context, plexID string) ([]plex.Extra, error) {
		asked = append(asked, plexID)
		return held[plexID], nil
	}
	s, _ := fixtureSeriesAndEpisodes()
	s.Status.Metadata.ExternalIDs = map[string]string{"plex": showPlex}
	s.Status.Metadata.PlexSeasons = []catalogv1.PlexSeasonRef{{Number: 2, ID: s2}, {Number: 0, ID: special}, {Number: 1, ID: s1}}
	h := extrasHandler(t, extras, s)

	rec := getJSON(t, h, "/plex/tv/library/metadata/"+string(seriesUID)+"/extras")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out extrasResponse
	require.NoError(t, decodeJSON(rec.Body.Bytes(), &out))
	var titles []string
	for _, m := range out.MediaContainer.Metadata {
		titles = append(titles, m.Title)
	}
	assert.Equal(t, []string{"season-one", "shared", "season-two"}, titles)
	assert.Equal(t, []string{showPlex, s1, s2}, asked)
}

// One season the gateway cannot answer for fails the show's whole list: a
// partial one would have PMS delete that season's trailers.
func TestAShowsExtrasFailWholeWhenOneSeasonCannotBeAnswered(t *testing.T) {
	const showPlex, s1 = "5d9c08ed46115600200ade5e", "5d9c08ed46115600200ade61"
	extras := func(_ context.Context, plexID string) ([]plex.Extra, error) {
		if plexID == s1 {
			return nil, errors.New("plex: 503")
		}
		return []plex.Extra{plex.Extra(`{"guid":"plex://clip/a","title":"a"}`)}, nil
	}
	s, _ := fixtureSeriesAndEpisodes()
	s.Status.Metadata.ExternalIDs = map[string]string{"plex": showPlex}
	s.Status.Metadata.PlexSeasons = []catalogv1.PlexSeasonRef{{Number: 1, ID: s1}}
	rec := getJSON(t, extrasHandler(t, extras, s), "/plex/tv/library/metadata/"+string(seriesUID)+"/extras")
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

// readyTrailer is the record the trailers agent leaves for an item whose
// trailer it fetched.
func readyTrailer() trailers.Entry {
	return trailers.Entry{
		State: trailers.StateReady, CheckedAt: time.Date(2026, 11, 17, 20, 0, 0, 0, time.UTC),
		Video: &trailers.Video{
			Site: "YouTube", Key: "hnEwJKVWjFM", Name: "Official Trailer", Type: "Trailer", Official: true,
			PublishedAt: time.Date(2021, 7, 27, 16, 0, 0, 0, time.UTC), Provider: "tmdb",
		},
		Path:      "/data/media/movies/.clustarr/trailers/movie-" + string(movieUID) + ".mp4",
		SizeBytes: 19739662, DurationMillis: 127198, Width: 1920, Height: 1080, VideoCodec: "h264", AudioCodec: "aac",
	}
}

func trailerHandler(t *testing.T, extras []plex.Extra, entry *trailers.Entry, readErr error, mappings []trailers.PathMapping, objs ...client.Object) (http.Handler, *[]string) {
	t.Helper()
	var asked []string
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
	return plex.Handler(plex.Options{
		ExternalURL: "https://clustarr.example",
		Extras:      func(context.Context, string) ([]plex.Extra, error) { return extras, nil },
		Trailer: func(_ context.Context, kind, uid string) (trailers.Entry, bool, error) {
			asked = append(asked, kind+"/"+uid)
			if readErr != nil || entry == nil {
				return trailers.Entry{}, false, readErr
			}
			return *entry, true, nil
		},
		PathMappings: mappings,
		Index:        func(ctx context.Context) (*projection.Index, error) { return projection.BuildIndex(ctx, c) },
	}), &asked
}

var moviesMapping = []trailers.PathMapping{{Clustarr: "/data/media/movies", Plex: "/media/movies"}}

// A movie Plex has no trailer for answers its own, after Plex's other
// extras: a clip whose part is the file where Plex mounts it, which PMS
// reads and plays as a local part (talos, 2026-11-17).
func TestAMovieWithoutAPlexTrailerAnswersItsOwnByItsFile(t *testing.T) {
	entry := readyTrailer()
	other := plex.Extra(`{"guid":"plex://clip/bts","type":"clip","subtype":"behindTheScenes","extraType":5,"title":"Making of"}`)
	h, asked := trailerHandler(t, []plex.Extra{other}, &entry, nil, moviesMapping, movieWithPlexID(moviePlex))

	rec := getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID)+"/extras")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out struct {
		MediaContainer struct {
			TotalSize int               `json:"totalSize"`
			Metadata  []json.RawMessage `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	require.NoError(t, decodeJSON(rec.Body.Bytes(), &out))
	require.Equal(t, 2, out.MediaContainer.TotalSize)
	assert.JSONEq(t, string(other), string(out.MediaContainer.Metadata[0]))
	assert.JSONEq(t, `{
		"guid": "clustarr://trailer/youtube/hnEwJKVWjFM",
		"key": "/library/metadata/`+string(movieUID)+`/extras/youtube-hnEwJKVWjFM",
		"ratingKey": "youtube-hnEwJKVWjFM",
		"type": "clip", "subtype": "trailer", "extraType": 1,
		"title": "Official Trailer", "parentTitle": "Trailer",
		"duration": 127198, "addedAt": 1794945600,
		"originallyAvailableAt": "2021-07-27", "year": 2021,
		"thumb": "https://i.ytimg.com/vi/hnEwJKVWjFM/hqdefault.jpg",
		"Media": [{
			"container": "mp4", "duration": 127198, "width": 1920, "height": 1080,
			"videoCodec": "h264", "audioCodec": "aac", "videoResolution": "1080", "optimizedForStreaming": true,
			"Part": [{
				"file": "/media/movies/.clustarr/trailers/movie-`+string(movieUID)+`.mp4",
				"container": "mp4", "duration": 127198, "size": 19739662,
				"Stream": [
					{"id": "youtube-hnEwJKVWjFM-video", "streamType": 1, "codec": "h264", "width": 1920, "height": 1080, "selected": true},
					{"id": "youtube-hnEwJKVWjFM-audio", "streamType": 2, "codec": "aac", "selected": true}
				]
			}]
		}]
	}`, string(out.MediaContainer.Metadata[1]))
	assert.Equal(t, []string{"movie/" + string(movieUID)}, *asked)
}

// The item's own trailer is answered only when it is wanted and can be:
// not beside a trailer of Plex's, not while it is not fetched, not for a
// file Plex is not told the place of, and never for a season. A movie
// with no Plex id at all still answers its own.
func TestAnItemsOwnTrailerIsAnsweredOnlyWhenPlexHasNoneAndTheFileCanBeNamed(t *testing.T) {
	plexTrailer := plex.Extra(`{"guid":"plex://clip/t","type":"clip","subtype":"trailer","extraType":1,"title":"Trailer"}`)
	ready := readyTrailer()
	failed := trailers.Entry{State: trailers.StateFailed, Video: ready.Video}
	count := func(h http.Handler, path string) int {
		rec := getJSON(t, h, path)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		var out extrasResponse
		require.NoError(t, decodeJSON(rec.Body.Bytes(), &out))
		return out.MediaContainer.TotalSize
	}
	moviePath := "/plex/movies/library/metadata/" + string(movieUID) + "/extras"

	h, asked := trailerHandler(t, []plex.Extra{plexTrailer}, &ready, nil, moviesMapping, movieWithPlexID(moviePlex))
	assert.Equal(t, 1, count(h, moviePath), "Plex's trailer stands alone")
	assert.Empty(t, *asked, "the record is not read for an item Plex has a trailer for")

	h, _ = trailerHandler(t, nil, &failed, nil, moviesMapping, movieWithPlexID(moviePlex))
	assert.Equal(t, 0, count(h, moviePath), "a trailer that was not fetched")
	h, _ = trailerHandler(t, nil, nil, nil, moviesMapping, movieWithPlexID(moviePlex))
	assert.Equal(t, 0, count(h, moviePath), "no record")
	h, _ = trailerHandler(t, nil, &ready, nil, nil, movieWithPlexID(moviePlex))
	assert.Equal(t, 0, count(h, moviePath), "no mapping tells Plex where the file is")
	h, _ = trailerHandler(t, nil, &ready, nil, []trailers.PathMapping{{Clustarr: "/data/media/tv", Plex: "/media/tv"}}, movieWithPlexID(moviePlex))
	assert.Equal(t, 0, count(h, moviePath), "the mapping holds another directory")

	h, _ = trailerHandler(t, nil, &ready, nil, moviesMapping, fixtureMovie())
	assert.Equal(t, 1, count(h, moviePath), "a movie Plex does not know answers its own")

	s, _ := fixtureSeriesAndEpisodes()
	h, asked = trailerHandler(t, nil, &ready, nil, moviesMapping, s)
	assert.Equal(t, 1, count(h, "/plex/tv/library/metadata/"+string(seriesUID)+"/extras"), "a show answers its own")
	assert.Equal(t, []string{"series/" + string(seriesUID)}, *asked)
}

// A record that cannot be read fails the list: answered without it, PMS
// would delete the trailer it holds from it.
func TestAnUnreadableTrailerRecordFailsTheList(t *testing.T) {
	h, _ := trailerHandler(t, nil, nil, errors.New("nats: timeout"), moviesMapping, movieWithPlexID(moviePlex))
	rec := getJSON(t, h, "/plex/movies/library/metadata/"+string(movieUID)+"/extras")
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}
