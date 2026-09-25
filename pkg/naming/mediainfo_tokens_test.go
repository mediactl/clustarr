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

package naming_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/naming"
)

// TestMediaInfoTokensRenderFromTheProbe covers the six new {MediaInfo ...}
// tokens (VideoCodec, VideoBitDepth, AudioLanguages, SubtitleLanguages,
// Simple, Full) plus AudioCodec/AudioChannels' default-stream selection.
//
// {MediaInfo AudioCodec} renders the stream's Codec field verbatim, the
// same passthrough render_test.go's TestRenderSplitBracketAcrossTwoAdjacentTokens
// already pins (it happens to feed an already-upper-case "EAC3", which
// cannot distinguish passthrough from upper-casing); this test's fixture
// uses a lower-case "eac3" to tell them apart, so the expectation here is
// lower-case too, per the existing token's real, unchanged behaviour.
func TestMediaInfoTokensRenderFromTheProbe(t *testing.T) {
	mi := commonv1.MediaInfo{
		VideoCodec: "hevc", VideoBitDepth: 10, Hdr: commonv1.HdrFormatHDR10,
		Audio: []commonv1.AudioStream{
			{Codec: "aac", Channels: 2, Language: "ja"},
			{Codec: "eac3", Channels: 6, Language: "en", Default: true},
		},
		Subtitles: []commonv1.SubtitleStream{{Language: "en"}, {Language: "es"}},
	}
	c := naming.Context{Kind: commonv1.MediaKindMovie, Title: "Akira", Year: 1988, MediaInfo: mi, ReleaseTitle: "Akira.1988.2160p.x265-GRP"}
	e := naming.NewEngine(naming.Config{})
	for tmpl, want := range map[string]string{
		"{MediaInfo VideoCodec}":        "x265",
		"{MediaInfo VideoBitDepth}":     "10",
		"{MediaInfo AudioCodec}":        "eac3", // the default stream, not the first
		"{MediaInfo AudioChannels}":     "5.1",
		"{MediaInfo AudioLanguages}":    "[JA+EN]",
		"{MediaInfo SubtitleLanguages}": "[EN+ES]",
		"{MediaInfo Simple}":            "x265 eac3",
		"{MediaInfo Full}":              "x265 eac3 [JA+EN] [EN+ES]",
	} {
		got, err := e.Render(tmpl, c)
		require.NoError(t, err, tmpl)
		require.Equal(t, want, got, tmpl)
	}

	one := c
	one.MediaInfo.Audio = one.MediaInfo.Audio[1:]
	got, err := e.Render("{MediaInfo AudioLanguages}", one)
	require.NoError(t, err)
	require.Equal(t, "", got, "a single language is not stated, Radarr's rule")
}

// TestVideoPresetsStateTheCodec is Step 1's second brief test: the movie
// file preset gains the dynamic-range and codec blocks after {Quality
// Full}, and a file that was never probed renders exactly as before.
func TestVideoPresetsStateTheCodec(t *testing.T) {
	c := naming.Context{
		Kind: commonv1.MediaKindMovie, Title: "Akira", Year: 1988,
		Quality:   commonv1.Quality{Source: commonv1.SourceBluray, Resolution: 1080},
		MediaInfo: commonv1.MediaInfo{VideoCodec: "hevc", Hdr: commonv1.HdrFormatHDR10},
	}
	got, err := naming.NewEngine(naming.Config{}).MovieFile(c)
	require.NoError(t, err)
	require.Equal(t, "Akira (1988) - [Bluray-1080p] [HDR10] [h265]", got)

	c.MediaInfo = commonv1.MediaInfo{}
	got, err = naming.NewEngine(naming.Config{}).MovieFile(c)
	require.NoError(t, err)
	require.Equal(t, "Akira (1988) - [Bluray-1080p]", got, "a file never probed renders as before")
}
