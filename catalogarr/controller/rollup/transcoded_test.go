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

package rollup_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/rollup"
)

func TestTranscoded(t *testing.T) {
	cases := []struct {
		name string
		mf   *catalogv1alpha1.MediaFile
		want bool
	}{
		{name: "no file", mf: nil, want: false},
		{name: "a fresh import: original unset (the CRD default, true), never probed", mf: &catalogv1alpha1.MediaFile{}, want: false},
		{name: "original true and probed without the tag", mf: &catalogv1alpha1.MediaFile{
			Spec:   catalogv1alpha1.MediaFileSpec{Original: ptr.To(true)},
			Status: catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{VideoCodec: "h264"}},
		}, want: false},
		{name: "a transcode swap replaced it (spec.original false)", mf: &catalogv1alpha1.MediaFile{
			Spec: catalogv1alpha1.MediaFileSpec{Original: ptr.To(false)},
		}, want: true},
		{name: "a rescan found a file an earlier install transcoded (the probe read the tag)", mf: &catalogv1alpha1.MediaFile{
			Spec:   catalogv1alpha1.MediaFileSpec{Original: ptr.To(true)},
			Status: catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{TranscodeProfile: "default@abc"}},
		}, want: true},
		{name: "swapped, then re-muxed without the tag: still final", mf: &catalogv1alpha1.MediaFile{
			Spec:   catalogv1alpha1.MediaFileSpec{Original: ptr.To(false)},
			Status: catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{VideoCodec: "hevc"}},
		}, want: true},
		// replaceSource=false: the encode is a derived copy beside the kept
		// source, and status.transcode.profileTag is recorded on the
		// SOURCE's MediaFile. Its own bytes are the untouched original.
		{name: "a kept source whose derived copy was recorded is not transcoded", mf: &catalogv1alpha1.MediaFile{
			Spec: catalogv1alpha1.MediaFileSpec{Original: ptr.To(true)},
			Status: catalogv1alpha1.MediaFileStatus{
				MediaInfo: &commonv1.MediaInfo{VideoCodec: "h264"},
				Transcode: &catalogv1alpha1.TranscodeState{ProfileTag: "default@abc", LastResult: catalogv1alpha1.TranscodeResultSucceeded},
			},
		}, want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, rollup.Transcoded(c.mf))
			if c.mf != nil {
				assert.Equal(t, c.want, rollup.TranscodedObject(c.mf), "the predicate extractor must agree")
			}
		})
	}
	assert.False(t, rollup.TranscodedObject(&catalogv1alpha1.Movie{}), "any other type is not a transcoded file")
}
