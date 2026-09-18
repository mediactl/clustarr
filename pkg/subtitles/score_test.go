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

package subtitles_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

func TestHashWeightEqualsSumOfOtherWeightsMinusOne(t *testing.T) {
	// The invariant Bazarr checks at load (research note §5): hash score ==
	// (sum of every other weight) - 1, so a hash match scores exactly one
	// point less than a perfect non-hash match (the -1 is the
	// hearing_impaired bonus, which a bare hash match does not carry).
	for _, kind := range []common.MediaKind{common.MediaKindEpisode, common.MediaKindMovie} {
		all := map[string]bool{
			subtitles.MatchHash: false, subtitles.MatchSeries: true, subtitles.MatchTitle: true,
			subtitles.MatchYear: true, subtitles.MatchSeason: true, subtitles.MatchEpisode: true,
			subtitles.MatchSource: true, subtitles.MatchReleaseGroup: true, subtitles.MatchAudioCodec: true,
			subtitles.MatchResolution: true, subtitles.MatchVideoCodec: true,
			subtitles.MatchHearingImpaired: true, subtitles.MatchStreamingService: true, subtitles.MatchEdition: true,
		}
		everythingElse, _ := subtitles.Score(kind, all)

		hashOnly, _ := subtitles.Score(kind, map[string]bool{subtitles.MatchHash: true})

		assert.Equal(t, everythingElse-1, hashOnly, "kind=%s", kind)
	}
}

func TestMaxScoreMatchesTheWeightSums(t *testing.T) {
	require.Equal(t, subtitles.MaxEpisodeScore, subtitles.MaxScore[common.MediaKindEpisode])
	require.Equal(t, subtitles.MaxMovieScore, subtitles.MaxScore[common.MediaKindMovie])
	assert.Equal(t, 360, subtitles.MaxEpisodeScore)
	assert.Equal(t, 180, subtitles.MaxMovieScore)
}

func TestCandidateMatchesHashCorroboration(t *testing.T) {
	tests := []struct {
		name           string
		kind           common.MediaKind
		hashVerifiable bool
		isSpecial      bool
		raw            map[string]bool
		wantOnlyHash   bool // true: matches collapses to {hash}; false: hash is dropped, rest kept
	}{
		{
			name: "episode hash corroborated by series+season+episode+source collapses to hash-only",
			kind: common.MediaKindEpisode, hashVerifiable: true,
			raw:          map[string]bool{subtitles.MatchHash: true, subtitles.MatchSeries: true, subtitles.MatchSeason: true, subtitles.MatchEpisode: true, subtitles.MatchSource: true, subtitles.MatchYear: true},
			wantOnlyHash: true,
		},
		{
			name: "episode hash without corroboration is dropped, other matches survive",
			kind: common.MediaKindEpisode, hashVerifiable: true,
			raw:          map[string]bool{subtitles.MatchHash: true, subtitles.MatchSeries: true, subtitles.MatchYear: true},
			wantOnlyHash: false,
		},
		{
			name: "special episode skips the season+episode requirement",
			kind: common.MediaKindEpisode, hashVerifiable: true, isSpecial: true,
			raw:          map[string]bool{subtitles.MatchHash: true, subtitles.MatchSeries: true, subtitles.MatchSource: true},
			wantOnlyHash: true,
		},
		{
			name: "movie hash corroborated by video_codec+source collapses to hash-only",
			kind: common.MediaKindMovie, hashVerifiable: true,
			raw:          map[string]bool{subtitles.MatchHash: true, subtitles.MatchVideoCodec: true, subtitles.MatchSource: true, subtitles.MatchTitle: true},
			wantOnlyHash: true,
		},
		{
			name: "non-hash-verifiable provider trusts the hash blindly",
			kind: common.MediaKindMovie, hashVerifiable: false,
			raw:          map[string]bool{subtitles.MatchHash: true, subtitles.MatchTitle: true},
			wantOnlyHash: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := subtitles.CandidateMatches(tt.kind, tt.hashVerifiable, tt.isSpecial, nil, tt.raw)
			if tt.wantOnlyHash {
				assert.Equal(t, map[string]bool{subtitles.MatchHash: true}, got)
			} else {
				assert.False(t, got[subtitles.MatchHash], "hash must be dropped, not trusted, without corroboration")
				assert.True(t, got[subtitles.MatchSeries] || got[subtitles.MatchTitle], "non-hash matches must survive")
			}
		})
	}
}

func TestCandidateMatchesAddsHearingImpairedBonusOnPreferenceMatch(t *testing.T) {
	wantHI := true
	got := subtitles.CandidateMatches(common.MediaKindMovie, true, false, &wantHI, map[string]bool{subtitles.MatchTitle: true})
	assert.True(t, got[subtitles.MatchHearingImpaired])
}

func TestCandidateMatchesOmitsHearingImpairedBonusWhenPreferenceUnset(t *testing.T) {
	got := subtitles.CandidateMatches(common.MediaKindMovie, true, false, nil, map[string]bool{subtitles.MatchTitle: true})
	assert.False(t, got[subtitles.MatchHearingImpaired])
}

func TestMinScorePercentages(t *testing.T) {
	assert.Equal(t, 324, subtitles.MinScore(common.MediaKindEpisode, 90)) // Bazarr's default
	assert.Equal(t, 126, subtitles.MinScore(common.MediaKindMovie, 70))   // Bazarr's default
	assert.Equal(t, 0, subtitles.MinScore(common.MediaKindMovie, 0))
}

// TestGuessMatchesFullMatch verifies GuessMatches against the REAL, landed
// pkg/release.Parse — not the pre-amendment brief's TargetRelease
// string-containment sketch. The candidate string and target field values
// below were verified by running release.Parse against the string directly
// (see the task report's "adapted to real pkg/release" note): Group=SPARKS,
// Quality.Source=webdl, Quality.Resolution=1080, Hints.Codec=[x265],
// Hints.Audio=[DTS-HD.MA] (pkg/release keeps the scene-title dot delimiter,
// unlike the pre-verification brief sketch's "DTS-HD MA"), Hints.Streaming=
// [AMZN] (pkg/release's hints.go maps rls's Collection tag through a fixed
// streaming-service vocabulary that does include AMZN), Edition=Extended.
// Source is WebDL, not Bluray, because "AMZN" only round-trips into
// Hints.Streaming on a WEB-DL/WEBRip release — an AMZN Bluray release does
// not exist in the real world and pkg/release does not parse one as such.
func TestGuessMatchesFullMatch(t *testing.T) {
	target := &release.ParsedRelease{
		Group:   "SPARKS",
		Quality: common.Quality{Source: common.SourceWebDL, Resolution: 1080},
		Hints:   release.Hints{Codec: []string{"x265"}, Audio: []string{"DTS-HD.MA"}, Streaming: []string{"AMZN"}},
		Edition: "Extended",
	}
	const releaseInfo = "Movie.Name.2020.EXTENDED.1080p.AMZN.WEB-DL.DTS-HD.MA.x265-SPARKS"

	got := subtitles.GuessMatches(common.MediaKindMovie, target, releaseInfo)
	assert.True(t, got[subtitles.MatchReleaseGroup])
	assert.True(t, got[subtitles.MatchSource])
	assert.True(t, got[subtitles.MatchResolution])
	assert.True(t, got[subtitles.MatchVideoCodec])
	assert.True(t, got[subtitles.MatchAudioCodec])
	assert.True(t, got[subtitles.MatchStreamingService])
	assert.True(t, got[subtitles.MatchEdition])
}

func TestGuessMatchesPartialMatchOnDifferingGroup(t *testing.T) {
	target := &release.ParsedRelease{
		Group:   "OTHERGROUP",
		Quality: common.Quality{Source: common.SourceWebDL, Resolution: 1080},
	}
	const releaseInfo = "Movie.Name.2020.1080p.AMZN.WEB-DL.x265-SPARKS"

	got := subtitles.GuessMatches(common.MediaKindMovie, target, releaseInfo)
	assert.False(t, got[subtitles.MatchReleaseGroup], "release groups differ, must not match")
	assert.True(t, got[subtitles.MatchSource])
	assert.True(t, got[subtitles.MatchResolution])
}

func TestGuessMatchesNilTargetYieldsNoMatchesAndNoError(t *testing.T) {
	got := subtitles.GuessMatches(common.MediaKindMovie, nil, "Movie.Name.2020.1080p.BluRay.x265-SPARKS")
	assert.Empty(t, got)
}

func TestScoreWithNilMatchesReturnsZero(t *testing.T) {
	require.NotPanics(t, func() {
		score, without := subtitles.Score(common.MediaKindMovie, nil)
		assert.Equal(t, 0, score)
		assert.Equal(t, 0, without)
	})
}

func TestScoreWithEmptyMatchesReturnsZero(t *testing.T) {
	score, without := subtitles.Score(common.MediaKindEpisode, map[string]bool{})
	assert.Equal(t, 0, score)
	assert.Equal(t, 0, without)
}

func TestCandidateMatchesWithNilRawAndNilWantHIReturnsEmptyMap(t *testing.T) {
	require.NotPanics(t, func() {
		got := subtitles.CandidateMatches(common.MediaKindMovie, true, false, nil, nil)
		assert.Empty(t, got)
	})
}

func TestCandidateMatchesWithEmptyRawReturnsEmptyMap(t *testing.T) {
	got := subtitles.CandidateMatches(common.MediaKindEpisode, true, false, nil, map[string]bool{})
	assert.Empty(t, got)
}

func TestGuessMatchesWithEmptyReleaseInfoYieldsNoMatchesAndNoError(t *testing.T) {
	target := &release.ParsedRelease{Group: "SPARKS"}
	require.NotPanics(t, func() {
		got := subtitles.GuessMatches(common.MediaKindMovie, target, "")
		assert.Empty(t, got, "release.Parse errors on an empty title; GuessMatches must swallow that as no matches, not panic or error")
	})
}
