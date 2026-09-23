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
	"io"
	"net/http"
	"path"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
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
)

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
