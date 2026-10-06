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
	"context"
	"testing"
	"time"

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

// deadlinePlex records the deadline it was asked under.
type deadlinePlex struct{ got *time.Time }

func (deadlinePlex) Name() string                           { return "plex" }
func (deadlinePlex) Capabilities() pkgmetadata.Capabilities { return pkgmetadata.Capabilities{} }
func (p deadlinePlex) ShowChildren(ctx context.Context, _ pkgmetadata.ExternalIDs) (*pkgmetadata.PlexChildren, error) {
	*p.got, _ = ctx.Deadline()
	return nil, context.DeadlineExceeded
}

// The Plex lookup is best effort inside the episode RPC: it gets at most
// half the caller's remaining time (and never more than plexLookupBudget),
// so a slow Plex cannot run the caller's 30 s deadline out and fail the
// episode sync it only enriches.
func TestWithPlexIDsLeavesTheCallerTimeToAnswer(t *testing.T) {
	var got time.Time
	reg := &pkgmetadata.Registry{Plex: []pkgmetadata.PlexProvider{deadlinePlex{got: &got}}}

	parent, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	deadline, _ := parent.Deadline()
	withPlexIDs(parent, reg, pkgmetadata.ExternalIDs{pkgmetadata.KeyTVDB: "9"}, []pkgmetadata.Episode{ep(1, 1, "")})
	require.False(t, got.IsZero(), "the lookup runs under a deadline")
	require.LessOrEqual(t, time.Until(got), 15*time.Second+time.Second, "at most half the caller's 30 s")
	require.True(t, got.Before(deadline))

	withPlexIDs(t.Context(), reg, pkgmetadata.ExternalIDs{pkgmetadata.KeyTVDB: "9"}, []pkgmetadata.Episode{ep(1, 1, "")})
	require.False(t, got.IsZero(), "a caller without a deadline still bounds the lookup")
	require.LessOrEqual(t, time.Until(got), plexLookupBudget)
}

func epAt(season, episode int32, tvdb, title, airDate string) pkgmetadata.Episode {
	e := ep(season, episode, tvdb)
	e.Title = title
	if airDate != "" {
		d, err := time.Parse("2006-01-02", airDate)
		if err != nil {
			panic(err)
		}
		e.AirDate = &d
	}
	return e
}

// The join's second-attribute fallbacks, from the episodes kind-cluster-plex
// had no Plex id for (2026-10-06): an episode is joined only when a second
// attribute agrees, so nothing ambiguous ever gets a plex:// GUID.
func TestJoinPlexEpisodesWhenTVDBDisagrees(t *testing.T) {
	plex := []pkgmetadata.PlexEpisode{
		// Plex files the episode under another TVDB id (Family Guy, VIP).
		{Season: 0, Episode: 64, TVDB: "4767506", ID: "aaaaaaaaaaaaaaaaaaaaaaa1", Title: "Happy Hell-O-Ween", AirDate: "2026-10-05"},
		{Season: 1, Episode: 3, TVDB: "11870618", ID: "aaaaaaaaaaaaaaaaaaaaaaa2", Title: "Episode Three", AirDate: "2023-09-01"},
		// Plex numbers the tail three lower (One Piece S21).
		{Season: 21, Episode: 182, ID: "bbbbbbbbbbbbbbbbbbbbbbb1", Title: "The World That Luffy Wants!", AirDate: "2023-09-17"},
		// Two Plex episodes share a title: a title alone names neither.
		{Season: 2, Episode: 1, ID: "ccccccccccccccccccccccc1", Title: "Pilot"},
		{Season: 3, Episode: 1, ID: "ccccccccccccccccccccccc2", Title: "Pilot"},
		// One Plex episode on an air date, another day with two.
		{Season: 4, Episode: 5, ID: "ddddddddddddddddddddddd1", Title: "Part One / Part Two", AirDate: "2024-02-07"},
		{Season: 5, Episode: 1, ID: "eeeeeeeeeeeeeeeeeeeeeee1", AirDate: "2025-01-01"},
		{Season: 5, Episode: 2, ID: "eeeeeeeeeeeeeeeeeeeeeee2", AirDate: "2025-01-01"},
		// Plex's S06E01 is another episode with another title and date.
		{Season: 6, Episode: 1, TVDB: "600", ID: "fffffffffffffffffffffff1", Title: "Other", AirDate: "2020-01-01"},
		// The merged two-parter clustarr's part 1 claims by TVDB id.
		{Season: 7, Episode: 1, TVDB: "701", ID: "ggggggggggggggggggggggg1", Title: "Career Day (1) / Career Day (2)", AirDate: "2024-02-07"},
	}
	episodes := []pkgmetadata.Episode{
		epAt(0, 64, "4780559", "Happy Hell-O-Ween", ""),         // pair, title agrees
		epAt(1, 3, "11521264", "", "2023-09-01"),                // pair, air date agrees
		epAt(21, 185, "999", "The World That Luffy Wants!", ""), // title, unique in the show
		epAt(2, 9, "", "Pilot", ""),                             // title shared by two: none
		epAt(4, 9, "", "", "2024-02-07"),                        // air date, unique in its season
		epAt(5, 9, "", "", "2025-01-01"),                        // two on that date: none
		epAt(6, 1, "601", "Mine", "2021-01-01"),                 // pair names another episode: none
		epAt(7, 1, "701", "Career Day (1)", "2024-02-07"),       // tvdb id
		epAt(7, 2, "702", "Career Day (2)", "2024-02-07"),       // part 2: Plex merged it, already claimed
	}
	joinPlexEpisodes(plex, episodes)
	got := make([]string, len(episodes))
	for i, e := range episodes {
		got[i] = e.PlexID
	}
	require.Equal(t, []string{
		"aaaaaaaaaaaaaaaaaaaaaaa1",
		"aaaaaaaaaaaaaaaaaaaaaaa2",
		"bbbbbbbbbbbbbbbbbbbbbbb1",
		"",
		"ddddddddddddddddddddddd1",
		"",
		"",
		"ggggggggggggggggggggggg1",
		"",
	}, got)
}
