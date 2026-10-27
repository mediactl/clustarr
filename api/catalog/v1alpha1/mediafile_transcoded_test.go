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

package v1alpha1_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

func TestMediaFileTranscoded(t *testing.T) {
	cases := []struct {
		name string
		mf   *catalogv1alpha1.MediaFile
		want bool
	}{
		{name: "no file", mf: nil, want: false},
		{name: "a fresh import: original unset (the CRD default, true), never probed", mf: &catalogv1alpha1.MediaFile{}, want: false},
		{name: "original true, no tag, but ffmpeg re-encoded the video to HEVC (Tdarr)", mf: &catalogv1alpha1.MediaFile{
			Spec:   catalogv1alpha1.MediaFileSpec{Original: new(true)},
			Status: catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{VideoCodec: "hevc", VideoEncoder: "Lavc61.3.100 hevc_qsv"}},
		}, want: true},
		{name: "an HEVC release with no encoder tag", mf: &catalogv1alpha1.MediaFile{
			Spec:   catalogv1alpha1.MediaFileSpec{Original: new(true)},
			Status: catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{VideoCodec: "hevc"}},
		}, want: false},
		{name: "original true and probed without the tag", mf: &catalogv1alpha1.MediaFile{
			Spec:   catalogv1alpha1.MediaFileSpec{Original: new(true)},
			Status: catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{VideoCodec: "h264"}},
		}, want: false},
		{name: "a transcode swap replaced it (spec.original false)", mf: &catalogv1alpha1.MediaFile{
			Spec: catalogv1alpha1.MediaFileSpec{Original: new(false)},
		}, want: true},
		{name: "a rescan found a file an earlier install transcoded (the probe read the tag)", mf: &catalogv1alpha1.MediaFile{
			Spec:   catalogv1alpha1.MediaFileSpec{Original: new(true)},
			Status: catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{TranscodeProfile: "default@abc"}},
		}, want: true},
		{name: "swapped, then re-muxed without the tag: still final", mf: &catalogv1alpha1.MediaFile{
			Spec:   catalogv1alpha1.MediaFileSpec{Original: new(false)},
			Status: catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{VideoCodec: "hevc"}},
		}, want: true},
		// replaceSource=false: the encode is a derived copy beside the kept
		// source, and status.transcode.profileTag is recorded on the
		// SOURCE's MediaFile. Its own bytes are the untouched original.
		{name: "a kept source whose derived copy was recorded is not transcoded", mf: &catalogv1alpha1.MediaFile{
			Spec: catalogv1alpha1.MediaFileSpec{Original: new(true)},
			Status: catalogv1alpha1.MediaFileStatus{
				MediaInfo: &commonv1.MediaInfo{VideoCodec: "h264"},
				Transcode: &catalogv1alpha1.TranscodeState{Phase: catalogv1alpha1.TranscodePhaseSucceeded, ProfileTag: "default@abc"},
			},
		}, want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, c.mf.Transcoded())
		})
	}
}

// An upgradeable transcode (the owner, 2026-10-27: The Sopranos' WEB-DL
// transcodes, to be replaced by Blu-rays) is still transcoded -- transcode
// never encodes it again -- but not final: catalog and import read it as the
// release it was imported from. Only "true" counts.
func TestAnUpgradeableTranscodeIsNotFinal(t *testing.T) {
	swapped := func(upgradeable string) *catalogv1alpha1.MediaFile {
		mf := &catalogv1alpha1.MediaFile{Spec: catalogv1alpha1.MediaFileSpec{Original: new(false)}}
		if upgradeable != "" {
			mf.Annotations = map[string]string{catalogv1alpha1.AnnotationUpgradeable: upgradeable}
		}
		return mf
	}
	assert.True(t, swapped("").Final())
	assert.True(t, swapped("true").Transcoded())
	assert.False(t, swapped("true").Final())
	assert.True(t, swapped("true").Upgradeable())
	assert.True(t, swapped("yes").Final(), "only \"true\" counts")
	assert.False(t, (&catalogv1alpha1.MediaFile{}).Final(), "an original is never final")
	var none *catalogv1alpha1.MediaFile
	assert.False(t, none.Final())
	assert.False(t, none.Upgradeable())
}

// A plan reads as what it does to the file, never as FFmpeg's "copy" (owner,
// 2026-10-15): a remux copies the video into the MP4 layout, its audio and
// subtitles given the layout; an encode names its encoder.
func TestATranscodePlanDescribesWhatItDoes(t *testing.T) {
	for _, tc := range []struct {
		plan *catalogv1alpha1.TranscodePlan
		want string
	}{
		{&catalogv1alpha1.TranscodePlan{Mode: catalogv1alpha1.TranscodeModeRemuxOnly, Encoder: "copy", VideoAction: "copy"}, "remux to MP4, video copied"},
		{&catalogv1alpha1.TranscodePlan{Mode: catalogv1alpha1.TranscodeModeTranscode, Encoder: "hevc_qsv", VideoAction: "encode"}, "encode with hevc_qsv"},
		{&catalogv1alpha1.TranscodePlan{Mode: catalogv1alpha1.TranscodeModeTranscode}, "encode"},
		{nil, ""},
	} {
		assert.Equal(t, tc.want, tc.plan.Describe())
	}
}
