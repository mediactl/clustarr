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

package plex_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/ui/plex"
)

// TestSeasonKeyRoundTrips proves [plex.SeasonKey] and [plex.ParseRatingKey]
// invert each other, per ratingKeyPattern
// `^([0-9a-f-]{36})(?:-s(\d{2,4}))?$`.
func TestSeasonKeyRoundTrips(t *testing.T) {
	uid := types.UID("22222222-2222-2222-2222-222222222222")
	key := plex.SeasonKey(uid, 7)
	require.Equal(t, "22222222-2222-2222-2222-222222222222-s07", key)

	gotUID, season, isSeason, ok := plex.ParseRatingKey(key)
	require.True(t, ok)
	require.True(t, isSeason)
	require.Equal(t, uid, gotUID)
	require.EqualValues(t, 7, season)
}

// TestParseRatingKeyPlainUID is a Movie/Series/Episode's own ratingKey: no
// season suffix.
func TestParseRatingKeyPlainUID(t *testing.T) {
	uid := types.UID("11111111-1111-1111-1111-111111111111")
	gotUID, season, isSeason, ok := plex.ParseRatingKey(string(uid))
	require.True(t, ok)
	require.False(t, isSeason)
	require.Equal(t, uid, gotUID)
	require.Zero(t, season)
}

// TestParseRatingKeyMalformed falsifies the pattern: a key with periods, an
// empty string and an unpadded season number are all not ratingKeys this
// provider ever minted.
func TestParseRatingKeyMalformed(t *testing.T) {
	for _, key := range []string{
		"", "not-a-uuid", "22222222-2222-2222-2222-222222222222-s7",
		"22222222.2222.2222.2222.222222222222", "22222222-2222-2222-2222-222222222222-s007",
	} {
		_, _, _, ok := plex.ParseRatingKey(key)
		require.False(t, ok, "key %q should not parse", key)
	}
}

// TestSeasonKeyZeroPads proves season 0 (the specials season) round-trips
// too, not just a season number that happens to already be two digits.
func TestSeasonKeyZeroPads(t *testing.T) {
	uid := types.UID("22222222-2222-2222-2222-222222222222")
	key := plex.SeasonKey(uid, 0)
	require.Equal(t, "22222222-2222-2222-2222-222222222222-s00", key)

	_, season, isSeason, ok := plex.ParseRatingKey(key)
	require.True(t, ok)
	require.True(t, isSeason)
	require.Zero(t, season)
}

// TestSeasonKeyRoundTripsEverySeasonWidth is the review's season-100 case:
// SeasonKey's %02d pads to two digits but never truncates, so seasons 100
// and up (and a daily show's year-numbered 2024) mint a wider key, which the
// old two-digit pattern refused -- the key Plex was handed could never be
// fetched back.
func TestSeasonKeyRoundTripsEverySeasonWidth(t *testing.T) {
	uid := types.UID("22222222-2222-2222-2222-222222222222")
	for _, n := range []int32{0, 1, 99, 100, 2024, 9999} {
		key := plex.SeasonKey(uid, n)
		gotUID, season, isSeason, ok := plex.ParseRatingKey(key)
		require.True(t, ok, "SeasonKey(%d) = %q does not parse", n, key)
		require.True(t, isSeason, "key %q", key)
		require.Equal(t, uid, gotUID, "key %q", key)
		require.Equal(t, n, season, "key %q", key)
	}
	require.Equal(t, "22222222-2222-2222-2222-222222222222-s2024", plex.SeasonKey(uid, 2024))
}

// TestParseRatingKeyRefusesNonCanonicalSeasons: widening the pattern must not
// admit a second spelling of one season, nor a UID that is not anchored.
func TestParseRatingKeyRefusesNonCanonicalSeasons(t *testing.T) {
	for _, key := range []string{
		"22222222-2222-2222-2222-222222222222-s0100", // 100 is minted -s100
		"22222222-2222-2222-2222-222222222222-s00007",
		"22222222-2222-2222-2222-222222222222-s12345",
		"x22222222-2222-2222-2222-222222222222-s01",
		"22222222-2222-2222-2222-222222222222-s01x",
	} {
		_, _, _, ok := plex.ParseRatingKey(key)
		require.False(t, ok, "key %q should not parse", key)
	}
}
