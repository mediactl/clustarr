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

package fileimport

import (
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/quality"
)

// replacesWrongLanguage reports whether an incoming file may replace
// existing although it is no quality upgrade: existing's probed audio lacks
// the language profile p wants (decision.LacksLanguage, anime dual-audio
// spec §5.3) and the incoming file's own probe, taken before this gate,
// carries it. Judging the new file by its audio, not its release title,
// keeps a wrong-language file from being swapped for another one.
func replacesWrongLanguage(p quality.Profile, originalTag string, existing *catalogv1alpha1.MediaFile, incoming *commonv1.MediaInfo) bool {
	have := decision.AudioLanguages(existing.Status.MediaInfo)
	if !decision.LacksLanguage(p, originalTag, have) {
		return false
	}
	got := decision.AudioLanguages(incoming)
	return got != nil && !decision.LacksLanguage(p, originalTag, got)
}
