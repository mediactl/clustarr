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

package subtitlerequest

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

func keysOf(ex []subtitlev1alpha1.ExistingSub) []string {
	out := make([]string, len(ex))
	for i, e := range ex {
		out[i] = e.LangKey
	}
	return out
}

// TestISO6392StreamLanguagesCountAsExisting is the bug class this project has
// hit three times: ffprobe says "eng" and "fre", the profile says "en" and
// "fr", and subtitles.Plan compares strings. Every stream, sidecar segment
// and audio track here is ISO 639-2; the only wanted language must be the one
// genuinely absent.
func TestISO6392StreamLanguagesCountAsExisting(t *testing.T) {
	mi := &commonv1alpha1.MediaInfo{
		Audio: []commonv1alpha1.AudioStream{{Index: 1, Language: "eng"}},
		Subtitles: []commonv1alpha1.SubtitleStream{
			{Index: 2, Codec: "subrip", Language: "eng"},
			{Index: 3, Codec: "subrip", Language: "fre", Forced: true},
		},
	}
	spec := subtitlev1alpha1.SubtitleProfileSpec{
		Languages: []subtitlev1alpha1.LanguageItem{
			{Key: "en", Language: "en", HI: subtitlev1alpha1.HIPolicyPrefer},
			{Key: "fr:forced", Language: "fr", Forced: true, HI: subtitlev1alpha1.HIPolicyPrefer},
			{Key: "de", Language: "de", HI: subtitlev1alpha1.HIPolicyPrefer},
			{Key: "es", Language: "es", HI: subtitlev1alpha1.HIPolicyPrefer},
			{Key: "it", Language: "it", HI: subtitlev1alpha1.HIPolicyPrefer},
		},
		Cutoff: ptr.To("de"),
	}
	// "it" is satisfied by a sidecar tagged with its ISO 639-2 code.
	names := []string{"Movie.mkv", "Movie.ita.srt"}

	pp, unknown, langs := plannerProfile(spec, nil)
	require.Empty(t, unknown)
	existing := buildExisting(mi, subtitlev1alpha1.EmbeddedSpec{}, "Movie.mkv", names, langs)
	assert.Equal(t, []string{"en", "fr:forced", "it"}, keysOf(existing))

	wanted, cutoffMet := subtitles.Plan(pp, audioLanguages(mi), existingKeys(existing))
	assert.False(t, cutoffMet)
	assert.Equal(t, []subtitles.LangKey{"de", "es"}, wanted,
		"an eng/fre stream or an .ita. sidecar must satisfy en/fr/it; only de and es are genuinely missing")
}

// TestISO6392AudioTripsAudioExclude covers the other half of the planner's
// input: audioExclude compares the profile's language to the audio tracks'.
func TestISO6392AudioTripsAudioExclude(t *testing.T) {
	mi := &commonv1alpha1.MediaInfo{Audio: []commonv1alpha1.AudioStream{{Language: "fre"}, {Language: "und"}}}
	spec := subtitlev1alpha1.SubtitleProfileSpec{Languages: []subtitlev1alpha1.LanguageItem{
		{Key: "fr", Language: "fr", HI: subtitlev1alpha1.HIPolicyPrefer, AudioExclude: true},
		{Key: "en", Language: "en", HI: subtitlev1alpha1.HIPolicyPrefer, AudioOnlyInclude: true},
		{Key: "de", Language: "de", HI: subtitlev1alpha1.HIPolicyPrefer},
	}, Cutoff: ptr.To("de")}
	pp, _, _ := plannerProfile(spec, nil)
	assert.Equal(t, []string{"fr"}, audioLanguages(mi), "und is dropped, fre becomes fr")
	wanted, _ := subtitles.Plan(pp, audioLanguages(mi), nil)
	assert.Equal(t, []subtitles.LangKey{"de"}, wanted,
		"French audio must exclude fr, and no English audio must drop the audioOnlyInclude en")
}

func TestEmbeddedPolicy(t *testing.T) {
	streams := []commonv1alpha1.SubtitleStream{
		{Index: 2, Codec: "hdmv_pgs_subtitle", Language: "eng", Bitmap: true},
		{Index: 3, Codec: "dvd_subtitle", Language: "fre", Bitmap: true},
		{Index: 4, Codec: "ass", Language: "ger"},
		{Index: 5, Codec: "ssa", Language: "spa"},
		{Index: 6, Codec: "subrip", Language: "ita", Title: "Director's Commentary"},
		{Index: 7, Codec: "subrip", Language: "und"},
		{Index: 8, Codec: "subrip", Language: ""},
		{Index: 9, Codec: "subrip", Language: "jpn", Forced: true, HearingImpaired: true},
		{Index: 10, Codec: "subrip", Language: "por", HearingImpaired: true},
	}
	mi := &commonv1alpha1.MediaInfo{Subtitles: streams}

	tests := []struct {
		name   string
		policy subtitlev1alpha1.EmbeddedSpec
		want   []string
	}{
		{
			name: "nothing ignored: every resolvable stream counts, bitmap included",
			want: []string{"en", "fr", "de", "es", "it", "ja:forced", "pt:hi"},
		},
		{
			name:   "every knob on",
			policy: subtitlev1alpha1.EmbeddedSpec{IgnorePGS: true, IgnoreVobSub: true, IgnoreASS: true, SkipCommentary: true},
			want:   []string{"ja:forced", "pt:hi"},
		},
		{
			name:   "ignorePGS is by codec, not by the bitmap flag",
			policy: subtitlev1alpha1.EmbeddedSpec{IgnorePGS: true},
			want:   []string{"fr", "de", "es", "it", "ja:forced", "pt:hi"},
		},
		{
			name:   "ignoreASS covers ssa",
			policy: subtitlev1alpha1.EmbeddedSpec{IgnoreASS: true},
			want:   []string{"en", "fr", "it", "ja:forced", "pt:hi"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := embeddedExisting(mi, tc.policy, "Movie.mkv")
			assert.Equal(t, tc.want, keysOf(got))
			for _, e := range got {
				assert.Equal(t, subtitlev1alpha1.SubtitleSourceEmbedded, e.Source)
				assert.Equal(t, "Movie.mkv", e.Path, "an embedded track's path is the media file itself")
				assert.NotNil(t, e.StreamIndex)
			}
		})
	}
}

func TestSidecarExisting(t *testing.T) {
	names := []string{
		"Movie.mkv",
		"Movie.eng.srt",
		"Movie.fre.forced.srt",
		"Movie.pt_BR.sdh.ass",
		"Movie.srt",       // no language: never guessed
		"Movie.qaa.srt",   // private-use code: no language this controller can name
		"Movie.1080p.srt", // not shaped like a language
		"Other.en.srt",    // another video's sidecar
		"Movie.en.nfo",    // not a subtitle
	}
	got := sidecarExisting(names, "Movie.mkv")
	require.Equal(t, []string{"en", "fr:forced", "pt-BR:hi"}, keysOf(got))
	for _, e := range got {
		assert.Equal(t, subtitlev1alpha1.SubtitleSourceSidecar, e.Source)
		assert.Nil(t, e.StreamIndex)
	}
	assert.Equal(t, "Movie.eng.srt", got[0].Path, "a sidecar's path is relative to the media file's directory")
}

func TestBuildExistingFiltersToTheProfileAndCaps(t *testing.T) {
	var streams []commonv1alpha1.SubtitleStream
	for i := range 80 {
		streams = append(streams, commonv1alpha1.SubtitleStream{Index: int32(i), Codec: "subrip", Language: "eng"})
	}
	streams = append(streams, commonv1alpha1.SubtitleStream{Index: 99, Codec: "subrip", Language: "kor"})
	mi := &commonv1alpha1.MediaInfo{Subtitles: streams}

	got := buildExisting(mi, subtitlev1alpha1.EmbeddedSpec{}, "Movie.mkv",
		[]string{"Movie.mkv", "Movie.ko.srt"}, map[string]bool{"en": true})
	require.Len(t, got, maxExisting, "status.existing has MaxItems=64; an over-long list would fail the whole apply")
	for _, e := range got {
		assert.Equal(t, "en", e.LangKey, "a language the profile does not want is not recorded")
	}
	assert.EqualValues(t, 0, *got[0].StreamIndex, "embedded streams come first, in stream order")
}

func TestPlannerProfileOverrideAndNormalisation(t *testing.T) {
	spec := subtitlev1alpha1.SubtitleProfileSpec{Languages: []subtitlev1alpha1.LanguageItem{
		{Key: "en", Language: "eng"},
		{Key: "fr", Language: "fr", HI: subtitlev1alpha1.HIPolicyExcluded},
		{Key: "pt-BR:hi", Language: "pt-BR", HI: subtitlev1alpha1.HIPolicyRequired},
	}}
	pp, unknown, langs := plannerProfile(spec, []string{"en", "pt-BR:hi", "zz"})
	assert.Equal(t, []string{"zz"}, unknown)
	require.Len(t, pp.Languages, 2)
	assert.Equal(t, "en", pp.Languages[0].Language, "a profile language is normalised like a stream language")
	assert.Equal(t, subtitles.HIPolicyPrefer, pp.Languages[0].HI, "an unset HI is the CRD default, prefer")
	assert.Equal(t, subtitles.LangKey("pt-BR:hi"), pp.Languages[1].Key, "keys are identities and stay verbatim")
	assert.Equal(t, map[string]bool{"en": true, "pt-BR": true}, langs)
}

func TestMinScoreFor(t *testing.T) {
	spec := subtitlev1alpha1.SubtitleProfileSpec{MinScorePercent: subtitlev1alpha1.ScorePct{Movie: 70, Episode: 90}}
	assert.EqualValues(t, subtitles.MinScore(commonv1alpha1.MediaKindMovie, 70), minScoreFor(commonv1alpha1.MediaKindMovie, spec, nil))
	assert.EqualValues(t, subtitles.MinScore(commonv1alpha1.MediaKindEpisode, 90), minScoreFor(commonv1alpha1.MediaKindEpisode, spec, nil))
	assert.EqualValues(t, subtitles.MinScore(commonv1alpha1.MediaKindMovie, 50), minScoreFor(commonv1alpha1.MediaKindMovie, spec, ptr.To[int32](50)))
}

func TestNormalizeKeyKeepsSuffixes(t *testing.T) {
	for in, want := range map[subtitles.LangKey]subtitles.LangKey{
		"fre:forced": "fr:forced", "eng:hi": "en:hi", "pt_BR": "pt-BR", "pt-BR": "pt-BR", "und": "", "qaa": "",
	} {
		got, ok := normalizeKey(in)
		assert.Equal(t, want != "", ok, "%q", in)
		assert.Equal(t, want, got, "%q", in)
	}
}
