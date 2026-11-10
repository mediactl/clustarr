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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/ui"
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

// renderRunning renders rows as the Downloads activity page's card does
// (views.ActivityList), the rows on its Running tab.
func renderRunning(t *testing.T, rows []views.DownloadView) string {
	t.Helper()
	v := views.ActivityView{Section: "downloads", Title: "Downloads", Active: rows, Pages: map[string]views.ListPage{}}
	v.Summary.Downloads.Active.Total = len(rows)
	var buf bytes.Buffer
	require.NoError(t, views.ActivityList(v).Render(context.Background(), &buf))
	return buf.String()
}

// renderClients renders the Downloads activity page with clients.
func renderClients(t *testing.T, clients ...downloadv1.DownloadClient) string {
	t.Helper()
	var buf bytes.Buffer
	v := views.ActivityView{Section: "downloads", Title: "Downloads", Clients: clients, Pages: map[string]views.ListPage{}}
	require.NoError(t, views.Activity(v).Render(context.Background(), &buf))
	return buf.String()
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

			var body string
			require.NotPanics(t, func() { body = renderRunning(t, []views.DownloadView{d}) })
			require.Contains(t, body, `data-phase="`+string(phase)+`"`)
			require.Contains(t, body, `data-protocol="torrent"`)
			require.Contains(t, body, `data-download="release-`+string(phase)+`"`)
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

	var body string
	require.NotPanics(t, func() { body = renderRunning(t, []views.DownloadView{d}) })
	require.Contains(t, body, `data-phase=""`)
	require.Contains(t, body, `data-download="unclaimed"`)
}

// TestDownloadRowWithNoMeaningfulPercentRendersNoProgressBar pins
// downloadPercent's -1 sentinel (the same "no meaningful percentage"
// convention pkg/pipeline.Entry.Percent uses) through to the row: a Pending
// grab has not started transferring, so no percent is meaningful yet, and
// helpers.go's clampPercent must suppress the bar rather than render a
// 0%-wide one.
func TestDownloadRowWithNoMeaningfulPercentRendersNoProgressBar(t *testing.T) {
	d := views.DownloadView{
		Row:      grabRow("not-started", commonv1.DownloadPhasePending),
		Progress: &schema.DownloadProgress{PercentMilli: 0},
	}

	// The bar is shadcn-templ's progress, role="progressbar"
	// (downloads.templ's progressBar).
	require.NotContains(t, renderRunning(t, []views.DownloadView{d}), `role="progressbar"`,
		"a Pending download has no meaningful progress and must not render a progress bar")
}

// TestDownloadRowsEmptyStateSaysSo: an empty tab says what it holds none
// of, in the dashboard's muted note.
func TestDownloadRowsEmptyStateSaysSo(t *testing.T) {
	body := renderRunning(t, nil)
	require.Contains(t, body, "Nothing is downloading.")
	require.Contains(t, body, "text-muted-foreground")
	require.NotContains(t, body, `data-download`)
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

	body := renderClients(t, c)
	require.Contains(t, body, `data-client="qbittorrent"`)
	require.Contains(t, body, `data-protocol="torrent"`)
}

// TestDownloadsPageRendersWithoutACluster proves GET /activity/downloads
// survives Options.Reader and Options.Dashboard being nil (no cluster
// configured at all).
func TestDownloadsPageRendersWithoutACluster(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/activity/downloads", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "text-muted-foreground")
}

// TestDownloadsPageListsTheProjectionsRows proves the handler wiring end to
// end: the grab rows come from the projection (Options.Dashboard), the
// running one on the Running tab, a failed one on the Failed tab.
func TestDownloadsPageListsTheProjectionsRows(t *testing.T) {
	var d projection.Dashboard
	d.Downloads.Active = projection.DownloadList{Rows: []projection.DownloadRow{grabRow("arrival", commonv1.DownloadPhaseDownloading)}, Total: 1}
	d.Downloads.Failed = projection.DownloadList{Rows: []projection.DownloadRow{grabRow("heat", commonv1.DownloadPhaseFailed)}, Total: 1}
	srv := ui.NewServer(t.Context(), ui.Options{Dashboard: func(context.Context) projection.Dashboard { return d }})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/activity/downloads", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	running := body[strings.Index(body, `data-list="downloads-running"`):strings.Index(body, `data-list="downloads-queued"`)]
	require.Contains(t, running, `data-download="arrival"`)
	require.Contains(t, running, `data-phase="Downloading"`)
	failedAt := strings.Index(body, `data-list="downloads-failed"`)
	require.GreaterOrEqual(t, failedAt, 0)
	require.Contains(t, body[failedAt:], `data-download="heat"`)
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
// best-effort, read-only seam (see routes.go's downloadClients doc comment),
// and a transient list failure is not a reason to fail the whole request.
func TestDownloadsPageToleratesAListErrorFromTheReader(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{Reader: erroringReader{}})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/activity/downloads", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), `data-client`)
}

// TestTheOldPagesRedirectToActivity: /downloads and /pipeline, which the
// Activity group replaced (owner, 2026-11-10), send old links to it.
func TestTheOldPagesRedirectToActivity(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	for path, to := range map[string]string{"/downloads": "/activity/downloads", "/pipeline": "/activity", "/activity": "/activity/downloads"} {
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusFound, rec.Code, path)
		require.Equal(t, to, rec.Header().Get("Location"), path)
	}
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
	require.Contains(t, renderClients(t, c), "via SOCKS5 10.64.0.1:1080 (UDP)")

	c.Spec.Torrent.Proxy.UDP = &udpOff
	require.Contains(t, renderClients(t, c), "via SOCKS5 10.64.0.1:1080<")
}
