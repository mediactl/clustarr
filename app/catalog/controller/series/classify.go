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
	"errors"
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
//
// Under an anime root (RootFolderSpec.AnimeRoot) nothing is detected: what
// is stored there is anime by the rule that the root folder decides (anime
// library design, 2026-11-25, §1), and is recorded so with no patch. That is
// also what keeps the record derivable for a series the classification
// routed there (AnimeRoute): its spec patch and its status record are two
// writes, and a pass that reads the series between them -- already under
// the anime root, not yet recorded -- records the same thing, where it
// would otherwise find no anime defaults, record nothing and release it.
func Classify(s *catalogv1alpha1.Series, rf *catalogv1alpha1.RootFolder) (*catalogv1alpha1.SeriesClassification, bool) {
	if s.Annotations[catalogv1alpha1.AnnotationClassify] == "off" || s.Status.Classification != nil ||
		s.Status.Metadata == nil || rf == nil {
		return nil, false
	}
	if rf.Spec.AnimeRoot() {
		return &catalogv1alpha1.SeriesClassification{Anime: true}, false
	}
	if rf.Spec.Defaults.Anime == nil {
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

// ErrAnimeRoot is an anime that cannot be routed: its root folder's anime
// defaults name a root folder that is missing, or is no anime root.
var ErrAnimeRoot = errors.New("series: the anime root folder is missing or is no anime root")

// AnimeRoute is where an anime classified under rf is stored (anime library
// design, 2026-11-25, §2): "" when rf's anime defaults route it nowhere, so
// it stays under rf, else the root folder they name. dest is that root
// folder, nil when it does not exist; one that is missing, or is no anime
// root (RootFolderSpec.AnimeRoot), is ErrAnimeRoot, and the caller records
// no classification: recorded without the move, the series would stay in TV
// Shows for good, a classification being once only.
func AnimeRoute(rf, dest *catalogv1alpha1.RootFolder) (string, error) {
	if rf == nil || rf.Spec.Defaults.Anime == nil || rf.Spec.Defaults.Anime.RootFolderRef == "" {
		return "", nil
	}
	if dest == nil || !dest.Spec.AnimeRoot() {
		return "", ErrAnimeRoot
	}
	return dest.Name, nil
}
