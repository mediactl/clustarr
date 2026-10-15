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
// {MediaInfo AudioCodec} renders Radarr's label for the probe's codec
// (AudioCodecLabel), not ffprobe's codec name: "EAC3", never "eac3".
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
		"{MediaInfo AudioCodec}":        "EAC3", // the default stream, not the first
		"{MediaInfo AudioChannels}":     "5.1",
		"{MediaInfo AudioLanguages}":    "[JA+EN]",
		"{MediaInfo SubtitleLanguages}": "[EN+ES]",
		"{MediaInfo Simple}":            "x265 EAC3",
		"{MediaInfo Full}":              "x265 EAC3 [JA+EN] [EN+ES]",
	} {
		got, err := e.Render(tmpl, c)
		require.NoError(t, err, tmpl)
		require.Equal(t, want, got, tmpl)
	}

	one := c
	one.MediaInfo.Audio = one.MediaInfo.Audio[1:]
	got, err := e.Render("{MediaInfo AudioLanguages}", one)
	require.NoError(t, err)
	require.Equal(t, "", got, "English alone is not stated, Sonarr's rule")
}

// TestAudioLanguagesAreSonarrs holds the language tokens to Sonarr's
// GetLanguagesToken: two-letter codes from the probe's ISO 639-2 tags, each
// language once, a filter after a colon, and a lone language left unstated
// only when it is the item's configured one (Sonarr's English) -- so a lone
// original-language track, or one that is neither, is always named. On the
// owner's anime library (2026-10-15) 231 of 509 files were Japanese alone,
// and every one rendered no block.
func TestAudioLanguagesAreSonarrs(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	for _, tc := range []struct {
		name, tmpl, configured string
		audio, subs            []string
		want                   string
	}{
		{"the original alone", "{MediaInfo AudioLanguages}", "en", []string{"jpn"}, nil, "[JA]"},
		{"neither original nor configured", "{MediaInfo AudioLanguages}", "en", []string{"spa"}, nil, "[ES]"},
		{"the configured language alone", "{MediaInfo AudioLanguages}", "en", []string{"eng"}, nil, ""},
		{"no configured language reads English", "{MediaInfo AudioLanguages}", "", []string{"eng"}, nil, ""},
		{"a configured French", "{MediaInfo AudioLanguages}", "fr", []string{"fre"}, nil, ""},
		{"English alone under a configured French", "{MediaInfo AudioLanguages}", "fr", []string{"eng"}, nil, "[EN]"},
		{"the configured language with another", "{MediaInfo AudioLanguages}", "en", []string{"eng", "jpn"}, nil, "[EN+JA]"},
		{"each language once", "{MediaInfo AudioLanguages}", "en", []string{"jpn", "jpn", "eng", "eng"}, nil, "[JA+EN]"},
		{"bibliographic and terminological codes are one", "{MediaInfo AudioLanguages}", "en", []string{"fre", "fra"}, nil, "[FR]"},
		{"a region is dropped", "{MediaInfo AudioLanguages}", "en", []string{"pt-BR"}, nil, "[PT]"},
		{"unknown tags are dropped", "{MediaInfo AudioLanguages}", "en", []string{"und", ""}, nil, ""},
		{"a code that names no language is kept", "{MediaInfo AudioLanguages}", "en", []string{"jpn", "qaa"}, nil, "[JA+QAA]"},
		{"excluded", "{MediaInfo AudioLanguages:-EN}", "en", []string{"eng", "jpn", "fre"}, nil, "[JA+FR]"},
		{"excluded down to the configured one", "{MediaInfo AudioLanguages:-JA-FR}", "en", []string{"eng", "jpn", "fre"}, nil, ""},
		{"included, in the filter's order", "{MediaInfo AudioLanguages:JA+EN}", "en", []string{"eng", "jpn", "fre"}, nil, "[JA+EN]"},
		{"a filter in lower case", "{MediaInfo AudioLanguages:ja}", "en", []string{"eng", "jpn"}, nil, "[JA]"},
		{"a wildcard for the rest", "{MediaInfo AudioLanguages:JA+}", "en", []string{"eng", "jpn"}, nil, "[JA+--]"},
		{"All states the configured language", "{MediaInfo AudioLanguagesAll}", "en", []string{"eng"}, nil, "[EN]"},
		{"subtitles never skip", "{MediaInfo SubtitleLanguages}", "en", nil, []string{"eng"}, "[EN]"},
		{"subtitles filtered", "{MediaInfo SubtitleLanguagesAll:-EN}", "en", nil, []string{"eng", "spa"}, "[ES]"},
		{"wrapped", "{ MediaInfo AudioLanguages:-EN}", "en", []string{"eng", "jpn"}, nil, " [JA]"},
	} {
		c := naming.Context{Language: tc.configured}
		for _, l := range tc.audio {
			c.MediaInfo.Audio = append(c.MediaInfo.Audio, commonv1.AudioStream{Language: l})
		}
		for _, l := range tc.subs {
			c.MediaInfo.Subtitles = append(c.MediaInfo.Subtitles, commonv1.SubtitleStream{Language: l})
		}
		got, err := e.Render(tc.tmpl, c)
		require.NoError(t, err, tc.name)
		require.Equal(t, tc.want, got, tc.name)
	}

	for _, tmpl := range []string{"{Release Group:-EN}", "{Movie Title:abc}"} {
		_, err := e.Render(tmpl, naming.Context{})
		require.ErrorIs(t, err, naming.ErrUnknownToken, "%s: only a language token takes a filter", tmpl)
	}
}

// TestTheTRaSHAnimeFormatRendersEveryField renders the owner's anime episode
// format (TRaSH's, as on the tv RootFolder) for a file Sonarr had named
// "Overlord (2015) - S03E08 - 034 - A Handful of Hope [Bluray-1080p
// v2][FLAC 2.0][JA][x264 10bit]-neko-kBaraka": Japanese alone, SDR, a
// second revision.
func TestTheTRaSHAnimeFormatRendersEveryField(t *testing.T) {
	const tmpl = "{Series CleanTitleWithoutYear} {(Series Year)} - S{season:00}E{episode:00} - {absolute:000} - " +
		"{Episode CleanTitle:90} {[Custom Formats]}{[Quality Full]}{[Mediainfo AudioCodec}{ Mediainfo AudioChannels]}" +
		"{MediaInfo AudioLanguages}{[MediaInfo VideoDynamicRangeType]}[{Mediainfo VideoCodec }{MediaInfo VideoBitDepth}bit]" +
		"{-Release Group}"
	c := naming.Context{
		Kind: commonv1.MediaKindEpisode, SeriesTitle: "Overlord", SeriesYear: 2015, Anime: true,
		Season: 3, Episodes: []int{8}, Absolute: []int{34}, EpisodeTitle: "A Handful of Hope",
		Quality:  commonv1.Quality{Source: commonv1.SourceBluray, Resolution: 1080},
		Revision: commonv1.Revision{Version: 2}, CustomFormats: []string{"v2"}, ReleaseGroup: "neko-kBaraka",
		ReleaseTitle: "[neko-kBaraka] Overlord III - 08 v2 [BD 1080p x264 10bit FLAC]", Language: "en",
		MediaInfo: commonv1.MediaInfo{
			VideoCodec: "h264", VideoBitDepth: 10, Hdr: commonv1.HdrFormatNone,
			Audio: []commonv1.AudioStream{{Codec: "flac", Channels: 2, Language: "jpn", Default: true}},
		},
	}
	e := naming.NewEngine(naming.Config{})
	got, err := e.Render(tmpl, c)
	require.NoError(t, err)
	require.Equal(t, "Overlord (2015) - S03E08 - 034 - A Handful of Hope [v2][Bluray-1080p v2][FLAC 2.0][JA][x264 10bit]-neko-kBaraka", got)

	c.MediaInfo.Audio = append(c.MediaInfo.Audio, commonv1.AudioStream{Codec: "flac", Channels: 2, Language: "eng"})
	c.MediaInfo.Hdr = commonv1.HdrFormatHDR10
	got, err = e.Render(tmpl, c)
	require.NoError(t, err)
	require.Equal(t, "Overlord (2015) - S03E08 - 034 - A Handful of Hope [v2][Bluray-1080p v2][FLAC 2.0][JA+EN][HDR10][x264 10bit]-neko-kBaraka", got)
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

// TestAudioCodecLabelIsRadarrs: every (codec, profile) pair the probe
// recorded across the owner's movie library (2026-09-29), against the
// label Radarr gave the same file's name. Radarr names HE-AAC plain "AAC".
// DTS:X and the rarer DTS profiles are Radarr's FormatAudioCodec, from
// ffprobe's own profile names.
func TestAudioCodecLabelIsRadarrs(t *testing.T) {
	for _, tc := range []struct{ codec, profile, want string }{
		{"aac", "LC", "AAC"},
		{"aac", "HE-AAC", "AAC"},
		{"ac3", "", "AC3"},
		{"dts", "DTS", "DTS"},
		{"dts", "DTS-HD MA", "DTS-HD MA"},
		{"dts", "DTS-HD MA + DTS:X", "DTS-X"},
		{"dts", "DTS-HD HRA", "DTS-HD HRA"},
		{"dts", "DTS-ES", "DTS-ES"},
		{"eac3", "", "EAC3"},
		{"eac3", "Dolby Digital Plus + Dolby Atmos", "EAC3 Atmos"},
		{"truehd", "", "TrueHD"},
		{"truehd", "Dolby TrueHD + Dolby Atmos", "TrueHD Atmos"},
		{"flac", "", "FLAC"},
		{"mp3", "", "MP3"},
		{"opus", "", "Opus"},
		{"vorbis", "", "Vorbis"},
		{"pcm_s16le", "", "PCM"},
		{"pcm_s24le", "", "PCM"},
		{"", "", ""},
	} {
		require.Equalf(t, tc.want, naming.AudioCodecLabel(tc.codec, tc.profile), "%s / %q", tc.codec, tc.profile)
	}

	e := naming.NewEngine(naming.Config{})
	c := naming.Context{MediaInfo: commonv1.MediaInfo{Audio: []commonv1.AudioStream{
		{Codec: "eac3", Profile: "Dolby Digital Plus + Dolby Atmos", Channels: 6, Default: true},
	}}}
	got, err := e.Render("{[MediaInfo AudioCodec}{ MediaInfo AudioChannels]}", c)
	require.NoError(t, err)
	require.Equal(t, "[EAC3 Atmos 5.1]", got)
}

// TestVideoDynamicRangeTypeIsRadarrs: Radarr writes HDR10+ as "HDR10Plus"
// (the owner's library, 2026-09-29), and its probe calls no stream below
// 10 bits HDR (VideoFileInfoReader.GetHdrFormat): an 8-bit h264 file whose
// transfer reads PQ was named without a range, and stays so.
func TestVideoDynamicRangeTypeIsRadarrs(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	for _, tc := range []struct {
		hdr   commonv1.HdrFormat
		depth int32
		want  string
	}{
		{commonv1.HdrFormatHDR10Plus, 10, "[HDR10Plus]"},
		{commonv1.HdrFormatDolbyVisionHDR10Plus, 10, "[DV HDR10Plus]"},
		{commonv1.HdrFormatDolbyVisionHDR10, 10, "[DV HDR10]"},
		{commonv1.HdrFormatHDR10, 10, "[HDR10]"},
		{commonv1.HdrFormatHDR10, 8, ""},
		{commonv1.HdrFormatHDR10, 0, "[HDR10]"}, // an unmeasured depth is not a judgement
	} {
		c := naming.Context{MediaInfo: commonv1.MediaInfo{Hdr: tc.hdr, VideoBitDepth: tc.depth}}
		got, err := e.Render("{[MediaInfo VideoDynamicRangeType]}", c)
		require.NoError(t, err)
		require.Equalf(t, tc.want, got, "%s at %d bits", tc.hdr, tc.depth)
	}
}
