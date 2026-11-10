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
	"time"

	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/views"
)

// An audiobook's interactive search (2026-11-10 design §9.5) is the
// episode's -- startInteractiveSearch, a Search without grabBest, a
// person's pick through GrabRelease -- shown under the audiobook's own
// columns (Format, Bitrate, Size, Age, Indexer, Peers, Score). The shared
// /searches panel draws Sonarr's columns, and a panel polls itself, so the
// audiobook's panel polls and grabs through routes of its own under the
// audiobook's page, which answer the same Search in its columns.

// handleAudiobookInteractiveSearch serves POST
// /library/{namespace}/audiobook/{name}/search/interactive, the page bar's
// Interactive Search and the modal's Search tab's: it starts the
// interactive search and answers htmx with the audiobook's modal on its
// Search tab, the results panel polling the audiobook's panel route; a
// form post is sent to the panel's page. An audiobook the cache does not
// hold is not found, and nothing is created for it.
func (s *Server) handleAudiobookInteractiveSearch(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	v, ok := s.audiobookView(r.Context(), ns, name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	panel, ok := s.startInteractiveSearch(w, r, ns, commonv1.MediaKindAudiobook, name)
	if !ok {
		return
	}
	p := audiobookSearchPanel(*panel, name, v)
	v.Tab, v.Search = "search", &p
	s.renderAudiobook(w, r, v, true)
}

// handleAudiobookSearchPanel serves GET
// /library/{namespace}/audiobook/{name}/search/panel/{search}: the Search's
// results panel under the audiobook's columns, which the panel polls until
// it is settled. A Search that is gone (its TTL expired) answers a panel
// that says so and stops polling. Anyone but htmx gets the shared panel's
// page.
func (s *Server) handleAudiobookSearchPanel(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	v, ok := s.audiobookView(r.Context(), ns, name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var search catalogv1.Search
	p := views.SearchPanel{Namespace: ns, Name: r.PathValue("search"), Gone: true}
	if s.getObject(r.Context(), types.NamespacedName{Namespace: ns, Name: r.PathValue("search")}, &search) {
		p = searchPanel(&search, time.Now())
	}
	s.renderAudiobookSearchPanel(w, r, audiobookSearchPanel(p, name, v), search.Spec.MediaRef)
}

// handleAudiobookGrab serves POST
// /library/{namespace}/audiobook/{name}/search/panel/{search}/grab with a
// "guid" field: the audiobook panel's download button, handleGrabRelease
// answered in the audiobook's columns. A release with a permanent
// rejection is grabbed with spec.override -- the button asked the person
// first. htmx gets the panel back, from the Search the patch returned,
// with the grab requested; a failure is rendered in the panel.
func (s *Server) handleAudiobookGrab(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	ns, name, guid := r.PathValue("namespace"), r.PathValue("name"), r.FormValue("guid")
	v, ok := s.audiobookView(r.Context(), ns, name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var search catalogv1.Search
	if !s.getObject(r.Context(), types.NamespacedName{Namespace: ns, Name: r.PathValue("search")}, &search) {
		if !isHTMX(r) {
			http.NotFound(w, r)
			return
		}
		s.renderAudiobookSearchPanel(w, r, audiobookSearchPanel(views.SearchPanel{Namespace: ns, Name: r.PathValue("search"), Gone: true}, name, v), nil)
		return
	}

	var err error
	release, found := resultByGUID(search.Status.Results, guid)
	if !found {
		err = fmt.Errorf("%w: release %q is not in the search's results", actions.ErrInvalid, guid)
	} else {
		var patched *catalogv1.Search
		if patched, err = s.opts.Actions.GrabRelease(r.Context(), &search, guid, permanentlyRejected(release)); err == nil {
			search = *patched
		}
	}
	if !isHTMX(r) {
		if err != nil {
			s.finishAction(w, r, err)
			return
		}
		http.Redirect(w, r, searchPanelURL(&search), http.StatusSeeOther)
		return
	}
	p := audiobookSearchPanel(searchPanel(&search, time.Now()), name, v)
	if err != nil {
		p.Error = failureOf(r, err)
	}
	s.renderAudiobookSearchPanel(w, r, p, search.Spec.MediaRef)
}

// renderAudiobookSearchPanel answers the audiobook's results panel to
// htmx, and the shared panel's page to anyone else.
func (s *Server) renderAudiobookSearchPanel(w http.ResponseWriter, r *http.Request, p views.AudiobookSearchPanel, ref *commonv1.MediaRef) {
	if !isHTMX(r) {
		s.renderSearchPanel(w, r, p.SearchPanel, ref)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.AudiobookSearchPanelView(p).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render audiobook search panel", "error", err)
	}
}

// audiobookSearchPanel is the shared panel under the audiobook's columns:
// the panel, the audiobook it polls and grabs through, and the runtime its
// releases' bitrates are estimated from (the metadata's, else the parts').
func audiobookSearchPanel(p views.SearchPanel, audiobook string, v views.AudiobookView) views.AudiobookSearchPanel {
	return views.AudiobookSearchPanel{SearchPanel: p, Audiobook: audiobook, RuntimeMinutes: v.RuntimeMinutes}
}
