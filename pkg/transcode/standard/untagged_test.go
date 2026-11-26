/*
Copyright (C) 2026 The Clustarr Authors

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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/transcode"
)

// audioLanguages is each output audio track's language, in output order.
func audioLanguages(p Result) []string {
	out := make([]string, 0, len(p.Audio))
	for _, a := range p.Audio {
		out = append(out, a.Language)
	}
	return out
}

// Panty & Stocking's Blu-ray remux (talos, 2026-11-26): Japanese FLAC
// tagged, the English 5.1 dub untagged, the release listed as English and
// Japanese. The transcode wrote the dub's two tracks untagged, the file
// was named [JA] and players listed "Unknown" (owner: "The audio appears
// to be mislabeled"). With the file's languages known, the one language no
// tagged track carries is the untagged track's.
func TestAnUntaggedTrackTakesTheOneLanguageNoTaggedTrackCarries(t *testing.T) {
	remux := func(languages ...string) transcode.MediaInfo {
		in := info(h264, aud("flac", 2, "jpn", ""), aud("truehd", 6, "", ""))
		in.Audio[0].Disposition.Default = true
		in.OriginalLanguage = "ja"
		in.Languages = languages
		return in
	}
	p := profile
	p.Languages = []string{"en", "original"}

	r := Plan(remux("ja", "en"), p, cpu)
	assert.Equal(t, []string{"jpn", "eng", "eng"}, audioLanguages(r), "the dub's stereo companion and its 5.1 track are English")
	assert.Equal(t, []string{"jpn", "", ""}, audioLanguages(Plan(remux(), p, cpu)), "without the file's languages nothing is named")
	assert.NotEqual(t, r.Hash(), Plan(remux(), p, cpu).Hash(), "the manager and the worker must plan from the same languages")

	// The source is left as probed: the rule names tracks in the plan alone.
	in := remux("ja", "en")
	Plan(in, p, cpu)
	assert.Empty(t, in.Audio[1].Language)

	t.Run("anything less certain names nothing", func(t *testing.T) {
		assert.Equal(t, []string{"jpn", "", ""}, audioLanguages(Plan(remux("ja"), p, cpu)), "no language is unaccounted for")
		assert.Equal(t, []string{"jpn", "", ""}, audioLanguages(Plan(remux("ja", "en", "de"), p, cpu)), "two are")

		all := info(h264, aud("flac", 2, "", ""), aud("truehd", 6, "", ""))
		all.Languages = []string{"en"}
		for _, l := range audioLanguages(Plan(all, p, cpu)) {
			assert.Empty(t, l, "no track is tagged, so which is which cannot be told")
		}

		tagged := info(h264, aud("flac", 2, "jpn", ""), aud("truehd", 6, "eng", ""))
		tagged.Languages = []string{"ja", "en", "fr"}
		assert.Equal(t, []string{"jpn", "eng", "eng"}, audioLanguages(Plan(tagged, p, cpu)), "no track is untagged")
	})

	t.Run("a named copy is not an as-is file", func(t *testing.T) {
		// Compliant HEVC in MP4 with AAC audio is skipped as it is; with an
		// untagged track to name it is remuxed, the video copied.
		mp4 := func(languages ...string) transcode.MediaInfo {
			in := mp4Info(hevc8, aud("aac", 2, "jpn", ""), aud("aac", 2, "", ""))
			in.Languages = languages
			return in
		}
		assert.Equal(t, DecisionSkip, Plan(mp4(), profile, cpu).Decision)
		named := Plan(mp4("ja", "en"), profile, cpu)
		assert.NotEqual(t, DecisionSkip, named.Decision, "%s", named.Reason)
		assert.Equal(t, []string{"jpn", "eng"}, audioLanguages(named))
	})
}
