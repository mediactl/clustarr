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
	"net/http"
	"strings"

	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/projection"
)

// Image is one Image[] entry, on a Metadata object or the /images container
// (research §7). Alt is never set here -- "typically movie title", and
// every caller already carries the title in the same Metadata object or, for
// the /images route, could look it up; the research note names no
// requirement to duplicate it here, and nothing in spec §D.5 asks for it.
type Image struct {
	Type string `json:"type"`
	URL  string `json:"url"`
	Alt  string `json:"alt,omitempty"`
}

// Plex image types this provider ever populates (research §7's five values;
// backgroundSquare is recommended too, but nothing in the catalog's own
// ArtworkEntry types distinguishes a square crop from [catalogv1.ImageTypeFanart],
// so it is left out rather than duplicating background under a second name).
const (
	plexImageBackground  = "background"
	plexImageClearLogo   = "clearLogo"
	plexImageCoverPoster = "coverPoster"
	plexImageSnapshot    = "snapshot"
)

// artworkFor is what [buildImages] and thumb/art both read from: the
// catalog kind, its own UID (for [projection.ArtURL]) and its ArtworkEntry
// list, plus the poster overlay when the kind has one (Movie and Series
// only, spec §B.3) -- the same three inputs
// ui/projection/library.go's posterArt already reads, generalised to every
// image type this route needs, not only the poster.
type artworkFor struct {
	kind    commonv1.MediaKind
	uid     types.UID
	artwork []catalogv1.ArtworkEntry
	overlay *catalogv1.OverlayEntry
}

// entry finds t in af.artwork, reporting false when it was never fetched.
func (af artworkFor) entry(t catalogv1.ImageType) (catalogv1.ArtworkEntry, bool) {
	for _, a := range af.artwork {
		if a.Type == t {
			return a, true
		}
	}
	return catalogv1.ArtworkEntry{}, false
}

// url returns the [projection.ArtURL] for image type t, preferring the
// rating-badge overlay for a poster (the same precedence
// ui/projection/library.go's posterArt already applies to the Library page's
// own poster), false when nothing has been fetched for t yet.
func (af artworkFor) url(externalURL string, t catalogv1.ImageType) (string, bool) {
	base := trimBase(externalURL)
	if t == catalogv1.ImageTypePoster && af.overlay != nil {
		return base + projection.ArtURL(af.kind, af.uid, t, af.overlay.Digest), true
	}
	if a, ok := af.entry(t); ok {
		return base + projection.ArtURL(af.kind, af.uid, t, a.Digest), true
	}
	return "", false
}

// buildImages builds a Metadata object's own Image[] (research §5.2):
// coverPoster from the poster artwork (overlay-first), background from
// fanart, clearLogo from logo. A type with nothing fetched yet is left out
// entirely (spec §D.5: "omitted, never blanked").
func buildImages(externalURL string, af artworkFor) []Image {
	var out []Image
	if u, ok := af.url(externalURL, catalogv1.ImageTypePoster); ok {
		out = append(out, Image{Type: plexImageCoverPoster, URL: u})
	}
	if u, ok := af.url(externalURL, catalogv1.ImageTypeFanart); ok {
		out = append(out, Image{Type: plexImageBackground, URL: u})
	}
	if u, ok := af.url(externalURL, catalogv1.ImageTypeLogo); ok {
		out = append(out, Image{Type: plexImageClearLogo, URL: u})
	}
	return out
}

// buildEpisodeImages is [buildImages]' episode counterpart (research §5.2's
// screenshot/snapshot, spec §D.5's "screenshot as snapshot when stored").
// EpisodeStatus carries no artwork field today (api/catalog/v1alpha1/episode_types.go),
// so this never has anything to report; it exists so the day that field is
// added, only this function changes.
func buildEpisodeImages(string, *catalogv1.Episode) []Image {
	return nil
}

// thumbAndArt is a Metadata object's own scalar thumb/art fields (research
// §5.1): the same URLs [buildImages]' coverPoster/background carry, spelled
// out separately because thumb/art and Image[] are two different fields on
// the wire (research §7's open question: which one PMS actually downloads is
// unverified, so this provider populates both).
func thumbAndArt(externalURL string, af artworkFor) (thumb, art string) {
	thumb, _ = af.url(externalURL, catalogv1.ImageTypePoster)
	art, _ = af.url(externalURL, catalogv1.ImageTypeFanart)
	return thumb, art
}

// handleImages answers GET {metadata key}/{ratingKey}/images: the artwork
// gallery (research §7). Unlike Metadata.Image[], which carries one of each
// type, "highly recommended", this route "lists all available assets" --
// but this provider fetches at most one of each type per item (catalogarr's
// metadata gateway keeps a single ArtworkEntry per type), so the gallery and
// Metadata.Image[] are the same list here.
func (h *handler) handleImages(root rootDef) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idx, ok := h.index(w, r)
		if !ok {
			return
		}

		af, ok := resolveArtwork(root, idx, r.PathValue("ratingKey"))
		if !ok {
			http.NotFound(w, r)
			return
		}

		images := nonNilImages(buildImages(h.opts.ExternalURL, af))
		writeJSON(w, http.StatusOK, imageContainerResponse{MediaContainer: ImageContainer{
			Offset:     0,
			TotalSize:  len(images),
			Identifier: root.identifier,
			Size:       len(images),
			Image:      images,
		}})
	}
}

// resolveArtwork looks ratingKey up in idx and returns the [artworkFor] it
// resolves to: a season's own resolves to its show's artwork (spec §D.5,
// "show's"); an episode resolves to an empty artworkFor, since
// EpisodeStatus has no artwork field ([buildEpisodeImages]'s own doc
// comment). false means ratingKey names nothing this index knows, or a type
// root does not declare ([rootDef.declares]).
func resolveArtwork(root rootDef, idx *projection.Index, ratingKey string) (artworkFor, bool) {
	uid, _, isSeason, ok := ParseRatingKey(ratingKey)
	if !ok {
		return artworkFor{}, false
	}
	if isSeason {
		if !root.declares(typeSeason) {
			return artworkFor{}, false
		}
		s, ok := idx.SeriesByUID(uid)
		if !ok {
			return artworkFor{}, false
		}
		return artworkFor{kind: commonv1.MediaKindSeries, uid: s.UID, artwork: s.Status.Artwork, overlay: s.Status.Overlay}, true
	}

	obj, ok := idx.ByUID(uid)
	if !ok || !root.declares(providerType(obj)) {
		return artworkFor{}, false
	}
	switch v := obj.(type) {
	case *catalogv1.Movie:
		return artworkFor{kind: commonv1.MediaKindMovie, uid: v.UID, artwork: v.Status.Artwork, overlay: v.Status.Overlay}, true
	case *catalogv1.Series:
		return artworkFor{kind: commonv1.MediaKindSeries, uid: v.UID, artwork: v.Status.Artwork, overlay: v.Status.Overlay}, true
	case *catalogv1.Episode:
		return artworkFor{kind: commonv1.MediaKindEpisode, uid: v.UID}, true
	default:
		return artworkFor{}, false
	}
}

// trimBase drops a trailing slash from externalURL, so
// "https://host/" + "/art/..." does not double up.
func trimBase(externalURL string) string {
	return strings.TrimSuffix(externalURL, "/")
}
