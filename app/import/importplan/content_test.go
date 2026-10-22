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

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/itemread"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/quality"
)

// movieIdentity is the identity itemread gives a Movie of title, year and
// alternates.
func movieIdentity(title string, year int32, alternates ...string) decision.Identity {
	return itemread.MovieIdentity(&catalogv1alpha1.Movie{Status: catalogv1alpha1.MovieStatus{
		Metadata: &catalogv1alpha1.MovieMetadata{Title: title, Year: year, AlternateTitles: alternates},
	}})
}

func runs(minutes int64) *schema.ProbeSummary {
	return &schema.ProbeSummary{VideoCodec: "h264", HDR: "none", DurationSeconds: minutes*60 + 20}
}

// The cases are the owner's library's (2026-10-16), the calibration the
// bounds' comment records.
func TestAMovieFileWhoseStreamsSayItIsNotTheMovieIsRefused(t *testing.T) {
	neverEnding := movieIdentity("The NeverEnding Story III", 1994, "Die unendliche Geschichte III")
	tests := []struct {
		name   string
		probe  *schema.ProbeSummary
		title  string
		id     decision.Identity
		rt     int
		refuse string
	}{{
		name: "another film the indexer answered the id with", probe: runs(118),
		title: "No.Escape.1994.1080p.BluRay.NAMELESS-MEDIA.EngSub.DTS.x264-xpost", id: neverEnding, rt: 95,
		refuse: "another film",
	}, {
		name: "a foreign title at the movie's runtime", probe: runs(89),
		title: "Die.unendliche.Geschichte.2.1990.German.DL.1080p.BluRay.x264-PL3X",
		id:    movieIdentity("The NeverEnding Story II: The Next Chapter", 1990), rt: 90,
	}, {
		name: "a title the parser misreads, at the movie's runtime", probe: runs(129),
		title: "ThelmaandLouise.1991.1080p.BluRay.DD.5.1.x264-playHD", id: movieIdentity("Thelma & Louise", 1991), rt: 130,
	}, {
		name: "a director's cut that names the movie, however long", probe: runs(288),
		title: "Until.the.End.of.the.World.1991.1080p.BluRay.x264-OFT", id: movieIdentity("Until the End of the World", 1991), rt: 158,
	}, {
		name: "one broadcast part of the film", probe: runs(47),
		title: "63.Up.2019.1080p.BluRay.x265-USURY", id: movieIdentity("63 Up", 2019), rt: 145,
		refuse: "a part of it",
	}, {
		name: "the shortest real cut", probe: runs(114),
		title: "Enter.the.Void.2009.1080p.BluRay.x265-Kitsune", id: movieIdentity("Enter the Void", 2009), rt: 161,
	}, {
		name: "a Dolby Vision file", probe: &schema.ProbeSummary{VideoCodec: "hevc", HDR: "dolbyVisionHdr10", DurationSeconds: 95 * 60},
		title: "Rushmore.1998.1080p.WEBRip.x265-SM737", id: movieIdentity("Rushmore", 1998), rt: 93,
		refuse: "Dolby Vision",
	}, {
		name: "no metadata runtime", probe: runs(118),
		title: "No.Escape.1994.1080p.BluRay.x264-xpost", id: neverEnding,
	}, {
		name: "no probe", title: "No.Escape.1994.1080p.BluRay.x264-xpost", id: neverEnding, rt: 95,
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := movieContentRejection("f.mkv", tt.probe, tt.title, tt.id, tt.rt)
			if tt.refuse == "" {
				assert.True(t, r.none(), "%+v", r)
				return
			}
			assert.Equal(t, commonv1.ImportClassReleaseFault, r.Class)
			assert.Contains(t, r.Text, tt.refuse)
		})
	}
}

// An import keeps a good transcode baseline from both files' streams, the
// grab decision having read only the title; a person's pick is exempt, and
// a file that lacks the language is replaced whatever it is.
func TestAnImportKeepsAGoodTranscodeBaseline(t *testing.T) {
	webrip, ok := quality.Lookup("video", "WEBRip-1080p")
	assert.True(t, ok)
	bluray, ok := quality.Lookup("video", "Bluray-1080p")
	assert.True(t, ok)
	prof := quality.Profile{
		Tiers:       [][]quality.Definition{{bluray}, {webrip}},
		CutoffIndex: 0, UpgradeAllowed: true, CutoffFormatScore: 10000, MinUpgradeFormatScore: 1,
		ProperPolicy: "preferAndUpgrade", LanguageName: "any",
		Scores: map[string]int{"x265-hd": 0},
	}
	existing := func(codec string, hdr commonv1.HdrFormat) catalogv1alpha1.MediaFile {
		return catalogv1alpha1.MediaFile{
			Spec: catalogv1alpha1.MediaFileSpec{
				Quality: webrip.Quality, FormatScore: -10000, MatchedFormats: []string{"x265-hd"},
				MediaRef: commonv1.MediaRef{Kind: "Movie", Name: "rushmore"},
			},
			Status: catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{VideoCodec: codec, Hdr: hdr}},
		}
	}
	incoming := func(codec, hdr string) candidate {
		q := bluray.Quality
		return candidate{kind: commonv1.MediaKindMovie, keys: []string{"rushmore"}, f: schema.InspectedFile{
			Frozen: schema.FrozenFields{Quality: &q},
			Probe:  &schema.ProbeSummary{VideoCodec: codec, HDR: hdr, DurationSeconds: 93 * 60},
		}}
	}
	p := &planner{in: Input{Profile: &prof}}

	r, refused := p.gate("f.mkv", incoming("h264", "none"), []catalogv1alpha1.MediaFile{existing("hevc", commonv1.HdrFormatHDR10)}, false)
	assert.True(t, refused)
	assert.Equal(t, commonv1.ImportClassItemState, r.Class)
	assert.Contains(t, r.Text, "SDR")

	r, refused = p.gate("f.mkv", incoming("h264", "none"), []catalogv1alpha1.MediaFile{existing("hevc", commonv1.HdrFormatNone)}, false)
	assert.True(t, refused)
	assert.Contains(t, r.Text, "AVC")

	_, refused = p.gate("f.mkv", incoming("hevc", "hdr10"), []catalogv1alpha1.MediaFile{existing("hevc", commonv1.HdrFormatHDR10)}, false)
	assert.False(t, refused, "an HDR10 HEVC Blu-ray is still the upgrade")

	_, refused = p.gate("f.mkv", incoming("h264", "none"), []catalogv1alpha1.MediaFile{existing("h264", commonv1.HdrFormatNone)}, false)
	assert.False(t, refused, "AVC over AVC is the profile's to judge")

	_, refused = p.gate("f.mkv", incoming("h264", "none"), []catalogv1alpha1.MediaFile{existing("hevc", commonv1.HdrFormatHDR10)}, true)
	assert.False(t, refused, "a person's pick is exempt")

	// The existing x265 file scores as the profile scores x265 (HD) today,
	// 0, not the -10000 frozen at its import: a same-tier HEVC release at
	// score 0 is no upgrade of it.
	same := incoming("hevc", "none")
	q := webrip.Quality
	same.f.Frozen.Quality = &q
	r, refused = p.gate("f.mkv", same, []catalogv1alpha1.MediaFile{existing("hevc", commonv1.HdrFormatNone)}, false)
	assert.True(t, refused, "%+v", r)
}

// TestADolbyVisionEpisodeFileIsRefused: an episode file is held to the rule
// a movie's is, Dolby Vision never taken. On 2026-10-22 a season pack whose
// title named no Dolby Vision -- "[ZeroBuild] Cyberpunk Edgerunners Season 1
// v2 (Web 1080p HEVC x265 10-bit AC3)" -- was Netflix's profile 5, with no
// HDR10 base layer: it replaced ten BluRay episodes, transcode skipped every
// one, and a client without Dolby Vision showed them in the wrong colours.
func TestADolbyVisionEpisodeFileIsRefused(t *testing.T) {
	for name, tt := range map[string]struct {
		hdr     string
		refused bool
	}{
		"profile 5, no base layer": {"dolbyVision", true},
		"profile 8.1, HDR10 base":  {"dolbyVisionHdr10", true},
		"HDR10":                    {"hdr10", false},
		"SDR":                      {"none", false},
	} {
		t.Run(name, func(t *testing.T) {
			r := episodeContentRejection("S01E01.mkv", &schema.ProbeSummary{VideoCodec: "hevc", HDR: tt.hdr, DurationSeconds: 24 * 60})

			assert.Equal(t, tt.refused, !r.none())
			if tt.refused {
				assert.Equal(t, commonv1.ImportClassReleaseFault, r.Class, "a release fault: the release is blocklisted and another searched")
				assert.Contains(t, r.Text, "Dolby Vision")
			}
		})
	}
	assert.True(t, episodeContentRejection("S01E01.mkv", nil).none(), "an unprobed file is not judged")
}
