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

package execextract_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/subtitles"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/embedded"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/embedded/execextract"
)

func skipIfNoFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
}

// The embedded provider wired to execextract extracts a real SubRip stream,
// as the provider did when it ran ffmpeg itself.
func TestExtractPullsTheStreamWithFFmpeg(t *testing.T) {
	skipIfNoFFmpeg(t)
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "sample.mkv")
	srtPath := filepath.Join(dir, "in.srt")
	require.NoError(t, os.WriteFile(srtPath, []byte("1\n00:00:00,000 --> 00:00:01,000\nHello.\n"), 0o644))
	out, err := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "color=c=black:s=64x64:d=1",
		"-i", srtPath, "-c:v", "libx264", "-c:s", "srt", "-shortest", mediaPath).CombinedOutput()
	require.NoError(t, err, "fixture generation must succeed for this test to mean anything: %s", out)

	info := common.MediaInfo{Subtitles: []common.SubtitleStream{{Index: 1, Codec: "subrip", Language: "eng"}}}
	p := embedded.New(embedded.Config{Path: mediaPath, Info: info, Extract: execextract.New("")})
	raw, name, err := p.Download(context.Background(), subtitles.Candidate{FetchID: "1"})
	require.NoError(t, err)
	assert.Contains(t, string(raw), "Hello.")
	assert.Equal(t, "stream-1.srt", name)
}

func TestExtractFailsWithoutPanickingWhenTheMediaFileDoesNotExist(t *testing.T) {
	skipIfNoFFmpeg(t)
	extract := execextract.New("")
	var raw []byte
	var err error
	require.NotPanics(t, func() {
		raw, err = extract(context.Background(), filepath.Join(t.TempDir(), "no-such-file.mkv"), 0)
	})
	assert.Error(t, err)
	assert.Nil(t, raw)
}
