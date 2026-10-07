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

package fsops_test

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

func TestSidecarPath(t *testing.T) {
	for _, tc := range []struct{ video, suffix, want string }{
		{"/m/Show - S01E01.mp4", "en.ass", "/m/Show - S01E01.en.ass"},
		{"/m/Show - S01E01.mp4", "en.forced.srt", "/m/Show - S01E01.en.forced.srt"},
		{"/m/Show - S01E01.mp4", "ass", "/m/Show - S01E01.ass"},
		// A part's sidecar is itself a part: <stem>.<lang...>.part-<uid8>-<n>.<ext>.
		{"/m/Show - S01E01.part-ab12cd34-2.mp4", "en.ass", "/m/Show - S01E01.en.part-ab12cd34-2.ass"},
		{"/m/Show - S01E01.part-ab12cd34-2.mp4", "en.forced.srt", "/m/Show - S01E01.en.forced.part-ab12cd34-2.srt"},
		{"/m/Show - S01E01.part-ab12cd34-2.mp4", "ass", "/m/Show - S01E01.part-ab12cd34-2.ass"},
	} {
		assert.Equal(t, tc.want, fsops.SidecarPath(tc.video, tc.suffix), "%s + %s", tc.video, tc.suffix)
	}
}

// A sidecar part is a part to every sweep (IsPart, ParseTranscodePart) and
// no sidecar to the subtitle scanner.
func TestASidecarPartIsAPartAndNoSidecar(t *testing.T) {
	p := fsops.SidecarPath("/m/Show.part-ab12cd34-2.mp4", "en.ass")
	assert.True(t, fsops.IsPart(p))
	tp, ok := fsops.ParseTranscodePart(p)
	require.True(t, ok)
	assert.Equal(t, "/m/Show.en", tp.Stem)
	assert.Equal(t, "ab12cd34", tp.JobUID8)
	assert.Equal(t, 2, tp.Attempt)
	assert.True(t, fsops.SubtitleExt(tp.Ext))
	assert.False(t, fsops.SubtitleExt(".mp4"))
	_, isSidecar := subtitles.ParseSidecar("Show", filepath.Base(p))
	assert.False(t, isSidecar)
	// The final name is a sidecar of the video.
	key, isSidecar := subtitles.ParseSidecar("Show", filepath.Base(fsops.SidecarPath("/m/Show.mp4", "en.forced.srt")))
	assert.True(t, isSidecar)
	assert.Contains(t, string(key), "forced")
}
