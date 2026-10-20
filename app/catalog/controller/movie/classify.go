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

package movie

import (
	"strings"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/lang"
)

// TMDB has no anime genre: a film is anime when a genre names animation and
// Japanese is its original language (20 of the owner's 840 films on
// 2026-10-20, every one of them anime). "Anime" is honoured too, for a
// document another provider wrote.
var animeGenres = []string{"Animation", "Anime"}

// Classify is the Movie's one-time anime detection, series.Classify's
// counterpart (docs/superpowers/specs/2026-10-06-anime-dual-audio-design.md
// §4). It returns nil when there is nothing to record yet -- classification
// is off, already done, the metadata has not arrived, or the RootFolder sets
// no anime defaults (so setting them later still classifies) -- and
// otherwise the classification to record, with patch true when m's spec must
// take the anime profile. AppliedAt is the caller's to set.
func Classify(m *catalogv1alpha1.Movie, rf *catalogv1alpha1.RootFolder) (*catalogv1alpha1.MovieClassification, bool) {
	if m.Annotations[catalogv1alpha1.AnnotationClassify] == "off" || m.Status.Classification != nil ||
		m.Status.Metadata == nil || rf == nil || rf.Spec.Defaults.Anime == nil {
		return nil, false
	}
	if !isAnime(m.Status.Metadata) {
		return &catalogv1alpha1.MovieClassification{Anime: false}, false
	}
	ref := rf.Spec.Defaults.Anime.QualityProfileRef
	return &catalogv1alpha1.MovieClassification{Anime: true, QualityProfileRef: ref}, m.Spec.QualityProfileRef != ref
}

// isAnime reports whether meta names a Japanese animated film.
func isAnime(meta *catalogv1alpha1.MovieMetadata) bool {
	tag, ok := lang.Normalize(meta.OriginalLanguage)
	if base, _, _ := strings.Cut(string(tag), "-"); !ok || base != "ja" {
		return false
	}
	for _, g := range append(append([]string(nil), meta.Genres...), meta.OriginalGenres...) {
		for _, a := range animeGenres {
			if strings.EqualFold(strings.TrimSpace(g), a) {
				return true
			}
		}
	}
	return false
}
