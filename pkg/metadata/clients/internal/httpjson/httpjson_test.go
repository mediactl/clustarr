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

package httpjson_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/internal/httpjson"
)

func TestCheckMapsStatusesOntoTheMetadataSentinels(t *testing.T) {
	tests := []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, metadata.ErrAuth},
		{http.StatusForbidden, metadata.ErrAuth},
		{http.StatusNotFound, metadata.ErrNotFound},
		{http.StatusTooManyRequests, metadata.ErrRateLimited},
	}
	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()
			c := &httpjson.Client{Provider: "p", HTTP: srv.Client()}

			var out map[string]any
			err := c.GetJSON(context.Background(), srv.URL+"/x", nil, &out)

			require.ErrorIs(t, err, tt.want)
		})
	}
}

func TestCheckReportsAnUnmappedStatusWithoutTheQueryString(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	c := &httpjson.Client{Provider: "fanart", HTTP: srv.Client()}

	var out map[string]any
	err := c.GetJSON(context.Background(), srv.URL+"/v3.2/movies/603?api_key=SECRET", nil, &out)

	var se *httpjson.StatusError
	require.ErrorAs(t, err, &se)
	require.Equal(t, http.StatusBadGateway, se.Code)
	require.NotContains(t, err.Error(), "SECRET", "an api_key in the query string must never reach an error string")
}

func TestDoRedactsTheQueryStringFromTransportErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // connection refused from here on

	c := &httpjson.Client{Provider: "fanart"}
	var out map[string]any
	err := c.GetJSON(context.Background(), url+"/v3.2/movies/603?api_key=SECRET", nil, &out)

	require.Error(t, err)
	require.NotContains(t, err.Error(), "SECRET")
}

func TestRateLimitedErrorCarriesRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	c := &httpjson.Client{Provider: "p", HTTP: srv.Client()}

	err := c.GetJSON(context.Background(), srv.URL, nil, &struct{}{})

	var rl *metadata.RateLimitedError
	require.ErrorAs(t, err, &rl)
	require.Equal(t, 42*time.Second, rl.RetryAfter)
	require.Equal(t, "p", rl.Provider)
}

func TestParseRetryAfterAcceptsSecondsAndHTTPDates(t *testing.T) {
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	require.Equal(t, 5*time.Second, httpjson.ParseRetryAfter("5", now))
	require.Equal(t, 90*time.Second, httpjson.ParseRetryAfter(now.Add(90*time.Second).Format(http.TimeFormat), now))
	require.Zero(t, httpjson.ParseRetryAfter("", now))
	require.Zero(t, httpjson.ParseRetryAfter("soon", now))
	require.Zero(t, httpjson.ParseRetryAfter("-3", now))
	require.Zero(t, httpjson.ParseRetryAfter(now.Add(-time.Minute).Format(http.TimeFormat), now), "a date already past is no hint")
}

func TestBodiesOverTheCapAreErrResponseTooLargeNotErrDecode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"a":"` + strings.Repeat("x", 64) + `"}`))
	}))
	defer srv.Close()
	c := &httpjson.Client{Provider: "p", HTTP: srv.Client(), MaxBody: 16}

	var out map[string]any
	err := c.GetJSON(context.Background(), srv.URL, nil, &out)

	require.ErrorIs(t, err, metadata.ErrResponseTooLarge)
	require.NotErrorIs(t, err, metadata.ErrDecode)
}

func TestMalformedBodiesAreErrDecode(t *testing.T) {
	for _, body := range []string{"", `{"a":`, "<html>"} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		c := &httpjson.Client{Provider: "p", HTTP: srv.Client()}
		var out map[string]any
		err := c.GetJSON(context.Background(), srv.URL, nil, &out)
		srv.Close()
		require.ErrorIs(t, err, metadata.ErrDecode, "body %q", body)
	}
}

func TestEveryRequestCarriesAUserAgent(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.UserAgent())
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	require.NoError(t, (&httpjson.Client{Provider: "p", HTTP: srv.Client()}).GetJSON(context.Background(), srv.URL, nil, &struct{}{}))
	require.NoError(t, (&httpjson.Client{Provider: "p", HTTP: srv.Client(), UserAgent: "me (x@y)"}).PostJSON(context.Background(), srv.URL, nil, map[string]string{}, &struct{}{}))

	require.Equal(t, []string{httpjson.DefaultUserAgent, "me (x@y)"}, got)
	require.True(t, strings.HasPrefix(httpjson.DefaultUserAgent, "Clustarr/"), "never Go's default User-Agent")
}

func TestACancelledLimiterWaitIsReturned(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &httpjson.Client{Provider: "p", Limiter: metadata.NewLimiter(1, 1)}
	c.Limiter.Allow() // drain the single token so Wait must block

	_, err := c.Do(ctx, httpjson.Request{URL: "http://127.0.0.1:1/"})

	require.ErrorIs(t, err, context.Canceled)
}

func TestANilLimiterMeansNoClientSideLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	c := &httpjson.Client{Provider: "p", HTTP: srv.Client()}

	for range 3 {
		require.NoError(t, c.GetJSON(context.Background(), srv.URL, nil, &struct{}{}))
	}
}
