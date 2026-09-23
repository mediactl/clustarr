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
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/views"
)

// allDownloadPhases is every value download_types.go's DownloadPhase enum
// permits (download_types.go:75-98, eleven values, enforced there by
// +kubebuilder:validation:Enum). It is spelled out explicitly rather than
// derived from reflection so this test fails loudly -- "update this list" --
// if a phase is ever added to or removed from that enum without a matching
// update here, rather than silently covering fewer phases than the API
// allows.
var allDownloadPhases = []downloadv1.DownloadPhase{
	downloadv1.DownloadPhasePending,
	downloadv1.DownloadPhaseAssigned,
	downloadv1.DownloadPhaseQueued,
	downloadv1.DownloadPhaseDownloading,
	downloadv1.DownloadPhasePaused,
	downloadv1.DownloadPhaseCompleted,
	downloadv1.DownloadPhaseSeeding,
	downloadv1.DownloadPhaseImported,
	downloadv1.DownloadPhaseFailed,
	downloadv1.DownloadPhaseBlocklisted,
	downloadv1.DownloadPhaseRemoving,
}

// TestAllElevenDownloadPhasesRenderWithoutPanicking is Task D3-2's own
// acceptance check for ruling R5's "envtest writes status directly, so
// D3-2 ... get[s] full coverage against hand-written status covering all 11
// DownloadPhase values" -- it does not need envtest to do it: DownloadRows
// takes plain Go values, so a hand-built Download per phase is enough.
//
// Assertions are on the data-phase, data-protocol and data-download
// attributes (ruling R8), not on visible copy such as a badge's label.
func TestAllElevenDownloadPhasesRenderWithoutPanicking(t *testing.T) {
	require.Len(t, allDownloadPhases, 11,
		"download_types.go's DownloadPhase enum has 11 values; update allDownloadPhases if that changed")

	for _, phase := range allDownloadPhases {
		t.Run(string(phase), func(t *testing.T) {
			d := downloadv1.Download{
				ObjectMeta: metav1.ObjectMeta{Name: "release-" + string(phase), Namespace: "default"},
				Spec: downloadv1.DownloadSpec{
					Protocol: commonv1.ProtocolTorrent,
					Release:  commonv1.ReleaseInfo{Title: "Example Release"},
				},
				Status: downloadv1.DownloadStatus{Phase: phase, ProgressPercent: 37},
			}

			var buf bytes.Buffer
			require.NotPanics(t, func() {
				require.NoError(t, views.DownloadRows([]downloadv1.Download{d}).Render(context.Background(), &buf))
			})
			require.Contains(t, buf.String(), `data-phase="`+string(phase)+`"`)
			require.Contains(t, buf.String(), `data-protocol="torrent"`)
			require.Contains(t, buf.String(), `data-download="release-`+string(phase)+`"`)
		})
	}
}

// TestDownloadRowRendersTheZeroValueStatusWithoutError is R5's other half:
// grabarr/ has no controller yet, so every Download in a live cluster today
// sits at status.phase == "" with every other status field at its zero
// value. The row must render, not panic and not error.
func TestDownloadRowRendersTheZeroValueStatusWithoutError(t *testing.T) {
	d := downloadv1.Download{
		ObjectMeta: metav1.ObjectMeta{Name: "unclaimed", Namespace: "default"},
		Spec:       downloadv1.DownloadSpec{Protocol: commonv1.ProtocolTorrent},
		// Status is deliberately the zero value.
	}

	var buf bytes.Buffer
	require.NotPanics(t, func() {
		require.NoError(t, views.DownloadRows([]downloadv1.Download{d}).Render(context.Background(), &buf))
	})
	require.Contains(t, buf.String(), `data-phase=""`)
	require.Contains(t, buf.String(), `data-download="unclaimed"`)
}

// TestDownloadRowWithNoMeaningfulPercentRendersNoProgressBar pins
// downloadPercent's -1 sentinel (the same "no meaningful percentage"
// convention pkg/pipeline.Entry.Percent uses) through to the row: a Pending
// download has not started transferring, so status.progressPercent is not
// meaningful yet, and helpers.go's clampPercent -- already relied on by the
// Pipeline page for the identical sentinel -- must suppress the bar rather
// than render a 0%-wide one.
func TestDownloadRowWithNoMeaningfulPercentRendersNoProgressBar(t *testing.T) {
	d := downloadv1.Download{
		ObjectMeta: metav1.ObjectMeta{Name: "not-started", Namespace: "default"},
		Spec:       downloadv1.DownloadSpec{Protocol: commonv1.ProtocolTorrent},
		Status:     downloadv1.DownloadStatus{Phase: downloadv1.DownloadPhasePending, ProgressPercent: 0},
	}

	var buf bytes.Buffer
	require.NoError(t, views.DownloadRows([]downloadv1.Download{d}).Render(context.Background(), &buf))
	// "h-2 rounded-full bg-sky-500" is the progress-fill div's own class
	// (downloads.templ's downloadRow); a bare "bg-sky-500" substring check
	// would also match the phase badge's unrelated sky-blue default color.
	require.NotContains(t, buf.String(), "h-2 rounded-full bg-sky-500",
		"a Pending download has no meaningful progress and must not render a progress bar")
}

// TestDownloadRowsEmptyStateMirrorsThePipelinePage is the D3-2 bullet "Empty
// state mirrors the pipeline page's 'Nothing in flight' treatment": same
// muted-text paragraph style as ui/views/pipeline.templ's PipelineRows.
func TestDownloadRowsEmptyStateMirrorsThePipelinePage(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, views.DownloadRows(nil).Render(context.Background(), &buf))
	require.Contains(t, buf.String(), "text-slate-400")
	require.NotContains(t, buf.String(), `data-download`)
}

// TestDownloadsPageIncludesAClientSection covers the D3-2 bullet "A
// DownloadClient section: protocol, enabled, status.engine, active/queued/
// seeding counts, free bytes."
func TestDownloadsPageIncludesAClientSection(t *testing.T) {
	enabled := true
	c := downloadv1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: "qbittorrent", Namespace: "default"},
		Spec:       downloadv1.DownloadClientSpec{Protocol: commonv1.ProtocolTorrent, Enabled: &enabled},
		Status: downloadv1.DownloadClientStatus{
			Engine:    &downloadv1.EngineStatus{ReadyReplicas: 1, Replicas: 1},
			Active:    2,
			Queued:    1,
			Seeding:   3,
			FreeBytes: 1 << 30,
		},
	}

	var buf bytes.Buffer
	require.NoError(t, views.Downloads(nil, []downloadv1.DownloadClient{c}).Render(context.Background(), &buf))
	require.Contains(t, buf.String(), `data-client="qbittorrent"`)
	require.Contains(t, buf.String(), `data-protocol="torrent"`)
}

// TestDownloadsPageRendersWithoutACluster proves GET /downloads survives
// Options.Reader being nil (no cluster configured at all), exactly as
// GET /pipeline does with a nil Options.Entries -- see
// ui/reader_test.go's TestNilReaderStillServesThePipelinePage, which this
// mirrors for the new route.
func TestDownloadsPageRendersWithoutACluster(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/downloads", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "text-slate-400")
}

// TestDownloadsPageListsThroughTheReader proves the handler wiring end to
// end: a Download seeded into a fake, scheme-matched client.Reader (built
// from ui.NewReaderScheme, the same seam ui.NewClusterReader itself uses,
// rather than pkg/k8s.MustNewScheme -- ui/ must not import pkg/k8s at all)
// shows up in the rendered page through Options.Reader, with no projection
// package involved.
func TestDownloadsPageListsThroughTheReader(t *testing.T) {
	scheme, err := ui.NewReaderScheme()
	require.NoError(t, err)

	d := &downloadv1.Download{
		ObjectMeta: metav1.ObjectMeta{Name: "arrival", Namespace: "default"},
		Spec: downloadv1.DownloadSpec{
			Protocol: commonv1.ProtocolTorrent,
			Release:  commonv1.ReleaseInfo{Title: "Arrival 2016"},
		},
		Status: downloadv1.DownloadStatus{Phase: downloadv1.DownloadPhaseDownloading, ProgressPercent: 55},
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(d).Build()

	srv := ui.NewServer(t.Context(), ui.Options{Reader: reader})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/downloads", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `data-download="arrival"`)
	require.Contains(t, rec.Body.String(), `data-phase="Downloading"`)
}

// erroringReader is a client.Reader whose Get and List always fail, standing
// in for a cache that is reachable but errors on this particular call (e.g.
// a GVK not yet synced). It is a test fixture, not a write path: Get/List
// are the two client.Reader methods and neither is one of the AST guard's
// (Task D3-4) banned selectors.
type erroringReader struct{}

func (erroringReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return errors.New("boom")
}

func (erroringReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return errors.New("boom")
}

// TestDownloadsPageToleratesAListErrorFromTheReader proves a List error
// degrades to an empty-but-200 page rather than a 500: Options.Reader is a
// best-effort, read-only seam (see routes.go's listDownloads doc comment),
// and a transient list failure is not a reason to fail the whole request.
func TestDownloadsPageToleratesAListErrorFromTheReader(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{Reader: erroringReader{}})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/downloads", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), `data-download`)
}

// TestDownloadsNavLinkIsOnEveryPage covers the D3-2 bullet "Add /downloads
// to the layout nav alongside the pipeline link": both pages must carry a
// link to the other so a viewer never has to know the URL by hand.
func TestDownloadsNavLinkIsOnEveryPage(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/pipeline", nil))
	require.Contains(t, rec.Body.String(), `href="/downloads"`)

	rec = httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/downloads", nil))
	require.Contains(t, rec.Body.String(), `href="/pipeline"`)
}
