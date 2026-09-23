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

package release

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractIDsRecognizesEveryJellyfinPlexArrForm(t *testing.T) {
	tests := []struct {
		name    string
		title   string
		wantIDs map[string]string
	}{
		{"tmdbid bracket", "Heat (1995) [tmdbid-949]", map[string]string{"tmdb": "949"}},
		{"tmdb brace", "Heat (1995) {tmdb-949}", map[string]string{"tmdb": "949"}},
		{"imdbid bracket", "Heat (1995) [imdbid-tt0113277]", map[string]string{"imdb": "tt0113277"}},
		{"imdb brace", "Heat (1995) {imdb-tt0113277}", map[string]string{"imdb": "tt0113277"}},
		{"tvdbid bracket", "Fringe (2008) [tvdbid-82459]", map[string]string{"tvdb": "82459"}},
		{"tvdb brace", "Fringe (2008) {tvdb-82459}", map[string]string{"tvdb": "82459"}},
		{"tvdb bracket", "Fringe (2008) [tvdb-82459]", map[string]string{"tvdb": "82459"}},
		{"bare imdb token", "Heat (1995) tt0113277", map[string]string{"imdb": "tt0113277"}},
		{"combined", "Heat (1995) [tmdbid-949] [imdbid-tt0113277]", map[string]string{"tmdb": "949", "imdb": "tt0113277"}},
		// The braced "id" spellings Jellyfin accepts, which a fixed table of
		// seven spellings missed: the bare-imdb fallback then took only the
		// value and left "{imdbid-}" behind.
		{"tvdbid brace", "Some Show S01E01 {tvdbid-121361}", map[string]string{"tvdb": "121361"}},
		{"imdbid brace", "{imdbid-tt0133093} The Matrix 1999 1080p", map[string]string{"imdb": "tt0133093"}},
		{"tmdbid brace", "Heat (1995) {tmdbid-949}", map[string]string{"tmdb": "949"}},
		{"tmdb bracket", "Heat (1995) [tmdb-949]", map[string]string{"tmdb": "949"}},
		{"imdb bracket", "Heat (1995) [imdb-tt0113277]", map[string]string{"imdb": "tt0113277"}},
		{"emby equals separator", "Heat (1995) [tmdbid=949]", map[string]string{"tmdb": "949"}},
		{"wrapped bare imdb", "Heat (1995) (tt0113277)", map[string]string{"imdb": "tt0113277"}},
		{"upper-case key and value", "Heat (1995) {IMDB-TT0113277}", map[string]string{"imdb": "tt0113277"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids, cleaned := extractIDs(tt.title)
			assert.Equal(t, tt.wantIDs, ids)
			for _, token := range []string{"tmdb", "imdb", "tvdb", "tt0113277", "tt0133093", "{", "}", "[", "]", "()"} {
				assert.NotContains(t, strings.ToLower(cleaned), token, "id token must be stripped from the cleaned title, delimiters included")
			}
		})
	}
}

func TestExtractIDsLeavesTitleWithNoIDsUnchanged(t *testing.T) {
	ids, cleaned := extractIDs("The.Matrix.1999.1080p.BluRay.x264-GROUP")
	assert.Empty(t, ids)
	assert.Equal(t, "The.Matrix.1999.1080p.BluRay.x264-GROUP", cleaned)
}

// TestExtractIDsLeavesUnrecognisedTokensAlone pins the other half of the
// value-shape rule: a token whose value does not fit its key is not an id,
// so it is neither recorded under the wrong key nor stripped.
func TestExtractIDsLeavesUnrecognisedTokensAlone(t *testing.T) {
	for _, title := range []string{
		"Heat (1995) {tmdb-tt0113277}",
		"Heat (1995) [imdbid-949]",
		"Heat (1995) {edition-Director's Cut}",
		"Heat (1995) [tmdbid-949}",
	} {
		ids, cleaned := extractIDs(title)
		assert.Empty(t, ids, title)
		assert.Equal(t, title, cleaned)
	}
}
