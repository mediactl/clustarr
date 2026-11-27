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
	"maps"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/quality"
)

// H.264 10-bit is the one codec the transcoder cannot take (owner,
// 2026-11-02: "the only real *off-limits* codec is encoding or decoding
// H.264 10-bit"): a 10bit or Hi10P title naming no other codec is
// clustarr's h264-10bit, refused under every profile as AV1 is, where an
// x265 10-bit release, which QSV decodes, and 8-bit H.264 are not.
func TestAnH264TenBitReleaseIsRefusedAndX265TenBitIsNot(t *testing.T) {
	for _, profile := range []string{"hd-bluray-web", "anime-remux-1080p"} {
		p, cat := builtin(t, profile)
		for title, refused := range map[string]bool{
			"Rushmore.1998.1080p.BluRay.10bit.x264.DTS-GROUP": true,
			"Rushmore.1998.1080p.BluRay.Hi10P.FLAC-GROUP":     true,
			"[Group] Rushmore (1998) [BD 1080p 10-bit FLAC]":  true,
			"Rushmore.1998.1080p.BluRay.10bit.x265.DTS-GROUP": false,
			"Rushmore.1998.1080p.BluRay.HEVC.10bit.DTS-GROUP": false,
			"Rushmore.1998.1080p.BluRay.AV1.10bit.Opus-GROUP": false, // AV1's own format refuses it
			"Rushmore.1998.1080p.BluRay.x264.DTS-GROUP":       false,
		} {
			got := decide(t, p, cat, rushmoreTarget(nil), title)
			matched := slices.Contains(got.Release.MatchedFormats, "h264-10bit")
			assert.Equal(t, refused, matched, "%s under %s: %v", title, profile, got.Release.MatchedFormats)
			if refused {
				assert.False(t, got.Approved, "%s under %s", title, profile)
				assert.True(t, hasRejection(got, decision.ReasonCustomFormatMinimumScore), "%s under %s: %v", title, profile, reasons(got))
			}
			assert.Equal(t, refused, hasRejection(got, decision.ReasonNeverTaken), "%s under %s: %v", title, profile, reasons(got))
		}
	}
}

// The spellings that got through until 2026-11-27, when Gosick, Altair and
// Rage of Bahamut each downloaded H.264 10-bit releases the import then
// refused, 21 in a night: Kametsu's "Hi10" without its P, Erai-raws'
// "AVC-YUV444P10", and a 10 bit with a space. An episode numbered 10 whose
// title begins "Bit" names no bit depth, nor does an HEVC release's pixel
// format.
func TestTheSpellingsOfH264TenBit(t *testing.T) {
	p, cat := builtin(t, "anime-remux-1080p")
	for title, refused := range map[string]bool{
		"[Kametsu] Rushmore (1998) (BD 1080p Hi10 FLAC) [B81767FA]":          true,
		"[Kametsu] Rushmore (1998) (BD 1080p Hi10P FLAC) [B81767FA]":         true,
		"[Erai-raws] Rushmore (1998) [1080p AVC-YUV444P10][E-AC3][E0F363A2]": true,
		"[Erai-raws] Rushmore (1998) [1080p AVC-YUV420P10][AC-3]":            true,
		"[Group] Rushmore (1998) [BD 1080p 10 bit FLAC]":                     true,
		"[Group] Rushmore (1998) [BD 1080p 10bits FLAC]":                     true,
		"[Cleo]Rushmore_(1998)_(Dual Audio_10bit_BD720p)":                    true,
		"Rushmore.1998.720p-Hi10p.BluRay.AAC2.0-CTR":                         true,
		"[Erai-raws] Rushmore (1998) [1080p HEVC-YUV420P10][E-AC3]":          false,
		"[Group] Rushmore (1998) [BD 1080p HEVC 10 bit FLAC]":                false,
		"Rushmore.1998.S01E10.Bit.by.Bit.1080p.AMZN.WEB-DL.DDP5.1.H.264-GRP": false,
		"Rushmore.1998.1080p.BluRay.DD5.1.x264-GRP":                          false,
		"[Coalgirls]_Rushmore_(1998)_(1920x1080_Blu-Ray_FLAC)":               false, // names no bit depth: the import's to catch
		"Rushmore.1998.1080p.BluRay.DD5.1.x264-HI10":                         true,  // a group of that name is not worth a miss
	} {
		got := decide(t, p, cat, rushmoreTarget(nil), title)
		assert.Equal(t, refused, hasRejection(got, decision.ReasonNeverTaken), "%s: %v %v", title, got.Release.MatchedFormats, reasons(got))
	}
}

// A release that names HEVC at 12 bits is refused under every profile, as
// H.264 10-bit is (owner, 2026-11-27: "Block 12-bit HEVC releases"): no GPU
// here decodes it, so the transcode skips the file. The 10-bit HEVC every
// other release is stays taken, and so does an episode numbered 12 whose
// title begins "Bit".
func TestAnHEVCTwelveBitReleaseIsRefused(t *testing.T) {
	for _, profile := range []string{"hd-bluray-web", "anime-remux-1080p"} {
		p, cat := builtin(t, profile)
		for title, refused := range map[string]bool{
			"Rushmore.1998.1080p.BluRay.x265.12bit.DTS-GROUP":                  true,
			"Rushmore.1998.1080p.BluRay.HEVC.12-bit.FLAC-GROUP":                true,
			"[Group] Rushmore (1998) [BD 1080p HEVC Main12 FLAC]":              true,
			"[Group] Rushmore (1998) [BD 1080p x265 Ma12p Opus]":               true,
			"[Erai-raws] Rushmore (1998) [1080p HEVC-YUV444P12][E-AC3]":        true,
			"Rushmore.1998.1080p.BluRay.x265.10bit.DTS-GROUP":                  false,
			"[Group] Rushmore (1998) [BD 1080p HEVC Main10 FLAC]":              false,
			"Rushmore.1998.1080p.BluRay.AV1.12bit.Opus-GROUP":                  false, // AV1's own format judges it
			"Rushmore.1998.S01E12.Bit.by.Bit.1080p.AMZN.WEB-DL.DDP5.1.H.265-G": false,
			"Rushmore.1998.1080p.BluRay.DD5.1.x265-GRP":                        false,
		} {
			got := decide(t, p, cat, rushmoreTarget(nil), title)
			matched := slices.Contains(got.Release.MatchedFormats, "hevc-12bit")
			assert.Equal(t, refused, matched, "%s under %s: %v", title, profile, got.Release.MatchedFormats)
			assert.Equal(t, refused, hasRejection(got, decision.ReasonNeverTaken), "%s under %s: %v", title, profile, reasons(got))
		}
	}
}

// No profile takes one (owner, 2026-11-27: "H.264 10-bit should *always*
// be blocked for every library - these cannot be transcoded"): the formats'
// -10000 is a score, which a profile that scores the format itself or
// names it a last resort gives back, and the release is refused all the
// same. An XviD one too.
func TestNoProfileTakesAnH264TenBitRelease(t *testing.T) {
	base, cat := builtin(t, "hd-bluray-web")
	const hi10 = "Rushmore.1998.1080p.BluRay.10bit.x264.DTS-GROUP"
	const xvid = "Rushmore.1998.1080p.BluRay.XviD.AC3-GROUP"

	scored := base
	scored.Scores = maps.Clone(base.Scores)
	scored.Scores["h264-10bit"], scored.Scores["mpeg4-part2"] = 0, 0
	lastResort := base
	lastResort.LastResortFormats = map[string]bool{"h264-10bit": true, "mpeg4-part2": true}

	for name, p := range map[string]quality.Profile{"scored 0": scored, "a last resort": lastResort} {
		for _, title := range []string{hi10, xvid} {
			got := decide(t, p, cat, rushmoreTarget(nil), title)
			assert.False(t, hasRejection(got, decision.ReasonCustomFormatMinimumScore), "%s, %s: the score no longer refuses it: %v", name, title, reasons(got))
			assert.True(t, hasRejection(got, decision.ReasonNeverTaken), "%s, %s: %v", name, title, reasons(got))
			assert.False(t, got.Approved, "%s, %s", name, title)
		}
	}
}

// A person's override takes none of them (owner, 2026-11-27, asked whether
// an override should still take an H.264 10-bit release: "Never - nor HEVC
// 12-bit", then "close XviD"): their rejection is the one spec.override
// does not pass, where Dolby Vision's stays a person's to overrule.
func TestNoOverrideTakesWhatPlexCannotPlay(t *testing.T) {
	p, cat := builtin(t, "hd-bluray-web")
	unoverridable := func(title string) bool {
		got := decide(t, p, cat, rushmoreTarget(nil), title)
		assert.False(t, got.Approved, title)
		return slices.ContainsFunc(got.Rejections, decision.Unoverridable)
	}
	assert.True(t, unoverridable("Rushmore.1998.1080p.BluRay.10bit.x264.DTS-GROUP"))
	assert.True(t, unoverridable("Rushmore.1998.1080p.BluRay.x265.12bit.DTS-GROUP"))
	assert.True(t, unoverridable("Rushmore.1998.1080p.BluRay.XviD.AC3-GROUP"))
	assert.False(t, unoverridable("Rushmore.1998.2160p.WEB-DL.DV.HEVC-GROUP"))
}

// An H.264 10-bit file does not meet the cutoff (owner, 2026-11-03: "These
// files shouldn't meet the cutoff - or should be upgraded"): a release the
// profile takes replaces it, a lower tier included, though it is no upgrade
// over the file's quality, where the same file in 8-bit stays.
func TestAnH264TenBitFileIsReplacedByAnyReleaseTheProfileTakes(t *testing.T) {
	p, cat := builtin(t, "hd-bluray-web")
	const bluray = "Rushmore.1998.1080p.BluRay.DD5.1.x264-GRP"
	const web = "Rushmore.1998.1080p.AMZN.WEB-DL.DDP5.1.H.264-GRP"
	cur := imported(t, p, cat, bluray, "h264", common.HdrFormatNone)

	same := decide(t, p, cat, rushmoreTarget(cur), bluray)
	assert.False(t, same.Approved, "an 8-bit file at the cutoff stays: %v", reasons(same))

	cur.Undecodable = true
	same = decide(t, p, cat, rushmoreTarget(cur), bluray)
	assert.True(t, same.Approved, "%v", reasons(same))
	lower := decide(t, p, cat, rushmoreTarget(cur), web)
	assert.True(t, lower.Approved, "a lower tier the profile takes replaces it: %v", reasons(lower))
	hi10p := decide(t, p, cat, rushmoreTarget(cur), "Rushmore.1998.1080p.BluRay.10bit.x264.DTS-GROUP")
	assert.False(t, hi10p.Approved, "never by another H.264 10-bit release")
	xvid := decide(t, p, cat, rushmoreTarget(cur), "Rushmore.1998.1080p.BluRay.XviD.AC3-GROUP")
	assert.False(t, xvid.Approved, "nor by an XviD one")
}

// The mpeg4-part2 format refuses an XviD or DivX release at search, under
// every profile, as h264-10bit refuses Hi10P (owner, 2026-11-03).
func TestAnXviDReleaseIsRefused(t *testing.T) {
	p, cat := builtin(t, "hd-bluray-web")
	for _, title := range []string{
		"Rushmore.1998.1080p.BluRay.XviD.AC3-GROUP",
		"Rushmore.1998.720p.BluRay.DivX5-GRP",
		"Rushmore.1998.1080p.WEB-DL.DD5.1.xvid-GRP",
	} {
		d := decide(t, p, cat, rushmoreTarget(nil), title)
		assert.False(t, d.Approved, "%s: %v", title, reasons(d))
	}
	d := decide(t, p, cat, rushmoreTarget(nil), "Rushmore.1998.1080p.BluRay.DD5.1.x264-GRP")
	assert.True(t, d.Approved, "an 8-bit x264 Blu-ray is taken: %v", reasons(d))
}

// A file that is not the shape its series is kept in does not meet the
// cutoff (owner, 2026-11-17): a release the profile takes replaces it, the
// same or a lower tier and a transcoded file included, since only the
// release's own file says what shape it is.
func TestAFileOfTheWrongShapeIsReplacedByAnyReleaseTheProfileTakes(t *testing.T) {
	p, cat := builtin(t, "hd-bluray-web")
	const bluray = "Rushmore.1998.1080p.BluRay.DD5.1.x264-GRP"
	const web = "Rushmore.1998.1080p.AMZN.WEB-DL.DDP5.1.H.264-GRP"
	cur := imported(t, p, cat, bluray, "h264", common.HdrFormatNone)
	cur.Transcoded = true

	same := decide(t, p, cat, rushmoreTarget(cur), bluray)
	assert.False(t, same.Approved, "a transcoded file of the wanted shape is final: %v", reasons(same))

	cur.WrongAspect = true
	same = decide(t, p, cat, rushmoreTarget(cur), bluray)
	assert.True(t, same.Approved, "%v", reasons(same))
	lower := decide(t, p, cat, rushmoreTarget(cur), web)
	assert.True(t, lower.Approved, "a lower tier the profile takes may be the wanted shape: %v", reasons(lower))
	sd := decide(t, p, cat, rushmoreTarget(cur), "Rushmore.1998.DVDRip.x264-GRP")
	assert.False(t, sd.Approved, "never by a quality the profile does not take")
}
