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

package rescan_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/importarr/worker/rescan"
	"github.com/mediactl/clustarr/pkg/release"
)

// failResolve fails the test if the metadata gateway is consulted. Every case
// but the imdb ones must reach its verdict without a network-shaped call.
func failResolve(t *testing.T) rescan.ResolveIMDb {
	t.Helper()
	return func(imdbID string) (int64, error) {
		t.Fatalf("ResolveIMDb must not be called: %q", imdbID)
		return 0, nil
	}
}

func mustParse(t *testing.T, path string) *release.ParsedRelease {
	t.Helper()
	parsed, err := release.ParsePath(path, release.Options{Kind: commonv1.MediaKindMovie})
	require.NoError(t, err)
	return parsed
}

func TestMatchMovie(t *testing.T) {
	heat := rescan.MovieCandidate{Name: "heat-1995", TmdbID: 949, Title: "Heat", Year: 1995}
	matrix := rescan.MovieCandidate{Name: "the-matrix-1999", TmdbID: 603, Title: "The Matrix", Year: 1999}

	tests := []struct {
		name     string
		path     string
		existing []rescan.MovieCandidate
		resolve  func(t *testing.T) rescan.ResolveIMDb
		want     rescan.MatchResult
		// reasonContains is asserted instead of an exact Reason where the
		// sentence is prose; it pins the part a user needs.
		reasonContains string
	}{
		{
			name:     "embedded tmdb id binds to the existing movie carrying it",
			path:     "/data/media/movies/Heat (1995) [tmdbid-949]/Heat (1995) [tmdbid-949] - Bluray-1080p.mkv",
			existing: []rescan.MovieCandidate{heat},
			want:     rescan.MatchResult{TmdbID: 949, ExistingName: "heat-1995"},
		},
		{
			name: "embedded tmdb id with no existing movie asks the caller to create one",
			path: "/data/media/movies/Heat (1995) [tmdbid-949]/Heat (1995) [tmdbid-949] - Bluray-1080p.mkv",
			want: rescan.MatchResult{TmdbID: 949},
		},
		{
			name:     "an embedded tmdb id wins over a conflicting title match",
			path:     "/data/media/movies/Heat (1995) [tmdbid-949]/Heat (1995) [tmdbid-949].mkv",
			existing: []rescan.MovieCandidate{{Name: "heat-remake", TmdbID: 12345, Title: "Heat", Year: 1995}},
			want:     rescan.MatchResult{TmdbID: 949},
		},
		{
			name: "embedded imdb id is resolved through the metadata gateway",
			path: "/data/media/movies/Heat (1995) [imdbid-tt0113277]/Heat (1995) [imdbid-tt0113277].mkv",
			resolve: func(t *testing.T) rescan.ResolveIMDb {
				return func(imdbID string) (int64, error) {
					assert.Equal(t, "tt0113277", imdbID)
					return 949, nil
				}
			},
			want: rescan.MatchResult{TmdbID: 949},
		},
		{
			name:     "a resolved imdb id binds to the existing movie carrying that tmdb id",
			path:     "/data/media/movies/Heat (1995) [imdbid-tt0113277]/Heat (1995) [imdbid-tt0113277].mkv",
			existing: []rescan.MovieCandidate{heat},
			resolve: func(*testing.T) rescan.ResolveIMDb {
				return func(string) (int64, error) { return 949, nil }
			},
			want: rescan.MatchResult{TmdbID: 949, ExistingName: "heat-1995"},
		},
		{
			name: "an imdb id the gateway cannot resolve is unmatched, never guessed",
			path: "/data/media/movies/Nonexistent (2020) [imdbid-tt9999999]/Nonexistent (2020) [imdbid-tt9999999].mkv",
			resolve: func(*testing.T) rescan.ResolveIMDb {
				return func(string) (int64, error) { return 0, errors.New("not found") }
			},
			want:           rescan.MatchResult{Unmatched: true, Code: rescan.CodeUnresolvedID},
			reasonContains: "tt9999999",
		},
		{
			name: "an imdb id the gateway answers with zero is unmatched",
			path: "/data/media/movies/Nonexistent (2020) [imdbid-tt9999999]/Nonexistent (2020) [imdbid-tt9999999].mkv",
			resolve: func(*testing.T) rescan.ResolveIMDb {
				return func(string) (int64, error) { return 0, nil }
			},
			want:           rescan.MatchResult{Unmatched: true, Code: rescan.CodeUnresolvedID},
			reasonContains: "tt9999999",
		},
		{
			name:     "an exact title and year match against one existing movie",
			path:     "/data/media/movies/The Matrix (1999)/The Matrix (1999) - Bluray-1080p.mkv",
			existing: []rescan.MovieCandidate{matrix},
			want:     rescan.MatchResult{TmdbID: 603, ExistingName: "the-matrix-1999"},
		},
		{
			name: "the year narrows a shared title down to one movie",
			path: "/data/media/movies/Poltergeist (1982)/Poltergeist (1982).mkv",
			existing: []rescan.MovieCandidate{
				{Name: "poltergeist-1982", TmdbID: 11663, Title: "Poltergeist", Year: 1982},
				{Name: "poltergeist-2015", TmdbID: 231617, Title: "Poltergeist", Year: 2015},
			},
			want: rescan.MatchResult{TmdbID: 11663, ExistingName: "poltergeist-1982"},
		},
		{
			name: "two existing movies sharing title and year are ambiguous, never guessed",
			path: "/data/media/movies/Poltergeist (1982)/Poltergeist (1982).mkv",
			existing: []rescan.MovieCandidate{
				{Name: "poltergeist-1982-b", TmdbID: 99999, Title: "Poltergeist", Year: 1982},
				{Name: "poltergeist-1982-a", TmdbID: 11663, Title: "Poltergeist", Year: 1982},
			},
			want: rescan.MatchResult{
				Unmatched:  true,
				Code:       rescan.CodeAmbiguousTitle,
				Candidates: []string{"poltergeist-1982-a", "poltergeist-1982-b"},
			},
			reasonContains: "ambiguous: 2 existing movies",
		},
		{
			name:           "no id and no title match is unmatched and names how many were considered",
			path:           "/data/media/movies/Some Unknown Film (2024)/Some Unknown Film (2024).mkv",
			existing:       []rescan.MovieCandidate{matrix},
			want:           rescan.MatchResult{Unmatched: true, Code: rescan.CodeNoMatch},
			reasonContains: "1 existing movies",
		},
		{
			name:           "an empty catalog leaves an id-less file unmatched",
			path:           "/data/media/movies/Some Unknown Film (2024)/Some Unknown Film (2024).mkv",
			want:           rescan.MatchResult{Unmatched: true, Code: rescan.CodeNoMatch},
			reasonContains: "0 existing movies",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resolve := failResolve(t)
			if tc.resolve != nil {
				resolve = tc.resolve(t)
			}

			got := rescan.MatchMovie(mustParse(t, tc.path), tc.existing, resolve)

			if tc.reasonContains != "" {
				assert.Contains(t, got.Reason, tc.reasonContains)
			}
			got.Reason = tc.want.Reason
			assert.Equal(t, tc.want, got)
		})
	}
}

// A nil ParsedRelease is what a parse failure hands the caller; it must be an
// unmatched file, not a panic.
func TestMatchMovieRejectsANilParse(t *testing.T) {
	got := rescan.MatchMovie(nil, nil, failResolve(t))
	assert.True(t, got.Unmatched)
	assert.Equal(t, rescan.CodeParseError, got.Code)
	assert.NotEmpty(t, got.Reason)
}

// Without a resolver the imdb path must decline rather than fall through to
// title matching, which would be exactly the guess the rule forbids.
func TestMatchMovieWithoutAResolverDoesNotFallBackToTitleMatching(t *testing.T) {
	parsed := mustParse(t, "/data/media/movies/Heat (1995) [imdbid-tt0113277]/Heat (1995) [imdbid-tt0113277].mkv")
	got := rescan.MatchMovie(parsed, []rescan.MovieCandidate{{Name: "heat-1995", TmdbID: 949, Title: "Heat", Year: 1995}}, nil)
	assert.True(t, got.Unmatched)
	assert.Equal(t, rescan.CodeUnresolvedID, got.Code)
	assert.Empty(t, got.ExistingName)
}

// Every unmatched verdict must carry a reason: an entry in
// LibraryScan.status.unmatched with an empty reason fails the CRD's own
// required field and tells the user nothing.
func TestEveryUnmatchedVerdictCarriesACodeAndAReason(t *testing.T) {
	cases := []rescan.MatchResult{
		rescan.MatchMovie(nil, nil, nil),
		rescan.MatchMovie(mustParse(t, "/data/media/movies/Unknown (2024)/Unknown (2024).mkv"), nil, nil),
		rescan.MatchMovie(
			mustParse(t, "/data/media/movies/X (2020) [imdbid-tt1]/X (2020) [imdbid-tt1].mkv"),
			nil,
			func(string) (int64, error) { return 0, errors.New("boom") },
		),
	}
	for i, got := range cases {
		require.True(t, got.Unmatched, "case %d", i)
		assert.NotEmpty(t, got.Code, "case %d", i)
		assert.NotEmpty(t, got.Reason, "case %d", i)
	}
}
