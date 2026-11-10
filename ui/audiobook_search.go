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

	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// An audiobook's interactive search (2026-11-10 design §9.5) is the
// episode's -- startInteractiveSearch, a Search without grabBest, a
// person's pick through GrabRelease -- shown under the audiobook's own
// columns (Format, Bitrate, Size, Age, Indexer, Peers, Score) by the shared
// panel, which draws them for an audiobook's Search
// (views.SearchPanel.Columns) and polls and grabs through the shared
// /searches routes. What the Search does not carry, the audiobook's
// runtime, those routes read here (audiobookRuntimeMinutes) so the
// Bitrate column holds through every poll and grab.

// handleAudiobookInteractiveSearch serves POST
// /library/{namespace}/audiobook/{name}/search/interactive, the page bar's
// Interactive Search and the modal's Search tab's: it starts the
// interactive search and answers htmx with the audiobook's modal on its
// Search tab, the results panel polling; a form post is sent to the
// panel's page. An audiobook the cache does not hold is not found, and
// nothing is created for it.
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
	v.Tab, v.Search = "search", panel
	s.renderAudiobook(w, r, v, true)
}

// audiobookRuntimeMinutes is the runtime the search panel estimates an
// audiobook release's bitrate from (views.SearchPanel.RuntimeMinutes), as
// the audiobook's page reads it: the metadata's runtime, else the parts'
// durations together; 0 when neither is known, or the cache does not hold
// the audiobook, and the column is empty.
func (s *Server) audiobookRuntimeMinutes(ctx context.Context, ns, name string) int32 {
	var a catalogv1.Audiobook
	if !s.getObject(ctx, types.NamespacedName{Namespace: ns, Name: name}, &a) {
		return 0
	}
	if md := a.Status.Metadata; md != nil && md.RuntimeMinutes > 0 {
		return md.RuntimeMinutes
	}
	return audiobookPartsMinutes(s.audiobookFiles(ctx, &a))
}
