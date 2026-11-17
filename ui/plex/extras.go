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
	"slices"
	"strconv"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/trailers"
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
// rpc.catalog.metadata.extras): it answers from the clustarr-plex-extras
// bucket and asks Plex on a miss. A show answers its seasons' extras after
// its own ([extrasPlexIDs]). An item with no Plex id answers none; when the
// gateway cannot answer, the route answers an error, never an empty list.
// A movie or a show Plex has no trailer for answers its own, when the
// trailers agent has fetched one ([trailerClip]).

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
		plexIDs, own, ok := extrasPlexIDs(root, idx, r.PathValue("ratingKey"))
		if !ok {
			http.NotFound(w, r)
			return
		}

		// One id that cannot be answered fails the whole list: a show's
		// list without one season's trailers would have PMS delete them.
		items := []Extra{}
		seen := map[string]bool{}
		for _, plexID := range plexIDs {
			if h.opts.Extras == nil {
				http.Error(w, "the provider cannot ask the metadata gateway for extras", http.StatusServiceUnavailable)
				return
			}
			got, err := h.opts.Extras(r.Context(), plexID)
			if err != nil {
				logging.FromContext(r.Context()).Error("plex: ask the metadata gateway for extras", "plexID", plexID, "err", err)
				http.Error(w, "the metadata gateway did not answer with extras", http.StatusBadGateway)
				return
			}
			for _, e := range got {
				if id := extraIdentity(e); id != "" {
					if seen[id] {
						continue
					}
					seen[id] = true
				}
				items = append(items, e)
			}
		}
		// The item's own trailer, where Plex has none: fetched by the
		// trailers agent and named to Plex by its file. An unreadable
		// record fails the list, since the trailer PMS holds from it would
		// be deleted by a list without it.
		if own.kind != "" && h.opts.Trailer != nil && !slices.ContainsFunc(items, trailers.IsPlexTrailer) {
			entry, have, err := h.opts.Trailer(r.Context(), own.kind, own.uid)
			if err != nil {
				logging.FromContext(r.Context()).Error("plex: read the item's trailer record", "kind", own.kind, "uid", own.uid, "err", err)
				http.Error(w, "the item's trailer record could not be read", http.StatusBadGateway)
				return
			}
			if have {
				if clip, ok := trailerClip(entry, h.opts.PathMappings, r.PathValue("ratingKey")); ok {
					items = append(items, clip)
				}
			}
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

// extraIdentity is what makes two extras the same clip: Plex's guid for
// it, else its ratingKey; "" when it carries neither, and such an extra is
// never dropped as a repeat.
func extraIdentity(e Extra) string {
	var v struct {
		GUID      string `json:"guid"`
		RatingKey string `json:"ratingKey"`
	}
	if err := json.Unmarshal(e, &v); err != nil {
		return ""
	}
	if v.GUID != "" {
		return v.GUID
	}
	return v.RatingKey
}

// extrasPlexIDs are the Plex ids whose extras the item ratingKey names is
// answered with, none when it has no Plex id; false when the root does not
// serve it. A movie, a season and an episode answer their own. A show
// answers its own and then each of its seasons', in season order: Plex's
// metadata service keeps most TV trailers on a season (Succession: none on
// the show, five on its fourth season) and PMS stores none for a season,
// so a show that answered only its own had no trailer at all (talos,
// 2026-11-17: 390 of 541 shows). Specials are left out. A music key has no
// Plex id (design §7.2, Q5), so every one a music root serves answers the
// empty list (§7.4: "/extras answers an empty list for every music key").
// own names the movie or the show itself, whose own trailer the list may
// end with; it is empty for a season, an episode and a music key.
func extrasPlexIDs(root rootDef, idx *projection.Index, ratingKey string) (ids []string, own trailerOwner, ok bool) {
	one := func(id string) []string {
		if id == "" {
			return nil
		}
		return []string{id}
	}
	k, ok := resolveKey(idx, ratingKey)
	if !ok {
		return nil, own, false
	}
	if root.music {
		_, ok := resolveMusicItem(root, idx, k)
		return nil, own, ok
	}
	if k.Track != "" || k.IsPart || k.Synthetic != "" {
		return nil, own, false
	}
	if k.IsSeason {
		s, ok := idx.SeriesByUID(k.UID)
		if !ok || !root.declares(typeSeason) {
			return nil, own, false
		}
		return one(seasonPlexID(s, k.Season)), own, true
	}
	if m, ok := idx.MovieByUID(k.UID); ok && root.declares(typeMovie) {
		return one(moviePlexID(m)), trailerOwner{kind: trailers.KindMovie, uid: string(m.UID)}, true
	}
	if s, ok := idx.SeriesByUID(k.UID); ok && root.declares(typeShow) {
		return append(one(seriesPlexID(s)), seasonPlexIDs(s)...), trailerOwner{kind: trailers.KindSeries, uid: string(s.UID)}, true
	}
	if e, ok := idx.EpisodeByUID(k.UID); ok && root.declares(typeEpisode) {
		return one(episodePlexID(e)), own, true
	}
	return nil, own, false
}

// trailerOwner is the Movie or Series a trailer record is kept for.
type trailerOwner struct{ kind, uid string }
