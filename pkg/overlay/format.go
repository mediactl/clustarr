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

package overlay

import (
	"fmt"
	"strconv"
)

// Rating source identifiers, mirroring api/catalog/v1alpha1.RatingSource's
// string values (shared_types.go's RatingSourceIMDb etc.) exactly. Badge.Source
// and FormatScore's source parameter are plain strings rather than
// catalogv1alpha1.RatingSource -- the task brief's interface -- so this
// package stays importable without api/ outside template.go's TemplateSpec; a
// caller passes string(catalogv1alpha1.RatingSourceIMDb) or one of these
// constants directly.
const (
	SourceIMDb       = "imdb"
	SourceTMDB       = "tmdb"
	SourceRTCritic   = "rottenTomatoesCritic"
	SourceRTAudience = "rottenTomatoesAudience"
	SourceMetacritic = "metacritic"
	SourceTrakt      = "trakt"
	SourceLetterboxd = "letterboxd"
)

// FormatScore renders centis -- a Rating.ValueCentis, the score scaled by
// 100 (api/catalog/v1alpha1/shared_types.go) -- as the text a badge shows
// for source, and reports whether there is a score to show at all: ok is
// false when centis is zero, matching spec §C.6 step 4's "a badge whose
// source has no rating is omitted".
//
// metacritic, rottenTomatoesCritic and rottenTomatoesAudience are 0-100
// scores (ValueCentis 0-10000 in steps of 100): FormatScore renders the
// whole number, "53" for 5300. Every other source -- imdb, tmdb, trakt,
// letterboxd, and any source this package does not otherwise recognize -- is
// treated as a 0-10 score (ValueCentis 0-1000) rendered with one decimal
// digit, truncated rather than rounded: "7.2" for 720, and "7.2" for 729 too.
func FormatScore(source string, centis int32) (string, bool) {
	if centis == 0 {
		return "", false
	}
	switch source {
	case SourceMetacritic, SourceRTCritic, SourceRTAudience:
		return strconv.Itoa(int(centis / 100)), true
	default:
		return fmt.Sprintf("%d.%d", centis/100, (centis%100)/10), true
	}
}
