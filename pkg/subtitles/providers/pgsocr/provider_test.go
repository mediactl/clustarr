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

package pgsocr_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/subtitles"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/pgsocr"
)

// The provider serves a file's own image sidecars: one candidate per .sup
// beside it, with the language, forced and hearing-impaired flags its name
// carries, scored as the file's own subtitle (a hash match an unverifiable
// provider is trusted on), which clears every default minimum.
func TestSearchListsOneCandidatePerImageSidecar(t *testing.T) {
	p := pgsocr.New(pgsocr.Config{Path: "/data/Movie (1999)/Movie (1999).mp4", Sidecars: []pgsocr.Sidecar{
		{Name: "Movie (1999).en.sup", LangKey: "en"},
		{Name: "Movie (1999).en.forced.sup", LangKey: "en:forced"},
		{Name: "Movie (1999).fr.sdh.sup", LangKey: "fr:hi"},
		{Name: "Movie (1999).en.srt", LangKey: "en"}, // a text sidecar is not this provider's
		{Name: "Movie (1999).sup", LangKey: ""},      // no language attributed
	}})
	cands, err := p.Search(context.Background(), subtitles.Query{Kind: common.MediaKindMovie})
	require.NoError(t, err)
	require.Len(t, cands, 3, "%+v", cands)
	assert.Equal(t, subtitles.Candidate{
		Provider: "ocr", FetchID: "Movie (1999).en.sup", Language: "en",
		Matches: map[string]bool{subtitles.MatchHash: true, subtitles.MatchHearingImpaired: true},
	}, cands[0])
	assert.True(t, cands[1].Forced)
	assert.Equal(t, "fr", cands[2].Language)
	assert.True(t, cands[2].HI)

	for _, kind := range []common.MediaKind{common.MediaKindMovie, common.MediaKindEpisode} {
		m := subtitles.CandidateMatches(kind, p.Capabilities().HashVerifiable, false, nil, cands[0].Matches)
		score, _ := subtitles.Score(kind, m)
		assert.GreaterOrEqual(t, score, subtitles.MinScore(kind, 90), kind)
	}
}

// Download reads the named .sup from the file's directory and hands it to
// the process's reader, naming the result .srt; a candidate naming anything
// but a configured sidecar is refused, and a process with no reader says so.
func TestDownloadReadsTheSidecarThroughTheReader(t *testing.T) {
	dir := t.TempDir()
	video := filepath.Join(dir, "Movie (1999).mp4")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Movie (1999).en.sup"), []byte("PG-stream"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "secret.sup"), []byte("not ours"), 0o644))

	var got []byte
	read := func(_ context.Context, sup io.Reader) ([]byte, error) {
		var err error
		got, err = io.ReadAll(sup)
		return []byte("1\n00:00:01,000 --> 00:00:02,000\nHi\n\n"), err
	}
	sidecars := []pgsocr.Sidecar{{Name: "Movie (1999).en.sup", LangKey: "en"}}
	p := pgsocr.New(pgsocr.Config{Path: video, Sidecars: sidecars, Read: read})
	cands, err := p.Search(context.Background(), subtitles.Query{})
	require.NoError(t, err)
	require.Len(t, cands, 1)

	raw, name, err := p.Download(context.Background(), cands[0])
	require.NoError(t, err)
	assert.Equal(t, "PG-stream", string(got), "the reader gets the .sup's bytes")
	assert.Equal(t, "Movie (1999).en.srt", name)
	assert.Contains(t, string(raw), "Hi")

	_, _, err = p.Download(context.Background(), subtitles.Candidate{FetchID: "secret.sup"})
	require.ErrorIs(t, err, pgsocr.ErrNotASidecar, "only a sidecar Search listed is read")
	_, _, err = p.Download(context.Background(), subtitles.Candidate{FetchID: "../Movie (1999).en.sup"})
	require.ErrorIs(t, err, pgsocr.ErrNotASidecar)

	_, _, err = pgsocr.New(pgsocr.Config{Path: video, Sidecars: sidecars}).Download(context.Background(), cands[0])
	require.ErrorIs(t, err, pgsocr.ErrNoReader)
}
