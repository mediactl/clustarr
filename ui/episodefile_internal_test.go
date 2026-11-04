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

package ui

import (
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/views"
)

// The video cell names the codec as people do with its bit depth and HDR
// format, and warns a codec no GPU here decodes (H.264 10-bit, XviD).
func TestVideoCellNamesTheCodecAndWarnsAnUndecodableOne(t *testing.T) {
	for name, tc := range map[string]struct {
		mi   *commonv1.MediaInfo
		want views.Cell
	}{
		"unprobed": {mi: nil, want: views.Cell{}},
		"8-bit SDR H.264": {
			mi:   &commonv1.MediaInfo{VideoCodec: "h264", VideoBitDepth: 8, Width: 1920, Height: 1080},
			want: views.Cell{Label: "H.264", Title: "1920x1080"},
		},
		"Dolby Vision over HDR10": {
			mi:   &commonv1.MediaInfo{VideoCodec: "hevc", VideoBitDepth: 10, Hdr: commonv1.HdrFormatDolbyVisionHDR10, FpsMilli: 24000},
			want: views.Cell{Label: "HEVC 10-bit DV/HDR10", Title: "24 fps, Dolby Vision over HDR10"},
		},
		"Hi10P is undecodable": {
			mi:   &commonv1.MediaInfo{VideoCodec: "h264", VideoBitDepth: 10, Width: 1280, Height: 720},
			want: views.Cell{Label: "H.264 10-bit", Tone: "warn", Title: "no GPU here decodes H.264 10-bit, so a release of any quality the profile takes replaces it; 1280x720"},
		},
		"XviD is undecodable": {
			mi:   &commonv1.MediaInfo{VideoCodec: "mpeg4"},
			want: views.Cell{Label: "MPEG-4", Tone: "warn", Title: "no GPU here decodes MPEG-4 Part 2 (XviD, DivX), so a release of any quality the profile takes replaces it"},
		},
		"AV1 is taken": {
			mi:   &commonv1.MediaInfo{VideoCodec: "av1", VideoBitDepth: 10},
			want: views.Cell{Label: "AV1 10-bit"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := &catalogv1.MediaFile{}
			f.Status.MediaInfo = tc.mi
			assert.Equal(t, tc.want, videoCell(f))
		})
	}
}

// The transcode cell reads status.transcode's phase, Succeeded as
// Transcoded, and a tagged file without a block as Transcoded too; a file
// no transcode has reached shows nothing.
func TestTranscodeCellReadsThePhaseOrTheTag(t *testing.T) {
	plan := &catalogv1.TranscodePlan{Mode: catalogv1.TranscodeModeRemuxOnly}
	for name, tc := range map[string]struct {
		file *catalogv1.MediaFile
		want views.Cell
	}{
		"untouched": {file: &catalogv1.MediaFile{}, want: views.Cell{}},
		"running": {
			file: &catalogv1.MediaFile{Status: catalogv1.MediaFileStatus{Transcode: &catalogv1.TranscodeState{Phase: catalogv1.TranscodePhaseRunning, Plan: plan, Profile: "hevc-small-tv", Pool: "hevc-small-tv-intel"}}},
			want: views.Cell{Label: "Running", Tone: "busy", Title: "remux to MP4, video copied; profile hevc-small-tv; pool hevc-small-tv-intel"},
		},
		"succeeded": {
			file: &catalogv1.MediaFile{Status: catalogv1.MediaFileStatus{Transcode: &catalogv1.TranscodeState{Phase: catalogv1.TranscodePhaseSucceeded}}},
			want: views.Cell{Label: "Transcoded", Tone: "done"},
		},
		"failed": {
			file: &catalogv1.MediaFile{Status: catalogv1.MediaFileStatus{Transcode: &catalogv1.TranscodeState{Phase: catalogv1.TranscodePhaseFailed, Reason: "OutputTooLarge", Message: "108% of the source"}}},
			want: views.Cell{Label: "Failed", Tone: "fail", Title: "OutputTooLarge; 108% of the source"},
		},
		"tagged, no block": {
			file: &catalogv1.MediaFile{Status: catalogv1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{VideoCodec: "hevc", TranscodeProfile: "hevc-small-tv@abc"}}},
			want: views.Cell{Label: "Transcoded", Tone: "done", Title: "tagged hevc-small-tv@abc; final: no automatic grab replaces it"},
		},
		"another tool's": {
			file: &catalogv1.MediaFile{Status: catalogv1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{VideoCodec: "hevc", VideoEncoder: "Lavc60.3.100 hevc_qsv"}}},
			want: views.Cell{Label: "Transcoded", Tone: "done", Title: "transcoded by another tool (Lavc60.3.100 hevc_qsv); final: no automatic grab replaces it"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, transcodeCell(tc.file))
		})
	}
}

// The subtitles cell is the block's phase, Wanted and Searching naming
// the languages still missing; no block shows nothing.
func TestSubtitlesCellNamesTheMissingLanguages(t *testing.T) {
	for name, tc := range map[string]struct {
		st   *catalogv1.SubtitlesStatus
		want views.Cell
	}{
		"no block": {st: nil, want: views.Cell{}},
		"satisfied": {
			st:   &catalogv1.SubtitlesStatus{Phase: catalogv1.SubtitlesPhaseSatisfied, Items: []catalogv1.SubtitleItemStatus{{LangKey: "en", State: catalogv1.SubtitleStateDownloaded, Provider: "embedded"}}},
			want: views.Cell{Label: "Satisfied", Tone: "done", Title: "en: downloaded (embedded)"},
		},
		"searching two": {
			st:   &catalogv1.SubtitlesStatus{Phase: catalogv1.SubtitlesPhaseSearching, Items: []catalogv1.SubtitleItemStatus{{LangKey: "en", State: catalogv1.SubtitleStateSearching}, {LangKey: "fr", State: catalogv1.SubtitleStateUnavailable, LastError: "no results"}}},
			want: views.Cell{Label: "Searching en, fr", Tone: "busy", Title: "en: searching\nfr: unavailable: no results"},
		},
		"blocked": {
			st:   &catalogv1.SubtitlesStatus{Phase: catalogv1.SubtitlesPhaseBlocked, Reason: "NoProvider", Message: "no SubtitleProvider is Ready"},
			want: views.Cell{Label: "Blocked", Tone: "fail", Title: "NoProvider: no SubtitleProvider is Ready"},
		},
		"not wanted": {
			st:   &catalogv1.SubtitlesStatus{Phase: catalogv1.SubtitlesPhaseNotWanted},
			want: views.Cell{Label: "Not wanted"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, subtitlesCell(tc.st))
		})
	}
}

// The markers cell names the segments' kinds once each, "None found" once
// something looked, "Error" when TheIntroDB could not be asked, and
// nothing before.
func TestMarkersCellNamesTheKinds(t *testing.T) {
	for name, tc := range map[string]struct {
		m    *catalogv1.FileMarkers
		want views.Cell
	}{
		"not looked up": {m: nil, want: views.Cell{}},
		"found": {
			m: &catalogv1.FileMarkers{Result: catalogv1.MarkersFound, Segments: []catalogv1.MarkerSegment{
				{Kind: catalogv1.MarkerIntro, StartMs: 1000, EndMs: 2000, Source: catalogv1.SegmentSourceChapters},
				{Kind: catalogv1.MarkerIntro, StartMs: 3000, EndMs: 4000},
				{Kind: catalogv1.MarkerCredits, StartMs: 5000, EndMs: 6000, Source: catalogv1.SegmentSourceAnalysis},
			}},
			want: views.Cell{Label: "intro · credits", Tone: "done", Title: "intro 0:01–0:02 (chapters), intro 0:03–0:04 (TheIntroDB), credits 0:05–0:06 (detected)"},
		},
		"analysis found none": {
			m:    &catalogv1.FileMarkers{Analysis: &catalogv1.SegmentAnalysis{Result: catalogv1.MarkersNotFound}},
			want: views.Cell{Label: "None found", Title: "neither TheIntroDB nor the analysis found a segment"},
		},
		"error": {
			m:    &catalogv1.FileMarkers{Result: catalogv1.MarkersError},
			want: views.Cell{Label: "Error", Tone: "fail", Title: "TheIntroDB unavailable"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, markersCell(tc.m))
		})
	}
}
