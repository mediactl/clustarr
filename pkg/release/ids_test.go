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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids, cleaned := extractIDs(tt.title)
			assert.Equal(t, tt.wantIDs, ids)
			for _, token := range []string{"tmdbid-", "tmdb-", "imdbid-", "imdb-", "tvdbid-", "tvdb-", "tt0113277"} {
				assert.NotContains(t, cleaned, token, "id token must be stripped from the cleaned title")
			}
		})
	}
}

func TestExtractIDsLeavesTitleWithNoIDsUnchanged(t *testing.T) {
	ids, cleaned := extractIDs("The.Matrix.1999.1080p.BluRay.x264-GROUP")
	assert.Empty(t, ids)
	assert.Equal(t, "The.Matrix.1999.1080p.BluRay.x264-GROUP", cleaned)
}
