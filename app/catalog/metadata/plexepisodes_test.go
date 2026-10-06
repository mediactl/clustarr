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

package metadata

import (
	"testing"

	"github.com/stretchr/testify/require"

	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

func ep(season, number int32, tvdb string) pkgmetadata.Episode {
	e := pkgmetadata.Episode{SeasonNumber: season, EpisodeNumber: number}
	if tvdb != "" {
		e.IDs = pkgmetadata.ExternalIDs{pkgmetadata.KeyTVDB: tvdb}
	}
	return e
}

func TestJoinPlexEpisodes(t *testing.T) {
	plex := []pkgmetadata.PlexEpisode{
		{Season: 1, Episode: 1, TVDB: "297989", ID: "aaaaaaaaaaaaaaaaaaaaaaa1"},
		{Season: 1, Episode: 2, TVDB: "297990", ID: "aaaaaaaaaaaaaaaaaaaaaaa2"},
		// Plex files a special TVDB numbers S00E03 as S00E07, with no tvdb id.
		{Season: 0, Episode: 7, ID: "aaaaaaaaaaaaaaaaaaaaaaa7"},
		// Two Plex episodes on one pair: ambiguous.
		{Season: 2, Episode: 1, ID: "bbbbbbbbbbbbbbbbbbbbbbb1"},
		{Season: 2, Episode: 1, ID: "bbbbbbbbbbbbbbbbbbbbbbb2"},
		// Plex's S03E01 is a different episode (tvdb 900) from clustarr's S03E01 (tvdb 901).
		{Season: 3, Episode: 1, TVDB: "900", ID: "ccccccccccccccccccccccc1"},
	}
	episodes := []pkgmetadata.Episode{
		ep(1, 1, "297989"), // by tvdb id
		ep(9, 9, "297990"), // by tvdb id, whatever its numbering
		ep(1, 2, ""),       // pair (1,2) is Plex's tvdb 297990, already claimed above
		ep(0, 7, "555"),    // pair fallback: Plex's episode carries no tvdb id
		ep(2, 1, ""),       // ambiguous pair
		ep(3, 1, "901"),    // pair fallback refused: Plex's episode names another tvdb id
		ep(4, 1, ""),       // Plex has nothing
	}
	joinPlexEpisodes(plex, episodes)
	got := make([]string, len(episodes))
	for i, e := range episodes {
		got[i] = e.PlexID
	}
	require.Equal(t, []string{
		"aaaaaaaaaaaaaaaaaaaaaaa1",
		"aaaaaaaaaaaaaaaaaaaaaaa2",
		"",
		"aaaaaaaaaaaaaaaaaaaaaaa7",
		"",
		"",
		"",
	}, got)
}

// Every episode of a list Plex answered is marked consulted -- so the
// Series reconciler may release an id Plex no longer gives it -- and none
// of a list Plex failed for, whose stored ids are kept.
func TestWithPlexIDsMarksTheEpisodesPlexWasConsultedFor(t *testing.T) {
	var gotIDs pkgmetadata.ExternalIDs
	answered := &pkgmetadata.Registry{Plex: []pkgmetadata.PlexProvider{stubPlexProvider{gotIDs: &gotIDs, children: &pkgmetadata.PlexChildren{
		Episodes: []pkgmetadata.PlexEpisode{{Season: 1, Episode: 1, TVDB: "1", ID: "aaaaaaaaaaaaaaaaaaaaaaa1"}},
	}}}}
	episodes := []pkgmetadata.Episode{ep(1, 1, "1"), ep(1, 2, "2")}
	withPlexIDs(t.Context(), answered, pkgmetadata.ExternalIDs{pkgmetadata.KeyTVDB: "9"}, episodes)
	require.True(t, episodes[0].PlexConsulted)
	require.True(t, episodes[1].PlexConsulted, "Plex answered and has no id for this one")
	require.Empty(t, episodes[1].PlexID)

	failed := &pkgmetadata.Registry{Plex: []pkgmetadata.PlexProvider{stubPlexProvider{gotIDs: &gotIDs, err: pkgmetadata.ErrRateLimited}}}
	episodes = []pkgmetadata.Episode{ep(1, 1, "1")}
	withPlexIDs(t.Context(), failed, pkgmetadata.ExternalIDs{pkgmetadata.KeyTVDB: "9"}, episodes)
	require.False(t, episodes[0].PlexConsulted)
}
