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

// White-box (package tvdb, not tvdb_test) so this file can substitute the
// unexported waitOnLimiter indirection to count calls -- the public API has
// no seam for that, and it should not grow one just for this assertion.
package tvdb

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
)

func TestDoRequestWaitsOnTheLimiterAgainBeforeTheRetriedRequest(t *testing.T) {
	login, err := os.ReadFile("../../../../testdata/metadata/tvdb/login.json")
	require.NoError(t, err)
	series, err := os.ReadFile("../../../../testdata/metadata/tvdb/series_121361.json")
	require.NoError(t, err)

	var seriesCalls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			_, _ = w.Write(login)
		case "/series/121361/extended":
			n := atomic.AddInt32(&seriesCalls, 1)
			if n == 1 {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write(series)
		default:
			t.Fatalf("unexpected request: %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	var waits int32
	original := waitOnLimiter
	waitOnLimiter = func(ctx context.Context, l *rate.Limiter) error {
		atomic.AddInt32(&waits, 1)
		return original(ctx, l)
	}
	t.Cleanup(func() { waitOnLimiter = original })

	c := New("test-key", "test-pin", srv.Client(), srv.URL, metadata.NewLimiter(rate.Inf, 1))

	_, err = c.Series(context.Background(), "121361")

	require.NoError(t, err)
	require.EqualValues(t, 2, atomic.LoadInt32(&waits),
		"doRequest must wait on the limiter again before the retried request after a 401, not just once up front")
}
