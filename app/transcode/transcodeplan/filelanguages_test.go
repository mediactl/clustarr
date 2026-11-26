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

package transcodeplan_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
)

// The file's languages reach the standard as base tags, in the manager's
// plan as in the task the worker plans from: a plan that names an untagged
// track must hash the same in both.
func TestTheFilesLanguagesReachThePlan(t *testing.T) {
	mf := &catalogv1alpha1.MediaFile{Spec: catalogv1alpha1.MediaFileSpec{
		Path: "/data/media/anime/Show/Season 01/e.mkv", SizeBytes: 1 << 30,
		Languages: []string{"Japanese", "English", "japanese", "Klingon"},
	}}
	assert.Equal(t, []string{"ja", "en"}, transcodeplan.FileLanguages(mf), "each once, a name without a tag left out")
	assert.Nil(t, transcodeplan.FileLanguages(nil))

	st := &catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{
		Container: "mkv", VideoCodec: "h264", Width: 1920, Height: 1080, RuntimeMillis: 1_400_000,
		Audio: []commonv1.AudioStream{{Language: "jpn", Codec: "flac", Channels: 2}, {Language: "und", Codec: "truehd", Channels: 6}},
	}}
	info, err := transcodeplan.MediaInfoOf(mf.Spec.Path, mf, st, "", "ja", 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"ja", "en"}, info.Languages)
}
