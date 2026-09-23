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

package ui

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// routes builds the route table. It is a method so handlers can close over
// s.opts without package-level state.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler()))
	mux.HandleFunc("GET /pipeline", s.handlePipeline)
	mux.HandleFunc("GET /events/pipeline", s.handlePipelineEvents)
	mux.HandleFunc("GET /downloads", s.handleDownloads)
	mux.HandleFunc("GET /events/downloads", s.handleDownloadsEvents)
	mux.HandleFunc("GET /library", s.handleLibraryIndex)
	mux.HandleFunc("GET /library/{tab}", s.handleLibrary)
	mux.HandleFunc("GET /events/library/{tab}", s.handleLibraryEvents)
	mux.HandleFunc("GET /library/{namespace}/{kind}/{name}", s.handleLibraryItem)
	mux.HandleFunc("GET /library/{namespace}/series/{name}/seasons/{n}", s.handleSeason)
	mux.HandleFunc("POST /library/{namespace}/series/{name}/seasons/{n}/monitor", s.handleSetSeasonMonitored)
	mux.HandleFunc("POST /library/{namespace}/{kind}/{name}/monitor", s.handleSetMonitored)
	mux.HandleFunc("POST /library/{namespace}/{kind}/{name}/search", s.handleSearchNow)
	mux.HandleFunc("POST /library/rescan", s.handleRescan)
	mux.HandleFunc("GET /unmatched", s.handleUnmatched)
	mux.HandleFunc("GET /events/unmatched", s.handleUnmatchedEvents)
	mux.HandleFunc("POST /unmatched/assign", s.handleManualAssign)
	mux.HandleFunc("GET /library-scans/{namespace}/{name}", s.handleLibraryScanDetail)
	mux.HandleFunc("GET /import-lists", s.handleImportLists)
	mux.HandleFunc("GET /events/import-lists", s.handleImportListsEvents)
	mux.HandleFunc("GET /settings", s.handleSettings)
	mux.HandleFunc("POST /settings/rootfolders/{namespace}/{name}", s.handleSetRootFolderScanSchedule)
	mux.HandleFunc("POST /settings/qualityprofiles/{name}", s.handleSetQualityProfileUpgradeAllowed)
	mux.HandleFunc("POST /settings/indexers/{namespace}/{name}", s.handleSetIndexerSettings)
	mux.HandleFunc("POST /settings/downloadclients/{namespace}/{name}", s.handleSetDownloadClientSettings)
	mux.HandleFunc("POST /settings/metadataproviders/{namespace}/{name}", s.handleSetMetadataProviderSettings)
	mux.HandleFunc("POST /settings/subtitleproviders/{namespace}/{name}", s.handleSetSubtitleProviderSettings)
	mux.HandleFunc("POST /settings/subtitleprofiles/{name}", s.handleSetSubtitleProfileDefault)
	mux.HandleFunc("POST /settings/transcodeprofiles/{name}", s.handleSetTranscodeProfilePriority)
	mux.HandleFunc("GET /{$}", s.handleIndex)

	return mux
}

// handleHealthz answers the liveness probe, unconditionally. It must not
// depend on the cluster, NATS, or anything else that could be down while the
// process itself is fine -- a probe that fails for the wrong reason causes
// Kubernetes to restart a healthy process. /readyz is where a dependency
// belongs; this one never gates on Options.Reader or anything else.
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleReadyz answers the readiness probe. Ruling R2:
// config/manager/ui.yaml used to point both probes at /healthz with a
// comment that ui "has no external dependency yet" -- Options.Reader makes
// that untrue, so a probe that stopped meaning anything would report a UI
// whose caches never finish their initial List as Ready forever, serving
// empty pages indefinitely with no signal anything is wrong.
//
// It answers 503 while Options.WaitForSync reports the cache has not
// finished syncing or Options.Projected reports no projection round has
// completed, and 200 once both have. A nil Options (no cluster
// configured at all) defaults WaitForSync to a function that always returns
// true -- see [NewServer] -- so this never blocks a cluster-less `clustarr
// ui` from becoming Ready.
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if !s.opts.WaitForSync(r.Context()) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("cluster reader is not synced yet"))
		return
	}
	if !s.opts.Projected() {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("the first projection round has not completed yet"))
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

// handleIndex sends "/" to the Pipeline page, the first page a fresh
// deployment has anything meaningful to show.
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/pipeline", http.StatusFound)
}

// handlePipeline renders the Pipeline page (§A3.4) from the current
// projection returned by Options.Entries.
func (s *Server) handlePipeline(w http.ResponseWriter, r *http.Request) {
	entries := s.opts.Entries(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.Pipeline(entries).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render pipeline page", "error", err)
	}
}

// handleDownloads renders the Downloads page (§A3.4) from the current
// cluster state, read directly through Options.Reader. Task D3-2 consumes
// D3-0's Reader on its own -- the shared projection loop (Task D3-1) that
// later backs /events/downloads (Task D3-3) is a separate package this task
// does not touch, so this handler lists Download and DownloadClient itself
// rather than waiting on it.
func (s *Server) handleDownloads(w http.ResponseWriter, r *http.Request) {
	downloads, clients := s.listDownloads(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.Downloads(downloads, clients).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render downloads page", "error", err)
	}
}

// listDownloads lists every Download and DownloadClient through
// Options.Reader, each sorted by name for a stable render.
//
// A nil Reader (no cluster configured, or a test that only cares about
// routing) returns two nil slices, exactly as a nil Options.Entries behaves
// for the Pipeline page -- the page must render something sensible with no
// cluster at all, not just with an empty one.
//
// A List error (a reachable but not-yet-synced cache, or a cluster that
// dropped mid-request) is logged and treated as "nothing to show" rather
// than failing the request: Options.Reader is a read-only, best-effort seam,
// and a transient list failure must not turn into a 500 for a page whose
// only job is to show what it currently knows.
func (s *Server) listDownloads(ctx context.Context) ([]downloadv1.Download, []downloadv1.DownloadClient) {
	if s.opts.Reader == nil {
		return nil, nil
	}

	var downloadList downloadv1.DownloadList
	if err := s.opts.Reader.List(ctx, &downloadList); err != nil {
		logging.FromContext(ctx).Error("list downloads", "error", err)
	}
	downloads := downloadList.Items
	sort.Slice(downloads, func(i, j int) bool { return downloads[i].Name < downloads[j].Name })

	var clientList downloadv1.DownloadClientList
	if err := s.opts.Reader.List(ctx, &clientList); err != nil {
		logging.FromContext(ctx).Error("list download clients", "error", err)
	}
	clients := clientList.Items
	sort.Slice(clients, func(i, j int) bool { return clients[i].Name < clients[j].Name })

	return downloads, clients
}

// handleLibrary renders the Library page (amendment §A3.4, Task G3-3) from
// the current library projection returned by Options.Library, plus a
// "Rescan" toolbar built from every known RootFolder (listRootFolders,
// mirroring listDownloads' own direct-Reader reads for config-like data).
func (s *Server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	tab, ok := projection.ParseTab(r.PathValue("tab"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	items := projection.ForTab(s.opts.Library(r.Context()), tab)
	rootFolders := s.listRootFolders(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.Library(tab, items, rootFolders).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render library page", "error", err)
	}
}

// handleLibraryIndex sends /library to its first tab: the library is one
// tab per media type (spec 2026-09-23-library-page-design), and a bare
// /library has no grid of its own.
func (s *Server) handleLibraryIndex(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/library/"+string(projection.TabMovies), http.StatusFound)
}

// listRootFolders lists every RootFolder through Options.Reader, sorted by
// name for a stable render -- listDownloads' own pattern for config-like
// data that does not need to ride the shared projection's live stream.
func (s *Server) listRootFolders(ctx context.Context) []catalogv1.RootFolder {
	if s.opts.Reader == nil {
		return nil
	}

	var list catalogv1.RootFolderList
	if err := s.opts.Reader.List(ctx, &list); err != nil {
		logging.FromContext(ctx).Error("list root folders", "error", err)
		return nil
	}
	rootFolders := list.Items
	sort.Slice(rootFolders, func(i, j int) bool { return rootFolders[i].Name < rootFolders[j].Name })
	return rootFolders
}

// handleLibraryItem renders one catalog item's detail view (§A3.4's "detail
// modal", Task G3-3). It looks the item up in the current library
// projection rather than reading the cluster directly a second time, so the
// detail view and the grid it was reached from always agree -- and so this
// handler needs no per-kind Get, which ui/projection's describeLibraryItem
// already solves once for the whole package.
func (s *Server) handleLibraryItem(w http.ResponseWriter, r *http.Request) {
	item, ok := findLibraryItem(s.opts.Library(r.Context()),
		r.PathValue("namespace"), r.PathValue("kind"), r.PathValue("name"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if item.Kind == commonv1.MediaKindSeries {
		s.renderSeriesPage(w, r, item)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.LibraryDetail(item).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render library detail page", "error", err)
	}
}

// findLibraryItem finds the item in items matching namespace, kind and name
// -- kind compared as a plain string against LibraryItem.Kind, since the URL
// path value is untyped input and any string a caller sends is a legal
// (if non-matching) MediaKind to compare against.
func findLibraryItem(items []projection.LibraryItem, namespace, kind, name string) (projection.LibraryItem, bool) {
	for _, item := range items {
		if item.Ref.Namespace == namespace && string(item.Kind) == kind && item.Ref.Name == name {
			return item, true
		}
	}
	return projection.LibraryItem{}, false
}

// handleSetMonitored is the "monitor"/"unmonitor" action (§A3.2) reached
// from the Library detail page's own form: POST
// /library/{namespace}/{kind}/{name}/monitor with a "monitored" field of
// "true" or "false". It calls Options.Actions.SetMonitored -- and nothing
// else in ui/ ever calls actions.Writer's Create or Patch, per ui/guard_test.go
// -- and renders any failure visibly through finishAction, including
// actions.ErrNoWriter from a ui process with no cluster configured (a nil
// Options.Actions), rather than silently doing nothing.
func (s *Server) handleSetMonitored(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	monitored := r.FormValue("monitored") == "true"

	kind := commonv1.MediaKind(r.PathValue("kind"))
	patched, err := s.opts.Actions.SetMonitored(r.Context(),
		r.PathValue("namespace"), kind, r.PathValue("name"), monitored)
	if isHTMX(r) && kind == commonv1.MediaKindEpisode {
		// An episode row's toggle swaps the row, not the page.
		s.replyEpisodeRow(w, r, r.PathValue("namespace"), r.PathValue("name"), patched, err)
		return
	}
	s.finishAction(w, r, err)
}

// handleSearchNow is the "search now" action (§A3.2): POST
// /library/{namespace}/{kind}/{name}/search, calling Options.Actions.SearchNow.
func (s *Server) handleSearchNow(w http.ResponseWriter, r *http.Request) {
	_, err := s.opts.Actions.SearchNow(r.Context(),
		r.PathValue("namespace"), commonv1.MediaKind(r.PathValue("kind")), r.PathValue("name"))
	s.finishAction(w, r, err)
}

// handleRescan is the "rescan" action (§A3.2): POST /library/rescan with
// "namespace" and "rootFolder" form fields, one submitted by each button in
// the Library page's own rescan toolbar (views.rescanToolbar), calling
// Options.Actions.Rescan.
func (s *Server) handleRescan(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	_, err := s.opts.Actions.Rescan(r.Context(), r.FormValue("namespace"), r.FormValue("rootFolder"))
	s.finishAction(w, r, err)
}

// finishAction is the shared tail of every Library-page write action: on
// success it redirects (303) back to the form's "return" field (defaulting
// to /library so a malformed or missing field still goes somewhere useful),
// and on failure it renders views.ActionError directly in the response
// (no redirect) with a status that reflects the failure -- 503 for
// actions.ErrNoWriter, 400 for actions.ErrInvalid, 500 otherwise -- so the
// error is visible to whatever submitted the form. ErrNoWriter is what a ui
// process with no cluster configured answers; until Task G3-5 wired
// Options.Actions in cmd/clustarr, it was what every production process
// answered.
func (s *Server) finishAction(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		code, status := actionErrorCode(err)
		logging.FromContext(r.Context()).Error("ui action failed", "error", err, "code", code)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(status)
		if renderErr := views.ActionError(code, err.Error()).Render(r.Context(), w); renderErr != nil {
			logging.FromContext(r.Context()).Error("render action error", "error", renderErr)
		}
		return
	}

	returnTo := r.FormValue("return")
	if returnTo == "" || returnTo[0] != '/' {
		returnTo = "/library"
	}
	http.Redirect(w, r, returnTo, http.StatusSeeOther)
}

// actionErrorCode maps an ui/actions error to the stable machine-readable
// code views.ActionError renders on data-action-error (ruling R8) and to the
// HTTP status finishAction answers with.
func actionErrorCode(err error) (code string, status int) {
	switch {
	case errors.Is(err, actions.ErrNoWriter):
		return "no-writer", http.StatusServiceUnavailable
	case errors.Is(err, actions.ErrInvalid):
		return "invalid", http.StatusBadRequest
	default:
		return "failed", http.StatusInternalServerError
	}
}

// handleUnmatched renders the Unmatched page (amendment §A3.4, Task G3-3)
// from the current unmatched-files projection returned by Options.Unmatched.
// Read-only in this task: no action handler here reaches
// Options.Actions -- the manual-assign action's mechanism is being defined
// by G2-4 in importarr, and G3-4 adds it once that lands.
func (s *Server) handleUnmatched(w http.ResponseWriter, r *http.Request) {
	entries := s.opts.Unmatched(r.Context())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.Unmatched(entries).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render unmatched page", "error", err)
	}
}

// handleManualAssign is the Unmatched page's "assign" action (Task G3-4,
// mechanism from G2-4: importarr/worker/rescan/doc.go, "Manual
// assignment"): POST /unmatched/assign with "namespace", "rootFolder",
// "subpath", "kind", "name" and "key" fields, one submitted by each row's
// own form (views.manualAssignForm). It calls Options.Actions.ManualAssign
// -- and nowhere else in ui/ calls actions.Writer's Create or Patch, per
// ui/guard_test.go.
//
// On failure -- actions.ErrNoWriter (Options.Actions unset: a ui process
// with no cluster configured), actions.ErrInvalid (a
// malformed target), or the apiserver's own error -- it renders through
// finishAction exactly like the Library page's own actions, so the failure
// is visible rather than silent. On success it does NOT use finishAction's
// generic "return" redirect: it sends the user to the newly created
// LibraryScan's own detail page (handleLibraryScanDetail) instead, per this
// task's own instruction ("point the user at the resulting LibraryScan so
// they can see whether the worker accepted it") -- the worker's verdict
// lands on that scan's own status, which this handler never touches; it
// only reads back the name and namespace the apiserver returned from the
// create.
func (s *Server) handleManualAssign(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	target := actions.ManualAssignTarget{
		Kind: commonv1.MediaKind(r.FormValue("kind")),
		Name: r.FormValue("name"),
		Key:  r.FormValue("key"),
	}
	scan, err := s.opts.Actions.ManualAssign(r.Context(),
		r.FormValue("namespace"), r.FormValue("rootFolder"), r.FormValue("subpath"), target)
	if err != nil {
		s.finishAction(w, r, err)
		return
	}
	http.Redirect(w, r, libraryScanDetailPath(scan.Namespace, scan.Name), http.StatusSeeOther)
}

// libraryScanDetailPath builds a LibraryScan's detail page path, matching
// "GET /library-scans/{namespace}/{name}" below.
func libraryScanDetailPath(namespace, name string) string {
	return fmt.Sprintf("/library-scans/%s/%s", namespace, name)
}

// handleLibraryScanDetail renders one LibraryScan's status (Task G3-4): a
// single Get through Options.Reader, exactly the same read-only seam every
// other page uses -- this is where a manual assignment's outcome (accepted,
// or refused with a reason) becomes visible, per this task's own
// instruction, and it is a read: the UI never writes status, and reading
// one object's status here is no different from the Library and Downloads
// pages already rendering status fields they never write.
//
// A nil Reader answers 404 as if the scan does not exist, mirroring
// handleLibraryItem's own "nothing to show without a cluster" behaviour --
// there being no cluster and the scan being genuinely absent look the same
// to a caller with no other way to tell them apart.
func (s *Server) handleLibraryScanDetail(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if s.opts.Reader == nil {
		http.NotFound(w, r)
		return
	}

	var scan catalogv1.LibraryScan
	key := client.ObjectKey{Namespace: r.PathValue("namespace"), Name: r.PathValue("name")}
	if err := s.opts.Reader.Get(ctx, key, &scan); err != nil {
		if apierrors.IsNotFound(err) {
			http.NotFound(w, r)
			return
		}
		logging.FromContext(ctx).Error("get library scan", "error", err)
		http.Error(w, "failed to load library scan", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.LibraryScanDetail(scan).Render(ctx, w); err != nil {
		logging.FromContext(ctx).Error("render library scan detail page", "error", err)
	}
}
