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
	"fmt"
	"net/http"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// handleLiveTVIndex sends /livetv to its first page, Channels.
func (s *Server) handleLiveTVIndex(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/livetv/channels", http.StatusFound)
}

// handleLiveTVSources renders the M3U/EPG Manager (the direct-tuners and ui
// design §5.5).
func (s *Server) handleLiveTVSources(w http.ResponseWriter, r *http.Request) {
	rows := projection.LiveTVSources(s.listIPTVProviders(r.Context()), s.now())
	preloadable(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.LiveTVSources(rows).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render the M3U/EPG Manager", "error", err)
	}
}

// handleLiveTVSourcesTables renders the Manager's tables alone, which
// fetch themselves again every views.SourcesRefresh.
func (s *Server) handleLiveTVSourcesTables(w http.ResponseWriter, r *http.Request) {
	rows := projection.LiveTVSources(s.listIPTVProviders(r.Context()), s.now())
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.LiveTVSourcesTables(rows).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render the M3U/EPG Manager's tables", "error", err)
	}
}

// handleRefreshLiveTV is a provider's "Refresh now".
func (s *Server) handleRefreshLiveTV(w http.ResponseWriter, r *http.Request) {
	err := s.opts.Actions.RefreshLiveTV(r.Context(), r.PathValue("namespace"), r.PathValue("name"))
	s.finishActionWith(w, r, err, "Refresh requested")
}

// handleSetProviderEnabled is a provider's enabled switch: a merge patch of
// spec.enabled, through the settings kind's write.
func (s *Server) handleSetProviderEnabled(w http.ResponseWriter, r *http.Request) {
	var enabled bool
	switch v := r.FormValue("enabled"); v {
	case "true", "false":
		enabled = v == "true"
	default:
		s.finishActionWith(w, r, fmt.Errorf("%w: enabled %q is neither true nor false", actions.ErrInvalid, v), "")
		return
	}
	k, _ := actions.ConfigKindBySlug("iptvproviders")
	_, err := s.opts.Actions.UpdateConfig(r.Context(), k, r.PathValue("namespace"), r.PathValue("name"), map[string]any{"enabled": enabled})
	done := "Disabled"
	if enabled {
		done = "Enabled"
	}
	s.finishActionWith(w, r, err, done)
}
