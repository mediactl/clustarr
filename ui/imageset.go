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

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/imageset"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
)

// imageSetMaxBytes bounds the YAML one Image Set post carries: JoJo's two
// hundred episodes are a few tens of kilobytes.
const imageSetMaxBytes = 1 << 20

// handleImageSet is the item page's Image Set dialog
// (docs/superpowers/specs/2026-10-21-image-sets-design.md): POST
// /library/{namespace}/{kind}/{name}/imageset with "yaml", a MediUX or
// ThePosterDB set in Kometa's image-files format. The entry for the item's
// own provider id (the set's only entry when it names another) becomes the
// item's spec.artwork and, for a series, spec.seasonArtwork through
// Options.Actions.SetArtwork, the item's existing overrides of types the
// set leaves alone kept, since a merge patch replaces each list whole.
func (s *Server) handleImageSet(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, imageSetMaxBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	ns, name := r.PathValue("namespace"), r.PathValue("name")
	kind := commonv1.MediaKind(r.PathValue("kind"))
	item, ok := findLibraryItem(s.opts.Library(r.Context()), ns, string(kind), name)
	if !ok {
		http.NotFound(w, r)
		return
	}
	entries, err := imageset.Parse(r.FormValue("yaml"))
	if err != nil {
		s.finishAction(w, r, fmt.Errorf("%w: %w", actions.ErrInvalid, err))
		return
	}
	entry, ok := imageset.For(entries, item.ProviderID)
	if !ok {
		s.finishAction(w, r, fmt.Errorf("%w: the set names %d items and none is this one (%s)", actions.ErrInvalid, len(entries), item.ProviderID))
		return
	}
	artwork, seasonArtwork := s.currentOverrides(r, item)
	artwork = mergeArtwork(artwork, entry.Artwork)
	if kind == commonv1.MediaKindSeries {
		seasonArtwork = mergeSeasonArtwork(seasonArtwork, entry.SeasonArtwork)
	} else {
		seasonArtwork = nil
	}
	_, err = s.opts.Actions.SetArtwork(r.Context(), ns, kind, name, artwork, seasonArtwork)
	s.finishAction(w, r, err)
}

// currentOverrides reads the item's spec.artwork and spec.seasonArtwork, so
// the set's merge keeps what it does not replace; none when the reader
// cannot give them.
func (s *Server) currentOverrides(r *http.Request, item projection.LibraryItem) ([]catalogv1.ArtworkOverride, []catalogv1.SeasonArtworkOverride) {
	switch item.Kind {
	case commonv1.MediaKindMovie:
		var m catalogv1.Movie
		if s.getObject(r.Context(), item.Ref, &m) {
			return m.Spec.Artwork, nil
		}
	case commonv1.MediaKindSeries:
		var sr catalogv1.Series
		if s.getObject(r.Context(), item.Ref, &sr) {
			return sr.Spec.Artwork, sr.Spec.SeasonArtwork
		}
	}
	return nil, nil
}

// mergeArtwork is current with set's entries replacing those of the same
// type, set's order first.
func mergeArtwork(current, set []catalogv1.ArtworkOverride) []catalogv1.ArtworkOverride {
	out := append([]catalogv1.ArtworkOverride(nil), set...)
	for _, c := range current {
		replaced := false
		for _, n := range set {
			if n.Type == c.Type {
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, c)
		}
	}
	return out
}

// mergeSeasonArtwork is current with set's entries replacing those of the
// same season, episode and type.
func mergeSeasonArtwork(current, set []catalogv1.SeasonArtworkOverride) []catalogv1.SeasonArtworkOverride {
	out := append([]catalogv1.SeasonArtworkOverride(nil), set...)
	for _, c := range current {
		replaced := false
		for _, n := range set {
			if sameSeasonImage(n, c) {
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, c)
		}
	}
	return out
}

func sameSeasonImage(a, b catalogv1.SeasonArtworkOverride) bool {
	if a.Season != b.Season || a.Type != b.Type || (a.Episode == nil) != (b.Episode == nil) {
		return false
	}
	return a.Episode == nil || *a.Episode == *b.Episode
}
