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

package standard

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/transcode"
)

// keptAudioSources is the audio sources a plan keeps, in output order
// (grouped by language, commentary after its language), once each.
func keptAudioSources(p Result) []int32 {
	var out []int32
	for _, a := range p.Audio {
		if len(out) == 0 || out[len(out)-1] != a.SourceIndex {
			out = append(out, a.SourceIndex)
		}
	}
	return out
}

func sidecarSources(p Result) []int32 {
	var out []int32
	for _, s := range p.Sidecars {
		out = append(out, s.SourceIndex)
	}
	return out
}

func commentaryAud(lang string) transcode.AudioStream {
	a := aud("aac", 2, lang, "Commentary")
	a.Disposition.Comment = true
	return a
}

// manyLanguages is a Japanese film's tracks as a release carries them.
func manyLanguages() transcode.MediaInfo {
	in := info(h264,
		aud("aac", 2, "jpn", ""),  // 0: the original
		aud("eac3", 6, "eng", ""), // 1: the English dub
		aud("ac3", 6, "spa", ""),  // 2
		commentaryAud("eng"),      // 3
		commentaryAud("fre"),      // 4
		aud("aac", 2, "", ""),     // 5: untagged
	)
	in.Audio[0].Disposition.Default = true
	in.Subtitles = []transcode.SubtitleStream{
		sub("subrip", "eng", "", false, false), // 0
		sub("subrip", "eng", "", true, false),  // 1: forced
		sub("subrip", "jpn", "", false, false), // 2
		sub("subrip", "", "", false, false),    // 3: untagged
		sub("subrip", "spa", "", false, false), // 4
	}
	in.OriginalLanguage = "ja"
	return in
}

// The owner's languages (2026-10-15): English audio and the original's,
// English subtitles. Audio in another language goes, commentary too unless
// in a kept language; an untagged track stays (it is most often the
// original or English); the original's subtitles go unless the original is
// English; a forced English track stays. Each drop is named in the plan.
// Beetlejuice Beetlejuice's 17 audio and 40 subtitle languages, all kept
// before, made one remux decode and encode 17 tracks at 2 MB/s and OOM.
func TestTheProfileKeepsEnglishAndTheOriginalLanguage(t *testing.T) {
	p := profile
	p.Languages, p.SubtitleLanguages = []string{"en", "original"}, []string{"en"}
	r := Plan(manyLanguages(), p, cpu)
	assert.Equal(t, []int32{0, 1, 3, 5}, keptAudioSources(r), "jpn, eng, the English commentary and the untagged track")
	assert.Equal(t, []int32{0, 1, 3}, sidecarSources(r), "English, forced English and untagged subtitles")
	dropped := strings.Join(r.Dropped, "\n")
	for _, d := range []string{"audio 2 (spa)", "audio 4 (fre)", "subtitle 2 (subrip \"\" jpn)", "subtitle 4 (subrip \"\" spa)"} {
		assert.Contains(t, dropped, d)
	}
}

func TestAnEnglishOriginalKeepsEnglishAlone(t *testing.T) {
	p := profile
	p.Languages, p.SubtitleLanguages = []string{"en", "original"}, []string{"en"}
	in := manyLanguages()
	in.OriginalLanguage = "en"
	assert.Equal(t, []int32{1, 3, 5}, keptAudioSources(Plan(in, p, cpu)))
}

// An item whose original language is unknown keeps every audio track:
// which dub is the original cannot be told, and a dropped track is lost
// with the source.
func TestAnUnknownOriginalKeepsEveryAudioTrack(t *testing.T) {
	p := profile
	p.Languages, p.SubtitleLanguages = []string{"en", "original"}, []string{"en"}
	in := manyLanguages()
	in.OriginalLanguage = ""
	r := Plan(in, p, cpu)
	assert.Equal(t, []int32{0, 1, 3, 2, 4, 5}, keptAudioSources(r))
	assert.Equal(t, []int32{0, 1, 3}, sidecarSources(r), "the subtitles need no original")
}

// Languages are compared as languages: a profile's "eng" or "en" matches a
// stream tagged "eng" or "en", and the original's TMDB "ja" matches "jpn".
func TestLanguagesAreComparedNormalized(t *testing.T) {
	p := profile
	p.Languages = []string{"eng"}
	in := manyLanguages()
	in.Audio[1].Language = "en"
	assert.Equal(t, []int32{1, 3, 5}, keptAudioSources(Plan(in, p, cpu)))
	p.Languages = []string{"original"}
	assert.Equal(t, []int32{0, 5}, keptAudioSources(Plan(in, p, cpu)), "the original's ja is the stream's jpn")
}

// No subtitle languages keeps every subtitle, as before.
func TestNoSubtitleLanguagesKeepsEverySubtitle(t *testing.T) {
	r := Plan(manyLanguages(), profile, cpu)
	assert.Equal(t, []int32{0, 1, 2, 3, 4}, sidecarSources(r))
	assert.Equal(t, []int32{0, 1, 3, 2, 4, 5}, keptAudioSources(r))
}

// An image subtitle holds a file for OCR only in a kept language: a
// Blu-ray's Japanese PGS track is dropped, not waited for.
func TestADroppedImageSubtitleHoldsNothing(t *testing.T) {
	p := profile
	p.SubtitleLanguages = []string{"en"}
	in := manyLanguages()
	in.Subtitles = append(in.Subtitles, transcode.SubtitleStream{Codec: "hdmv_pgs_subtitle", Bitmap: true, Language: "jpn"})
	r := Plan(in, p, cpu)
	assert.NotEqual(t, DecisionSkip, r.Decision, r.Reason)
	in.Subtitles[len(in.Subtitles)-1].Language = "eng"
	assert.Equal(t, HoldImageSubtitles, Plan(in, p, cpu).Reason, "an English one still holds it")
}
