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

package remediation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/mediafile"
)

// The first swap of an original records the size it replaces as
// spec.sourceSizeBytes, which the profile's bitrateLimits judge the file by;
// a later swap keeps it, a file transcoded before it was recorded gets none,
// and an original carrying one -- a MediaFile import re-pointed at a new
// file -- is applied without it so the field is released (2026-10-27).
func TestASwapRecordsTheSourceSize(t *testing.T) {
	draft := &catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{VideoCodec: "hevc", RuntimeMillis: 2_580_000}}
	file := func(original bool, size, source int64) *catalogv1alpha1.MediaFile {
		mf := &catalogv1alpha1.MediaFile{ObjectMeta: metav1.ObjectMeta{Name: "s01e03", Namespace: "media"}}
		mf.Spec.Path, mf.Spec.SizeBytes, mf.Spec.SourceSizeBytes = "/data/tv/S01E03.mkv", size, source
		mf.Spec.Original = &original
		return mf
	}
	swap := &MainIntent{Path: "/data/tv/S01E03.mp4", SizeBytes: 400_000_000, Swap: true}
	ac, send := renderMain(file(true, 1_200_000_000, 0), draft, swap)
	require.True(t, send)
	require.NotNil(t, ac.Spec.SourceSizeBytes)
	assert.Equal(t, int64(1_200_000_000), *ac.Spec.SourceSizeBytes, "the original's size")

	ac, _ = renderMain(file(false, 400_000_000, 1_200_000_000), draft, &MainIntent{Path: "/data/tv/S01E03.mp4", SizeBytes: 300_000_000, Swap: true})
	require.NotNil(t, ac.Spec.SourceSizeBytes)
	assert.Equal(t, int64(1_200_000_000), *ac.Spec.SourceSizeBytes, "a later swap keeps the first source")

	ac, _ = renderMain(file(false, 175_000_000, 0), draft, swap)
	assert.Nil(t, ac.Spec.SourceSizeBytes, "a transcode's size is no source")

	// Its labels already current, an original is applied only to release a
	// source size it still carries.
	settled := func(source int64) *catalogv1alpha1.MediaFile {
		mf := file(true, 900_000_000, source)
		mf.Labels = mediafile.MirrorLabels(mf.Spec.MediaRef.Kind, mf.Spec.Quality, draft.MediaInfo, true)
		return mf
	}
	_, send = renderMain(settled(0), draft, nil)
	assert.False(t, send, "nothing to send")
	ac, send = renderMain(settled(1_200_000_000), draft, nil)
	assert.True(t, send, "a re-pointed original is applied")
	assert.Nil(t, ac.Spec, "without the field, releasing it")
}
