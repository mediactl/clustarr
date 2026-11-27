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
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/projection"
)

// The unmatched rows and the download clients are shadcn-templ items like
// the library's (design 2026-09-23): one item group per list, the title as
// the item's title, the stage or phase as a badge that keeps its meaning
// colour, and the unmatched row's manual-assign form in the item's footer.
// A download is a row of its Activity list since 2026-11-27 (owner: "each
// item in the list should follow the dashboard page's styling and how it
// leveraged the progress component"): the progress component in full, its
// percent on data-progress, the phase a toned badge. Every data attribute
// the earlier tests key on stays on the row's element.

func TestDownloadRowsAreItems(t *testing.T) {
	scheme, err := ui.NewReaderScheme()
	require.NoError(t, err)
	d := grabbingMovie("arrival", grabEntry("arrival", commonv1.DownloadPhaseDownloading))
	enabled := true
	c := &downloadv1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: "qbittorrent", Namespace: "default"},
		Spec:       downloadv1.DownloadClientSpec{Protocol: commonv1.ProtocolTorrent, Enabled: &enabled},
		Status:     downloadv1.DownloadClientStatus{Active: 2, Queued: 1, Seeding: 3, FreeBytes: 1 << 30},
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(d, c).Build()
	rows, err := projection.ListDownloadRows(t.Context(), reader)
	require.NoError(t, err)
	// The progress bar's percent is the transfer's clustarr-progress
	// telemetry, not a field of the entry.
	srv := ui.NewServer(t.Context(), ui.Options{
		Reader: reader,
		Dashboard: func(context.Context) projection.Dashboard {
			var dash projection.Dashboard
			dash.Downloads.Active = projection.DownloadList{Rows: rows, Total: len(rows)}
			return dash
		},
		TransferProgress: func(_ context.Context, uid string) (schema.DownloadProgress, bool, error) {
			return schema.DownloadProgress{PercentMilli: 55000}, uid == "arrival-uid", nil
		},
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/activity/downloads", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	require.Equal(t, 1, strings.Count(body, `data-slot="item-group"`), "the clients are a group; the downloads are a list's rows")
	requireTag(t, body, `data-download="arrival"`, `grid-cols-subgrid`, `data-phase="Downloading"`, `data-protocol="torrent"`)
	require.Regexp(t, `data-slot="badge"[^>]*bg-sky-500/20[^>]*>Downloading<`, body)
	requireTag(t, body, `data-progress="55"`, `data-slot="progress"`)
	require.Contains(t, body, `data-slot="progress-label"`, "the dashboard's bar: its label and its value over it")
	require.Contains(t, body, `data-slot="progress-value"`)
	requireTag(t, body, `data-client="qbittorrent"`, `data-slot="item"`, `data-protocol="torrent"`)
	require.Regexp(t, `data-slot="badge"[^>]*>enabled<`, body, "the client's enabled state is a badge")
}

func TestUnmatchedRowsAreItems(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{Unmatched: func(context.Context) []projection.UnmatchedEntry {
		return []projection.UnmatchedEntry{{
			ScanRef: types.NamespacedName{Namespace: "default", Name: "scan"}, RootFolder: "movies",
			Path: "Unknown (2019)/file.mkv", Reason: "no_match", Candidates: []string{"Unknown", "The Unknown"},
		}}
	}})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unmatched", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	require.Equal(t, 1, strings.Count(body, `data-slot="item-group"`))
	requireTag(t, body, `data-path="Unknown (2019)/file.mkv"`, `data-slot="item"`, `data-scan="default/scan"`,
		`data-root-folder="movies"`, `data-reason="no_match"`, `data-candidates="Unknown,The Unknown"`)
	require.Regexp(t, `data-slot="badge"[^>]*>movies<`, body, "the root folder is a badge")
	require.Regexp(t, `data-slot="item-footer"[^>]*>\s*<form[^>]*data-manual-assign-form=`, body, "the assign form is the item's footer")
}
