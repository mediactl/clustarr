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
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/ui/plex"
)

// TestParseKeyGrammars is design §7.2's three key grammars beside the
// video roots' two: every minter's key parses back to what it named, and
// nothing else parses.
func TestParseKeyGrammars(t *testing.T) {
	uid := types.UID("88888888-8888-8888-8888-888888888888")
	rec := "aa11bb22-cc33-dd44-ee55-ff6677889900"

	for _, tc := range []struct {
		name string
		key  string
		want plex.Key
	}{
		{"an item's UID", string(uid), plex.Key{UID: uid}},
		{"a season", plex.SeasonKey(uid, 7), plex.Key{UID: uid, Season: 7, IsSeason: true}},
		{"a track", plex.TrackKey(uid, rec), plex.Key{UID: uid, Track: rec}},
		{"a part", plex.PartKey(uid, 3), plex.Key{UID: uid, Part: 3, IsPart: true}},
		{"part 100", plex.PartKey(uid, 100), plex.Key{UID: uid, Part: 100, IsPart: true}},
		{"a synthetic artist", plex.SyntheticArtistKey("Ines Varga"), plex.Key{Synthetic: plex.SyntheticArtistKey("Ines Varga")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := plex.ParseKey(tc.key)
			require.True(t, ok, "key %q", tc.key)
			require.Equal(t, tc.want, got)
		})
	}

	require.Equal(t, string(uid)+"-t"+rec, plex.TrackKey(uid, rec))
	require.Equal(t, string(uid)+"-p03", plex.PartKey(uid, 3))
	require.Equal(t, string(uid)+"-p100", plex.PartKey(uid, 100))
	require.Regexp(t, regexp.MustCompile(`^a-[0-9a-f]{16}$`), plex.SyntheticArtistKey("Ines Varga"))
	require.Equal(t, plex.SyntheticArtistKey("Ines Varga"), plex.SyntheticArtistKey("Ines Varga"), "the same name mints the same key")
	require.NotEqual(t, plex.SyntheticArtistKey("Ines Varga"), plex.SyntheticArtistKey("Ada Marsh"))
	require.NotEqual(t, plex.SyntheticArtistKey(""), plex.SyntheticArtistKey("Ines Varga"), "an empty name mints a key of its own")
}

// TestParseKeyRefusesWhatNoMinterSpells: a part number not padded as
// PartKey pads it, a short recording id, a synthetic key of the wrong
// width or case, two suffixes, and a bare suffix.
func TestParseKeyRefusesWhatNoMinterSpells(t *testing.T) {
	uid := "88888888-8888-8888-8888-888888888888"
	for _, key := range []string{
		uid + "-p3", uid + "-p003", uid + "-p00007",
		uid + "-t", uid + "-tabc", uid + "-taa11bb22-cc33-dd44-ee55-ff66778899001",
		uid + "-s01-p01", uid + "-p01-t" + "aa11bb22-cc33-dd44-ee55-ff6677889900",
		"a-", "a-0123456789abcde", "a-0123456789abcdef0", "a-0123456789ABCDEF", "A-0123456789abcdef",
		"-t" + "aa11bb22-cc33-dd44-ee55-ff6677889900", "-p01",
	} {
		_, ok := plex.ParseKey(key)
		require.False(t, ok, "key %q should not parse", key)
	}
}

// TestParseRatingKeyIsTheVideoRootsReading: the video roots' ParseRatingKey
// still reads a UID and a season, and never a music grammar -- a Plex id
// check against a track key must fail, not mistake its album for the item.
func TestParseRatingKeyIsTheVideoRootsReading(t *testing.T) {
	uid := types.UID("88888888-8888-8888-8888-888888888888")
	got, _, isSeason, ok := plex.ParseRatingKey(plex.SeasonKey(uid, 2))
	require.True(t, ok && isSeason)
	require.Equal(t, uid, got)
	for _, key := range []string{
		plex.TrackKey(uid, "aa11bb22-cc33-dd44-ee55-ff6677889900"),
		plex.PartKey(uid, 1),
		plex.SyntheticArtistKey("x"),
	} {
		_, _, _, ok := plex.ParseRatingKey(key)
		require.False(t, ok, "key %q is not a video root's", key)
	}
}
