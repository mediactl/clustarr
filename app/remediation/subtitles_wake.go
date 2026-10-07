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

package remediation

import (
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// subtitleProfileSignature is S1”s ProfileSignature (loop spec §3.3): the
// file's winning SubtitleProfile and its subtitles phase, the two inputs of
// the slim SubtitleProfile reconciler's counts.
func subtitleProfileSignature(o client.Object) string {
	mf, ok := o.(*catalogv1alpha1.MediaFile)
	if !ok || mf.Status.Subtitles == nil {
		return ""
	}
	s := mf.Status.Subtitles
	return s.Profile + "/" + string(s.Phase)
}
