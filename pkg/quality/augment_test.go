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

	unknown := commonv1.Quality{Name: "Unknown", Source: commonv1.SourceUnknown}
	q, changed = quality.AugmentFromMediaInfo(unknown, mi)
	require.False(t, changed, "the table defines no quality for an unknown source at any resolution, so the name's stays")
	require.Equal(t, unknown, q, "the probe never supplies a source")

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

// TestAugmentFromMediaInfoPlacesAProbedResolutionOnADefinedQuality is ruling
// R10, Radarr's QualityFinder.FindBySourceAndResolution: a probed
// resolution the source's ladder has no rung for lands on the source's
// resolution-less quality, else its nearest rung at or below, else the
// name's quality stands -- never "Unknown", which no profile tier holds.
func TestAugmentFromMediaInfoPlacesAProbedResolutionOnADefinedQuality(t *testing.T) {
	named := func(name string, src commonv1.Source, res int32, mod commonv1.Modifier) commonv1.Quality {
		return commonv1.Quality{Name: name, Source: src, Resolution: res, Modifier: mod}
	}
	dvd := named("DVD", commonv1.SourceDVD, commonv1.ResolutionUnknown, commonv1.ModifierNone)
	for _, tc := range []struct {
		name          string
		in            commonv1.Quality
		width, height int32
		want          commonv1.Quality
		changed       bool
	}{
		{"a DVD rip at 720x576 stays DVD", dvd, 720, 576, dvd, false},
		{"a DVD rip at 704x400 stays DVD", dvd, 704, 400, dvd, false},
		{
			"a 320x240 Bluray encode is Bluray-480p, the ladder's floor",
			named("Bluray-2160p", commonv1.SourceBluray, commonv1.Resolution2160p, commonv1.ModifierNone), 320, 240,
			named("Bluray-480p", commonv1.SourceBluray, commonv1.Resolution480p, commonv1.ModifierNone), true,
		},
		{
			"a 960x540 Bluray encode takes the rung below, Bluray-480p",
			named("Bluray-1080p", commonv1.SourceBluray, commonv1.Resolution1080p, commonv1.ModifierNone), 960, 540,
			named("Bluray-480p", commonv1.SourceBluray, commonv1.Resolution480p, commonv1.ModifierNone), true,
		},
		{
			"a 1280x720 WEB-DL named 1080p is WEBDL-720p",
			named("WEBDL-1080p", commonv1.SourceWebDL, commonv1.Resolution1080p, commonv1.ModifierNone), 1280, 720,
			named("WEBDL-720p", commonv1.SourceWebDL, commonv1.Resolution720p, commonv1.ModifierNone), true,
		},
		{
			"a 576-line WEB-DL takes the rung below, WEBDL-480p, not 720p",
			named("WEBDL-1080p", commonv1.SourceWebDL, commonv1.Resolution1080p, commonv1.ModifierNone), 720, 576,
			named("WEBDL-480p", commonv1.SourceWebDL, commonv1.Resolution480p, commonv1.ModifierNone), true,
		},
		{
			"a remux with no rung at or below keeps the name's quality",
			named("Remux-1080p", commonv1.SourceBluray, commonv1.Resolution1080p, commonv1.ModifierRemux), 1280, 720,
			named("Remux-1080p", commonv1.SourceBluray, commonv1.Resolution1080p, commonv1.ModifierRemux), false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, changed := quality.AugmentFromMediaInfo(tc.in, &commonv1.MediaInfo{Width: tc.width, Height: tc.height})
			require.Equal(t, tc.want, got)
			require.Equal(t, tc.changed, changed)
			require.NotEqual(t, "Unknown", got.Name)
		})
	}
}
