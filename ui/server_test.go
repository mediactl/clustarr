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

package ui_test

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/pipeline"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/views"
)

// readSSEEvent reads one complete SSE event -- every line up to and
// including the blank line that terminates it -- and returns it joined back
// together, newlines included, exactly as it arrived on the wire. It is
// TestPipelineEventsStreamsServerSentEvents' own inline read loop, factored
// out so the Task D3-3 tests below can read more than one event off the
// same connection.
func readSSEEvent(t *testing.T, r *bufio.Reader) string {
	t.Helper()
	var lines []string
	for {
		line, err := r.ReadString('\n')
		lines = append(lines, line)
		if err != nil {
			require.NoError(t, err, "reading SSE event")
		}
		if strings.TrimSpace(line) == "" && len(lines) > 1 {
			break
		}
	}
	return strings.Join(lines, "")
}

func TestPipelinePageRendersWithoutACluster(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{Entries: func(context.Context) []pipeline.Entry {
		return []pipeline.Entry{{Title: "The Shawshank Redemption", Stage: pipeline.StageDownloading, Percent: 42}}
	}})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/pipeline", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "Shawshank")
	require.Contains(t, rec.Body.String(), "42")
}

func TestHealthzDoesNotDependOnTheCluster(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	require.Equal(t, http.StatusOK, rec.Code)
}

func TestPipelinePageRendersWithNoEntries(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/pipeline", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "Nothing in flight")
}

func TestIndexRedirectsToPipeline(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	require.Equal(t, http.StatusFound, rec.Code)
	require.Equal(t, "/pipeline", rec.Header().Get("Location"))
}

func TestPipelineEventsStreamsServerSentEvents(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{Entries: func(context.Context) []pipeline.Entry {
		return []pipeline.Entry{{Title: "The Shawshank Redemption", Stage: pipeline.StageDownloading, Percent: 42}}
	}})

	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpSrv.URL+"/events/pipeline", nil)
	require.NoError(t, err)

	resp, err := httpSrv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	reader := bufio.NewReader(resp.Body)
	var lines []string
	for {
		line, err := reader.ReadString('\n')
		lines = append(lines, line)
		if err != nil || strings.TrimSpace(line) == "" && len(lines) > 1 {
			break
		}
	}
	joined := strings.Join(lines, "")
	require.Contains(t, joined, "event: pipeline")
	// The payload must be the same rendered HTML fragment views.PipelineRows
	// produces for the initial page load -- htmx's sse-swap replaces
	// #pipeline-rows's innerHTML with this verbatim, so raw JSON here would
	// show up as literal text in the browser instead of a row. Assert on an
	// attribute pipelineRow's markup writes and rule out a JSON encoding.
	require.Contains(t, joined, `data-stage="Downloading"`)
	require.Contains(t, joined, "Shawshank")
	require.NotContains(t, joined, `"title"`)
	require.NotContains(t, joined, `"stage"`)
}

// TestNewServerWarnsThroughTheContextLogger covers the removal of
// ui.Server's *slog.Logger field (CLAUDE.md: "no logger struct fields").
//
// The auth warning is the one line this service logs outside a request, so
// it is the one that would have justified keeping a field. It does not:
// NewServer takes a ctx for it and keeps nothing. Everything else logs
// through logging.FromContext(r.Context()), which ui.Run installs via the
// http.Server's BaseContext and which degrades to the discard logger under
// httptest -- the designed behaviour, and why the tests above can call
// NewServer with a bare context and see no output.
func TestNewServerWarnsThroughTheContextLogger(t *testing.T) {
	var buf bytes.Buffer
	ctx := logging.NewContext(t.Context(), slog.New(slog.NewJSONHandler(&buf, nil)))

	ui.NewServer(ctx, ui.Options{})

	require.Contains(t, buf.String(), "no built-in authentication",
		"NewServer must log the §A3.5 warning through the logger on its context")
}

// TestDownloadsEventsStreamsServerSentEvents is Task D3-3's basic
// acceptance test, mirroring TestPipelineEventsStreamsServerSentEvents:
// GET /events/downloads must stream an "event: downloads" frame whose data
// is rendered markup, not JSON. Per ruling R8, the assertions are on the
// stable data-download/data-phase/data-protocol attributes downloads.templ
// puts on every row, not on any visible copy.
func TestDownloadsEventsStreamsServerSentEvents(t *testing.T) {
	d := downloadv1.Download{
		ObjectMeta: metav1.ObjectMeta{Name: "arrival"},
		Spec:       downloadv1.DownloadSpec{Protocol: commonv1.ProtocolTorrent},
		Status:     downloadv1.DownloadStatus{Phase: downloadv1.DownloadPhaseDownloading, ProgressPercent: 42},
	}
	srv := ui.NewServer(t.Context(), ui.Options{
		SubscribeDownloads: func() (<-chan []downloadv1.Download, func()) {
			ch := make(chan []downloadv1.Download, 1)
			ch <- []downloadv1.Download{d}
			return ch, func() {}
		},
	})

	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpSrv.URL+"/events/downloads", nil)
	require.NoError(t, err)

	resp, err := httpSrv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	frame := readSSEEvent(t, bufio.NewReader(resp.Body))
	require.Contains(t, frame, "event: downloads")
	require.Contains(t, frame, `data-download="arrival"`)
	require.Contains(t, frame, `data-phase="Downloading"`)
	require.Contains(t, frame, `data-protocol="torrent"`)
	require.NotContains(t, frame, `"phase"`)
}

// TestDownloadsEventsStreamReflectsAStatusChangeBetweenTicks is Task D3-3's
// own bullet: "a status change between ticks produces a frame whose
// data-phase changed." Options.SubscribeDownloads is given a channel the
// test drives directly -- production feeds it from
// ui/projection.Projection's ticker, but the handler only ever sees "the
// next slice this channel yields," so pushing two slices for the same
// Download, one per simulated tick, exercises exactly that contract without
// waiting on a real timer.
func TestDownloadsEventsStreamReflectsAStatusChangeBetweenTicks(t *testing.T) {
	pending := downloadv1.Download{
		ObjectMeta: metav1.ObjectMeta{Name: "arrival"},
		Spec:       downloadv1.DownloadSpec{Protocol: commonv1.ProtocolTorrent},
		Status:     downloadv1.DownloadStatus{Phase: downloadv1.DownloadPhasePending},
	}
	downloading := downloadv1.Download{
		ObjectMeta: metav1.ObjectMeta{Name: "arrival"},
		Spec:       downloadv1.DownloadSpec{Protocol: commonv1.ProtocolTorrent},
		Status:     downloadv1.DownloadStatus{Phase: downloadv1.DownloadPhaseDownloading, ProgressPercent: 10},
	}

	ch := make(chan []downloadv1.Download, 2)
	ch <- []downloadv1.Download{pending}

	srv := ui.NewServer(t.Context(), ui.Options{
		SubscribeDownloads: func() (<-chan []downloadv1.Download, func()) {
			return ch, func() {}
		},
	})

	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpSrv.URL+"/events/downloads", nil)
	require.NoError(t, err)

	resp, err := httpSrv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	reader := bufio.NewReader(resp.Body)

	first := readSSEEvent(t, reader)
	require.Contains(t, first, `data-phase="Pending"`)

	// Simulate the next tick: the SAME Download, moved to Downloading.
	ch <- []downloadv1.Download{downloading}

	second := readSSEEvent(t, reader)
	require.Contains(t, second, `data-phase="Downloading"`)
	require.NotContains(t, second, `data-phase="Pending"`,
		"the second frame must reflect the status change, not repeat the first frame's phase")
}

// TestDownloadsEventsStreamEndsOnRequestContextCancel is Task D3-3's other
// bullet, mirroring run_test.go's TestRunEndsAnOpenSSEStreamOnContextCancel
// for /events/downloads specifically: handleDownloadsEvents' `case
// <-ctx.Done(): return` must fire -- and so end the stream from the
// server's side -- when the request's context is cancelled, exactly as
// handlePipelineEvents' already does.
//
// This drives the cancellation from the client rather than through
// ui.Run/BaseContext (run_test.go's version does that, generically, for
// every route): net/http already cancels a server handler's
// r.Context() once the client that issued the request cancels its own
// request context, closing the underlying connection -- so this reaches the
// same handler code path without spinning up ui.Run.
func TestDownloadsEventsStreamEndsOnRequestContextCancel(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{
		SubscribeDownloads: func() (<-chan []downloadv1.Download, func()) {
			ch := make(chan []downloadv1.Download, 1)
			ch <- []downloadv1.Download{}
			return ch, func() {}
		},
	})

	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	reqCtx, cancelReq := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, httpSrv.URL+"/events/downloads", nil)
	require.NoError(t, err)

	resp, err := httpSrv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	streamEnded := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		close(streamEnded)
	}()

	// Give the client a moment to actually be attached to the stream before
	// cancelling, mirroring run_test.go's own reasoning: exercise "cancel
	// with a client connected", not a race with connection setup.
	time.Sleep(50 * time.Millisecond)
	cancelReq()

	select {
	case <-streamEnded:
	case <-time.After(2 * time.Second):
		t.Fatal("downloads SSE stream was not closed after the request context was cancelled")
	}
}

// TestDownloadsEventPayloadIsExactlyViewsDownloadRows is the explicit
// markup-contract sanity check the task brief calls for: D3-2 built
// #downloads-rows and views.DownloadRows expecting /events/downloads (this
// task) to feed it byte-for-byte identical markup, since htmx's sse-swap
// replaces that element's innerHTML with the event's data verbatim
// (ui/views/downloads.templ's doc comments on Downloads and DownloadRows
// make the same point). This proves it directly: it renders the same
// downloads slice straight through views.DownloadRows, reproduces
// writeDownloadsEvent's own multi-line "data:" framing (ui/sse.go, unchanged
// by this task) around that output, and asserts the wire frame equals that
// byte-for-byte.
func TestDownloadsEventPayloadIsExactlyViewsDownloadRows(t *testing.T) {
	downloads := []downloadv1.Download{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "arrival"},
			Spec: downloadv1.DownloadSpec{
				Protocol: commonv1.ProtocolTorrent,
				Release:  commonv1.ReleaseInfo{Title: "Arrival 2016"},
			},
			Status: downloadv1.DownloadStatus{Phase: downloadv1.DownloadPhaseDownloading, ProgressPercent: 55},
		},
	}

	srv := ui.NewServer(t.Context(), ui.Options{
		SubscribeDownloads: func() (<-chan []downloadv1.Download, func()) {
			ch := make(chan []downloadv1.Download, 1)
			ch <- downloads
			return ch, func() {}
		},
	})

	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpSrv.URL+"/events/downloads", nil)
	require.NoError(t, err)
	resp, err := httpSrv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	got := readSSEEvent(t, bufio.NewReader(resp.Body))

	var direct bytes.Buffer
	require.NoError(t, views.DownloadRows(downloads).Render(context.Background(), &direct))

	var want bytes.Buffer
	want.WriteString("event: downloads\n")
	for _, line := range bytes.Split(direct.Bytes(), []byte{'\n'}) {
		want.WriteString("data: ")
		want.Write(line)
		want.WriteByte('\n')
	}
	want.WriteByte('\n')

	require.Equal(t, want.String(), got,
		"the event: downloads frame must carry exactly views.DownloadRows' rendered markup for #downloads-rows")
}
