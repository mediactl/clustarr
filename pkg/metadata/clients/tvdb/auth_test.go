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

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tvdb"
)

func TestSeriesReAuthenticatesOnceOnA401(t *testing.T) {
	login, _ := os.ReadFile("../../../../testdata/metadata/tvdb/login.json")
	series, _ := os.ReadFile("../../../../testdata/metadata/tvdb/series_121361.json")
	var seriesCalls int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/login":
			w.Write(login)
		case r.URL.Path == "/series/121361/extended":
			n := atomic.AddInt32(&seriesCalls, 1)
			if n == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Write(series)
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := tvdb.New("test-key", "test-pin", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	s, err := c.Series(context.Background(), "121361")

	require.NoError(t, err)
	require.Equal(t, "Game of Thrones", s.Title)
	require.EqualValues(t, 2, atomic.LoadInt32(&seriesCalls), "the client must retry exactly once after re-authenticating")
}

func TestSeriesGivesUpWithErrAuthAfterASecondConsecutive401(t *testing.T) {
	login, _ := os.ReadFile("../../../../testdata/metadata/tvdb/login.json")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/login":
			w.Write(login)
		case r.URL.Path == "/series/121361/extended":
			w.WriteHeader(http.StatusUnauthorized)
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	c := tvdb.New("test-key", "test-pin", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	_, err := c.Series(context.Background(), "121361")

	require.ErrorIs(t, err, metadata.ErrAuth)
}
