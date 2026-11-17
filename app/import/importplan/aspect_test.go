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
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/quality"
)

// A series is kept in one shape (owner, 2026-11-17). A release's shape is
// known when its file is probed, so import holds it: a file of the wrong
// shape replaces no file and is the release's fault, a person's pick
// exempt; a file of the wanted shape replaces one that is not, at any
// quality the profile takes, a transcode included, past the upgrade table;
// and an episode with no file takes whatever came.
func TestAnImportHoldsAFileToTheShapeItsSeriesIsKeptIn(t *testing.T) {
	web, ok := quality.Lookup("video", "WEBDL-1080p")
	assert.True(t, ok)
	bluray, ok := quality.Lookup("video", "Bluray-1080p")
	assert.True(t, ok)
	prof := quality.Profile{
		Tiers: [][]quality.Definition{{web}, {bluray}}, CutoffIndex: 1, UpgradeAllowed: true, CutoffFormatScore: 10000,
		ProperPolicy: "preferAndUpgrade", LanguageName: "any",
	}
	const ep = "seinfeld-s09e01"
	existing := func(milli int32, transcoded bool) catalogv1alpha1.MediaFile {
		mi := &commonv1.MediaInfo{VideoCodec: "hevc", VideoBitDepth: 8, RuntimeMillis: 1_380_000, DisplayAspectMilli: milli}
		if transcoded {
			mi.TranscodeProfile = "hevc-mkv@abc"
		}
		return catalogv1alpha1.MediaFile{
			Spec: catalogv1alpha1.MediaFileSpec{
				Quality: bluray.Quality, SizeBytes: 400_000_000,
				MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: ep},
			},
			Status: catalogv1alpha1.MediaFileStatus{MediaInfo: mi},
		}
	}
	q := web.Quality
	incoming := func(milli int32) candidate {
		return candidate{kind: commonv1.MediaKindEpisode, keys: []string{ep}, f: schema.InspectedFile{
			SizeBytes: 1_000_000_000,
			Frozen:    schema.FrozenFields{Quality: &q},
			Probe:     &schema.ProbeSummary{VideoCodec: "h264", VideoBitDepth: 8, DurationSeconds: 1380, DisplayAspectMilli: milli},
		}}
	}
	planner := func(want catalogv1alpha1.AspectRatio) *planner {
		in := Input{Profile: &prof}
		if want != "" {
			in.WantedAspect = map[string]catalogv1alpha1.AspectRatio{ItemKey(commonv1.MediaKindEpisode, ep): want}
		}
		return &planner{in: in}
	}
	wide, full := catalogv1alpha1.AspectRatioWidescreen, catalogv1alpha1.AspectRatioFullscreen
	files := func(mfs ...catalogv1alpha1.MediaFile) []catalogv1alpha1.MediaFile { return mfs }

	// The series is kept 4:3 and holds the cropped widescreen remaster,
	// transcoded: a 4:3 release of a lower tier replaces it.
	_, refused := planner(full).gate("f.mkv", incoming(1333), files(existing(1778, true)), false)
	assert.False(t, refused, "the wanted shape replaces a transcoded file of the wrong one, a lower tier included")

	// Another widescreen release does not, and is the release's fault.
	r, refused := planner(full).gate("f.mkv", incoming(1778), files(existing(1778, true)), false)
	assert.True(t, refused)
	assert.Equal(t, commonv1.ImportClassReleaseFault, r.Class)
	assert.Contains(t, r.Text, "widescreen (1.78:1)")
	assert.Contains(t, r.Text, "kept 4:3")

	// Nor does a wrong-shaped file replace one of the wanted shape,
	// whatever it would upgrade.
	r, refused = planner(wide).gate("f.mkv", incoming(1333), files(existing(1778, false)), false)
	assert.True(t, refused)
	assert.Equal(t, commonv1.ImportClassReleaseFault, r.Class)
	assert.Contains(t, r.Text, "4:3 (1.33:1)")

	// A person's pick is never refused for its shape.
	r, refused = planner(wide).gate("f.mkv", incoming(1333), files(existing(1778, false)), true)
	assert.False(t, refused, "%s", r.Text)

	// An episode with no file takes the file that came.
	_, refused = planner(wide).gate("f.mkv", incoming(1333), nil, false)
	assert.False(t, refused, "a wrong shape is better than no file")

	// Two files of the wanted shape are judged as ever: the transcode is
	// final.
	r, refused = planner(wide).gate("f.mkv", incoming(1778), files(existing(1778, true)), false)
	assert.True(t, refused, "the shape rule replaces nothing between two files of the wanted shape")
	assert.True(t, r.Transcoded)

	// A file whose shape the probe did not read is judged by none of it,
	// either side; nor is any under "any" or for a kind with no setting.
	r, refused = planner(full).gate("f.mkv", incoming(0), files(existing(1778, true)), false)
	assert.True(t, refused)
	assert.True(t, r.Transcoded, "an incoming file of unknown shape replaces nothing by shape: %s", r.Text)
	r, refused = planner(full).gate("f.mkv", incoming(1333), files(existing(0, true)), false)
	assert.True(t, refused)
	assert.True(t, r.Transcoded, "an existing file of unknown shape is not replaced by shape: %s", r.Text)
	r, refused = planner(catalogv1alpha1.AspectRatioAny).gate("f.mkv", incoming(1333), files(existing(1778, true)), false)
	assert.True(t, refused)
	assert.True(t, r.Transcoded)
	r, refused = planner("").gate("f.mkv", incoming(1333), files(existing(1778, true)), false)
	assert.True(t, refused)
	assert.True(t, r.Transcoded)
}

// A series that crops (spec.aspectRatioFix crop; owner, 2026-11-17: "for
// some shows, we should prefer the high quality and crop ourselves during
// transcode", and a transcoded file of the wrong shape is downloaded
// again and the new file cropped): a release of either shape replaces a
// wrong-shaped transcode, since its own transcode cuts it; a wrong-shaped
// file that is a transcode already is refused, being final; and a held
// file of the wrong shape that is not yet a transcode is not wrong, so
// nothing replaces it past the upgrade table.
func TestASeriesThatCropsTakesEitherShapeInPlaceOfAWrongShapedTranscode(t *testing.T) {
	web, ok := quality.Lookup("video", "WEBDL-1080p")
	assert.True(t, ok)
	bluray, ok := quality.Lookup("video", "Bluray-1080p")
	assert.True(t, ok)
	prof := quality.Profile{
		Tiers: [][]quality.Definition{{web}, {bluray}}, CutoffIndex: 1, UpgradeAllowed: true, CutoffFormatScore: 10000,
		ProperPolicy: "preferAndUpgrade", LanguageName: "any",
	}
	const ep = "seinfeld-s09e01"
	existing := func(milli int32, transcoded bool) []catalogv1alpha1.MediaFile {
		mi := &commonv1.MediaInfo{VideoCodec: "hevc", VideoBitDepth: 8, RuntimeMillis: 1_380_000, DisplayAspectMilli: milli}
		if transcoded {
			mi.TranscodeProfile = "hevc-mkv@abc"
		}
		return []catalogv1alpha1.MediaFile{{
			Spec: catalogv1alpha1.MediaFileSpec{
				Quality: bluray.Quality, SizeBytes: 400_000_000,
				MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: ep},
			},
			Status: catalogv1alpha1.MediaFileStatus{MediaInfo: mi},
		}}
	}
	q := web.Quality
	incoming := func(milli int32, transcoded bool) candidate {
		return candidate{kind: commonv1.MediaKindEpisode, keys: []string{ep}, f: schema.InspectedFile{
			SizeBytes: 1_000_000_000,
			Frozen:    schema.FrozenFields{Quality: &q},
			Probe: &schema.ProbeSummary{
				VideoCodec: "h264", VideoBitDepth: 8, DurationSeconds: 1380, DisplayAspectMilli: milli, Transcoded: transcoded,
			},
		}}
	}
	wide := catalogv1alpha1.AspectRatioWidescreen
	planner := func(crops bool) *planner {
		return &planner{in: Input{
			Profile: &prof, CropsAspect: crops,
			WantedAspect: map[string]catalogv1alpha1.AspectRatio{ItemKey(commonv1.MediaKindEpisode, ep): wide},
		}}
	}

	// The 4:3 transcode the library holds gives way to a 4:3 original of
	// a lower tier, which the series' transcode will cut to 16:9.
	r, refused := planner(true).gate("f.mkv", incoming(1333, false), existing(1333, true), false)
	assert.False(t, refused, "%s", r.Text)
	// A series that does not crop refuses the same file.
	r, refused = planner(false).gate("f.mkv", incoming(1333, false), existing(1333, true), false)
	assert.True(t, refused)
	assert.Equal(t, commonv1.ImportClassReleaseFault, r.Class)

	// A release that is itself a transcode of the wrong shape is final,
	// so it cannot be cropped: refused as the release's fault.
	r, refused = planner(true).gate("f.mkv", incoming(1333, true), existing(1333, true), false)
	assert.True(t, refused)
	assert.Equal(t, commonv1.ImportClassReleaseFault, r.Class)
	assert.Contains(t, r.Text, "4:3 (1.33:1)")

	// A widescreen release still replaces the wrong-shaped transcode.
	r, refused = planner(true).gate("f.mkv", incoming(1778, false), existing(1333, true), false)
	assert.False(t, refused, "%s", r.Text)

	// A held 4:3 file not yet transcoded waits for its crop: it is not
	// wrong, so a lower tier does not replace it by shape, whichever
	// shape came. Without the crop the widescreen one would.
	for _, milli := range []int32{1333, 1778} {
		r, refused = planner(true).gate("f.mkv", incoming(milli, false), existing(1333, false), false)
		assert.True(t, refused, "%d", milli)
		assert.NotEqual(t, commonv1.ImportClassReleaseFault, r.Class, "%d: an ordinary refusal by the upgrade table, not the release's fault: %s", milli, r.Text)
	}
	_, refused = planner(false).gate("f.mkv", incoming(1778, false), existing(1333, false), false)
	assert.False(t, refused, "a series that replaces takes the widescreen file over the 4:3 one")

	// And a right-shaped held file is kept from a wrong-shaped one only
	// by the table: no release is blocklisted for a shape a crop mends.
	r, refused = planner(true).gate("f.mkv", incoming(1333, false), existing(1778, false), false)
	assert.True(t, refused)
	assert.NotEqual(t, commonv1.ImportClassReleaseFault, r.Class, "%s", r.Text)
}
