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

package tvdb_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tvdb"
)

func TestSeriesLogsInOnceAndReusesTheToken(t *testing.T) {
	login, _ := os.ReadFile("../../../../testdata/metadata/tvdb/login.json")
	series, _ := os.ReadFile("../../../../testdata/metadata/tvdb/series_121361.json")
	var logins int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/login":
			atomic.AddInt32(&logins, 1)
			w.Write(login)
		case r.URL.Path == "/series/121361/extended":
			require.Equal(t, "Bearer eyJhbGciOiJIUzI1NiJ9.test-payload.test-signature", r.Header.Get("Authorization"))
			w.Write(series)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	c := tvdb.New("test-key", "test-pin", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	s1, err := c.Series(context.Background(), "121361")
	require.NoError(t, err)
	require.Equal(t, "Game of Thrones", s1.Title)
	require.Equal(t, "tt0944947", s1.IDs[metadata.KeyIMDb])
	require.Equal(t, metadata.SeriesStatusEnded, s1.Status)

	_, err = c.Series(context.Background(), "121361")
	require.NoError(t, err)
	require.EqualValues(t, 1, atomic.LoadInt32(&logins), "the second call must reuse the cached token, not log in again")
}

func TestEpisodesUsesTheRequestedSeasonOrder(t *testing.T) {
	login, _ := os.ReadFile("../../../../testdata/metadata/tvdb/login.json")
	episodes, _ := os.ReadFile("../../../../testdata/metadata/tvdb/episodes_121361_default.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/login":
			w.Write(login)
		case r.URL.Path == "/series/121361/episodes/default":
			w.Write(episodes)
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := tvdb.New("test-key", "test-pin", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	eps, err := c.Episodes(context.Background(), "121361", "default")

	require.NoError(t, err)
	require.Len(t, eps, 1)
	require.Equal(t, "Winter Is Coming", eps[0].Title)
	require.EqualValues(t, 1, eps[0].SeasonNumber)
	require.EqualValues(t, 1, *eps[0].AbsoluteNumber)
}

func TestUpdatesReturnsRecordIDsSinceTheGivenTime(t *testing.T) {
	login, _ := os.ReadFile("../../../../testdata/metadata/tvdb/login.json")
	updates, _ := os.ReadFile("../../../../testdata/metadata/tvdb/updates_since.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/login":
			w.Write(login)
		case r.URL.Path == "/updates":
			require.Equal(t, "1700000000", r.URL.Query().Get("since"))
			w.Write(updates)
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := tvdb.New("test-key", "test-pin", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	ids, err := c.Updates(context.Background(), time.Unix(1700000000, 0))

	require.NoError(t, err)
	require.ElementsMatch(t, []string{"121361", "3254641"}, ids)
}
