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
	"time"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

func chapters(ends ...int64) []commonv1.Chapter {
	out := make([]commonv1.Chapter, len(ends))
	var start int64
	for i, e := range ends {
		out[i] = commonv1.Chapter{StartMillis: start, EndMillis: e}
		start = e
	}
	return out
}

// The three films talos held cut short (2026-11-25), by their probes, and
// the files that are whole.
func TestAFileThatEndsBeforeItsChaptersIsTruncated(t *testing.T) {
	for name, tc := range map[string]struct {
		runtime  int64
		chapters []commonv1.Chapter
		want     time.Duration
	}{
		"The Drama: 28 of 106 minutes":                          {1689392, chapters(699693, 1701611, 6133627, 6339760), 4650368 * time.Millisecond},
		"Finding Nemo: 67 of 101 minutes":                       {4008000, chapters(3000000, 6048000), 2040 * time.Second},
		"a whole film ends with its chapters":                   {6828000, chapters(3000000, 6828000), 0},
		"a last chapter seconds past the end":                   {6800000, chapters(3000000, 6828000), 0},
		"a minute and a half of four hours is under a fiftieth": {14310000, chapters(14400000), 0},
		"over a fiftieth but under a minute":                    {600000, chapters(650000), 0},
		"two minutes of ninety":                                 {5280000, chapters(5400000), 2 * time.Minute},
		"no chapters":                                           {1689392, nil, 0},
		"an unread length":                                      {0, chapters(6339760), 0},
		"a file longer than its chapters":                       {7000000, chapters(6339760), 0},
	} {
		assert.Equal(t, tc.want, catalogv1alpha1.TruncatedBy(tc.runtime, tc.chapters), name)
	}
}

// Only a probed file with video is judged: an audiobook's chapters are no
// film's.
func TestOnlyAProbedVideoEndsBeforeItsChapters(t *testing.T) {
	var none *catalogv1alpha1.MediaFile
	assert.Zero(t, none.TruncatedBy())
	assert.Zero(t, none.CutShort(105*time.Minute))
	mf := &catalogv1alpha1.MediaFile{}
	assert.Zero(t, mf.TruncatedBy(), "unprobed")
	assert.Zero(t, mf.CutShort(105*time.Minute), "unprobed")
	mf.Status.MediaInfo = &commonv1.MediaInfo{RuntimeMillis: 1689392, ChapterList: chapters(6339760)}
	assert.Zero(t, mf.TruncatedBy(), "no video")
	mf.Status.MediaInfo.VideoCodec = "hevc"
	assert.Equal(t, 4650368*time.Millisecond, mf.TruncatedBy())
}

// A download cut short shows twice: the file ends before its chapters do,
// and it runs under its item's listed runtime. talos' movies (2026-11-25):
// three that were, two whole files under chapter lists too long, and a
// shorter cut that ends with its chapters.
func TestADownloadCutShortEndsBeforeItsChaptersAndUnderItsListedRuntime(t *testing.T) {
	file := func(runtime int64, ends ...int64) *catalogv1alpha1.MediaFile {
		return &catalogv1alpha1.MediaFile{Status: catalogv1alpha1.MediaFileStatus{MediaInfo: &commonv1.MediaInfo{
			VideoCodec: "hevc", RuntimeMillis: runtime, ChapterList: chapters(ends...),
		}}}
	}
	for name, tc := range map[string]struct {
		mf     *catalogv1alpha1.MediaFile
		listed time.Duration
		short  bool
	}{
		"The Drama: 28 minutes, chapters to 106, listed 105":     {file(1_689_392, 699_693, 6_339_760), 105 * time.Minute, true},
		"Finding Nemo: 67 minutes, chapters to 101, listed 100":  {file(4_008_000, 6_048_000), 100 * time.Minute, true},
		"Eighth Grade: 76 minutes, chapters to 94, listed 94":    {file(4_530_000, 5_616_000), 94 * time.Minute, true},
		"Toy Story 2: whole at 92, chapters to 98":               {file(5_544_000, 5_148_000, 5_892_000), 92 * time.Minute, false},
		"Pastoral: whole at 101, chapters to 111":                {file(6_054_000, 6_462_000, 6_678_000), 101 * time.Minute, false},
		"another cut: 108 of a listed 135, ending with chapters": {file(6_504_000, 6_504_000), 135 * time.Minute, false},
		"under the listed runtime by less than a twentieth":      {file(5_520_000, 5_892_000), 95 * time.Minute, false},
		"a runtime nobody listed":                                {file(1_689_392, 6_339_760), 0, false},
	} {
		assert.Equal(t, tc.short, tc.mf.CutShort(tc.listed) > 0, name)
	}
	assert.Equal(t, 4650368*time.Millisecond, file(1_689_392, 6_339_760).CutShort(105*time.Minute), "what is missing is the chapters' measure")
}
