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
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/projection"
)

// The list pages paginate (design 2026-09-23: 817 movies and 15,630
// episodes on one real library made each of them one unbounded response).
// ?page and ?per select a window, the default page is 50 rows, the pager
// links keep the size, and each page's SSE stream carries the same
// parameters so a live push redraws only the page in view. The fixtures
// are 120 rows so every page shows the default size, a middle page and a
// short last page.

func libraryFixture(n int) []projection.LibraryItem {
	out := make([]projection.LibraryItem, n)
	for i := range out {
		out[i] = projection.LibraryItem{
			Ref:   types.NamespacedName{Namespace: "default", Name: fmt.Sprintf("m-%03d", i)},
			Kind:  commonv1.MediaKindMovie,
			Tab:   projection.TabMovies,
			Title: fmt.Sprintf("Movie %03d", i),
		}
	}
	return out
}

func unmatchedFixture(n int) []projection.UnmatchedEntry {
	out := make([]projection.UnmatchedEntry, n)
	for i := range out {
		out[i] = projection.UnmatchedEntry{
			ScanRef:    types.NamespacedName{Namespace: "default", Name: "scan"},
			RootFolder: "movies",
			Path:       fmt.Sprintf("Unknown %03d/file.mkv", i),
			Reason:     "no_match",
		}
	}
	return out
}

func getPage(t *testing.T, srv *ui.Server, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	require.Equal(t, http.StatusOK, rec.Code, "GET %s", path)
	return rec.Body.String()
}

// TestListPagesShowOneWindowOfRows drives each page through the same
// table: the row attribute counted, the path, and which rows must and
// must not be on it.
func TestListPagesShowOneWindowOfRows(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{
		Library:   func(context.Context) []projection.LibraryItem { return libraryFixture(120) },
		Unmatched: func(context.Context) []projection.UnmatchedEntry { return unmatchedFixture(120) },
	})

	for name, tc := range map[string]struct {
		path     string
		rowAttr  string
		wantRows int
		wantIn   []string
		wantOut  []string
		pager    []string
	}{
		"unmatched small pages": {
			path: "/unmatched?per=25&page=5", rowAttr: `data-path="`, wantRows: 20,
			wantIn: []string{`Unknown 119/file.mkv`}, wantOut: []string{`Unknown 099/file.mkv`},
			pager: []string{`data-page="5"`, `data-last="5"`, `href="/unmatched?page=4&amp;per=25"`},
		},
		// The library scrolls instead of paging (design 2026-09-24): a page
		// is the start of the window, and the sentinel at its end fetches the
		// window one page wider; no pager.
		"library tab page": {
			path: "/library/movies?page=2", rowAttr: `data-ref="`, wantRows: 50,
			wantIn:  []string{`data-ref="default/m-050"`, `data-ref="default/m-099"`, `hx-get="/library/movies?page=2&amp;pages=2&amp;per=50"`},
			wantOut: []string{`data-ref="default/m-049"`, `data-ref="default/m-100"`, `data-pager`},
		},
		"library window": {
			path: "/library/movies?page=1&pages=2", rowAttr: `data-ref="`, wantRows: 100,
			wantIn:  []string{`data-ref="default/m-000"`, `data-ref="default/m-099"`, `hx-get="/library/movies?page=1&amp;pages=3&amp;per=50"`},
			wantOut: []string{`data-ref="default/m-100"`, `data-pager`},
		},
		"library whole window": {
			path: "/library/movies?pages=3", rowAttr: `data-ref="`, wantRows: 120,
			wantIn: []string{`data-ref="default/m-119"`}, wantOut: []string{`data-load-more`, `data-pager`},
		},
	} {
		t.Run(name, func(t *testing.T) {
			body := getPage(t, srv, tc.path)
			require.Equal(t, tc.wantRows, strings.Count(body, tc.rowAttr), "rows on %s", tc.path)
			for _, s := range tc.wantIn {
				require.Contains(t, body, s)
			}
			for _, s := range tc.wantOut {
				require.NotContains(t, body, s)
			}
			for _, s := range tc.pager {
				require.Contains(t, body, s, "pager on %s", tc.path)
			}
		})
	}
}

// TestListPagesTellTheirStreamWhichPageToPush: the SSE connection each
// page opens carries the page's own ?page and ?per, or the first push
// would redraw page 1 over whatever page the reader is on.
func TestListPagesTellTheirStreamWhichPageToPush(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{
		Library:   func(context.Context) []projection.LibraryItem { return libraryFixture(120) },
		Unmatched: func(context.Context) []projection.UnmatchedEntry { return unmatchedFixture(120) },
	})
	for path, want := range map[string]string{
		"/unmatched?per=25":             `sse-connect="/events/unmatched?page=1&amp;per=25"`,
		"/library/movies?page=2&per=50": `sse-connect="/events/library/movies?page=2&amp;per=50"`,
	} {
		require.Contains(t, getPage(t, srv, path), want, "GET %s", path)
	}
}

// TestListStreamsPushOnlyTheRequestedWindow: a stream opened with ?page
// and ?per slices every push to that window, so a 120-row list pushes 20
// rows to a reader on the last page, not 120.
func TestListStreamsPushOnlyTheRequestedWindow(t *testing.T) {
	// Every subscription gets a channel of its own holding one push, so a
	// case never waits on a push an earlier case's handler took.
	srv := ui.NewServer(t.Context(), ui.Options{
		SubscribeLibrary: func() (<-chan []projection.LibraryItem, func()) {
			ch := make(chan []projection.LibraryItem, 1)
			ch <- libraryFixture(120)
			return ch, func() {}
		},
		SubscribeUnmatched: func() (<-chan []projection.UnmatchedEntry, func()) {
			ch := make(chan []projection.UnmatchedEntry, 1)
			ch <- unmatchedFixture(120)
			return ch, func() {}
		},
	})
	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	for name, tc := range map[string]struct {
		path    string
		rowAttr string
		want    int
		wantIn  string
		wantOut string
	}{
		"library":              {"/events/library/movies?page=2&per=50", `data-ref="`, 50, `default/m-050`, `default/m-100`},
		"library window":       {"/events/library/movies?page=1&per=50&pages=2", `data-ref="`, 100, `default/m-099`, `default/m-100`},
		"library later window": {"/events/library/movies?page=2&per=50", `data-ref="`, 50, `data-load-prev`, `default/m-049`},
		"unmatched":            {"/events/unmatched?page=5&per=25", `data-path="`, 20, `Unknown 100/`, `Unknown 099/`},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpSrv.URL+tc.path, nil)
			require.NoError(t, err)
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()
			require.Equal(t, http.StatusOK, resp.StatusCode)

			frame := readSSEEvent(t, bufio.NewReader(resp.Body))
			require.Equal(t, tc.want, strings.Count(frame, tc.rowAttr), "rows in the first frame of %s", tc.path)
			require.Contains(t, frame, tc.wantIn)
			require.NotContains(t, frame, tc.wantOut)
			if strings.HasPrefix(tc.path, "/events/library/") {
				require.NotContains(t, frame, `data-pager`, "the library scrolls; its frame carries the sentinel instead")
				require.Contains(t, frame, `data-load-more`)
			} else {
				require.Contains(t, frame, `data-pager`, "the pager rides the frame so its counts stay live")
			}
		})
	}
}
