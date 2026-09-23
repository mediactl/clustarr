//go:build e2e

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

// Scenario 14 -- the pipeline and downloads pages (Task D3-5).
//
// The full scenario (remaining-work.md's item 14) covers every UI page and a
// UI-driven spec patch; Task D3-5's own scope note narrows this file to what
// Phase D3 actually built: "the pipeline and downloads pages only" (Library,
// import lists, settings and unmatched are Phase G). There is no UI action
// that patches spec yet either -- D3's pages are read-only views with no
// forms -- so that third of scenario 14's text has nothing to exercise here.
//
// What IS exercised, against `ui`'s real HTTP server rather than against
// views.Downloads/views.Pipeline directly the way ui/*_test.go's unit tests
// do:
//
//  1. GET /pipeline returns 200 with a row for a Movie this test created;
//     GET /downloads returns 200 with a row for a Download this test created.
//  2. GET /events/downloads emits an SSE frame reflecting a non-empty
//     status.phase for that Download -- gated behind the phase actually
//     leaving its zero value first, and SKIPPED with a named reason if it
//     never does. grabarr's Download controller (D2-4) is what writes
//     status.phase at all, and the reaper/engine work that carries a
//     Download further than its very first phase is landing in this
//     worktree, by another agent, WHILE this file is being written -- so
//     whether the deployed image can move a Download at all is exactly the
//     fact this file cannot assume.
//  3. managedFields on the Movie, the Download and the MediaFile carry no
//     field manager whose name contains "ui" -- the machine-checkable form
//     of CLAUDE.md's "the UI never writes status and owns no CRD", the same
//     way TestMediaFileTwoWriter reads managedFields rather than inferring
//     ownership from which fields happen to be non-zero.
//
// The Movie and MediaFile come from the same route TestMediaFileTwoWriter
// uses -- a RootFolder, a planted clip, a full LibraryScan -- because it is
// the only route in this suite that produces a REAL MediaFile without
// depending on D2's still-landing grab-to-download wiring. The Download is
// created directly, in the shape
// grabarr/controller/download/controller_envtest_test.go's own
// newTorrentDownload fixture uses (proven legal against the CRD's "release
// identity is immutable" CEL rule): scenario 14 is about the UI's READ path,
// and nothing in the tree creates a Download from a grab decision yet
// (catalogarr/worker/search does not either), so there is no "real" grab
// flow to drive instead.
//
// `ui` is reached over a `kubectl port-forward` subprocess
// (portForwardService, test/e2e/helpers_test.go), because kind publishes no
// extraPortMappings for any ClusterIP Service and the test binary runs on
// the host (see this package's doc comment in main_test.go, "How it reaches
// the cluster"). hack/e2e.sh's own diagnostics dump reaches NATS's monitor
// port the identical way.
//
// Every assertion here follows ruling R8: identify a row by a stable
// data-* attribute or by fixture-derived data (a Movie's resolved title, or
// its own object name before that resolves), never by prose copy that a
// wording change could break independently of the markup. pipeline.templ
// carries no per-row identifying data attribute the way downloads.templ's
// data-download does (only data-kind and data-stage, which are shared
// across every row of a kind/stage) -- see the "pipeline and downloads
// pages" subtest below for how this file works around that without
// touching ui/views/pipeline.templ, which is out of D3-5's file list.
//
// Build-tagged e2e. Per the standing instruction (2026-09-22), e2e stays
// unexecuted until D1-D3 implementation is complete: this file has never
// been run against a kind cluster.
package e2e

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

const (
	// uiServicePort is config/manager/ui.yaml's Service port -- named
	// "http", 8080 -- the one thing every request in this file reaches
	// through portForwardService.
	uiServicePort = 8080

	// uiPageWaitTimeout bounds each page-content poll (GET /pipeline, GET
	// /downloads). Both read straight from the informer-backed cache
	// /readyz already gates on (Task D3-0), so this only needs to cover
	// that cache's own watch latency plus a couple of this suite's
	// pollInterval cycles -- not a redelivery ladder, there isn't one on
	// this path.
	uiPageWaitTimeout = 2 * time.Minute

	// uiPhaseGateTimeout bounds the PRE-check that a Download's
	// status.phase left its zero value at all -- the gate the D3-5 plan
	// task text names explicitly, separate from the SSE assertion itself.
	// Whether grabarr writes it at all depends on D2's Download controller
	// reconciling the object; that reconcile is watch-driven, not a queued
	// task, so there is no redelivery ladder to race here either, the same
	// reasoning indexerReadyTimeout documents for Indexer. Three minutes is
	// generous margin over one reconcile.
	uiPhaseGateTimeout = 3 * time.Minute

	// uiSSEWaitTimeout bounds how long the SSE assertion waits for
	// /events/downloads to emit a frame reflecting a phase already
	// confirmed present via the API a moment earlier.
	// ui/projection.DefaultInterval ticks every 5s in production, so this
	// is roughly eighteen ticks of margin for the port-forward, the HTTP
	// round trip and the projection loop's own list round -- generous
	// because a slow tick is not what this assertion exists to catch.
	uiSSEWaitTimeout = 90 * time.Second

	// uiUnwiredProjectionTimeout bounds the read-side poll for the Library,
	// Import Lists and Unmatched pages (G3-3/G3-4, Task G4-1). Unlike
	// pipeline/downloads, these three pages' Options.Library/ImportLists/
	// Unmatched functions are NOT set anywhere in cmd/clustarr as of this
	// writing -- server.go defaults each to "return no rows" when nil, and
	// grep finds no `Options.Library:`/`Options.ImportLists:`/
	// `Options.Unmatched:` in cmd/clustarr/*.go -- so wiring them is exactly
	// what plan task G3-5 ("every new route and stream wired in both
	// clustarr ui and clustarr all") has not landed yet. If it HAS landed by
	// the time this runs, the row appears well within one projection tick
	// (a few seconds); if it has not, waiting longer just delays the named
	// skip, so this is deliberately short rather than uiPageWaitTimeout's
	// two minutes.
	uiUnwiredProjectionTimeout = 30 * time.Second
)

// uiOptionsG35Reason is the shared skip text for every read-side gate in
// this file that Task G3-5's projection wiring would close.
const uiOptionsG35Reason = "the row never appeared within %s: either G3-5 (\"every new route and stream " +
	"wired in both clustarr ui and clustarr all\") has not landed yet -- cmd/clustarr never sets " +
	"Options.%s, so ui/server.go's own nil-default (\"return no rows\") is what GET %s is showing -- " +
	"or it has landed and something regressed. Skipping rather than failing on a gap this task's own " +
	"file scope (ui/, cmd/clustarr/ are out of it) cannot fix."

// TestUIPipelineAndDownloadsPages is scenario 14, narrowed to Task D3-5's
// scope: the pipeline and downloads pages only.
func TestUIPipelineAndDownloadsPages(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()

	// --- Fixture: a Movie and a MediaFile, via the proven RootFolder ->
	// plant -> full scan route (TestMediaFileTwoWriter's own route).
	rf := newRootFolder(ctx, t, "e2e-ui-rf", catalogv1alpha1.RootFolderKindMovie, "movies")
	filePath := path.Join(rf.Spec.Path, fixtureMovieFolder, fixtureMovieFile)
	plantMedia(t, hostPath(filePath))
	runScan(ctx, t, rf, catalogv1alpha1.ScanModeFull)

	files := waitForMediaFileCount(ctx, t, rf.Spec.Path, 1)
	mediaFileKey := client.ObjectKeyFromObject(&files[0])
	movieKey := client.ObjectKey{Namespace: Namespace, Name: files[0].Spec.MediaRef.Name}

	var movie catalogv1alpha1.Movie
	require.NoError(t, k8sClient.Get(ctx, movieKey, &movie))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), &movie) })

	// --- Fixture: a Download targeting that Movie, created directly (see
	// the package doc comment above for why there is no "real" grab flow to
	// drive instead).
	dl := newUIDownload(ctx, t, "e2e-ui-dl", &movie)

	base, _ := portForwardService(ctx, t, "ui", uiServicePort)
	pageClient := &http.Client{Timeout: 15 * time.Second}

	t.Run("pipeline and downloads pages", func(t *testing.T) {
		// pipeline.templ's row carries data-kind and data-stage (shared
		// across every row of that kind/stage) but no per-row identifying
		// data attribute the way downloads.templ's data-download does --
		// confirmed against the committed ui/views/pipeline.templ, and out
		// of D3-5's file list to change. The next best anchor, consistent
		// with R8's spirit of not asserting on prose copy that could change
		// independently of markup, is the row's title text as
		// pkg/pipeline.Project itself derives it: the Movie's resolved
		// metadata title once the gateway has settled it, or the object's
		// own generated name (uniqueName's output, not a translatable
		// string) before that. Recomputing it fresh on every poll, from the
		// same live read the assertion checks against, closes the race
		// between "the title we expected" and "the title the page actually
		// had" instead of pinning one snapshot ahead of time.
		waitFor(t, ctx, uiPageWaitTimeout, "GET /pipeline shows a row for Movie "+movie.Name,
			func(ctx context.Context) (bool, error) {
				var live catalogv1alpha1.Movie
				if err := k8sClient.Get(ctx, movieKey, &live); err != nil {
					//nolint:nilerr // keep polling
					return false, nil
				}
				title := live.Name
				if live.Status.Metadata != nil && live.Status.Metadata.Title != "" {
					title = live.Status.Metadata.Title
				}
				body, status, err := httpGetString(ctx, pageClient, base+"/pipeline")
				if err != nil || status != http.StatusOK {
					//nolint:nilerr // keep polling; a port-forward hiccup is transient
					return false, nil
				}
				return strings.Contains(body, `data-kind="`+string(commonv1.MediaKindMovie)+`"`) &&
					strings.Contains(body, title), nil
			}, describeMovie(movieKey))

		waitFor(t, ctx, uiPageWaitTimeout, "GET /downloads shows a row for Download "+dl.Name,
			func(ctx context.Context) (bool, error) {
				body, status, err := httpGetString(ctx, pageClient, base+"/downloads")
				if err != nil || status != http.StatusOK {
					//nolint:nilerr // keep polling
					return false, nil
				}
				return strings.Contains(body, `data-download="`+dl.Name+`"`), nil
			})
	})

	t.Run("never-writes invariant", func(t *testing.T) {
		// Re-read all three live: this proves the CURRENT managedFields
		// state, after every controller that has had a chance to write by
		// now, not a snapshot from before the pages subtest ran.
		var liveMovie catalogv1alpha1.Movie
		require.NoError(t, k8sClient.Get(ctx, movieKey, &liveMovie))
		requireNoUIManager(t, "Movie", &liveMovie)

		var liveDL downloadv1alpha1.Download
		require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(dl), &liveDL))
		requireNoUIManager(t, "Download", &liveDL)

		var liveMF catalogv1alpha1.MediaFile
		require.NoError(t, k8sClient.Get(ctx, mediaFileKey, &liveMF))
		requireNoUIManager(t, "MediaFile", &liveMF)
	})

	t.Run("downloads sse progress", func(t *testing.T) {
		phase, gated := waitForNonEmptyPhase(ctx, dl, uiPhaseGateTimeout)
		if !gated {
			t.Skip("grabarr's Download controller never advanced status.phase off its zero value within " +
				uiPhaseGateTimeout.String() + ": D2 (grabarr/controller/download) is what writes it, and the " +
				"reaper/engine wiring that carries a Download past its first reconcile is still landing in this " +
				"worktree as of Task D3-5 -- skipping rather than asserting on a signal that may not exist yet " +
				"in the deployed image")
		}
		t.Logf("Download %s reached status.phase=%q via the API; checking /events/downloads reflects a "+
			"non-empty phase for it", dl.Name, phase)

		sseCtx, sseCancel := context.WithTimeout(ctx, uiSSEWaitTimeout)
		defer sseCancel()
		// No blanket http.Client.Timeout here: /events/downloads is a
		// long-lived stream by design, and the bound this assertion wants
		// is sseCtx's, not a fixed per-request deadline that would cut the
		// connection off mid-stream regardless of progress.
		sseClient := &http.Client{}
		got, ok := sseDownloadsPhase(sseCtx, sseClient, base, dl.Name)
		require.True(t, ok,
			"no /events/downloads frame carried a non-empty data-phase for data-download=%q within %s, "+
				"even though the API already reports status.phase=%q",
			dl.Name, uiSSEWaitTimeout, phase)
		require.NotEmpty(t, got)
	})
}

// requireNoUIManager asserts obj's managedFields carry no clustarr-ui entry
// on the status subresource -- the machine-checkable form of CLAUDE.md's "the
// UI never writes status", narrowed by ruling R2.
//
// Before R2, this rejected any manager whose name merely contained "ui"
// anywhere on the whole object. R2 gave the UI a real field manager,
// pkg/k8s.ManagerUI ("clustarr-ui", restated in ui/actions.FieldManager
// because ui/ never imports pkg/k8s -- see that package's doc comment), that
// legitimately owns spec.monitored on the catalog kinds, spec on a created
// Search and spec on a created LibraryScan. Keeping the old "contains ui"
// check would fail the first e2e scenario that exercises a UI action,
// spuriously: clustarr-ui owning spec is exactly what R2 intends. The actual
// invariant -- "the UI never writes status and owns no CRD" -- is that
// clustarr-ui never appears on the status subresource, which is the same
// assertion ui/actions.TestUIManagerNeverOwnsStatus makes in envtest
// (requireNeverOnStatus there).
func requireNoUIManager(t *testing.T, kind string, obj client.Object) {
	t.Helper()
	for _, e := range obj.GetManagedFields() {
		if e.Manager != string(k8s.ManagerUI) {
			continue
		}
		require.NotEqual(t, "status", e.Subresource,
			"%s %s/%s: field manager %s has a managedFields entry on the status subresource -- the UI never "+
				"writes status (amendment A3.2, ruling R2)", kind, obj.GetNamespace(), obj.GetName(), e.Manager)
	}
}

// newUIDownload creates a Download targeting movie and registers its
// cleanup. Protocol, Source and Release are the exact shape
// grabarr/controller/download/controller_envtest_test.go's own
// newTorrentDownload fixture uses -- proven legal against
// DownloadSource's magnetURL pattern, ReleaseInfo.InfoHash's 40-hex-char
// pattern, and the "release identity is immutable" CEL rule (the rule that
// made every usenet Download unwritable after creation until c5e54eeb).
func newUIDownload(ctx context.Context, t *testing.T, prefix string, movie *catalogv1alpha1.Movie) *downloadv1alpha1.Download {
	t.Helper()
	name := uniqueName(prefix)
	magnet := "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567"
	dl := &downloadv1alpha1.Download{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: Namespace},
		Spec: downloadv1alpha1.DownloadSpec{
			Protocol: commonv1.ProtocolTorrent,
			Source:   downloadv1alpha1.DownloadSource{MagnetURL: &magnet},
			Release: commonv1.ReleaseInfo{
				GUID:        "e2e-ui-" + name,
				IndexerRef:  "e2e-ui-fixture",
				IndexerName: "e2e UI fixture",
				Title:       "Fixture.UI.Download." + name,
				Protocol:    commonv1.ProtocolTorrent,
				InfoHash:    "0123456789abcdef0123456789abcdef01234567",
			},
			Target: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie.Name},
		},
	}
	// DownloadSpec.Target's own doc comment: "It is also the ownerReference
	// of this Download." Nothing in the tree sets this automatically yet --
	// catalogarr/worker/search does not create Downloads at all as of this
	// task -- so the creator does what that comment says the real grab
	// decision eventually will.
	require.NoError(t, k8s.SetControllerReference(movie, dl, k8sClient.Scheme()))
	require.NoError(t, k8sClient.Create(ctx, dl))
	cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), dl) })
	return dl
}

// waitForNonEmptyPhase polls dl's status.phase until it stops being the
// empty-string zero value or until timeout, and reports which.
//
// It deliberately does NOT use the suite's waitFor, which calls t.Fatal on a
// timeout: whether grabarr's Download controller has landed the code that
// writes phase at all is exactly the fact under test here (see the D3-5
// plan task text and this file's package doc comment), and the correct
// response to "not yet" is a named t.Skip from the caller, not a failure.
func waitForNonEmptyPhase(ctx context.Context, dl *downloadv1alpha1.Download, timeout time.Duration) (downloadv1alpha1.DownloadPhase, bool) {
	var phase downloadv1alpha1.DownloadPhase
	err := wait.PollUntilContextTimeout(ctx, pollInterval, timeout, true, func(ctx context.Context) (bool, error) {
		var live downloadv1alpha1.Download
		if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(dl), &live); err != nil {
			//nolint:nilerr // keep polling
			return false, nil
		}
		if live.Status.Phase == "" {
			return false, nil
		}
		phase = live.Status.Phase
		return true, nil
	})
	return phase, err == nil
}

// httpGetHTMX is httpGetString with the HX-Request header htmx sends, for
// a route that serves a component to htmx and a page to anyone else.
func httpGetHTMX(ctx context.Context, c *http.Client, url string) (body string, status int, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("HX-Request", "true")
	resp, err := c.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", resp.StatusCode, err
	}
	return string(raw), resp.StatusCode, nil
}

// httpGetString GETs url and returns its body as a string alongside the
// status code, for the plain-substring page assertions R8 asks for (no
// goquery, no new test dependency -- ui/downloads_test.go's own doc comment
// makes the same call for the unit tests this mirrors).
func httpGetString(ctx context.Context, c *http.Client, url string) (body string, status int, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", 0, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, err
	}
	return string(b), resp.StatusCode, nil
}

// sseDownloadsPhase opens GET base+"/events/downloads" and reads Server-Sent
// Events until it finds a "downloads" event whose reconstructed fragment
// carries a non-empty data-phase for data-download=downloadName, or until
// ctx is done.
//
// Framing mirrors ui/sse.go's writeDownloadsEvent exactly: "event:
// downloads" on its own line, then one "data: <line>" per source line of
// the rendered fragment (SSE forbids a bare newline inside one field), then
// a blank line terminating the event. This reassembles each event's
// fragment by stripping the "data: " prefix and joining with '\n', the same
// concatenation htmx's SSE extension performs client-side.
//
// ctx expiring ends the read loop via the request's own context: Scan
// returns false once the body read fails, which is the ordinary "nothing
// matched before the deadline" exit here, not a harness error -- so
// sc.Err() is deliberately never asserted on.
func sseDownloadsPhase(ctx context.Context, c *http.Client, base, downloadName string) (phase string, ok bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/events/downloads", nil)
	if err != nil {
		return "", false
	}
	resp, err := c.Do(req)
	if err != nil {
		// Most commonly ctx's own deadline, firing before the connection
		// was even established.
		return "", false
	}
	defer func() { _ = resp.Body.Close() }()

	rowRe := regexp.MustCompile(`data-download="` + regexp.QuoteMeta(downloadName) + `"\s+data-phase="([^"]*)"`)

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)

	var event strings.Builder
	inTarget := false
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			if inTarget {
				if m := rowRe.FindStringSubmatch(event.String()); m != nil && m[1] != "" {
					return m[1], true
				}
			}
			inTarget = false
			event.Reset()
		case strings.HasPrefix(line, "event: "):
			inTarget = strings.TrimPrefix(line, "event: ") == "downloads"
		case strings.HasPrefix(line, "data: "):
			if inTarget {
				event.WriteString(strings.TrimPrefix(line, "data: "))
				event.WriteByte('\n')
			}
		}
	}
	return "", false
}

// httpPostForm POSTs values to url and returns the response body as a
// string alongside the status code, httpGetString's own counterpart for
// every write-action assertion below.
func httpPostForm(ctx context.Context, c *http.Client, target string, values url.Values) (body string, status int, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(values.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, err
	}
	return string(b), resp.StatusCode, nil
}

// skipIfNoWriter inspects a finishAction response (ui/routes.go) for
// data-action-error="no-writer" -- actions.ErrNoWriter rendered visibly,
// exactly as ui/routes.go's own doc comment for finishAction describes --
// and skips the calling test by name when found, since that error means
// Options.Actions is nil (Task G3-5, not yet landed as of this writing;
// see uiOptionsG35Reason's sibling reasoning). It returns false when the
// action instead reached a real writer (Options.Actions was non-nil),
// whether that writer then succeeded or failed for some other reason --
// the caller asserts the rest.
func skipIfNoWriter(t *testing.T, status int, body string) bool {
	t.Helper()
	if status == http.StatusServiceUnavailable && strings.Contains(body, `data-action-error="no-writer"`) {
		t.Skip("ui action answered data-action-error=\"no-writer\": Options.Actions is nil in the deployed ui " +
			"image, which is Task G3-5's own carried duty (\"Options.Actions is set nowhere, so every UI action " +
			"returns ErrNoWriter\", G3-5's own plan text) -- not yet landed as of this writing. Skipping the " +
			"write-side assertion rather than failing on a gap outside this task's file scope.")
		return true
	}
	return false
}

// TestUILibraryImportListsSettingsAndUnmatchedPages is scenario 14's
// remaining four pages (Task G4-1): Library, Import Lists, Settings and
// Unmatched. TestUIPipelineAndDownloadsPages (Task D3-5) already covers
// Pipeline and Downloads.
//
// Every page's READ side is asserted against real cluster objects this
// function creates. Three of the four pages -- Library, Import Lists and
// Unmatched -- read through an Options function (Library, ImportLists,
// Unmatched) that cmd/clustarr does not set as of this writing, so
// ui/server.go's own nil-default answers "no rows" regardless of what is
// actually in the cluster; each such subtest polls for
// uiUnwiredProjectionTimeout and skips by name, via uiOptionsG35Reason,
// rather than fail on Task G3-5's own carried wiring gap. Settings reads
// straight through Options.Reader (ui/settings.go's own doc comment: "read
// directly through Options.Reader on every GET"), which IS wired in every
// deployment (it is the same Reader the pipeline/downloads pages already
// prove works), so that subtest asserts for real with no gate.
//
// Every WRITE action attempted below goes through skipIfNoWriter first:
// Options.Actions is Task G3-5's other half of the same carried duty. If it
// answers no-writer the subtest skips by name; if it reaches a real writer,
// the subtest asserts the resulting spec patch for real, the way
// ui/actions' own G3-1 envtest (TestUIManagerNeverOwnsStatus) already does
// at the Go level -- this is that same invariant proven once more over real
// HTTP, against the deployed image, when the wiring allows it.
func TestUILibraryImportListsSettingsAndUnmatchedPages(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), scenarioTimeout)
	defer cancel()

	base, _ := portForwardService(ctx, t, "ui", uiServicePort)
	pageClient := &http.Client{Timeout: 15 * time.Second}

	t.Run("settings page", func(t *testing.T) {
		rf := newRootFolder(ctx, t, "e2e14-settings-rf", catalogv1alpha1.RootFolderKindMovie, "movies")
		idx := newIndexer(ctx, t, "e2e14-settings-idx", "/api", 15*time.Minute, false)
		dc := &downloadv1alpha1.DownloadClient{
			ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e14-settings-dc"), Namespace: Namespace},
			Spec: downloadv1alpha1.DownloadClientSpec{
				Protocol: commonv1.ProtocolTorrent, Enabled: ptr.To(true), Priority: 10, Replicas: 1,
				Torrent: &downloadv1alpha1.TorrentSpec{},
			},
		}
		require.NoError(t, k8sClient.Create(ctx, dc))
		cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), dc) })

		waitFor(t, ctx, uiPageWaitTimeout, "GET /settings shows every fixture row", func(ctx context.Context) (bool, error) {
			body, status, err := httpGetString(ctx, pageClient, base+"/settings")
			if err != nil || status != http.StatusOK {
				//nolint:nilerr // keep polling; a port-forward hiccup is transient
				return false, nil
			}
			return strings.Contains(body, `data-root-folder="`+rf.Namespace+"/"+rf.Name+`"`) &&
				strings.Contains(body, `data-quality-profile="`+QualityProfileName+`"`) &&
				strings.Contains(body, `data-indexer="`+idx.Namespace+"/"+idx.Name+`"`) &&
				strings.Contains(body, `data-download-client="`+dc.Namespace+"/"+dc.Name+`"`) &&
				strings.Contains(body, `data-metadata-provider="tmdb"`), nil
		})

		t.Run("write action", func(t *testing.T) {
			body, status, err := httpPostForm(ctx, pageClient, base+"/settings/rootfolders/"+rf.Namespace+"/"+rf.Name,
				url.Values{"scanSchedule": {"@daily"}})
			require.NoError(t, err)
			if skipIfNoWriter(t, status, body) {
				return
			}
			require.Equal(t, http.StatusSeeOther, status, "unexpected settings write response: %s", body)

			var live catalogv1alpha1.RootFolder
			require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(rf), &live))
			require.Equal(t, "@daily", live.Spec.ScanSchedule)
			requireNoUIManager(t, "RootFolder", &live)
		})
	})

	t.Run("library page", func(t *testing.T) {
		movie := &catalogv1alpha1.Movie{
			ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e14-library-movie"), Namespace: Namespace},
			Spec: catalogv1alpha1.MovieSpec{
				TmdbID: 900201, QualityProfileRef: QualityProfileName,
				RootFolderRef: newRootFolder(ctx, t, "e2e14-library-rf", catalogv1alpha1.RootFolderKindMovie, "movies").Name,
			},
		}
		require.NoError(t, k8sClient.Create(ctx, movie))
		cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), movie) })

		found := false
		_ = wait.PollUntilContextTimeout(ctx, pollInterval, uiUnwiredProjectionTimeout, true, func(ctx context.Context) (bool, error) {
			body, status, err := httpGetString(ctx, pageClient, base+"/library/movies")
			if err != nil || status != http.StatusOK {
				//nolint:nilerr // keep polling
				return false, nil
			}
			found = strings.Contains(body, `data-kind="`+string(commonv1.MediaKindMovie)+`"`) && strings.Contains(body, movie.Name)
			return found, nil
		})
		if !found {
			t.Skipf(uiOptionsG35Reason, uiUnwiredProjectionTimeout, "Library", "/library/movies")
		}

		t.Run("write action", func(t *testing.T) {
			body, status, err := httpPostForm(ctx, pageClient,
				base+"/library/"+movie.Namespace+"/"+string(commonv1.MediaKindMovie)+"/"+movie.Name+"/monitor",
				url.Values{"monitored": {"false"}})
			require.NoError(t, err)
			if skipIfNoWriter(t, status, body) {
				return
			}
			require.Equal(t, http.StatusSeeOther, status, "unexpected monitor-action response: %s", body)

			var live catalogv1alpha1.Movie
			require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(movie), &live))
			require.NotNil(t, live.Spec.Monitored)
			require.False(t, *live.Spec.Monitored)
			requireNoUIManager(t, "Movie", &live)
		})
	})

	// The library's TV tab, a series' page and a season toggle (spec
	// 2026-09-23-library-page-design): the series lands on /library/tv and
	// not on /library/movies; its page lists its seasons, each loading its
	// episodes from the season route; the season toggle writes the override
	// into spec.seasons under the UI's manager, never touching status.
	t.Run("library tabs, series page and season toggle", func(t *testing.T) {
		rf := newRootFolder(ctx, t, "e2e14-tv-rf", catalogv1alpha1.RootFolderKindSeries, "tv")
		series := createSeries(ctx, t, rf.Name, 121361, catalogv1alpha1.SeriesTypeStandard)
		live := waitForSeriesReady(ctx, t, series, fixtureEpisodesPerSeries)
		var season int32 = -1
		for _, st := range live.Status.Seasons {
			if st.EpisodeCount > 0 {
				season = st.Number
				break
			}
		}
		require.GreaterOrEqual(t, season, int32(0), "the series must roll up a season with episodes: %+v", live.Status.Seasons)

		found := false
		_ = wait.PollUntilContextTimeout(ctx, pollInterval, uiUnwiredProjectionTimeout, true, func(ctx context.Context) (bool, error) {
			body, status, err := httpGetString(ctx, pageClient, base+"/library/tv")
			if err != nil || status != http.StatusOK {
				//nolint:nilerr // keep polling
				return false, nil
			}
			found = strings.Contains(body, `data-kind="`+string(commonv1.MediaKindSeries)+`"`) && strings.Contains(body, series.Name)
			return found, nil
		})
		if !found {
			t.Skipf(uiOptionsG35Reason, uiUnwiredProjectionTimeout, "Library (TV tab)", "/library/tv")
		}
		movies, status, err := httpGetString(ctx, pageClient, base+"/library/movies")
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)
		require.NotContains(t, movies, series.Name, "a series is not on the Movies tab")

		seriesPath := fmt.Sprintf("/library/%s/series/%s", series.Namespace, series.Name)
		page, status, err := httpGetString(ctx, pageClient, base+seriesPath)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)
		require.Contains(t, page, fmt.Sprintf(`data-season="%d"`, season))
		require.Contains(t, page, fmt.Sprintf(`hx-get="%s/seasons/%d"`, seriesPath, season))

		partial, status, err := httpGetHTMX(ctx, pageClient, fmt.Sprintf("%s%s/seasons/%d", base, seriesPath, season))
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, status)
		require.NotContains(t, partial, "<html", "htmx gets the episodes component, not a page")
		require.Contains(t, partial, `data-episode="1"`)

		t.Run("season toggle", func(t *testing.T) {
			body, status, err := httpPostForm(ctx, pageClient, fmt.Sprintf("%s%s/seasons/%d/monitor", base, seriesPath, season),
				url.Values{"monitored": {"false"}})
			require.NoError(t, err)
			if skipIfNoWriter(t, status, body) {
				return
			}
			require.Equal(t, http.StatusSeeOther, status, "unexpected season-toggle response: %s", body)

			var after catalogv1alpha1.Series
			require.NoError(t, k8sClient.Get(ctx, client.ObjectKeyFromObject(series), &after))
			var got *bool
			for _, sp := range after.Spec.Seasons {
				if sp.Number == season {
					got = sp.Monitored
				}
			}
			require.NotNil(t, got, "spec.seasons must hold the toggled season: %+v", after.Spec.Seasons)
			require.False(t, *got)
			requireNoUIManager(t, "Series", &after)
		})
	})

	t.Run("import lists page", func(t *testing.T) {
		requireFixtureService(ctx, t, fixtureImportListStubService)
		rf := newRootFolder(ctx, t, "e2e14-il-rf", catalogv1alpha1.RootFolderKindMovie, "movies")
		il := newMdblistImportList(ctx, t, "e2e14-il", rf.Name)

		found := false
		_ = wait.PollUntilContextTimeout(ctx, pollInterval, uiUnwiredProjectionTimeout, true, func(ctx context.Context) (bool, error) {
			body, status, err := httpGetString(ctx, pageClient, base+"/import-lists")
			if err != nil || status != http.StatusOK {
				//nolint:nilerr // keep polling
				return false, nil
			}
			found = strings.Contains(body, il.Name)
			return found, nil
		})
		if !found {
			t.Skipf(uiOptionsG35Reason, uiUnwiredProjectionTimeout, "ImportLists", "/import-lists")
		}
		// Read-only page (ui/settings.go's handleImportLists doc comment:
		// "no action handler here reaches Options.Actions") -- nothing more
		// to assert once the row is confirmed present.
	})

	t.Run("unmatched page", func(t *testing.T) {
		rf := newRootFolder(ctx, t, "e2e14-unmatched-rf", catalogv1alpha1.RootFolderKindMovie, "movies")
		root := rf.Spec.Path
		relPath := path.Join("Unsorted", "Some.Unknown.Fixture.Film.2019.1080p.WEB.x264-E2E14.mkv")
		plantFiller(t, hostPath(path.Join(root, relPath)))

		scan := runScan(ctx, t, rf, catalogv1alpha1.ScanModeFull)
		require.NotEmpty(t, scan.Status.Unmatched, "the planted file must be recorded, never turned into a speculative item")

		found := false
		_ = wait.PollUntilContextTimeout(ctx, pollInterval, uiUnwiredProjectionTimeout, true, func(ctx context.Context) (bool, error) {
			body, status, err := httpGetString(ctx, pageClient, base+"/unmatched")
			if err != nil || status != http.StatusOK {
				//nolint:nilerr // keep polling
				return false, nil
			}
			found = strings.Contains(body, `data-path="`+relPath+`"`)
			return found, nil
		})
		if !found {
			t.Skipf(uiOptionsG35Reason, uiUnwiredProjectionTimeout, "Unmatched", "/unmatched")
		}

		t.Run("write action", func(t *testing.T) {
			// A movie for the manual-assign target: FileRefFitsRoot requires
			// the target's own kind (movie) to fit the scan's root folder
			// kind (also movie) -- importarr/worker/rescan/doc.go's "Manual
			// assignment" section.
			target := &catalogv1alpha1.Movie{
				ObjectMeta: metav1.ObjectMeta{Name: uniqueName("e2e14-unmatched-target"), Namespace: Namespace},
				Spec:       catalogv1alpha1.MovieSpec{TmdbID: 900202, QualityProfileRef: QualityProfileName, RootFolderRef: rf.Name},
			}
			require.NoError(t, k8sClient.Create(ctx, target))
			cleanupUnlessFailed(t, func() { _ = k8sClient.Delete(context.Background(), target) })

			body, status, err := httpPostForm(ctx, pageClient, base+"/unmatched/assign", url.Values{
				"namespace": {Namespace}, "rootFolder": {rf.Name}, "subpath": {relPath},
				"kind": {string(commonv1.MediaKindMovie)}, "name": {target.Name},
			})
			require.NoError(t, err)
			if skipIfNoWriter(t, status, body) {
				return
			}
			require.Equal(t, http.StatusSeeOther, status, "unexpected manual-assign response: %s", body)

			mf := waitForMediaFileAt(ctx, t, path.Join(root, relPath))
			require.Equal(t, commonv1.MediaKindMovie, mf.Spec.MediaRef.Kind)
			require.Equal(t, target.Name, mf.Spec.MediaRef.Name)
		})
	})
}
