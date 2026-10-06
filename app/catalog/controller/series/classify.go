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

package series

import (
	"strings"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// animeGenre is TheTVDB's genre for anime (19 of 150 series on the owner's
// library, 2026-10-06).
const animeGenre = "Anime"

// Classify is the one-time anime detection
// (docs/superpowers/specs/2026-10-06-anime-dual-audio-design.md §4). It
// returns nil when there is nothing to record yet -- classification is off,
// already done, the metadata has not arrived, or the RootFolder sets no
// anime defaults (so setting them later still classifies) -- and otherwise
// the classification to record, with patch true when s's spec must take the
// anime profile and type. AppliedAt is the caller's to set.
func Classify(s *catalogv1alpha1.Series, rf *catalogv1alpha1.RootFolder) (*catalogv1alpha1.SeriesClassification, bool) {
	if s.Annotations[catalogv1alpha1.AnnotationClassify] == "off" || s.Status.Classification != nil ||
		s.Status.Metadata == nil || rf == nil || rf.Spec.Defaults.Anime == nil {
		return nil, false
	}
	anime := false
	for _, g := range s.Status.Metadata.Genres {
		if strings.EqualFold(g, animeGenre) {
			anime = true
			break
		}
	}
	if !anime {
		return &catalogv1alpha1.SeriesClassification{Anime: false}, false
	}
	d := rf.Spec.Defaults.Anime
	st := d.SeriesType
	if st == "" {
		st = catalogv1alpha1.SeriesTypeAnime
	}
	return &catalogv1alpha1.SeriesClassification{Anime: true, QualityProfileRef: d.QualityProfileRef, SeriesType: st}, true
}
