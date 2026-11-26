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
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// The ui's request series, on the process's default registry beside the Go
// and process collectors, served on Options.MetricsBindAddress. They are
// the only way to see what a ui pod is doing: the ui logs no request and
// until 2026-11-26 exposed nothing, so a pod that held 250m for ten minutes
// while its two siblings idled (talos, the night the autoscaler took the
// ui from one pod to three) could not be asked what it was serving.
//
// route is the pattern the request matched, never its path: "/library/{tab}",
// "/plex/tv/library/metadata/{ratingKey}" (the provider's own mux, mounted
// on the request path unchanged, names its routes itself), "/" for the
// styled 404 and "unmatched" for a method no pattern takes. The patterns
// are a fixed set, so the series are bounded whatever a client asks for.
var (
	httpRequests = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "clustarr_ui_http_requests_total",
		Help: "Requests the ui answered, by matched route pattern, method and status code.",
	}, []string{"route", "method", "code"})
	httpRequestSeconds = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "clustarr_ui_http_request_duration_seconds",
		Help:    "Time from a request's arrival to its handler's return, by matched route pattern and method. A stream is one long request.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60},
	}, []string{"route", "method"})
	httpResponseBytes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "clustarr_ui_http_response_bytes_total",
		Help: "Response body bytes written to the client, after compression, by matched route pattern.",
	}, []string{"route"})
	httpInFlight = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "clustarr_ui_http_requests_in_flight",
		Help: "Requests being served now, by the route pattern of the ui's own mux (the provider's routes read /plex/). An open stream counts for as long as it is open.",
	}, []string{"route"})

	registerHTTPMetrics sync.Once
)

// requestMetrics records every request next serves. mux is the route table
// next ends in: it names the route before the handler runs, for the
// in-flight gauge, and leaves the pattern the request matched on the
// request itself, which is read after the handler returns. Nothing between
// this handler and the mux may copy the request (r.WithContext), or the
// pattern stays with the copy and every request reads "unmatched".
func requestMetrics(mux *http.ServeMux, next http.Handler) http.Handler {
	registerHTTPMetrics.Do(func() {
		prometheus.MustRegister(httpRequests, httpRequestSeconds, httpResponseBytes, httpInFlight)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, mounted := mux.Handler(r)
		inFlight := httpInFlight.WithLabelValues(routeLabel(mounted))
		inFlight.Inc()
		began := time.Now()
		mw := &meteredWriter{ResponseWriter: w}
		defer func() {
			inFlight.Dec()
			route, method := routeLabel(r.Pattern), methodLabel(r.Method)
			code := mw.code
			if code == 0 {
				code = http.StatusOK // a handler that wrote nothing answered 200
			}
			httpRequests.WithLabelValues(route, method, strconv.Itoa(code)).Inc()
			httpRequestSeconds.WithLabelValues(route, method).Observe(time.Since(began).Seconds())
			httpResponseBytes.WithLabelValues(route).Add(float64(mw.bytes))
		}()
		next.ServeHTTP(mw, r)
	})
}

// routeLabel is a mux pattern without its method ("GET /library/{tab}" is
// "/library/{tab}"); "unmatched" for none.
func routeLabel(pattern string) string {
	if _, path, ok := strings.Cut(pattern, " "); ok {
		pattern = path
	}
	if pattern == "" {
		return "unmatched"
	}
	return pattern
}

// methodLabel is a request's method when it is one the ui's routes are
// written for, else "other": a client chooses its method, and a label takes
// no value a client can invent.
func methodLabel(m string) string {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return m
	}
	return "other"
}

// meteredWriter counts what a handler writes and keeps its status code. It
// flushes when the writer under it does, since the streams assert
// http.Flusher on the writer they are handed (sse.go).
type meteredWriter struct {
	http.ResponseWriter
	code  int
	bytes int64
}

func (m *meteredWriter) WriteHeader(code int) {
	if m.code == 0 {
		m.code = code
	}
	m.ResponseWriter.WriteHeader(code)
}

func (m *meteredWriter) Write(p []byte) (int, error) {
	if m.code == 0 {
		m.code = http.StatusOK
	}
	n, err := m.ResponseWriter.Write(p)
	m.bytes += int64(n)
	return n, err
}

func (m *meteredWriter) Flush() {
	if f, ok := m.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (m *meteredWriter) Unwrap() http.ResponseWriter { return m.ResponseWriter }

// serveMetrics serves /metrics on addr until ctx ends: the default
// registry, so the request series above with the Go runtime's and the
// process's. It is its own listener, never a route of the ui's: the ui's
// address is behind a Gateway, and the provider's paths behind a public
// one. The channel carries the one error that ends it early (an address
// it cannot bind), and nothing when it ends with ctx.
func serveMetrics(ctx context.Context, addr string) <-chan error {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.Handler())
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          logging.StdLogger(ctx, "ui", slog.LevelWarn),
	}
	failed := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			failed <- fmt.Errorf("ui: serve metrics on %s: %w", addr, err)
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
	return failed
}
