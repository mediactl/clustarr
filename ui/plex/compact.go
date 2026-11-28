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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// CompactPostersPath is the pairing list's path under the ui: outside /art,
// which serves images alone, and beside the provider roots, whose index it
// is built from.
const CompactPostersPath = "/plex/compact-posters"

// compactPair is one poster with a compact twin (the compact-posters
// design, 2026-11-27 §5.3).
type compactPair struct {
	// URL is the poster URL the provider hands Plex for the item, exactly
	// as handed: Plex names the poster it stores by this URL's SHA-1, which
	// is all a row of Plex's keeps of it.
	URL string `json:"url"`
	// Compact is the twin's path under the ui's own address, its digest in
	// ?v so the answer may be kept for good.
	Compact string `json:"compact"`
}

// handleCompactPosters answers the pairing list: for every movie and series
// that has a compact rendition (Options.Compact), the regular poster's URL
// and the twin's path. The proxy in the Plex pod reads it about once a
// minute to answer a small poster request with the twin; it sends the ETag
// back, and an unchanged list is a 304.
//
// The URL is built by the code that builds an item's coverPoster
// (artworkFor.url), so the two cannot drift: a poster whose URL is not what
// Plex was handed would simply never pair. The list carries no title and
// no name, and is sorted, so the same library is the same bytes.
func (h *handler) handleCompactPosters(w http.ResponseWriter, r *http.Request) {
	pairs := []compactPair{}
	if h.opts.Compact != nil && h.opts.ExternalURL != "" {
		idx, ok := h.index(w, r)
		if !ok {
			return
		}
		add := func(af artworkFor) {
			digest, ok := h.opts.Compact(af.kind, af.uid)
			if !ok {
				return
			}
			u, ok := af.url(h.opts.ExternalURL, catalogv1.ImageTypePoster)
			if !ok {
				return
			}
			pairs = append(pairs, compactPair{
				URL:     u,
				Compact: "/art/" + string(af.kind) + "/" + string(af.uid) + "/" + string(catalogv1.ImageTypePoster) + "?size=compact&v=" + digest,
			})
		}
		for _, m := range idx.Movies() {
			add(artworkFor{kind: commonv1.MediaKindMovie, uid: m.UID, artwork: m.Status.Artwork, overlay: m.Status.Overlay, contentType: h.opts.ArtContentType})
		}
		for _, s := range idx.AllSeries() {
			add(artworkFor{kind: commonv1.MediaKindSeries, uid: s.UID, artwork: s.Status.Artwork, overlay: s.Status.Overlay, contentType: h.opts.ArtContentType})
		}
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].URL < pairs[j].URL })
	}
	body, err := json.Marshal(struct {
		Posters []compactPair `json:"posters"`
	}{pairs})
	if err != nil {
		logging.FromContext(r.Context()).Error("plex: encode the compact posters list", "err", err)
		writeError(w, http.StatusInternalServerError, "failed to encode the list")
		return
	}
	sum := sha256.Sum256(body)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
