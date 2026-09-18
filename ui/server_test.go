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
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/pipeline"
	"github.com/mediactl/clustarr/ui"
)

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
