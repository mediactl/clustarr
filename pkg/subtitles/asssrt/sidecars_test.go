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

package asssrt_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/subtitles/asssrt"
)

const oneLine = "[Script Info]\n\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n" +
	"Dialogue: 0,0:00:01.00,0:00:02.00,Default,,0,0,0,,{\\i1}Hello{\\i0}\n"

func write(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

// Every .ass sidecar of a video becomes the .srt of the same name, its
// original moved under the backup directory at its relative path:
//   - one with an .srt already of its name is redundant and only moved;
//   - one beside no video of its stem (a replaced release's leftover) is
//     left;
//   - one with no text is left, never replaced by an empty .srt;
//   - nothing under .clustarr or the backup directory is walked.
func TestConvertSidecarsReplacesEveryASSSidecar(t *testing.T) {
	root := t.TempDir()
	backup := filepath.Join(root, ".clustarr", "ass-backup")
	show := filepath.Join(root, "tv", "Show (2024)", "Season 01")
	write(t, filepath.Join(show, "Show - S01E01 [AnoZu].mp4"), "video")
	write(t, filepath.Join(show, "Show - S01E01 [AnoZu].en.ass"), oneLine)
	write(t, filepath.Join(show, "Show - S01E01 [AnoZu].en.forced.ass"), oneLine)
	write(t, filepath.Join(show, "Show - S01E01 [AnoZu].ja.ass"), oneLine)
	write(t, filepath.Join(show, "Show - S01E01 [AnoZu].ja.srt"), "1\n00:00:01,000 --> 00:00:02,000\nkeep me\n")
	write(t, filepath.Join(show, "Show - S01E01 [Kitsune].en.ass"), oneLine)
	write(t, filepath.Join(show, "Show - S01E02.mkv"), "video")
	write(t, filepath.Join(show, "Show - S01E02.en.ass"), "[Script Info]\n\n[Events]\nFormat: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")
	write(t, filepath.Join(root, ".clustarr", "donors", "x.mkv"), "video")
	write(t, filepath.Join(root, ".clustarr", "donors", "x.en.ass"), oneLine)

	rep, err := asssrt.ConvertSidecars(root, backup, false)
	require.NoError(t, err)
	assert.Equal(t, 2, rep.Converted)
	assert.Equal(t, 1, rep.Redundant)
	assert.Equal(t, 1, rep.Orphans)
	require.Len(t, rep.Failed, 1)
	assert.Contains(t, rep.Failed[0], "Show - S01E02.en.ass")

	b, err := os.ReadFile(filepath.Join(show, "Show - S01E01 [AnoZu].en.srt"))
	require.NoError(t, err)
	assert.Equal(t, "1\n00:00:01,000 --> 00:00:02,000\nHello\n\n", string(b))
	assert.True(t, exists(filepath.Join(show, "Show - S01E01 [AnoZu].en.forced.srt")))
	assert.False(t, exists(filepath.Join(show, "Show - S01E01 [AnoZu].en.ass")), "the original is moved away")
	assert.True(t, exists(filepath.Join(backup, "tv", "Show (2024)", "Season 01", "Show - S01E01 [AnoZu].en.ass")), "and kept under the backup")
	b, err = os.ReadFile(filepath.Join(show, "Show - S01E01 [AnoZu].ja.srt"))
	require.NoError(t, err)
	assert.Contains(t, string(b), "keep me", "an existing .srt is never overwritten")
	assert.False(t, exists(filepath.Join(show, "Show - S01E01 [AnoZu].ja.ass")), "its redundant .ass is moved away")
	assert.True(t, exists(filepath.Join(show, "Show - S01E01 [Kitsune].en.ass")), "a leftover of no video is left")
	assert.True(t, exists(filepath.Join(show, "Show - S01E02.en.ass")), "an empty script is left")
	assert.False(t, exists(filepath.Join(show, "Show - S01E02.en.srt")))
	assert.True(t, exists(filepath.Join(root, ".clustarr", "donors", "x.en.ass")), ".clustarr is never walked")

	again, err := asssrt.ConvertSidecars(root, backup, false)
	require.NoError(t, err)
	assert.Zero(t, again.Converted+again.Redundant, "a second run finds nothing to do")
}

// A dry run reports and changes nothing.
func TestConvertSidecarsDryRunChangesNothing(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "Film (2020)", "Film (2020).mp4"), "video")
	write(t, filepath.Join(root, "Film (2020)", "Film (2020).en.ass"), oneLine)
	rep, err := asssrt.ConvertSidecars(root, filepath.Join(root, ".clustarr", "b"), true)
	require.NoError(t, err)
	assert.Equal(t, 1, rep.Converted)
	assert.True(t, exists(filepath.Join(root, "Film (2020)", "Film (2020).en.ass")))
	assert.False(t, exists(filepath.Join(root, "Film (2020)", "Film (2020).en.srt")))
}
