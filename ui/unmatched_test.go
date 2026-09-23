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
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/projection"
)

// TestUnmatchedPageRendersWithoutACluster proves GET /unmatched survives
// Options.Unmatched being nil (no cluster configured at all), mirroring
// ui/downloads_test.go's TestDownloadsPageRendersWithoutACluster and this
// task's own TestLibraryPageRendersWithoutACluster.
func TestUnmatchedPageRendersWithoutACluster(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unmatched", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "Nothing unmatched.")
}

// TestUnmatchedPageRendersEntriesWithDataAttributes proves the handler
// wiring end to end: a fixture UnmatchedEntry (candidates included) shows up
// in the rendered page with the data-scan, data-root-folder, data-path,
// data-reason and data-candidates attributes ruling R8 requires tests to
// assert against instead of visible copy. This task's Unmatched page is
// read-only (G3-4 adds the manual-assign action once G2-4 lands it in
// importarr), so there is deliberately no action form to assert on here.
func TestUnmatchedPageRendersEntriesWithDataAttributes(t *testing.T) {
	entry := projection.UnmatchedEntry{
		ScanRef:    types.NamespacedName{Namespace: "default", Name: "movies-scan-abc12"},
		RootFolder: "movies",
		Path:       "Some.Movie.2024.mkv",
		Reason:     "ambiguous title match",
		Candidates: []string{"some-movie-2023", "some-movie-2024-remaster"},
		SeenAt:     metav1.Now(),
	}

	srv := ui.NewServer(t.Context(), ui.Options{
		Unmatched: func(context.Context) []projection.UnmatchedEntry { return []projection.UnmatchedEntry{entry} },
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unmatched", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	require.Contains(t, body, `data-scan="default/movies-scan-abc12"`)
	require.Contains(t, body, `data-root-folder="movies"`)
	require.Contains(t, body, `data-path="Some.Movie.2024.mkv"`)
	require.Contains(t, body, `data-reason="ambiguous title match"`)
	require.Contains(t, body, `data-candidates="some-movie-2023,some-movie-2024-remaster"`)
}

// TestUnmatchedPageEmptyStateCarriesNoRowAttributes mirrors
// ui/downloads_test.go's TestDownloadRowsEmptyStateMirrorsThePipelinePage:
// the empty state must not carry a stray data-path (or any other row
// attribute) that a test asserting "no rows" could be fooled by.
func TestUnmatchedPageEmptyStateCarriesNoRowAttributes(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unmatched", nil))
	require.NotContains(t, rec.Body.String(), "data-path")
}

// TestUnmatchedEventsStreamReflectsAChangeBetweenTicks mirrors this task's
// own TestLibraryEventsStreamReflectsAStatusChangeBetweenTicks and
// ui/server_test.go's downloads-stream analogue: pushing two slices on
// Options.SubscribeUnmatched's channel exercises the "next slice this
// channel yields" contract without waiting on a real timer.
func TestUnmatchedEventsStreamReflectsAChangeBetweenTicks(t *testing.T) {
	first := projection.UnmatchedEntry{
		ScanRef: types.NamespacedName{Namespace: "default", Name: "scan-a"}, RootFolder: "movies",
		Path: "a.mkv", Reason: "no embedded id",
	}
	second := projection.UnmatchedEntry{
		ScanRef: types.NamespacedName{Namespace: "default", Name: "scan-b"}, RootFolder: "tv",
		Path: "b.mkv", Reason: "ambiguous title",
	}

	ch := make(chan []projection.UnmatchedEntry, 2)
	ch <- []projection.UnmatchedEntry{first}

	srv := ui.NewServer(t.Context(), ui.Options{
		SubscribeUnmatched: func() (<-chan []projection.UnmatchedEntry, func()) { return ch, func() {} },
	})

	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpSrv.URL+"/events/unmatched", nil)
	require.NoError(t, err)
	resp, err := httpSrv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	reader := bufio.NewReader(resp.Body)
	firstFrame := readSSEEvent(t, reader)
	require.Contains(t, firstFrame, "event: unmatched")
	require.Contains(t, firstFrame, `data-path="a.mkv"`)

	ch <- []projection.UnmatchedEntry{second}
	secondFrame := readSSEEvent(t, reader)
	require.Contains(t, secondFrame, `data-path="b.mkv"`)
	require.NotContains(t, secondFrame, `data-path="a.mkv"`,
		"the second frame must reflect the change, not repeat the first frame's file")
}
