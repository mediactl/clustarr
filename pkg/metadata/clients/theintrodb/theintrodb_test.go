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

package theintrodb_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/theintrodb"
)

// recorded serves the responses recorded from the live API on 2026-09-30,
// keyed by the query string, and remembers the last request.
type recorded struct {
	last *http.Request
}

func (rec *recorded) client(t *testing.T, key string) *theintrodb.Client {
	t.Helper()
	files := map[string]string{
		"duration_ms=3480000&episode=1&season=1&tmdb_id=1396": "tv-1396-s01e01.json",
		"duration_ms=8160000&tmdb_id=603":                     "movie-603.json",
		"duration_ms=1380000&episode=1&season=1&tmdb_id=1668": "tv-1668-s01e01.json",
		"episode=1&season=1&tvdb_id=81189":                    "tv-1396-s01e01.json",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.last = r
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Ratelimit-Remaining", "29")
		w.Header().Set("X-Usagelimit-Remaining", "499")
		name, ok := files[r.URL.Query().Encode()]
		if r.URL.Path != "/v3/media" || !ok {
			w.WriteHeader(http.StatusNotFound)
			name = "notfound.json"
		}
		body, err := os.ReadFile("../../../../test/data/metadata/theintrodb/" + name)
		require.NoError(t, err)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	c, err := theintrodb.New(theintrodb.Config{
		HTTPClient: srv.Client(), BaseURL: srv.URL, Limiter: metadata.NewLimiter(rate.Inf, 1), APIKey: key,
	})
	require.NoError(t, err)
	return c
}

func TestEpisodeSegmentsResolveAnOpenEndToTheFilesDuration(t *testing.T) {
	rec := &recorded{}
	got, err := rec.client(t, "").Markers(context.Background(), metadata.MarkersQuery{
		IDs: metadata.ExternalIDs{metadata.KeyTMDB: "1396"}, Season: 1, Episode: 1, DurationMs: 3480000,
	})
	require.NoError(t, err)
	assert.Equal(t, []metadata.Segment{{StartMs: 228664, EndMs: 246143}}, got.Intro)
	assert.Equal(t, []metadata.Segment{{StartMs: 3431000, EndMs: 3480000}}, got.Credits, "a null end runs to the file's end")
	assert.Empty(t, got.Recap)
	assert.Empty(t, rec.last.Header.Get("Authorization"), "no key configured: anonymous")
}

func TestMovieSegmentsResolveAnOpenStartToZero(t *testing.T) {
	got, err := (&recorded{}).client(t, "").Markers(context.Background(), metadata.MarkersQuery{
		IDs: metadata.ExternalIDs{metadata.KeyTMDB: "603", metadata.KeyIMDb: "tt0133093"}, DurationMs: 8160000,
	})
	require.NoError(t, err)
	assert.Equal(t, []metadata.Segment{{StartMs: 0, EndMs: 40000}}, got.Intro)
}

func TestAnEmptySegmentIsDropped(t *testing.T) {
	got, err := (&recorded{}).client(t, "").Markers(context.Background(), metadata.MarkersQuery{
		IDs: metadata.ExternalIDs{metadata.KeyTMDB: "1668"}, Season: 1, Episode: 1, DurationMs: 1380000,
	})
	require.NoError(t, err)
	assert.Empty(t, got.Intro, `{"start_ms":null,"end_ms":0} is no intro`)
	assert.Equal(t, []metadata.Segment{{StartMs: 1358600, EndMs: 1364000}}, got.Credits)
}

func TestAnOpenEndWithNoDurationIsDropped(t *testing.T) {
	got, err := (&recorded{}).client(t, "").Markers(context.Background(), metadata.MarkersQuery{
		IDs: metadata.ExternalIDs{metadata.KeyTVDB: "81189"}, Season: 1, Episode: 1,
	})
	require.NoError(t, err)
	assert.Len(t, got.Intro, 1)
	assert.Empty(t, got.Credits, "a credits segment running to an unknown end cannot be placed")
}

func TestAnUnknownTitleIsNotFound(t *testing.T) {
	_, err := (&recorded{}).client(t, "").Markers(context.Background(), metadata.MarkersQuery{
		IDs: metadata.ExternalIDs{metadata.KeyTMDB: "83867"}, Season: 1, Episode: 1, DurationMs: 2400000,
	})
	require.ErrorIs(t, err, metadata.ErrNotFound)
}

func TestNoUsableIDIsNotFoundWithoutAsking(t *testing.T) {
	rec := &recorded{}
	_, err := rec.client(t, "").Markers(context.Background(), metadata.MarkersQuery{IDs: metadata.ExternalIDs{"anilist": "1"}})
	require.ErrorIs(t, err, metadata.ErrNotFound)
	assert.Nil(t, rec.last, "nothing to ask TheIntroDB by")
}

func TestTheKeyIsABearerTokenAndNeverInAnError(t *testing.T) {
	rec := &recorded{}
	c := rec.client(t, "s3cret-key")
	_, err := c.Markers(context.Background(), metadata.MarkersQuery{
		IDs: metadata.ExternalIDs{metadata.KeyTMDB: "83867"}, Season: 1, Episode: 1, DurationMs: 1,
	})
	require.Error(t, err)
	assert.Equal(t, "Bearer s3cret-key", rec.last.Header.Get("Authorization"))
	assert.NotContains(t, err.Error(), "s3cret-key")
}

// limited serves one 429 with the given headers.
func limited(t *testing.T, headers map[string]string) *theintrodb.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	c, err := theintrodb.New(theintrodb.Config{HTTPClient: srv.Client(), BaseURL: srv.URL, Limiter: metadata.NewLimiter(rate.Inf, 1)})
	require.NoError(t, err)
	return c
}

// An exhausted usage allowance is a 429 with no Retry-After (recorded
// live, 2026-09-30): the wait is the allowance's reset, in seconds, not
// the caller's backoff.
func TestAnExhaustedUsageAllowanceWaitsForItsReset(t *testing.T) {
	c := limited(t, map[string]string{
		"X-Ratelimit-Limit": "30", "X-Ratelimit-Remaining": "29", "X-Ratelimit-Reset": "10",
		"X-Usagelimit-Limit": "500", "X-Usagelimit-Remaining": "0", "X-Usagelimit-Reset": "6945",
	})
	_, err := c.Markers(context.Background(), metadata.MarkersQuery{IDs: metadata.ExternalIDs{metadata.KeyTMDB: "603"}})
	var rl *metadata.RateLimitedError
	require.ErrorAs(t, err, &rl)
	assert.Equal(t, 6945*time.Second, rl.RetryAfter)
}

func TestAnExhaustedRateWindowWaitsForItsReset(t *testing.T) {
	c := limited(t, map[string]string{
		"X-Ratelimit-Remaining": "0", "X-Ratelimit-Reset": "10", "X-Usagelimit-Remaining": "12", "X-Usagelimit-Reset": "6945",
	})
	_, err := c.Markers(context.Background(), metadata.MarkersQuery{IDs: metadata.ExternalIDs{metadata.KeyTMDB: "603"}})
	var rl *metadata.RateLimitedError
	require.ErrorAs(t, err, &rl)
	assert.Equal(t, 10*time.Second, rl.RetryAfter)
}

func TestRetryAfterWinsOverTheLimitHeaders(t *testing.T) {
	c := limited(t, map[string]string{"Retry-After": "42", "X-Usagelimit-Remaining": "0", "X-Usagelimit-Reset": "6945"})
	_, err := c.Markers(context.Background(), metadata.MarkersQuery{IDs: metadata.ExternalIDs{metadata.KeyTMDB: "603"}})
	var rl *metadata.RateLimitedError
	require.ErrorAs(t, err, &rl)
	assert.Equal(t, 42*time.Second, rl.RetryAfter)
}
