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

package decision_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// builtin is the embedded built-in profile name, resolved against the real
// catalogue -- the profile the owner's 840 movies use, with clustarr's own
// score adjustments.
func builtin(t *testing.T, name string) (quality.Profile, *catalogue.Catalogue) {
	t.Helper()
	cat := catalogue.LoadedCatalogue()
	profiles, errs := quality.BuiltinProfiles(cat)
	require.Empty(t, errs)
	p, ok := profiles[name]
	require.True(t, ok, "built-in %s", name)
	return p, cat
}

// imported is the Current a file imported from title has: its quality and
// the formats the real profile matched, frozen as import freezes them, plus
// what the probe read.
func imported(t *testing.T, p quality.Profile, cat *catalogue.Catalogue, title, codec string, hdr common.HdrFormat) *decision.Current {
	t.Helper()
	ds := decision.EvaluateRelease(context.Background(), rushmoreTarget(nil), p, cat,
		[]common.ReleaseInfo{{GUID: "idx:" + title, IndexerRef: "idx", Protocol: common.ProtocolUsenet, Title: title}}, usenetOnly)
	require.Len(t, ds, 1)
	rel := ds[0].Release
	return &decision.Current{
		Quality: rel.Quality, FormatScore: int(rel.FormatScore), Formats: rel.MatchedFormats,
		AudioLanguages: []string{"en"}, VideoCodec: codec, HDR: hdr,
	}
}

var usenetOnly = decision.Options{ProtocolsEnabled: map[string]bool{"usenet": true, "torrent": true}}

func rushmoreTarget(cur *decision.Current) decision.Target {
	return decision.Target{
		Kind: common.MediaKindMovie, Monitored: true, Available: true, OriginalLanguageTag: "en",
		Identity: decision.Identity{Titles: []string{"Rushmore"}, Year: 1998},
		Current:  cur,
	}
}

func decide(t *testing.T, p quality.Profile, cat *catalogue.Catalogue, tg decision.Target, title string) decision.Decision {
	t.Helper()
	ds := decision.Evaluate(context.Background(), tg, p, cat,
		[]common.ReleaseInfo{{GUID: "idx:" + title, IndexerRef: "idx", Protocol: common.ProtocolUsenet, Title: title, SizeBytes: 8 << 30}}, usenetOnly)
	require.Len(t, ds, 1)
	return ds[0]
}

func hasRejection(d decision.Decision, r decision.Reason) bool {
	for _, rej := range d.Rejections {
		if r.Of(rej) {
			return true
		}
	}
	return false
}

func reasons(d decision.Decision) []string {
	var out []string
	for _, r := range d.Rejections {
		out = append(out, r.Reason)
	}
	return out
}

// Rushmore, 2026-10-16: a 4.2 GB 1080p DV HDR10 x265 WEBRip read as below
// hd-bluray-web's Bluray cutoff and scored -10000 for x265 (HD), so a
// 16 GB SDR x264 Blu-ray replaced it. Neither may replace it now.
func TestRushmoresBluRayNoLongerReplacesItsHDRHEVCFile(t *testing.T) {
	p, cat := builtin(t, "hd-bluray-web")
	cur := imported(t, p, cat, "Rushmore.1998.1080p.WEBRip.DDP5.1.HDR10.x265-SM737", "hevc", common.HdrFormatHDR10)

	got := decide(t, p, cat, rushmoreTarget(cur), "Rushmore.1998.1080p.BluRay.Plus.Comm.DD5.1.x264-EbP")
	require.False(t, got.Approved)
	assert.True(t, hasRejection(got, decision.ReasonLosesHDR), "%v", reasons(got))
	assert.True(t, hasRejection(got, decision.ReasonAVCOverHEVC), "%v", reasons(got))

	// An HDR10 HEVC Blu-ray is still the profile's upgrade of a WEBRip.
	got = decide(t, p, cat, rushmoreTarget(cur), "Rushmore.1998.1080p.BluRay.DDP5.1.HDR10.x265-GROUP")
	assert.True(t, got.Approved, "%v", reasons(got))
}

// x265 (HD) scores nothing in the built-ins, so a 1080p x265 release is no
// longer below hd-bluray-web's minimum, and an imported x265 file is scored
// as the profile scores its formats today, not with the -10000 frozen at its
// import -- otherwise any x265 release would out-score it.
func TestX265AtHDIsScoredNothingInTheBuiltIns(t *testing.T) {
	p, cat := builtin(t, "hd-bluray-web")
	assert.Equal(t, 0, p.Scores["x265-hd"])

	got := decide(t, p, cat, rushmoreTarget(nil), "Rushmore.1998.1080p.BluRay.DDP5.1.x265-GROUP")
	assert.True(t, got.Approved, "a 1080p x265 Blu-ray is grabbed: %v", reasons(got))

	cur := imported(t, p, cat, "Rushmore.1998.1080p.BluRay.DDP5.1.x265-GROUP", "hevc", common.HdrFormatNone)
	cur.FormatScore = -10000 // frozen when x265 (HD) still scored -10000
	got = decide(t, p, cat, rushmoreTarget(cur), "Rushmore.1998.1080p.BluRay.DDP5.1.x265-OTHER")
	assert.False(t, got.Approved, "a same-quality, same-score release is no upgrade: %v", reasons(got))
	assert.False(t, hasRejection(got, decision.ReasonAVCOverHEVC))
}

func TestADolbyVisionReleaseIsNeverTaken(t *testing.T) {
	p, cat := builtin(t, "hd-bluray-web")
	for _, title := range []string{
		"Rushmore.1998.1080p.WEBRip.DDP5.1.DV.HDR10.x265-SM737",
		"Rushmore.1998.1080p.WEB-DL.DDP5.1.DoVi.HDR10.H.265-FLUX",
		"Rushmore.1998.1080p.WEB-DL.DV.H.265-GROUP",
	} {
		got := decide(t, p, cat, rushmoreTarget(nil), title)
		assert.True(t, hasRejection(got, decision.ReasonDolbyVision), "%s: %v", title, reasons(got))
	}
	got := decide(t, p, cat, rushmoreTarget(nil), "Rushmore.1998.1080p.WEB-DL.DDP5.1.HDR10.H.265-FLUX")
	assert.False(t, hasRejection(got, decision.ReasonDolbyVision), "HDR10 alone is not Dolby Vision: %v", reasons(got))
}

func TestABaselineGivesWayOnlyToWhatKeepsIt(t *testing.T) {
	p, cat := builtin(t, "hd-bluray-web")
	tests := []struct {
		name    string
		current *decision.Current
		title   string
		want    []decision.Reason
	}{{
		name:    "an SDR HEVC file is kept from an AVC Blu-ray",
		current: imported(t, p, cat, "Rushmore.1998.1080p.WEBRip.DDP5.1.x265-GROUP", "hevc", common.HdrFormatNone),
		title:   "Rushmore.1998.1080p.BluRay.DD5.1.x264-EbP",
		want:    []decision.Reason{decision.ReasonAVCOverHEVC},
	}, {
		name:    "an AV1 file is kept from an AVC Blu-ray",
		current: imported(t, p, cat, "Rushmore.1998.1080p.WEBRip.DDP5.1-GROUP", "av1", common.HdrFormatNone),
		title:   "Rushmore.1998.1080p.BluRay.DD5.1.H.264-GROUP",
		want:    []decision.Reason{decision.ReasonAVCOverHEVC},
	}, {
		name:    "an AVC file takes an AVC Blu-ray, as before",
		current: imported(t, p, cat, "Rushmore.1998.1080p.WEBRip.DDP5.1.x264-GROUP", "h264", common.HdrFormatNone),
		title:   "Rushmore.1998.1080p.BluRay.DD5.1.x264-EbP",
	}, {
		name:    "an unprobed file fails open",
		current: imported(t, p, cat, "Rushmore.1998.1080p.WEBRip.DDP5.1.x265-GROUP", "", ""),
		title:   "Rushmore.1998.1080p.BluRay.DD5.1.x264-EbP",
	}, {
		name:    "a title that names no codec fails open",
		current: imported(t, p, cat, "Rushmore.1998.1080p.WEBRip.DDP5.1.x265-GROUP", "hevc", common.HdrFormatNone),
		title:   "Rushmore.1998.1080p.BluRay.DD5.1-EbP",
	}, {
		name:    "an HDR file is kept from an SDR HEVC Blu-ray",
		current: imported(t, p, cat, "Rushmore.1998.1080p.WEB-DL.DDP5.1.HDR10.H.265-GROUP", "hevc", common.HdrFormatHDR10),
		title:   "Rushmore.1998.1080p.BluRay.DD5.1.x265-GROUP",
		want:    []decision.Reason{decision.ReasonLosesHDR},
	}, {
		// rls reads "10bit" as HDR10: a 10-bit SDR encode names no HDR.
		name:    "a 10-bit SDR release is not HDR",
		current: imported(t, p, cat, "Rushmore.1998.1080p.WEB-DL.DDP5.1.HDR10.H.265-GROUP", "hevc", common.HdrFormatHDR10Plus),
		title:   "Rushmore.1998.1080p.BluRay.x265.10bit.AAC5.1-GROUP",
		want:    []decision.Reason{decision.ReasonLosesHDR},
	}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decide(t, p, cat, rushmoreTarget(tt.current), tt.title)
			for _, r := range []decision.Reason{decision.ReasonLosesHDR, decision.ReasonAVCOverHEVC} {
				assert.Equal(t, contains(tt.want, r), hasRejection(got, r), "%s: %v", r.Code, reasons(got))
			}
			if len(tt.want) == 0 {
				assert.True(t, got.Approved, "%v", reasons(got))
			}
		})
	}
}

// A higher resolution is an upgrade the AVC rule leaves alone: the rule
// keeps a baseline from a sideways move, not from a better picture.
func TestAnAVCReleaseOfAGreaterResolutionStillUpgradesAnHEVCFile(t *testing.T) {
	p, cat := builtin(t, "uhd-bluray-web")
	cur := imported(t, p, cat, "Rushmore.1998.1080p.WEB-DL.DDP5.1.H.265-GROUP", "hevc", common.HdrFormatNone)
	got := decide(t, p, cat, rushmoreTarget(cur), "Rushmore.1998.2160p.WEB-DL.DDP5.1.H.264-GROUP")
	assert.False(t, hasRejection(got, decision.ReasonAVCOverHEVC), "%v", reasons(got))
}

// A file that lacks the profile's language is replaced whatever it is, as
// upgradeRejection already replaces it.
func TestAFileThatLacksTheLanguageIsReplacedWhateverItsCodec(t *testing.T) {
	p, cat := builtin(t, "hd-bluray-web")
	cur := imported(t, p, cat, "Rushmore.1998.1080p.WEB-DL.DDP5.1.HDR10.H.265-GROUP", "hevc", common.HdrFormatHDR10)
	cur.AudioLanguages = []string{"fr"}
	got := decide(t, p, cat, rushmoreTarget(cur), "Rushmore.1998.1080p.BluRay.DD5.1.x264-EbP")
	assert.False(t, hasRejection(got, decision.ReasonLosesHDR), "%v", reasons(got))
	assert.False(t, hasRejection(got, decision.ReasonAVCOverHEVC), "%v", reasons(got))
}

func contains(rs []decision.Reason, r decision.Reason) bool {
	for _, x := range rs {
		if x == r {
			return true
		}
	}
	return false
}
