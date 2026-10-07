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
	"errors"
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

// TestEmbeddedCandidatesClearEveryDefaultMinimumScore runs a candidate
// through the scoring path a caller actually uses -- CandidateMatches with
// the provider's own Capabilities().HashVerifiable, then Score -- rather than
// asserting on Matches alone, which a hash-verifiable claim silently
// undoes. research note §4.6: embedded candidates are "full score".
func TestEmbeddedCandidatesClearEveryDefaultMinimumScore(t *testing.T) {
	info := common.MediaInfo{Subtitles: []common.SubtitleStream{{Index: 2, Codec: "subrip", Language: "eng"}}}
	p := embedded.New(embedded.Config{Path: "/data/movie.mkv", Info: info})

	for _, tc := range []struct {
		kind common.MediaKind
		pct  int // SubtitleProfileSpec.MinScorePercent defaults
	}{
		{common.MediaKindMovie, 70},
		{common.MediaKindEpisode, 90},
	} {
		cands, err := p.Search(context.Background(), subtitles.Query{Kind: tc.kind})
		require.NoError(t, err)
		require.Len(t, cands, 1)

		matches := subtitles.CandidateMatches(tc.kind, p.Capabilities().HashVerifiable, false, nil, cands[0].Matches)
		score, _ := subtitles.Score(tc.kind, matches)
		assert.GreaterOrEqual(t, score, subtitles.MinScore(tc.kind, tc.pct),
			"%s: an embedded track must reach the default minimum score", tc.kind)
	}
}

func TestSearchExcludesBitmapSubtitleCodecsRegardlessOfIgnoreFlags(t *testing.T) {
	info := common.MediaInfo{Subtitles: []common.SubtitleStream{{Index: 1, Codec: "dvd_subtitle", Bitmap: true}}}
	p := embedded.New(embedded.Config{Info: info})
	cands, err := p.Search(context.Background(), subtitles.Query{Kind: "movie", Languages: []subtitles.LangKey{"en"}})
	require.NoError(t, err)
	assert.Empty(t, cands, "bitmap subtitle codecs are never text-extractable — there is no Ignore* knob for them")
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

func TestDownloadRejectsANonIntegerFetchID(t *testing.T) {
	p := embedded.New(embedded.Config{
		Path: "/data/movie.mkv",
		Extract: func(context.Context, string, int) ([]byte, error) {
			t.Fatal("extractor called for an invalid stream index")
			return nil, nil
		},
	})
	_, _, err := p.Download(context.Background(), subtitles.Candidate{FetchID: "not-a-number"})
	assert.Error(t, err)
}

func TestDownloadUsesTheExtractor(t *testing.T) {
	var gotPath string
	var gotStream int
	p := embedded.New(embedded.Config{
		Path: "/data/movie.mkv",
		Extract: func(_ context.Context, path string, stream int) ([]byte, error) {
			gotPath, gotStream = path, stream
			return []byte("1\n00:00:00,000 --> 00:00:01,000\nHello.\n"), nil
		},
	})
	raw, name, err := p.Download(context.Background(), subtitles.Candidate{FetchID: "1"})
	require.NoError(t, err)
	assert.Contains(t, string(raw), "Hello.")
	assert.Equal(t, "stream-1.srt", name)
	assert.Equal(t, "/data/movie.mkv", gotPath)
	assert.Equal(t, 1, gotStream)
}

func TestDownloadWrapsTheExtractorsFailure(t *testing.T) {
	boom := errors.New("boom")
	p := embedded.New(embedded.Config{
		Path:    "/data/movie.mkv",
		Extract: func(context.Context, string, int) ([]byte, error) { return nil, boom },
	})
	raw, name, err := p.Download(context.Background(), subtitles.Candidate{FetchID: "0"})
	require.ErrorIs(t, err, boom)
	assert.Nil(t, raw)
	assert.Empty(t, name)
}

// The manager links this package for Search and never sets an extractor
// (spec §7.3.1). A Download there must refuse, not run a program.
func TestDownloadWithoutAnExtractorIsErrNoExtractor(t *testing.T) {
	p := embedded.New(embedded.Config{Path: "/data/movie.mkv"})
	_, _, err := p.Download(context.Background(), subtitles.Candidate{FetchID: "1"})
	require.ErrorIs(t, err, embedded.ErrNoExtractor)
}

func TestIsTextCodec(t *testing.T) {
	for codec, want := range map[string]bool{
		"subrip": true, "ass": true, "ssa": true, "webvtt": true, "mov_text": true,
		"hdmv_pgs_subtitle": false, "dvd_subtitle": false, "": false,
	} {
		assert.Equal(t, want, embedded.IsTextCodec(codec), codec)
	}
}
