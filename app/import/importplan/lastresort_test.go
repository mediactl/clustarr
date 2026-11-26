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

package importplan

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/quality"
)

// A file of a last-resort tier (quality.Profile.LastResort; owner,
// 2026-11-26: 720p "should also not meet the quality cutoff *even* when
// transcoded") is replaced by a file of a better tier though it is a
// transcode carrying every wanted language; a file of such a tier replaces
// nothing better by its audio or by coming in a pack.
func TestAnImportReplacesATranscodeOfALastResortTier(t *testing.T) {
	def := func(name string) quality.Definition {
		d, ok := quality.Lookup("video", name)
		require.True(t, ok, name)
		return d
	}
	web1080, web720, web480 := def("WEBDL-1080p"), def("WEBDL-720p"), def("WEBDL-480p")
	profile := func(lastResort bool) quality.Profile {
		p := quality.Profile{
			Tiers: [][]quality.Definition{{web1080}, {web720}, {web480}}, CutoffIndex: 0, UpgradeAllowed: true, CutoffFormatScore: 10000,
			ProperPolicy: "preferAndUpgrade", LanguageName: "any",
			AudioLanguages: []string{"en", "original"}, GraftAudio: true, AudioCompleteFirst: true,
		}
		if lastResort {
			p.LastResortFrom = 1
		}
		return p
	}
	existing := func(q quality.Definition, release string, audio ...string) catalogv1alpha1.MediaFile {
		mi := &commonv1.MediaInfo{VideoCodec: "hevc", RuntimeMillis: 1_420_000, TranscodeProfile: "hevc-mkv@abc"}
		for _, l := range audio {
			mi.Audio = append(mi.Audio, commonv1.AudioStream{Language: l})
		}
		mf := catalogv1alpha1.MediaFile{
			Spec: catalogv1alpha1.MediaFileSpec{
				Quality: q.Quality, SizeBytes: 300_000_000,
				MediaRef:     commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: "frieren-s01e03"},
				ImportedFrom: &catalogv1alpha1.ImportSource{ReleaseTitle: release},
			},
			Status: catalogv1alpha1.MediaFileStatus{MediaInfo: mi},
		}
		require.True(t, mf.Final(), "the existing file is a transcode")
		return mf
	}
	incoming := func(q quality.Definition, audio ...string) candidate {
		qq := q.Quality
		return candidate{kind: commonv1.MediaKindEpisode, keys: []string{"frieren-s01e03"}, f: schema.InspectedFile{
			SizeBytes: 900_000_000, Frozen: schema.FrozenFields{Quality: &qq},
			Probe: &schema.ProbeSummary{VideoCodec: "h264", VideoBitDepth: 8, DurationSeconds: 1420, AudioLanguages: audio},
		}}
	}
	const single = "Frieren.S01E03.WEB-DL.AAC2.0.x265-SINGLE"
	singleEntry := catalogv1alpha1.DownloadEntry{
		GrabbedBy: commonv1.GrabSourceSearch,
		Release:   catalogv1alpha1.DownloadRelease{Title: "Frieren.S01E03.WEB-DL.x264-OTHER"},
	}
	packEntry := catalogv1alpha1.DownloadEntry{
		GrabbedBy: commonv1.GrabSourceSearch,
		Release:   catalogv1alpha1.DownloadRelease{Title: "Frieren.S01.720p.WEB-DL.x264-PACK"},
		Episodes:  []catalogv1alpha1.EpisodeNumber{{Season: 1, Number: 3}, {Season: 1, Number: 4}},
	}
	gate := func(lastResort bool, e catalogv1alpha1.DownloadEntry, c candidate, have catalogv1alpha1.MediaFile) bool {
		prof := profile(lastResort)
		p := &planner{in: Input{Profile: &prof, OriginalLanguage: "ja", Entry: e}}
		_, refused := p.gate("f.mkv", c, []catalogv1alpha1.MediaFile{have}, false)
		return refused
	}

	dual720 := existing(web720, single, "ja", "en")
	assert.False(t, gate(true, singleEntry, incoming(web1080, "ja"), dual720), "a Japanese-only 1080p file replaces a dual-audio 720p transcode")
	assert.True(t, gate(false, singleEntry, incoming(web1080, "ja"), dual720), "without the mark the transcode is final")
	assert.True(t, gate(true, singleEntry, incoming(web720, "ja", "en"), dual720), "another 720p file does not")
	assert.False(t, gate(true, singleEntry, incoming(web720, "ja"), existing(web480, single, "ja")), "720p replaces a 480p transcode")

	jp1080 := existing(web1080, single, "ja")
	assert.True(t, gate(true, singleEntry, incoming(web720, "ja", "en"), jp1080), "a dual-audio 720p file fills nothing over a 1080p one")
	assert.False(t, gate(false, singleEntry, incoming(web720, "ja", "en"), jp1080), "without the mark its audio replaces the file")
	assert.True(t, gate(true, packEntry, incoming(web720, "ja"), jp1080), "nor does a 720p pack's file replace a single's 1080p one")
	assert.False(t, gate(false, packEntry, incoming(web720, "ja"), jp1080), "without the mark a pack's file does")
}
