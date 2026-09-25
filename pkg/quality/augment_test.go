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

package quality_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
)

func TestAugmentFromMediaInfoCorrectsResolutionAndKeepsSource(t *testing.T) {
	mi := &commonv1.MediaInfo{Width: 1920, Height: 1080, VideoCodec: "hevc", VideoBitrateKbps: 4000}

	q, changed := quality.AugmentFromMediaInfo(commonv1.Quality{Name: "Bluray-2160p", Source: commonv1.SourceBluray, Resolution: 2160}, mi)
	require.True(t, changed)
	require.Equal(t, commonv1.Quality{Name: "Bluray-1080p", Source: commonv1.SourceBluray, Resolution: 1080}, q)

	q, changed = quality.AugmentFromMediaInfo(commonv1.Quality{Name: "Unknown", Source: commonv1.SourceUnknown}, mi)
	require.True(t, changed)
	require.Equal(t, int32(1080), q.Resolution)
	require.Equal(t, commonv1.SourceUnknown, q.Source, "the probe never supplies a source")

	q, changed = quality.AugmentFromMediaInfo(commonv1.Quality{Name: "Remux-1080p", Source: commonv1.SourceBluray, Resolution: 1080, Modifier: commonv1.ModifierRemux}, mi)
	require.True(t, changed)
	require.Equal(t, commonv1.ModifierNone, q.Modifier, "4 Mbit/s HEVC is not a remux")
	require.Equal(t, "Bluray-1080p", q.Name)

	same, changed := quality.AugmentFromMediaInfo(commonv1.Quality{Name: "Bluray-1080p", Source: commonv1.SourceBluray, Resolution: 1080}, mi)
	require.False(t, changed)
	require.Equal(t, "Bluray-1080p", same.Name)

	_, changed = quality.AugmentFromMediaInfo(commonv1.Quality{Name: "Bluray-1080p", Source: commonv1.SourceBluray, Resolution: 1080}, nil)
	require.False(t, changed, "no probe, no change")
}
