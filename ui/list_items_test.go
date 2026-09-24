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
	"github.com/mediactl/clustarr/pkg/pipeline"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/projection"
)

// The pipeline, downloads and unmatched rows are shadcn-templ items like
// the library's (design 2026-09-23): one item group per list, the title as
// the item's title, the stage or phase as a badge that keeps its meaning
// colour, the progress bar carrying data-progress, and the unmatched row's
// manual-assign form in the item's footer. Every data attribute the
// earlier tests key on stays on the item element.

func TestPipelineRowsAreItems(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{Entries: func(context.Context) []pipeline.Entry {
		return []pipeline.Entry{
			{Ref: types.NamespacedName{Namespace: "default", Name: "arrival"}, Kind: commonv1.MediaKindMovie,
				Title: "Arrival", Stage: pipeline.StageDownloading, Percent: 42, Detail: "1.2 GiB of 2.9 GiB"},
			{Ref: types.NamespacedName{Namespace: "default", Name: "heat"}, Kind: commonv1.MediaKindMovie,
				Title: "Heat", Stage: pipeline.StageFailed, Percent: -1, Failure: "no seeders"},
		}
	}})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/pipeline", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	require.Equal(t, 1, strings.Count(body, `data-slot="item-group"`))
	require.Equal(t, 2, strings.Count(body, `data-slot="item"`))
	requireTag(t, body, `data-ref="default/arrival"`, `data-slot="item"`, `data-kind="movie"`, `data-stage="Downloading"`)
	require.Regexp(t, `data-slot="item-title"[^>]*>[^<]*Arrival`, body)
	require.Regexp(t, `data-slot="badge"[^>]*bg-sky-500/20 text-sky-300"[^>]*>Downloading<`, body, "an in-flight stage keeps its colour")
	require.Regexp(t, `data-slot="badge"[^>]*bg-red-500/20 text-red-300"[^>]*>Failed<`, body, "a failed stage keeps its colour")
	require.Contains(t, body, `data-progress="42"`)
	require.Equal(t, 1, strings.Count(body, `data-progress=`), "a stage without a meaningful percent shows no bar")
	require.Contains(t, body, "no seeders")
}

func TestDownloadRowsAreItems(t *testing.T) {
	scheme, err := ui.NewReaderScheme()
	require.NoError(t, err)
	d := &downloadv1.Download{
		ObjectMeta: metav1.ObjectMeta{Name: "arrival", Namespace: "default"},
		Spec:       downloadv1.DownloadSpec{Protocol: commonv1.ProtocolTorrent, Release: commonv1.ReleaseInfo{Title: "Arrival 2016"}},
		Status:     downloadv1.DownloadStatus{Phase: downloadv1.DownloadPhaseDownloading, ProgressPercent: 55},
	}
	enabled := true
	c := &downloadv1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: "qbittorrent", Namespace: "default"},
		Spec:       downloadv1.DownloadClientSpec{Protocol: commonv1.ProtocolTorrent, Enabled: &enabled},
		Status:     downloadv1.DownloadClientStatus{Active: 2, Queued: 1, Seeding: 3, FreeBytes: 1 << 30},
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(d, c).Build()
	srv := ui.NewServer(t.Context(), ui.Options{Reader: reader})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/downloads", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	require.Equal(t, 2, strings.Count(body, `data-slot="item-group"`), "the clients and the downloads are each a group")
	requireTag(t, body, `data-download="arrival"`, `data-slot="item"`, `data-phase="Downloading"`, `data-protocol="torrent"`)
	require.Regexp(t, `data-slot="badge"[^>]*>Downloading<`, body)
	require.Contains(t, body, `data-progress="55"`)
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
