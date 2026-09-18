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

package release

import (
	"github.com/dlclark/regexp2"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// episodeTokenRegex is the minimal movie-vs-episode heuristic: an SxxEyy or
// season-only token anywhere in the title.
var episodeTokenRegex = mustCompile(`\bS\d{1,2}(?:E\d{1,4})?\b`, regexp2.IgnoreCase)

// ClassifyKind guesses the MediaKind of a release title when the caller
// doesn't already know it. It is a heuristic, not a parse: Parse still runs
// the real per-kind regex family and returns an error if the guess was
// wrong for the actual title shape.
func ClassifyKind(title string) commonv1.MediaKind {
	if ok, err := episodeTokenRegex.MatchString(title); err == nil && ok {
		return commonv1.MediaKindEpisode
	}
	return commonv1.MediaKindMovie
}
