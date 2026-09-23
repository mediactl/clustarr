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
	"net/http"

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
