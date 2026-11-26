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

package ui

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sample is a counter's or a gauge's value now.
func sample(t *testing.T, m prometheus.Metric) float64 {
	t.Helper()
	var out dto.Metric
	require.NoError(t, m.Write(&out))
	if out.Counter != nil {
		return out.Counter.GetValue()
	}
	return out.Gauge.GetValue()
}

// observations is how many requests a histogram of the request series has
// timed, over every route.
func observations(t *testing.T) uint64 {
	t.Helper()
	ch := make(chan prometheus.Metric, 1024)
	go func() { httpRequestSeconds.Collect(ch); close(ch) }()
	var n uint64
	for m := range ch {
		var out dto.Metric
		require.NoError(t, m.Write(&out))
		n += out.Histogram.GetSampleCount()
	}
	return n
}

// Every request is counted by the route pattern it matched, never by its
// path: the library tab, the provider's own route under /plex/, the styled
// 404. Before 2026-11-26 the ui exposed nothing, and a pod holding 250m
// beside two idle ones could not be asked what it was serving.
func TestARequestIsCountedByTheRouteItMatched(t *testing.T) {
	srv := NewServer(t.Context(), Options{Plex: &PlexOptions{ExternalURL: "http://ui.example"}})
	h := srv.Handler()
	requests := func(route, method, code string) float64 {
		return sample(t, httpRequests.WithLabelValues(route, method, code))
	}
	get := func(path string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec
	}

	for _, tc := range []struct {
		name, path, route string
		code              int
	}{
		{"a page, by its pattern and not its tab", "/library/movies", "/library/{tab}", http.StatusOK},
		{"the liveness probe", "/healthz", "/healthz", http.StatusOK},
		{"a provider route, named by the provider's own mux", "/plex/tv/library/metadata/no-such-key", "/plex/tv/library/metadata/{ratingKey}", http.StatusNotFound},
		{"a path no route takes: the styled 404", "/no/such/page", "/", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			label := map[int]string{http.StatusOK: "200", http.StatusNotFound: "404"}[tc.code]
			before := requests(tc.route, http.MethodGet, label)
			timed := observations(t)
			rec := get(tc.path)
			require.Equal(t, tc.code, rec.Code, rec.Body.String())
			assert.InDelta(t, before+1, requests(tc.route, http.MethodGet, label), 0, "counted once, under its pattern")
			assert.Equal(t, timed+1, observations(t), "and timed")
		})
	}

	// The bytes are the ones sent: a page's body, compressed or not.
	before := sample(t, httpResponseBytes.WithLabelValues("/healthz"))
	rec := get("/healthz")
	assert.InDelta(t, before+float64(rec.Body.Len()), sample(t, httpResponseBytes.WithLabelValues("/healthz")), 0)

	// A method a client invents is "other", and no label carries a path.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("BREW", "/library/"+strings.Repeat("x", 40), nil))
	ch := make(chan prometheus.Metric, 4096)
	go func() { httpRequests.Collect(ch); close(ch) }()
	for m := range ch {
		var out dto.Metric
		require.NoError(t, m.Write(&out))
		for _, l := range out.Label {
			assert.NotContains(t, l.GetValue(), "xxxx", "no label carries what a client sent")
			assert.NotEqual(t, "BREW", l.GetValue())
		}
	}
	assert.Equal(t, "other", methodLabel("BREW"))
	assert.Equal(t, "unmatched", routeLabel(""))
	assert.Equal(t, "/library/{tab}", routeLabel("GET /library/{tab}"))
}

// An open stream is in flight for as long as it is open, and still flushes
// through the counting writer: the streams assert http.Flusher on the
// writer they are handed.
func TestAnOpenStreamIsInFlightAndStillFlushes(t *testing.T) {
	srv := NewServer(t.Context(), Options{})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	inFlight := func() float64 { return sample(t, httpInFlight.WithLabelValues("/events/library/{tab}")) }
	before := inFlight()

	ctx, cancel := context.WithCancel(t.Context())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/events/library/movies", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err, "the response headers arrive, so the stream flushed")
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Eventually(t, func() bool { return inFlight() == before+1 }, 5*time.Second, 10*time.Millisecond)

	cancel()
	_, _ = io.Copy(io.Discard, resp.Body)
	require.Eventually(t, func() bool { return inFlight() == before }, 5*time.Second, 10*time.Millisecond)
}

// /metrics is served on its own address, never as a route of the ui's: the
// ui's address is behind a Gateway and the provider's paths a public one.
func TestMetricsAreServedOnTheirOwnAddressOnly(t *testing.T) {
	srv := NewServer(t.Context(), Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusNotFound, rec.Code, "the ui's own address serves no /metrics")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	ctx, cancel := context.WithCancel(t.Context())
	failed := serveMetrics(ctx, addr)

	var body []byte
	require.Eventually(t, func() bool {
		resp, err := http.Get("http://" + addr + "/metrics")
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		body, err = io.ReadAll(resp.Body)
		return err == nil && resp.StatusCode == http.StatusOK
	}, 5*time.Second, 20*time.Millisecond)
	for _, series := range []string{"clustarr_ui_http_requests_total", "clustarr_ui_http_request_duration_seconds_bucket", "go_goroutines", "process_cpu_seconds_total"} {
		assert.Contains(t, string(body), series)
	}

	// A second listener on the same address fails, and says so.
	select {
	case err := <-serveMetrics(ctx, addr):
		require.ErrorContains(t, err, "serve metrics")
	case <-time.After(5 * time.Second):
		t.Fatal("a listener on a taken address reported nothing")
	}

	cancel()
	require.Eventually(t, func() bool {
		_, err := http.Get("http://" + addr + "/metrics")
		return err != nil
	}, 5*time.Second, 20*time.Millisecond, "the listener ends with its context")
	select {
	case err := <-failed:
		t.Fatalf("a listener that ended with its context reported %v", err)
	default:
	}
}
