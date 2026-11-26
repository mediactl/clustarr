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

package v1alpha1

import (
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// TruncatedMinimum is the least a video file may end before its own
// chapters do and be judged cut short (TruncatedBy); it must also be over a
// fiftieth of the chapters' length.
const TruncatedMinimum = time.Minute

// TruncatedBy is how much of a file is missing when it ends before its own
// chapters do, zero when it does not: runtimeMillis is the file's probed
// length and chapters its chapter list, whose last end is where the film it
// was made from ended. A transcode writes what it was given under the
// source's chapters, so a source cut short reads so afterwards: on talos
// (2026-11-25) The Drama ran 28 of its chapters' 106 minutes, Finding Nemo
// 67 of 101 and Eighth Grade 76 of 94, each a finished transcode, where the
// four films shorter than their listed runtime for being another cut ended
// with their chapters. Under TruncatedMinimum, or a fiftieth of the
// chapters' length, is no verdict: a last chapter may outrun the streams by
// seconds.
func TruncatedBy(runtimeMillis int64, chapters []commonv1.Chapter) time.Duration {
	if runtimeMillis <= 0 {
		return 0
	}
	var end int64
	for i := range chapters {
		end = max(end, chapters[i].EndMillis)
	}
	missing := time.Duration(end-runtimeMillis) * time.Millisecond
	if missing <= TruncatedMinimum || missing*50 <= time.Duration(end)*time.Millisecond {
		return 0
	}
	return missing
}

// TruncatedBy is how much mf's probed video ends before its own chapters do
// (TruncatedBy), zero for one that ends with them, one with no video or
// chapters, and a nil or unprobed mf. It is the file's own sign of a
// download cut short and not yet a verdict: a chapter list can outrun a
// whole film (CutShort).
func (mf *MediaFile) TruncatedBy() time.Duration {
	if mf == nil || mf.Status.MediaInfo == nil {
		return 0
	}
	mi := mf.Status.MediaInfo
	if mi.VideoCodec == "" {
		return 0
	}
	return TruncatedBy(mi.RuntimeMillis, mi.ChapterList)
}

// CutShort is how much of mf is missing when its download was cut short,
// zero when it was not: the file ends before its own chapters do
// (TruncatedBy) and runs more than a twentieth under listed, the runtime
// its item is listed with. Each sign alone misleads. Of talos' 1,279 movie
// files (2026-11-25) the chapters outran five: The Drama, Finding Nemo and
// Eighth Grade, at 27, 67 and 80 percent of their listed runtime, and Toy
// Story 2 and Pastoral, whole files at their listed runtime under chapter
// lists six and ten minutes too long. And four films ran well under their
// listed runtime for being another cut, ending with their chapters. A
// listed runtime that is unknown gives no verdict. Such a file does not
// meet the cutoff whatever its quality, a transcode included, and a release
// of any quality the profile takes replaces it.
func (mf *MediaFile) CutShort(listed time.Duration) time.Duration {
	missing := mf.TruncatedBy()
	if missing == 0 || listed <= 0 {
		return 0
	}
	if runtime := time.Duration(mf.Status.MediaInfo.RuntimeMillis) * time.Millisecond; runtime*20 >= listed*19 {
		return 0
	}
	return missing
}
