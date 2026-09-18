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

package embedded_test

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
)

func TestSearchReturnsOneCandidatePerEligibleTextStream(t *testing.T) {
	info := common.MediaInfo{
		Subtitles: []common.SubtitleStream{
			{Index: 2, Codec: "subrip", Language: "eng", Forced: true},
			{Index: 3, Codec: "subrip", Language: "eng", HearingImpaired: true},
			{Index: 4, Codec: "hdmv_pgs_subtitle", Language: "eng"}, // bitmap, must be skipped
			{Index: 5, Codec: "subrip", Language: "eng", Title: "Commentary"},
		},
	}
	p := embedded.New(embedded.Config{Path: "/data/movie.mkv", Info: info, SkipCommentary: true})

	cands, err := p.Search(context.Background(), subtitles.Query{Kind: "movie", Languages: []subtitles.LangKey{"en", "en:forced", "en:hi"}})
	require.NoError(t, err)
	require.Len(t, cands, 2, "PGS (bitmap) and the commentary track must be excluded")

	for _, c := range cands {
		assert.Equal(t, "embedded", c.Provider)
		assert.True(t, c.Matches[subtitles.MatchHash], "embedded candidates score as fully matched, research note §4.6")
		assert.True(t, c.Matches[subtitles.MatchHearingImpaired])
	}
}

func TestSearchExcludesBitmapSubtitleCodecsRegardlessOfIgnoreFlags(t *testing.T) {
	info := common.MediaInfo{Subtitles: []common.SubtitleStream{{Index: 1, Codec: "dvd_subtitle", Bitmap: true}}}
	p := embedded.New(embedded.Config{Info: info}) // IgnoreVobSub defaults false, so it's still excluded — bitmap is always excluded
	cands, err := p.Search(context.Background(), subtitles.Query{Kind: "movie", Languages: []subtitles.LangKey{"en"}})
	require.NoError(t, err)
	assert.Empty(t, cands, "bitmap subtitle codecs are never text-extractable, regardless of Ignore* flags")
}

func TestSearchHonoursIgnoreASSFlag(t *testing.T) {
	info := common.MediaInfo{Subtitles: []common.SubtitleStream{
		{Index: 1, Codec: "ass", Language: "eng"},
		{Index: 2, Codec: "subrip", Language: "eng"},
	}}
	p := embedded.New(embedded.Config{Info: info, IgnoreASS: true})
	cands, err := p.Search(context.Background(), subtitles.Query{Kind: "movie", Languages: []subtitles.LangKey{"en"}})
	require.NoError(t, err)
	require.Len(t, cands, 1)
	assert.Equal(t, "2", cands[0].FetchID)
}

func TestDownloadExtractsTheStreamViaFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	dir := t.TempDir()
	mediaPath := filepath.Join(dir, "sample.mkv")

	// Build a tiny real MKV with one burned-in SubRip stream via ffmpeg's
	// lavfi source generator.
	srtPath := filepath.Join(dir, "in.srt")
	require.NoError(t, os.WriteFile(srtPath, []byte("1\n00:00:00,000 --> 00:00:01,000\nHello.\n"), 0o644))
	cmd := exec.Command("ffmpeg", "-y", "-f", "lavfi", "-i", "color=c=black:s=64x64:d=1",
		"-i", srtPath, "-c:v", "libx264", "-c:s", "srt", "-shortest", mediaPath)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "fixture generation must succeed for this test to mean anything: %s", out)

	info := common.MediaInfo{Subtitles: []common.SubtitleStream{{Index: 1, Codec: "subrip", Language: "eng"}}}
	p := embedded.New(embedded.Config{Path: mediaPath, Info: info})

	raw, name, err := p.Download(context.Background(), subtitles.Candidate{FetchID: "1"})
	require.NoError(t, err)
	assert.Contains(t, string(raw), "Hello.")
	assert.NotEmpty(t, name)
}

func TestDownloadRejectsANonIntegerFetchID(t *testing.T) {
	p := embedded.New(embedded.Config{Path: "/data/movie.mkv"})
	_, _, err := p.Download(context.Background(), subtitles.Candidate{FetchID: "not-a-number"})
	assert.Error(t, err)
}
