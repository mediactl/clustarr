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

package plex

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/ui/projection"
)

// PMS asks a custom provider for an item's extras on every refresh --
// GET {root}/library/metadata/{ratingKey}/extras, a route Plex's provider
// docs do not list -- and reads a 404 or an empty list as "this item has
// none", deleting the trailers it holds for it. Answering 404 cost the
// owner's library the Internet Video Archive trailers Plex had attached
// (kind-cluster-plex, 2026-10-06). The route answers Plex's own extras for
// the item's Plex id, as Plex's metadata service sends them: clips with
// their extraType and Media URLs, which PMS plays from Internet Video
// Archive. The metadata gateway holds them (Options.Extras, bound to
// rpc.catalogarr.metadata.extras): it answers from the clustarr-plex-extras
// bucket and asks Plex on a miss. An item with no Plex id answers none;
// when the gateway cannot answer, the route answers an error, never an
// empty list.

// Extra is one of an item's extras exactly as Plex's metadata service sent
// it, so every field PMS reads reaches it unchanged.
type Extra = json.RawMessage

type extrasContainer struct {
	Offset     int     `json:"offset"`
	TotalSize  int     `json:"totalSize"`
	Identifier string  `json:"identifier"`
	Size       int     `json:"size"`
	Metadata   []Extra `json:"Metadata"`
}

type extrasContainerResponse struct {
	MediaContainer extrasContainer `json:"MediaContainer"`
}

func (h *handler) handleExtras(root rootDef) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idx, ok := h.index(w, r)
		if !ok {
			return
		}
		plexID, ok := extrasPlexID(root, idx, r.PathValue("ratingKey"))
		if !ok {
			http.NotFound(w, r)
			return
		}

		items := []Extra{}
		if plexID != "" {
			if h.opts.Extras == nil {
				http.Error(w, "the provider cannot ask the metadata gateway for extras", http.StatusServiceUnavailable)
				return
			}
			got, err := h.opts.Extras(r.Context(), plexID)
			if err != nil {
				logging.FromContext(r.Context()).Error("plex: ask the metadata gateway for extras", "plexID", plexID, "error", err)
				http.Error(w, "the metadata gateway did not answer with extras", http.StatusBadGateway)
				return
			}
			items = got
		}

		start, size := containerWindow(r, len(items))
		page := items[min(start, len(items)):min(start+size, len(items))]
		writeJSON(w, http.StatusOK, extrasContainerResponse{MediaContainer: extrasContainer{
			Offset:     start,
			TotalSize:  len(items),
			Identifier: root.identifier,
			Size:       len(page),
			Metadata:   page,
		}})
	}
}

// containerWindow is the X-Plex-Container-Start and -Size PMS asks for, as
// query parameters or headers; the whole list when it names none.
func containerWindow(r *http.Request, total int) (start, size int) {
	param := func(name string) (int, bool) {
		v := r.URL.Query().Get(name)
		if v == "" {
			v = r.Header.Get(name)
		}
		n, err := strconv.Atoi(v)
		return n, err == nil && n >= 0
	}
	start, _ = param("X-Plex-Container-Start")
	size, ok := param("X-Plex-Container-Size")
	if !ok {
		size = total
	}
	return start, size
}

// extrasPlexID is the Plex id of the item ratingKey names, "" when it has
// none; false when the root does not serve it.
func extrasPlexID(root rootDef, idx *projection.Index, ratingKey string) (string, bool) {
	uid, season, isSeason, ok := resolveKey(idx, ratingKey)
	if !ok {
		return "", false
	}
	if isSeason {
		s, ok := idx.SeriesByUID(uid)
		if !ok || !root.declares(typeSeason) {
			return "", false
		}
		return seasonPlexID(s, season), true
	}
	if m, ok := idx.MovieByUID(uid); ok && root.declares(typeMovie) {
		return moviePlexID(m), true
	}
	if s, ok := idx.SeriesByUID(uid); ok && root.declares(typeShow) {
		return seriesPlexID(s), true
	}
	if e, ok := idx.EpisodeByUID(uid); ok && root.declares(typeEpisode) {
		return episodePlexID(e), true
	}
	return "", false
}
