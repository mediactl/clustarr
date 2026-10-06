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

package rollup

import (
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/lang"
)

// ProbedAudioLanguages returns mf's audio track languages as canonical
// BCP-47 tags in stream order, deduplicated, or nil when they are not all
// known: not probed, no audio stream, or any track untagged ("und", "" or
// unparseable). One unknown track makes the whole set unknown, since that
// track may be the wanted language (decision.LacksLanguage never fires on
// nil).
func ProbedAudioLanguages(mf *catalogv1alpha1.MediaFile) []string {
	if mf == nil || mf.Status.MediaInfo == nil || len(mf.Status.MediaInfo.Audio) == 0 {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, a := range mf.Status.MediaInfo.Audio {
		t, ok := lang.Normalize(a.Language)
		if !ok {
			return nil
		}
		if !seen[string(t)] {
			seen[string(t)] = true
			out = append(out, string(t))
		}
	}
	return out
}
