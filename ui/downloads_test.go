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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/paging"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// grabEntry is a minimal torrent grab entry with id in phase, as an owner's
// status.downloads holds it (ADR-0019 §6.2).
func grabEntry(id string, phase commonv1.DownloadPhase) catalogv1.DownloadEntry {
	return catalogv1.DownloadEntry{
		ID: id, UID: id + "-uid", Phase: phase,
		Release: catalogv1.DownloadRelease{Title: "Example Release", Protocol: commonv1.ProtocolTorrent},
	}
}

// grabRow is the Downloads-page row of a grab entry with id in phase, held
// by a Movie of the same name.
func grabRow(id string, phase commonv1.DownloadPhase) projection.DownloadRow {
	return projection.DownloadRow{
		Owner:     schema.ItemRef{Kind: "Movie", Ref: schema.Ref{Namespace: "default", Name: id, UID: id + "-owner"}},
		OwnerKind: commonv1.MediaKindMovie,
		Entry:     grabEntry(id, phase),
	}
}

// grabbingMovie is a Movie named name whose status.downloads holds entries:
// what a cluster reader lists for the Downloads page since the Download kind
// was folded into its owners.
func grabbingMovie(name string, entries ...catalogv1.DownloadEntry) *catalogv1.Movie {
	return &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID(name + "-owner")},
		Status:     catalogv1.MovieStatus{Downloads: entries},
	}
}

// allDownloadPhases is every value the DownloadPhase enum permits
// (api/common/v1alpha1/download_enums.go, eleven values, enforced there by
// +kubebuilder:validation:Enum). It is spelled out explicitly rather than
// derived from reflection so this test fails loudly -- "update this list" --
// if a phase is ever added to or removed from that enum without a matching
// update here, rather than silently covering fewer phases than the API
// allows.
var allDownloadPhases = []commonv1.DownloadPhase{
	commonv1.DownloadPhasePending,
	commonv1.DownloadPhaseAssigned,
	commonv1.DownloadPhaseQueued,
	commonv1.DownloadPhaseDownloading,
	commonv1.DownloadPhasePaused,
	commonv1.DownloadPhaseCompleted,
	commonv1.DownloadPhaseSeeding,
	commonv1.DownloadPhaseImported,
	commonv1.DownloadPhaseFailed,
	commonv1.DownloadPhaseBlocklisted,
	commonv1.DownloadPhaseRemoving,
}

// TestAllElevenDownloadPhasesRenderWithoutPanicking is Task D3-2's own
// acceptance check for ruling R5's "envtest writes status directly, so
// D3-2 ... get[s] full coverage against hand-written status covering all 11
// DownloadPhase values" -- it does not need envtest to do it: DownloadRows
// takes plain Go values, so a hand-built grab entry per phase is enough.
//
// Assertions are on the data-phase, data-protocol and data-download
// attributes (ruling R8), not on visible copy such as a badge's label.
func TestAllElevenDownloadPhasesRenderWithoutPanicking(t *testing.T) {
	require.Len(t, allDownloadPhases, 11,
		"the DownloadPhase enum has 11 values; update allDownloadPhases if that changed")

	for _, phase := range allDownloadPhases {
		t.Run(string(phase), func(t *testing.T) {
			d := views.DownloadView{
				Row:      grabRow("release-"+string(phase), phase),
				Progress: &schema.DownloadProgress{PercentMilli: 37000},
			}

			var buf bytes.Buffer
			require.NotPanics(t, func() {
				require.NoError(t, views.DownloadRows([]views.DownloadView{d}).Render(context.Background(), &buf))
			})
			require.Contains(t, buf.String(), `data-phase="`+string(phase)+`"`)
			require.Contains(t, buf.String(), `data-protocol="torrent"`)
			require.Contains(t, buf.String(), `data-download="release-`+string(phase)+`"`)
		})
	}
}

// TestDownloadRowRendersTheZeroValueStatusWithoutError is R5's other half:
// a grab entry the downloads stage has not admitted yet sits at phase ""
// with every other field at its zero value, and no telemetry exists for it.
// The row must render, not panic and not error.
func TestDownloadRowRendersTheZeroValueStatusWithoutError(t *testing.T) {
	d := views.DownloadView{Row: projection.DownloadRow{
		OwnerKind: commonv1.MediaKindMovie,
		// The entry is deliberately the zero value but for its id.
		Entry: catalogv1.DownloadEntry{ID: "unclaimed"},
	}}

	var buf bytes.Buffer
	require.NotPanics(t, func() {
		require.NoError(t, views.DownloadRows([]views.DownloadView{d}).Render(context.Background(), &buf))
	})
	require.Contains(t, buf.String(), `data-phase=""`)
	require.Contains(t, buf.String(), `data-download="unclaimed"`)
}

// TestDownloadRowWithNoMeaningfulPercentRendersNoProgressBar pins
// downloadPercent's -1 sentinel (the same "no meaningful percentage"
// convention pkg/pipeline.Entry.Percent uses) through to the row: a Pending
// grab has not started transferring, so no percent is meaningful yet, and
// helpers.go's clampPercent -- already relied on by the Pipeline page for
// the identical sentinel -- must suppress the bar rather than render a
// 0%-wide one.
func TestDownloadRowWithNoMeaningfulPercentRendersNoProgressBar(t *testing.T) {
	d := views.DownloadView{
		Row:      grabRow("not-started", commonv1.DownloadPhasePending),
		Progress: &schema.DownloadProgress{PercentMilli: 0},
	}

	var buf bytes.Buffer
	require.NoError(t, views.DownloadRows([]views.DownloadView{d}).Render(context.Background(), &buf))
	// "h-2 rounded-full bg-primary" is the progress-fill div's own class
	// (downloads.templ's downloadRow); a bare "bg-primary" substring check
	// would also match the phase badge's unrelated sky-blue default color.
	require.NotContains(t, buf.String(), "h-2 rounded-full bg-primary",
		"a Pending download has no meaningful progress and must not render a progress bar")
}

// TestDownloadRowsEmptyStateMirrorsThePipelinePage is the D3-2 bullet "Empty
// state mirrors the pipeline page's 'Nothing in flight' treatment": same
// muted-text paragraph style as ui/views/pipeline.templ's PipelineRows.
func TestDownloadRowsEmptyStateMirrorsThePipelinePage(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, views.DownloadRows(nil).Render(context.Background(), &buf))
	require.Contains(t, buf.String(), "text-muted-foreground")
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
	require.NoError(t, views.Downloads(paging.Paginate(0, 1, paging.DefaultPer), nil, []downloadv1.DownloadClient{c}).Render(context.Background(), &buf))
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
	require.Contains(t, rec.Body.String(), "text-muted-foreground")
}

// TestDownloadsPageListsThroughTheReader proves the handler wiring end to
// end: a grab entry on a Movie seeded into a fake, scheme-matched
// client.Reader (built from ui.NewReaderScheme, the same seam
// ui.NewClusterReader itself uses, rather than pkg/k8s.MustNewScheme -- ui/
// must not import pkg/k8s at all) shows up in the rendered page through
// Options.Reader (projection.ListDownloadRows), with no projection running.
func TestDownloadsPageListsThroughTheReader(t *testing.T) {
	scheme, err := ui.NewReaderScheme()
	require.NoError(t, err)

	reader := fake.NewClientBuilder().WithScheme(scheme).
		WithObjects(grabbingMovie("arrival", grabEntry("arrival", commonv1.DownloadPhaseDownloading))).Build()

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

// TestDownloadsPageShowsATorrentClientsProxy: a proxied client says so,
// and whether UDP goes through it.
func TestDownloadsPageShowsATorrentClientsProxy(t *testing.T) {
	udpOff := false
	c := downloadv1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: "frugal", Namespace: "default"},
		Spec: downloadv1.DownloadClientSpec{Protocol: commonv1.ProtocolTorrent, Torrent: &downloadv1.TorrentSpec{
			Proxy: &downloadv1.TorrentProxy{Host: "10.64.0.1", Port: 1080},
		}},
	}
	var buf bytes.Buffer
	require.NoError(t, views.Downloads(paging.Paginate(0, 1, paging.DefaultPer), nil, []downloadv1.DownloadClient{c}).Render(context.Background(), &buf))
	require.Contains(t, buf.String(), "via SOCKS5 10.64.0.1:1080 (UDP)")

	c.Spec.Torrent.Proxy.UDP = &udpOff
	buf.Reset()
	require.NoError(t, views.Downloads(paging.Paginate(0, 1, paging.DefaultPer), nil, []downloadv1.DownloadClient{c}).Render(context.Background(), &buf))
	require.Contains(t, buf.String(), "via SOCKS5 10.64.0.1:1080<")
}
