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

package mediafile

import (
	"strconv"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// mirrorLabels builds the catalog.clustarr.io/* label set MediaFile.status's
// doc comment promises ("controller-mirrored labels: kind, resolution,
// source, modifier, video-codec, hdr, original"). q is spec.quality (frozen
// at import, never this controller's to change); mi is the current probe,
// nil before the first successful probe.
func mirrorLabels(kind commonv1.MediaKind, q commonv1.Quality, mi *commonv1.MediaInfo, original bool) map[string]string {
	labels := map[string]string{
		catalogv1alpha1.LabelKind:     string(kind),
		catalogv1alpha1.LabelOriginal: strconv.FormatBool(original),
	}
	if q.Resolution != 0 {
		labels[catalogv1alpha1.LabelResolution] = strconv.Itoa(int(q.Resolution))
	}
	if q.Source != "" {
		labels[catalogv1alpha1.LabelSource] = string(q.Source)
	}
	if q.Modifier != "" {
		labels[catalogv1alpha1.LabelModifier] = string(q.Modifier)
	}
	if mi != nil {
		if mi.VideoCodec != "" {
			labels[catalogv1alpha1.LabelVideoCodec] = mi.VideoCodec
		}
		labels[catalogv1alpha1.LabelHdr] = string(mi.Hdr)
	}
	return labels
}
