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
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
)

// ProbedAudioLanguages is decision.AudioLanguages over mf's probe: its
// audio languages as canonical BCP-47 tags, or nil when not all are known.
func ProbedAudioLanguages(mf *catalogv1alpha1.MediaFile) []string {
	if mf == nil {
		return nil
	}
	return decision.AudioLanguages(mf.Status.MediaInfo)
}

// AudioLanguagesObject is ProbedAudioLanguages as a watch key, for
// k8s.StatusFieldChanged. The Movie and Episode controllers wake on its
// change because a probe writes status.mediaInfo in a status write that
// bumps no generation, and WrongLanguage is read from it.
func AudioLanguagesObject(o client.Object) string {
	mf, ok := o.(*catalogv1alpha1.MediaFile)
	if !ok {
		return ""
	}
	return strings.Join(ProbedAudioLanguages(mf), ",")
}
