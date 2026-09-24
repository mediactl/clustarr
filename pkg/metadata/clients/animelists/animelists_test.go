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

package animelists_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/animelists"
	"github.com/mediactl/clustarr/pkg/metadata/clients/extid"
)

// 35 real entries of the live file, 2026-09-23.
const fixture = "../../../../test/data/metadata/animelists/anime-list-full.json"

type server struct {
	*httptest.Server
	mu       sync.Mutex
	requests []*http.Request
	status   int // 0 = serve the file
}

func serve(t *testing.T) *server {
	t.Helper()
	body, err := os.ReadFile(fixture)
	require.NoError(t, err)
	s := &server{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r)
		status := s.status
		s.mu.Unlock()
		if status != 0 {
			w.WriteHeader(status)
			return
		}
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		if r.Method == http.MethodHead {
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *server) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func newClient(s *server, now func() time.Time) *animelists.Client {
	return animelists.New(animelists.Config{HTTPClient: s.Client(), URL: s.URL, Now: now})
}

func resolve(t *testing.T, c *animelists.Client, kind commonv1.MediaKind, ids metadata.ExternalIDs) metadata.ExternalIDs {
	t.Helper()
	out, err := c.Resolve(context.Background(), kind, ids)
	require.NoError(t, err)
	return out
}

func TestASeriesTVDBIDNamesItsFirstSeasonTVEntry(t *testing.T) {
	c := newClient(serve(t), nil)

	// Attack on Titan: eleven entries share TVDB 267440; one is season 1's TV.
	require.Equal(t, metadata.ExternalIDs{
		extid.KeyAniDB:      "9541",
		metadata.KeyAniList: "16498",
		extid.KeyMAL:        "16498",
		extid.KeyKitsu:      "7442",
		metadata.KeyTVDB:    "267440",
		metadata.KeyTMDB:    "1429",
		metadata.KeyIMDb:    "tt2560140",
	}, resolve(t, c, commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyTVDB: "267440"}))

	// Naruto Shippuden's TV entry carries no season at all.
	require.Equal(t, "4880", resolve(t, c, commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyTVDB: "79824"})[extid.KeyAniDB])
}

func TestASeriesTMDBTVIDFollowsTheSameRule(t *testing.T) {
	c := newClient(serve(t), nil)
	require.Equal(t, "9541", resolve(t, c, commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyTMDB: "1429"})[extid.KeyAniDB])
}

func TestAUniqueIDNamesItsEntryDirectly(t *testing.T) {
	c := newClient(serve(t), nil)

	// AoT season 2, by MAL: a different anime from season 1, same series.
	ids := resolve(t, c, commonv1.MediaKindSeries, metadata.ExternalIDs{extid.KeyMAL: "25777"})

	require.Equal(t, "10944", ids[extid.KeyAniDB])
	require.Equal(t, "20958", ids[metadata.KeyAniList], "AniList and MAL numbered this season differently")
	require.Equal(t, "267440", ids[metadata.KeyTVDB])
}

func TestTwoFirstSeasonsAreAmbiguous(t *testing.T) {
	c := newClient(serve(t), nil)

	_, err := c.Resolve(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyTVDB: "254931"})

	require.ErrorIs(t, err, animelists.ErrAmbiguous, "Gundam SEED and SEED Destiny both claim TVDB 254931 season 1")
}

func TestIDsThatDisagreeWithTheDatasetAreAConflict(t *testing.T) {
	c := newClient(serve(t), nil)

	_, err := c.Resolve(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{extid.KeyMAL: "25777", metadata.KeyTVDB: "79824"})

	require.ErrorIs(t, err, animelists.ErrConflict)
}

func TestAMovieByTMDBOrIMDb(t *testing.T) {
	c := newClient(serve(t), nil)

	byTMDB := resolve(t, c, commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyTMDB: "714194"})
	require.Equal(t, metadata.ExternalIDs{
		extid.KeyAniDB: "15582", metadata.KeyAniList: "119113", extid.KeyMAL: "42091", extid.KeyKitsu: "43212",
		metadata.KeyTMDB: "714194", metadata.KeyIMDb: "tt12415546",
	}, byTMDB, "a movie gets its tmdb movie id, not its series' tv id or tvdb id")

	byIMDb := resolve(t, c, commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyIMDb: "tt3646944"})
	require.Equal(t, metadata.ExternalIDs{extid.KeyAniDB: "10583"}, byIMDb, "an entry listing two imdb ids gets neither")

	_, err := c.Resolve(context.Background(), commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyIMDb: "tt2560140"})
	require.ErrorIs(t, err, metadata.ErrNotFound, "the series' imdb id names no MOVIE entry")
}

func TestUnknownIDsAreNotFound(t *testing.T) {
	c := newClient(serve(t), nil)
	_, err := c.Resolve(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{extid.KeyMAL: "999999999"})
	require.ErrorIs(t, err, metadata.ErrNotFound)
}

func TestNothingIsDownloadedForWhatCannotBeResolved(t *testing.T) {
	s := serve(t)
	c := newClient(s, nil)
	ctx := context.Background()

	_, err := c.Resolve(ctx, commonv1.MediaKindComic, metadata.ExternalIDs{metadata.KeyAniList: "30002"})
	require.ErrorIs(t, err, metadata.ErrUnsupported)
	_, err = c.Resolve(ctx, commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyMBArtist: "x"})
	require.ErrorIs(t, err, metadata.ErrUnsupported)

	require.Zero(t, s.count())
}

func TestTheDatasetIsReusedThenRevalidatedWithItsETag(t *testing.T) {
	s := serve(t)
	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	c := newClient(s, func() time.Time { return now })
	ids := metadata.ExternalIDs{metadata.KeyTVDB: "267440"}

	resolve(t, c, commonv1.MediaKindSeries, ids)
	resolve(t, c, commonv1.MediaKindSeries, ids)
	require.Equal(t, 1, s.count(), "one download serves every call inside the refresh interval")

	now = now.Add(animelists.DefaultRefresh)
	resolve(t, c, commonv1.MediaKindSeries, ids)
	require.Equal(t, 2, s.count())
	require.Equal(t, `"v1"`, s.requests[1].Header.Get("If-None-Match"), "a refresh is conditional; the 304 keeps the loaded dataset")
}

func TestAFailedRefreshKeepsServingTheLoadedDataset(t *testing.T) {
	s := serve(t)
	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	c := newClient(s, func() time.Time { return now })
	ids := metadata.ExternalIDs{metadata.KeyTVDB: "267440"}
	resolve(t, c, commonv1.MediaKindSeries, ids)

	s.mu.Lock()
	s.status = http.StatusBadGateway
	s.mu.Unlock()
	now = now.Add(animelists.DefaultRefresh)

	require.Equal(t, "9541", resolve(t, c, commonv1.MediaKindSeries, ids)[extid.KeyAniDB])
	resolve(t, c, commonv1.MediaKindSeries, ids)
	require.Equal(t, 2, s.count(), "a failed refresh is retried after another interval, not on every call")
}

func TestAFailedFirstDownloadIsAnError(t *testing.T) {
	s := serve(t)
	s.status = http.StatusNotFound
	_, err := newClient(s, nil).Resolve(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyTVDB: "267440"})
	require.ErrorIs(t, err, metadata.ErrNotFound)
}

func TestPingIsAHeadRequest(t *testing.T) {
	s := serve(t)
	require.NoError(t, newClient(s, nil).Ping(context.Background()))
	require.Equal(t, http.MethodHead, s.requests[0].Method)
}
