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

package overlay_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/overlay"
)

func TestFormatScore(t *testing.T) {
	cases := []struct {
		name     string
		source   string
		centis   int32
		wantText string
		wantOK   bool
	}{
		// metacritic, rottenTomatoesCritic and rottenTomatoesAudience are
		// 0-100 scores (ValueCentis 0-10000): whole number, no decimal.
		{"metacritic whole number", overlay.SourceMetacritic, 5300, "53", true},
		{"rt critic whole number", overlay.SourceRTCritic, 5300, "53", true},
		{"rt audience whole number", overlay.SourceRTAudience, 8700, "87", true},
		{"metacritic single digit", overlay.SourceMetacritic, 900, "9", true},
		{"metacritic max", overlay.SourceMetacritic, 10000, "100", true},

		// imdb, tmdb, trakt and letterboxd are 0-10 scores (ValueCentis
		// 0-1000): one decimal digit.
		{"imdb one decimal", overlay.SourceIMDb, 720, "7.2", true},
		{"tmdb one decimal", overlay.SourceTMDB, 830, "8.3", true},
		{"trakt one decimal", overlay.SourceTrakt, 910, "9.1", true},
		{"letterboxd one decimal", overlay.SourceLetterboxd, 450, "4.5", true},
		{"imdb max", overlay.SourceIMDb, 1000, "10.0", true},
		{"decimal truncates, does not round", overlay.SourceIMDb, 729, "7.2", true},

		// An unrecognized source falls back to the decimal (0-10) format
		// rather than panicking or guessing at a whole-number scale.
		{"unknown source falls back to decimal", "unknownSource", 555, "5.5", true},

		// Zero always means "no rating for this source" -- the badge is
		// omitted (spec §C.6 step 4) -- regardless of source.
		{"zero metacritic", overlay.SourceMetacritic, 0, "", false},
		{"zero imdb", overlay.SourceIMDb, 0, "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotText, gotOK := overlay.FormatScore(tc.source, tc.centis)
			require.Equal(t, tc.wantOK, gotOK, "ok")
			require.Equal(t, tc.wantText, gotText, "text")
		})
	}
}
