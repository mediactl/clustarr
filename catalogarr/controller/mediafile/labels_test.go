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
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

func TestMirrorLabels(t *testing.T) {
	q := commonv1.Quality{Name: "Bluray-1080p", Source: commonv1.SourceBluray, Resolution: commonv1.Resolution1080p, Modifier: commonv1.ModifierNone}
	mi := &commonv1.MediaInfo{VideoCodec: "hevc", Hdr: commonv1.HdrFormatHDR10}

	got := mirrorLabels(commonv1.MediaKindMovie, q, mi, true)

	assert.Equal(t, "movie", got[catalogv1alpha1.LabelKind])
	assert.Equal(t, "1080", got[catalogv1alpha1.LabelResolution])
	assert.Equal(t, "bluray", got[catalogv1alpha1.LabelSource])
	assert.Equal(t, "none", got[catalogv1alpha1.LabelModifier])
	assert.Equal(t, "hevc", got[catalogv1alpha1.LabelVideoCodec])
	assert.Equal(t, "hdr10", got[catalogv1alpha1.LabelHdr])
	assert.Equal(t, "true", got[catalogv1alpha1.LabelOriginal])
}

func TestMirrorLabelsNilMediaInfoOmitsCodecAndHdr(t *testing.T) {
	got := mirrorLabels(commonv1.MediaKindEpisode, commonv1.Quality{}, nil, false)
	assert.Equal(t, "episode", got[catalogv1alpha1.LabelKind])
	assert.Equal(t, "false", got[catalogv1alpha1.LabelOriginal])
	_, ok := got[catalogv1alpha1.LabelVideoCodec]
	assert.False(t, ok)
}
