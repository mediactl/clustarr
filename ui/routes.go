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
	"net/http"
	"sort"

	downloadv1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/views"
)

// routes builds the route table. It is a method so handlers can close over
// s.opts without package-level state.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /readyz", s.handleReadyz)
	mux.HandleFunc("GET /pipeline", s.handlePipeline)
	mux.HandleFunc("GET /events/pipeline", s.handlePipelineEvents)
	mux.HandleFunc("GET /downloads", s.handleDownloads)
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
// finished syncing, and 200 once it has. A nil Options (no cluster
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
