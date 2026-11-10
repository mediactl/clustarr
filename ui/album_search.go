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
	"fmt"
	"net/http"
	"time"

	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
	"github.com/mediactl/clustarr/ui/views"
)

// An album's interactive search (2026-11-10 design §9.2; plan W4b.1) is
// the episode's -- a Search without grabBest (startInteractiveSearch) whose
// results the panel polls, its download button adding the release to
// spec.grab -- drawn with Lidarr's columns (views.AlbumSearchModal). The
// shared /searches routes draw the video columns and would swap them in at
// the first poll, so the album panel polls and grabs through routes of its
// own here, over the same Search and the same actions.

// handleAlbumInteractiveSearch serves POST
// /library/{namespace}/album/{name}/search/interactive: it starts an
// interactive search for the album and answers htmx with the album's search
// modal, its results panel polling; a form post is sent to the panel's
// page. An album the cache does not hold is not found, and nothing is
// created for it.
func (s *Server) handleAlbumInteractiveSearch(w http.ResponseWriter, r *http.Request) {
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	a, item, ok := s.albumWithArtist(r.Context(), ns, name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	panel, ok := s.startInteractiveSearch(w, r, ns, commonv1.MediaKindAlbum, name)
	if !ok {
		return
	}
	p := views.AlbumSearchPanel{SearchPanel: *panel, Album: albumTableRow(a, time.Now()), ArtistTitle: item.Title}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.AlbumSearchModal(p).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render album search modal", "error", err)
	}
}

// handleAlbumSearchPanel serves GET
// /library/{namespace}/album/{name}/search/panel/{search}: the album's
// results panel, which polls this route until it is settled. A Search that
// is gone, or one that is not this album's, answers a panel that says so
// and stops polling. Anyone but htmx is sent to the search's own page.
func (s *Server) handleAlbumSearchPanel(w http.ResponseWriter, r *http.Request) {
	ns, name, searchName := r.PathValue("namespace"), r.PathValue("name"), r.PathValue("search")
	a, item, ok := s.albumWithArtist(r.Context(), ns, name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !isHTMX(r) {
		http.Redirect(w, r, views.SearchPanel{Namespace: ns, Name: searchName}.URL(), http.StatusSeeOther)
		return
	}
	p, _ := s.albumSearchPanel(r.Context(), a, item, searchName)
	s.renderAlbumSearchPanel(w, r, p)
}

// handleAlbumGrabRelease serves POST
// /library/{namespace}/album/{name}/search/panel/{search}/grab with a
// "guid" field: the album panel's download button, handleGrabRelease over
// the album's columns. A release with a permanent rejection is grabbed
// with spec.override -- the button asked the person first. htmx gets the
// panel back from the Search the patch returned, with the grab requested;
// a failure is rendered in the panel.
func (s *Server) handleAlbumGrabRelease(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	ns, name, searchName, guid := r.PathValue("namespace"), r.PathValue("name"), r.PathValue("search"), r.FormValue("guid")
	a, item, ok := s.albumWithArtist(r.Context(), ns, name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	p, search := s.albumSearchPanel(r.Context(), a, item, searchName)
	if search == nil {
		if !isHTMX(r) {
			http.NotFound(w, r)
			return
		}
		s.renderAlbumSearchPanel(w, r, p)
		return
	}

	var err error
	release, found := resultByGUID(search.Status.Results, guid)
	if !found {
		err = fmt.Errorf("%w: release %q is not in the search's results", actions.ErrInvalid, guid)
	} else {
		var patched *catalogv1.Search
		if patched, err = s.opts.Actions.GrabRelease(r.Context(), search, guid, permanentlyRejected(release)); err == nil {
			search = patched
		}
	}
	if !isHTMX(r) {
		if err != nil {
			s.finishAction(w, r, err)
			return
		}
		http.Redirect(w, r, searchPanelURL(search), http.StatusSeeOther)
		return
	}
	p.SearchPanel = searchPanel(search, time.Now())
	if err != nil {
		p.Error = failureOf(r, err)
	}
	s.renderAlbumSearchPanel(w, r, p)
}

// albumSearchPanel is the album's panel for the Search named: the shared
// panel (searchPanel) over the album's row and artist, with the Search
// read; a Search that is gone, or one for another item, is a Gone panel
// and no Search.
func (s *Server) albumSearchPanel(ctx context.Context, a *catalogv1.Album, item projection.LibraryItem, searchName string) (views.AlbumSearchPanel, *catalogv1.Search) {
	now := time.Now()
	p := views.AlbumSearchPanel{Album: albumTableRow(a, now), ArtistTitle: item.Title}
	var search catalogv1.Search
	if s.getObject(ctx, types.NamespacedName{Namespace: a.Namespace, Name: searchName}, &search) && searchIsFor(&search, commonv1.MediaKindAlbum, a.Name) {
		p.SearchPanel = searchPanel(&search, now)
		return p, &search
	}
	p.SearchPanel = views.SearchPanel{Namespace: a.Namespace, Name: searchName, Gone: true}
	return p, nil
}

// searchIsFor is whether the Search was made for the item kind/name.
func searchIsFor(search *catalogv1.Search, kind commonv1.MediaKind, name string) bool {
	ref := search.Spec.MediaRef
	return ref != nil && ref.Kind == kind && ref.Name == name
}

// renderAlbumSearchPanel answers htmx with the album's panel.
func (s *Server) renderAlbumSearchPanel(w http.ResponseWriter, r *http.Request, p views.AlbumSearchPanel) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := views.AlbumSearchPanelView(p).Render(r.Context(), w); err != nil {
		logging.FromContext(r.Context()).Error("render album search panel", "error", err)
	}
}
