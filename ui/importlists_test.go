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
	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/projection"
)

// TestImportListsPageRendersWithoutACluster proves GET /import-lists
// survives Options.ImportLists being nil (no cluster configured at all),
// mirroring ui/unmatched_test.go's TestUnmatchedPageRendersWithoutACluster.
func TestImportListsPageRendersWithoutACluster(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/import-lists", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "No import lists configured.")
}

// TestImportListsPageRendersEntriesWithDataAttributes proves the handler
// wiring end to end: a fixture ImportListEntry, including a pending
// device-code flow, shows up in the rendered page with the data-list,
// data-enabled, data-source, data-sync-level and data-auth-state attributes
// ruling R8 requires tests to assert against, plus the user code and
// verification URL this task's own instruction calls out ("Show the code
// and URL clearly while authorization is pending").
func TestImportListsPageRendersEntriesWithDataAttributes(t *testing.T) {
	entry := projection.ImportListEntry{
		Ref:        types.NamespacedName{Namespace: "default", Name: "trakt-watchlist"},
		Kinds:      []string{"movie", "series"},
		Enabled:    true,
		SourceType: "trakt",
		SyncLevel:  "logOnly",
		ItemCount:  10,
		Auth: &projection.AuthView{
			State: "pending", UserCode: "AB12-CD34", VerificationURL: "https://trakt.tv/activate",
		},
	}

	srv := ui.NewServer(t.Context(), ui.Options{
		ImportLists: func(context.Context) []projection.ImportListEntry { return []projection.ImportListEntry{entry} },
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/import-lists", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	require.Contains(t, body, `data-list="default/trakt-watchlist"`)
	require.Contains(t, body, `data-enabled="true"`)
	require.Contains(t, body, `data-source="trakt"`)
	require.Contains(t, body, `data-sync-level="logOnly"`)
	require.Contains(t, body, `data-auth-state="pending"`)
	require.Contains(t, body, "AB12-CD34", "the pending flow's user code must be shown clearly")
	require.Contains(t, body, "https://trakt.tv/activate", "the pending flow's verification URL must be shown clearly")
}

// TestImportListsPageEmptyStateCarriesNoRowAttributes mirrors
// ui/unmatched_test.go's TestUnmatchedPageEmptyStateCarriesNoRowAttributes:
// the empty state must not carry a stray data-list that a test asserting
// "no rows" could be fooled by.
func TestImportListsPageEmptyStateCarriesNoRowAttributes(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/import-lists", nil))
	require.NotContains(t, rec.Body.String(), "data-list")
}

// TestImportListsEventsStreamReflectsAChangeBetweenTicks mirrors
// ui/unmatched_test.go's TestUnmatchedEventsStreamReflectsAChangeBetweenTicks:
// pushing two slices on Options.SubscribeImportLists's channel exercises the
// "next slice this channel yields" contract without waiting on a real timer.
func TestImportListsEventsStreamReflectsAChangeBetweenTicks(t *testing.T) {
	first := projection.ImportListEntry{
		Ref: types.NamespacedName{Namespace: "default", Name: "list-a"}, SourceType: "trakt", Enabled: true,
	}
	second := projection.ImportListEntry{
		Ref: types.NamespacedName{Namespace: "default", Name: "list-b"}, SourceType: "plex", Enabled: false,
	}

	ch := make(chan []projection.ImportListEntry, 2)
	ch <- []projection.ImportListEntry{first}

	srv := ui.NewServer(t.Context(), ui.Options{
		SubscribeImportLists: func() (<-chan []projection.ImportListEntry, func()) { return ch, func() {} },
	})

	httpSrv := httptest.NewServer(srv.Handler())
	defer httpSrv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, httpSrv.URL+"/events/import-lists", nil)
	require.NoError(t, err)
	resp, err := httpSrv.Client().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	reader := bufio.NewReader(resp.Body)
	firstFrame := readSSEEvent(t, reader)
	require.Contains(t, firstFrame, "event: import-lists")
	require.Contains(t, firstFrame, `data-list="default/list-a"`)

	ch <- []projection.ImportListEntry{second}
	secondFrame := readSSEEvent(t, reader)
	require.Contains(t, secondFrame, `data-list="default/list-b"`)
	require.NotContains(t, secondFrame, `data-list="default/list-a"`,
		"the second frame must reflect the change, not repeat the first frame's list")
}

// TestImportListsNoAuthRendersNoAuthState proves an entry with no pending
// device-code flow carries no data-auth-state value and shows no user code
// or verification URL -- the page must not fabricate an authorization box
// for a list that has none.
func TestImportListsNoAuthRendersNoAuthState(t *testing.T) {
	entry := projection.ImportListEntry{
		Ref: types.NamespacedName{Namespace: "default", Name: "plex-watchlist"}, SourceType: "plex", Enabled: true,
	}
	srv := ui.NewServer(t.Context(), ui.Options{
		ImportLists: func(context.Context) []projection.ImportListEntry { return []projection.ImportListEntry{entry} },
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/import-lists", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	require.Contains(t, body, `data-auth-state=""`)
	require.NotContains(t, body, "Authorization required")
}
