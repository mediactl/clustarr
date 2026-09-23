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

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/ui/actions"
)

// verifyUI proves a running ui process -- `clustarr ui` or `clustarr all`'s
// ui, whichever the case started -- reads the cluster on the pages Task G3-3
// and G3-4 built, streams them, and writes through the actions Task G3-1
// built. Until plan task G3-5 none of that was reachable in production:
// the Library, Unmatched and Import Lists accessors and streams were wired
// into neither command, so those pages rendered no rows at all, and
// Options.Actions was set nowhere, so every button answered 503.
//
// It creates one Movie (with a status catalogarr would have written), one
// LibraryScan listing an unmatched file, and one ImportList, then:
//
//   - GETs /library, /unmatched and /import-lists and waits for each object's
//     row;
//   - opens /events/library, /events/unmatched and /events/import-lists
//     BEFORE changing anything, and waits on each open connection for a frame
//     carrying the change -- a stream that only ever replays its first frame
//     cannot pass;
//   - POSTs "unmonitor" and "search now" exactly as the Library page's own
//     forms do, and asserts the apiserver saw them: spec.monitored flipped
//     with clustarr-ui owning f:spec.f:monitored and nothing on status (ruling
//     R2) while catalogarr still owns the status it wrote, and a Search
//     labelled clustarr.io/origin=ui for the Movie.
//
// suffix keeps the two ui cases' objects apart.
func verifyUI(t *testing.T, cfg *rest.Config, addr, suffix string) {
	t.Helper()
	ctx := context.Background()
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	if err != nil {
		t.Fatalf("build client: %v", err)
	}
	base := "http://" + addr
	const ns = "default"
	name := "ui-probe-" + suffix

	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.MovieSpec{
			TmdbID: 603, QualityProfileRef: "hd-bluray-web", RootFolderRef: "movies", Monitored: ptr.To(true),
		},
	}
	if err := c.Create(ctx, movie); err != nil {
		t.Fatalf("create Movie: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), movie) })
	// A status catalogarr owns, so the action lands on an object that
	// already has one: only then can an over-claim of status by clustarr-ui
	// show up in managedFields (CLAUDE.md, "Gotchas found the hard way").
	if _, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, catalogac.Movie(name, ns).
		WithStatus(catalogac.MovieStatus().WithPhase(catalogv1alpha1.MoviePhaseWanted))); err != nil {
		t.Fatalf("seed Movie status: %v", err)
	}

	scan := &catalogv1alpha1.LibraryScan{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       catalogv1alpha1.LibraryScanSpec{RootFolderRef: "movies"},
	}
	if err := c.Create(ctx, scan); err != nil {
		t.Fatalf("create LibraryScan: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), scan) })
	firstPath := "unsorted/" + name + "-a.mkv"
	secondPath := "unsorted/" + name + "-b.mkv"
	seedUnmatched(t, c, name, ns, firstPath)

	list := &catalogv1alpha1.ImportList{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.ImportListSpec{
			Kinds:    []string{"movie"},
			Custom:   &catalogv1alpha1.CustomList{URL: "http://127.0.0.1:1/list.json"},
			Defaults: catalogv1alpha1.ListDefaults{QualityProfileRef: "hd-bluray-web", RootFolderRef: "movies"},
		},
	}
	if err := c.Create(ctx, list); err != nil {
		t.Fatalf("create ImportList: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(context.Background(), list) })

	ref := ns + "/" + name
	movieRow := `data-ref="` + ref + `"`
	listRow := `data-list="` + ref + `"`

	// The pages. Each reads the shared projection, which ticks every
	// projection.DefaultInterval, so the rows arrive within a tick.
	page := waitForPage(t, base+"/library", movieRow)
	if got := rowAttr(page, movieRow, "data-monitored"); got != "true" {
		t.Fatalf("GET /library: %s renders data-monitored=%q, want \"true\"", ref, got)
	}
	waitForPage(t, base+"/unmatched", `data-path="`+firstPath+`"`)
	waitForPage(t, base+"/import-lists", listRow)

	// The streams, opened before any change.
	libraryFrames := openSSE(t, base+"/events/library")
	unmatchedFrames := openSSE(t, base+"/events/unmatched")
	listFrames := openSSE(t, base+"/events/import-lists")
	awaitFrame(t, "/events/library's first frame", libraryFrames, func(f string) bool {
		return rowAttr(f, movieRow, "data-monitored") == "true"
	})
	awaitFrame(t, "/events/unmatched's first frame", unmatchedFrames, func(f string) bool {
		return strings.Contains(f, `data-path="`+firstPath+`"`)
	})
	awaitFrame(t, "/events/import-lists' first frame", listFrames, func(f string) bool {
		return rowAttr(f, listRow, "data-enabled") == "true"
	})

	// "Unmonitor", as the Library detail page's form posts it.
	postAction(t, base+"/library/"+ns+"/movie/"+name+"/monitor", url.Values{
		"monitored": {"false"}, "return": {"/library"},
	})
	var got catalogv1alpha1.Movie
	if err := c.Get(ctx, client.ObjectKeyFromObject(movie), &got); err != nil {
		t.Fatalf("get Movie: %v", err)
	}
	if got.Spec.Monitored == nil || *got.Spec.Monitored {
		t.Fatalf("after POST monitor=false, Movie %s spec.monitored = %v, want false", ref, got.Spec.Monitored)
	}
	requireUIOwnsOnlySpecLeaf(t, &got, "f:monitored")
	if got.Status.Phase != catalogv1alpha1.MoviePhaseWanted {
		t.Errorf("the monitor action changed status.phase to %q; the UI never writes status", got.Status.Phase)
	}
	if m := statusManagers(got.ManagedFields); len(m) != 1 || !m[string(k8s.ManagerCatalogarr)] {
		t.Errorf("status managers after the UI action = %v, want only %s", m, k8s.ManagerCatalogarr)
	}
	awaitFrame(t, "/events/library to push the unmonitored Movie", libraryFrames, func(f string) bool {
		return rowAttr(f, movieRow, "data-monitored") == "false"
	})

	// A rescan reporting a second unattributable file.
	seedUnmatched(t, c, name, ns, firstPath, secondPath)
	awaitFrame(t, "/events/unmatched to push the second unmatched file", unmatchedFrames, func(f string) bool {
		return strings.Contains(f, `data-path="`+secondPath+`"`)
	})

	// The list disabled.
	if err := c.Patch(ctx, list, client.RawPatch(types.MergePatchType, []byte(`{"spec":{"enabled":false}}`))); err != nil {
		t.Fatalf("disable ImportList: %v", err)
	}
	awaitFrame(t, "/events/import-lists to push the disabled list", listFrames, func(f string) bool {
		return rowAttr(f, listRow, "data-enabled") == "false"
	})

	// "Search now": the create half of the action writer.
	postAction(t, base+"/library/"+ns+"/movie/"+name+"/search", url.Values{"return": {"/library"}})
	var searches catalogv1alpha1.SearchList
	if err := c.List(ctx, &searches, client.InNamespace(ns),
		client.MatchingLabels{actions.LabelOrigin: actions.OriginUI}); err != nil {
		t.Fatalf("list Searches: %v", err)
	}
	var found bool
	for i := range searches.Items {
		s := &searches.Items[i]
		if s.Spec.MediaRef == nil || s.Spec.MediaRef.Name != name {
			continue
		}
		found = true
		t.Cleanup(func() { _ = c.Delete(context.Background(), s) })
		if s.Spec.MediaRef.Kind != commonv1alpha1.MediaKindMovie {
			t.Errorf("the UI's Search names kind %q, want movie", s.Spec.MediaRef.Kind)
		}
		var byUI bool
		for _, e := range s.ManagedFields {
			if e.Manager == string(k8s.ManagerUI) {
				byUI = true
			}
		}
		if !byUI {
			t.Errorf("Search %s was not created as %s: managedFields %v", s.Name, k8s.ManagerUI, s.ManagedFields)
		}
	}
	if !found {
		t.Fatalf("POST search created no Search labelled %s=%s for Movie %s", actions.LabelOrigin, actions.OriginUI, ref)
	}
}

// seedUnmatched writes a LibraryScan's status as importarr's rescan worker
// would: Completed, listing paths as unattributable. Each call is a complete
// declaration of the status importarr owns.
func seedUnmatched(t *testing.T, c client.Client, name, ns string, paths ...string) {
	t.Helper()
	status := catalogac.LibraryScanStatus().WithPhase(catalogv1alpha1.ScanPhaseCompleted)
	now := metav1.NewTime(time.Now().Truncate(time.Second))
	for _, p := range paths {
		status.WithUnmatched(catalogac.UnmatchedFile().WithPath(p).WithReason("no_candidate").WithSeenAt(now))
	}
	if _, err := k8s.PatchStatus(context.Background(), c, k8s.ManagerImportarr,
		catalogac.LibraryScan(name, ns).WithStatus(status)); err != nil {
		t.Fatalf("seed LibraryScan status: %v", err)
	}
}

// requireUIOwnsOnlySpecLeaf asserts ruling R2 on metadata.managedFields --
// the one place an over-claim is visible, since pkg/k8s forces ownership and
// every value assertion keeps passing through one: clustarr-ui has an entry,
// that entry owns spec.<leaf>, and no clustarr-ui entry is on the status
// subresource or names any status field.
func requireUIOwnsOnlySpecLeaf(t *testing.T, obj metav1.Object, leaf string) {
	t.Helper()
	var ownsLeaf bool
	for _, e := range obj.GetManagedFields() {
		if e.Manager != string(k8s.ManagerUI) {
			continue
		}
		if e.Subresource == "status" {
			t.Errorf("%s has a managedFields entry on the status subresource; the UI never writes status",
				k8s.ManagerUI)
		}
		var fields map[string]any
		if e.FieldsV1 != nil {
			if err := json.Unmarshal(e.FieldsV1.GetRawBytes(), &fields); err != nil {
				t.Fatalf("decode %s's managedFields: %v", k8s.ManagerUI, err)
			}
		}
		if _, ok := fields["f:status"]; ok {
			t.Errorf("%s owns status fields %v; the UI never writes status", k8s.ManagerUI, fields["f:status"])
		}
		if spec, ok := fields["f:spec"].(map[string]any); ok {
			if _, ok := spec[leaf]; ok {
				ownsLeaf = true
			}
		}
	}
	if !ownsLeaf {
		t.Errorf("no %s managedFields entry owns f:spec.%s: managedFields %v",
			k8s.ManagerUI, leaf, obj.GetManagedFields())
	}
}

// waitForPage GETs url until its body contains want, and returns that body.
// It allows waitForLong's margin rather than waitFor's: a fresh ui process's
// first projection rounds start an informer per kind the projection lists
// (seventeen of them) before the next tick can see a new object, which took
// 12.6s of waitFor's 30 on the first measured run.
func waitForPage(t *testing.T, url, want string) string {
	t.Helper()
	var body string
	waitForLong(t, "GET "+url+" to render "+want, func() bool {
		resp, err := http.Get(url) //nolint:noctx // bounded by waitFor's deadline
		if err != nil {
			return false
		}
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != http.StatusOK {
			return false
		}
		body = string(b)
		return strings.Contains(body, want)
	})
	return body
}

// postAction submits a Library-page action form and requires the redirect
// finishAction sends on success. A 503 is actions.ErrNoWriter: the command
// never wired Options.Actions.
func postAction(t *testing.T, target string, form url.Values) {
	t.Helper()
	noRedirect := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := noRedirect.PostForm(target, form) //nolint:noctx // bounded by the client timeout
	if err != nil {
		t.Fatalf("POST %s: %v", target, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusSeeOther {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST %s = %d, want 303 (the action's success redirect); a 503 is "+
			"actions.ErrNoWriter, i.e. ui.Options.Actions is unset. Body: %s", target, resp.StatusCode, body)
	}
}

// openSSE opens an event stream and returns a channel of its frames, each
// the frame's data lines joined. The connection closes when the test ends.
func openSSE(t *testing.T, url string) <-chan string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("build request for %s: %v", url, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		_ = resp.Body.Close()
		t.Fatalf("GET %s = %d %q, want 200 text/event-stream", url, resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	frames := make(chan string, 64)
	go func() {
		defer func() { _ = resp.Body.Close() }()
		defer close(frames)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
		var data strings.Builder
		for sc.Scan() {
			line := sc.Text()
			switch {
			case line == "":
				if data.Len() > 0 {
					select {
					case frames <- data.String():
					case <-ctx.Done():
						return
					}
					data.Reset()
				}
			case strings.HasPrefix(line, "data: "):
				data.WriteString(strings.TrimPrefix(line, "data: "))
				data.WriteByte('\n')
			}
		}
	}()
	return frames
}

// awaitFrame reads frames until one satisfies match, for up to 30s.
func awaitFrame(t *testing.T, what string, frames <-chan string, match func(string) bool) {
	t.Helper()
	deadline := time.After(30 * time.Second)
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatalf("waiting for %s: the stream closed", what)
			}
			if match(f) {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// rowAttr returns attr's value on the element whose opening tag contains
// anchor (e.g. data-ref="default/x"), or "" when there is no such element
// or it carries no such attribute. Rows carry stable data-* attributes
// (ruling R8), so this reads one row out of a page or SSE fragment listing
// many.
func rowAttr(html, anchor, attr string) string {
	i := strings.Index(html, anchor)
	if i < 0 {
		return ""
	}
	start := strings.LastIndex(html[:i], "<")
	end := strings.Index(html[i:], ">")
	if start < 0 || end < 0 {
		return ""
	}
	m := regexp.MustCompile(`\s` + regexp.QuoteMeta(attr) + `="([^"]*)"`).FindStringSubmatch(html[start : i+end])
	if m == nil {
		return ""
	}
	return m[1]
}
