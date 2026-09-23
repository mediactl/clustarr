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
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/actions"
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

// TestUnmatchedPageRendersManualAssignFormPerRow proves the handler wiring
// for Task G3-4's manual-assign form: a stable data-manual-assign-form
// attribute (ruling R8), the hidden namespace/rootFolder/subpath fields
// built from the entry, a kind select offering every actions.MediaKinds()
// value, and the row's own candidates offered through a <datalist> the name
// field's list attribute references -- the "picker built from the row's
// candidates... plus a free-form kind/name" this task asks for.
func TestUnmatchedPageRendersManualAssignFormPerRow(t *testing.T) {
	entry := projection.UnmatchedEntry{
		ScanRef:    types.NamespacedName{Namespace: "media", Name: "movies-scan-abc12"},
		RootFolder: "movies",
		Path:       "Some Movie (2024)/Some.Movie.2024.mkv",
		Reason:     "ambiguous title match",
		Candidates: []string{"some-movie-2023", "some-movie-2024-remaster"},
	}
	srv := ui.NewServer(t.Context(), ui.Options{
		Unmatched: func(context.Context) []projection.UnmatchedEntry { return []projection.UnmatchedEntry{entry} },
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unmatched", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	require.Contains(t, body, `data-manual-assign-form="Some Movie (2024)/Some.Movie.2024.mkv"`)
	require.Contains(t, body, `action="/unmatched/assign"`)
	require.Contains(t, body, `name="namespace" value="media"`)
	require.Contains(t, body, `name="rootFolder" value="movies"`)
	require.Contains(t, body, `name="subpath" value="Some Movie (2024)/Some.Movie.2024.mkv"`)
	require.Contains(t, body, `name="return" value="/unmatched"`)
	require.Contains(t, body, `data-action="manual-assign"`)
	for _, kind := range actions.MediaKinds() {
		require.Contains(t, body, `<option value="`+string(kind)+`">`, "kind select is missing %s", kind)
	}
	require.Contains(t, body, `<option value="some-movie-2023">`)
	require.Contains(t, body, `<option value="some-movie-2024-remaster">`)
}

// TestUnmatchedPageManualAssignFormOmitsDatalistWithoutCandidates proves an
// entry with no candidates still renders the free-form kind/name/key
// fallback, without a stray empty <datalist>.
func TestUnmatchedPageManualAssignFormOmitsDatalistWithoutCandidates(t *testing.T) {
	entry := projection.UnmatchedEntry{
		ScanRef: types.NamespacedName{Namespace: "media", Name: "scan-a"}, RootFolder: "movies", Path: "x.mkv",
	}
	srv := ui.NewServer(t.Context(), ui.Options{
		Unmatched: func(context.Context) []projection.UnmatchedEntry { return []projection.UnmatchedEntry{entry} },
	})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unmatched", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "<datalist")
	require.Contains(t, rec.Body.String(), `action="/unmatched/assign"`)
}

// TestManualAssignActionWithNoWriterRendersVisibleError mirrors
// ui/library_test.go's own no-writer tests: a ui process with no cluster
// configured has a nil Options.Actions, which must still answer
// with a visible, machine-checkable error.
func TestManualAssignActionWithNoWriterRendersVisibleError(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	form := url.Values{
		"namespace": {"media"}, "rootFolder": {"movies"}, "subpath": {"x.mkv"},
		"kind": {"movie"}, "name": {"heat"},
	}
	req := httptest.NewRequest(http.MethodPost, "/unmatched/assign", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.Contains(t, rec.Body.String(), `data-action-error="no-writer"`)
}

// TestManualAssignActionWithInvalidTargetRendersVisibleError proves an
// invalid target renders actions.ErrInvalid visibly rather than a generic
// 500 or a silent no-op.
func TestManualAssignActionWithInvalidTargetRendersVisibleError(t *testing.T) {
	scheme := libraryTestScheme(t)
	writer := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := ui.NewServer(t.Context(), ui.Options{Actions: actions.New(writer)})

	rec := httptest.NewRecorder()
	form := url.Values{
		"namespace": {"media"}, "rootFolder": {"movies"}, "subpath": {"x.mkv"},
		"kind": {"podcast"}, "name": {"heat"},
	}
	req := httptest.NewRequest(http.MethodPost, "/unmatched/assign", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), `data-action-error="invalid"`)
}

// TestManualAssignActionSucceedsAndRedirectsToScanDetail proves the success
// path end to end against a real (fake) writer: the LibraryScan is created
// with the annotation and spec this task's own instruction describes, and
// the handler redirects to that scan's own detail page -- not to a fixed
// "return" field -- so the user can see whether the worker accepted it.
func TestManualAssignActionSucceedsAndRedirectsToScanDetail(t *testing.T) {
	scheme := libraryTestScheme(t)
	writer := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := ui.NewServer(t.Context(), ui.Options{Actions: actions.New(writer)})

	rec := httptest.NewRecorder()
	form := url.Values{
		"namespace": {"media"}, "rootFolder": {"movies"}, "subpath": {"Heat (1995)/Heat.mkv"},
		"kind": {"movie"}, "name": {"heat-1995"},
	}
	req := httptest.NewRequest(http.MethodPost, "/unmatched/assign", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	srv.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusSeeOther, rec.Code)
	location := rec.Header().Get("Location")
	require.Regexp(t, `^/library-scans/media/assign-`, location)

	var scans catalogv1.LibraryScanList
	require.NoError(t, writer.List(t.Context(), &scans))
	require.Len(t, scans.Items, 1)
	scan := scans.Items[0]
	require.Equal(t, "movies", scan.Spec.RootFolderRef)
	require.Equal(t, "Heat (1995)/Heat.mkv", scan.Spec.Subpath)
	require.Equal(t, catalogv1.ScanModeFull, scan.Spec.Mode)
	require.Equal(t, "movie/heat-1995", scan.Annotations[actions.AnnotationImportTarget])
	require.Equal(t, actions.OriginUI, scan.Labels[actions.LabelOrigin])
	require.Equal(t, "/library-scans/media/"+scan.Name, location)
}

// TestLibraryScanDetailPageRendersStatus proves GET
// /library-scans/{namespace}/{name} reads the scan through Options.Reader
// and renders its phase, counts, and -- when the Ready condition is not
// True -- the reason and message a manual-assign refusal (a bad target, a
// subpath outside the root, a path recorded elsewhere) would carry.
func TestLibraryScanDetailPageRendersStatus(t *testing.T) {
	scheme := libraryTestScheme(t)
	scan := &catalogv1.LibraryScan{
		ObjectMeta: metav1.ObjectMeta{Name: "assign-abc12", Namespace: "media"},
		Spec:       catalogv1.LibraryScanSpec{RootFolderRef: "movies", Subpath: "Heat (1995)/Heat.mkv"},
		Status: catalogv1.LibraryScanStatus{
			Phase: catalogv1.ScanPhaseFailed, FilesSeen: 1, FilesMatched: 0,
			Conditions: []metav1.Condition{{
				Type: "Ready", Status: metav1.ConditionFalse, Reason: "TargetNotFound",
				Message: "movie \"heat-1995\" does not exist", LastTransitionTime: metav1.Now(),
			}},
		},
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(scan).Build()
	srv := ui.NewServer(t.Context(), ui.Options{Reader: reader})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library-scans/media/assign-abc12", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	require.Contains(t, body, `data-scan="media/assign-abc12"`)
	require.Contains(t, body, `data-phase="Failed"`)
	require.Contains(t, body, "TargetNotFound")
	require.Contains(t, body, `movie &#34;heat-1995&#34; does not exist`)
}

// TestLibraryScanDetailPageNotFoundForAnUnknownScan proves a missing scan
// 404s rather than panicking or rendering an empty page as if it were real.
func TestLibraryScanDetailPageNotFoundForAnUnknownScan(t *testing.T) {
	scheme := libraryTestScheme(t)
	reader := fake.NewClientBuilder().WithScheme(scheme).Build()
	srv := ui.NewServer(t.Context(), ui.Options{Reader: reader})

	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library-scans/media/nope", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}

// TestLibraryScanDetailPageNotFoundWithoutACluster proves a nil
// Options.Reader (no cluster configured) also 404s, rather than panicking.
func TestLibraryScanDetailPageNotFoundWithoutACluster(t *testing.T) {
	srv := ui.NewServer(t.Context(), ui.Options{})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/library-scans/media/assign-abc12", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)
}
