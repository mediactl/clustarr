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

package metadata_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/metadata"
)

func TestExternalIDsMergeKeepsExistingValuesAndFillsGaps(t *testing.T) {
	a := metadata.ExternalIDs{metadata.KeyTMDB: "27205", metadata.KeyIMDb: "tt1375666"}
	b := metadata.ExternalIDs{metadata.KeyIMDb: "tt0000000", metadata.KeyTVDB: "121361"}

	got := a.Merge(b)

	require.Equal(t, "tt1375666", got[metadata.KeyIMDb], "a's value must win over a conflicting b")
	require.Equal(t, "121361", got[metadata.KeyTVDB], "a gains a's b-only key")
	require.Equal(t, "27205", got[metadata.KeyTMDB])
}

func TestValidateRejectsMalformedKnownIDs(t *testing.T) {
	tests := []struct {
		name    string
		ids     metadata.ExternalIDs
		wantErr bool
	}{
		{"valid imdb", metadata.ExternalIDs{metadata.KeyIMDb: "tt1375666"}, false},
		{"invalid imdb missing tt", metadata.ExternalIDs{metadata.KeyIMDb: "1375666"}, true},
		{"valid tmdb", metadata.ExternalIDs{metadata.KeyTMDB: "27205"}, false},
		{"invalid tmdb non-numeric", metadata.ExternalIDs{metadata.KeyTMDB: "tt27205"}, true},
		{"valid isbn13", metadata.ExternalIDs{metadata.KeyISBN13: "9780141439518"}, false},
		{"invalid isbn13 checksum", metadata.ExternalIDs{metadata.KeyISBN13: "9780141439519"}, true},
		{"valid asin", metadata.ExternalIDs{metadata.KeyASIN: "B0036I54I6"}, false},
		{"invalid asin too short", metadata.ExternalIDs{metadata.KeyASIN: "B0036"}, true},
		{"valid comicvine", metadata.ExternalIDs{metadata.KeyComicVine: "4050-12345"}, false},
		{"invalid comicvine no dash", metadata.ExternalIDs{metadata.KeyComicVine: "405012345"}, true},
		{"valid mb-artist", metadata.ExternalIDs{metadata.KeyMBArtist: "a74b1b7f-71a5-4011-9441-d0b5e4122711"}, false},
		{"invalid mb-artist not a uuid", metadata.ExternalIDs{metadata.KeyMBArtist: "radiohead"}, true},
		{"valid anilist", metadata.ExternalIDs{metadata.KeyAniList: "101922"}, false},
		{"unrecognised key passes through unchecked", metadata.ExternalIDs{"wikidata": "Q6033"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := metadata.Validate(tt.ids)
			if tt.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
