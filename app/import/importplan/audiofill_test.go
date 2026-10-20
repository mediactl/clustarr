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

package importplan

import (
	"testing"

	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/quality"
)

// TestAudioFill: under a dual-audio policy that does not graft, Akira's
// Portuguese-and-Japanese file is replaced by a file whose own probe
// carries English and Japanese, and a file the title called dual audio
// that carries Japanese alone is the release's fault.
func TestAudioFill(t *testing.T) {
	p := quality.Profile{Language: "original", AudioLanguages: []string{"en", "original"}}
	probe := func(langs ...string) *commonv1.MediaInfo {
		mi := &commonv1.MediaInfo{}
		for _, l := range langs {
			mi.Audio = append(mi.Audio, commonv1.AudioStream{Language: l})
		}
		return mi
	}
	file := func(langs ...string) catalogv1alpha1.MediaFile {
		return catalogv1alpha1.MediaFile{Status: catalogv1alpha1.MediaFileStatus{MediaInfo: probe(langs...)}}
	}
	files := func(fs ...catalogv1alpha1.MediaFile) []catalogv1alpha1.MediaFile { return fs }
	// The incoming file's probe as the inspect summarises it (A3.8).
	summary := func(langs ...string) *schema.ProbeSummary {
		return &schema.ProbeSummary{AudioLanguages: decision.AudioLanguages(probe(langs...))}
	}

	fills, mislabelled := audioFill(p, "ja", files(file("por", "jpn")), summary("jpn", "eng"))
	require.True(t, fills, "a dual-audio file completes Akira")
	require.Nil(t, mislabelled)

	fills, mislabelled = audioFill(p, "ja", files(file("por", "jpn")), summary("jpn"))
	require.False(t, fills)
	require.Equal(t, []string{"ja"}, mislabelled, "the title said dual audio; the file is Japanese alone")

	fills, mislabelled = audioFill(p, "ja", files(file("por", "jpn")), summary("jpn", "und"))
	require.False(t, fills, "the new file's audio is not all known: the usual rules hold it")
	require.Nil(t, mislabelled)

	fills, mislabelled = audioFill(p, "ja", files(file("eng", "jpn")), summary("jpn", "eng"))
	require.False(t, fills, "the existing file is complete: the usual rules apply")
	require.Nil(t, mislabelled)

	fills, _ = audioFill(p, "ja", files(file("por", "jpn"), file("eng", "jpn")), summary("jpn", "eng"))
	require.False(t, fills, "it fills only when every file it replaces is incomplete")

	graft := p
	graft.GraftAudio = true
	fills, mislabelled = audioFill(graft, "ja", files(file("por", "jpn")), summary("jpn", "eng"))
	require.False(t, fills, "a grafting policy adds the dub to the file instead")
	require.Nil(t, mislabelled)
}
