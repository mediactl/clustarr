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

package torrent

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/events/schema"
)

// The catalog half of a selection -- reading each covered Episode's scene
// numbering, absolute number and UTC air date (a day either side) -- moved
// to the manager with ADR-0019 §6.7 (app/remediation/downloads'
// enrichSelection); the engine builds its selector from what the command
// carries (selectionFrom). These tests hold the engine's half.

// TestSelectionWantsOnlyTheTargetedEpisodes is the carried item's case end
// to end through the pure half: a season pack grabbed for S01E03 alone
// wants S01E03's video and its subtitle, skips the other episodes, and keeps
// what names no episode at all.
func TestSelectionWantsOnlyTheTargetedEpisodes(t *testing.T) {
	sel := selectionFrom(&schema.TransferSelection{Episodes: []schema.EpisodeNo{{Season: 1, Number: 3}}})
	want := sel.selector()
	if !assert.NotNil(t, want) {
		return
	}

	cases := map[string]bool{
		"Show.S01.1080p.WEB-DL/Show.S01E03.1080p.WEB-DL.mkv":         true,
		"Show.S01.1080p.WEB-DL/Subs/Show.S01E03.1080p.WEB-DL.en.srt": true,
		"Show.S01.1080p.WEB-DL/Show.S01E01.1080p.WEB-DL.mkv":         false,
		"Show.S01.1080p.WEB-DL/Show.S01E04.1080p.WEB-DL.mkv":         false,
		"Show.S01.1080p.WEB-DL/Show.S02E03.1080p.WEB-DL.mkv":         false,
		"Show.S01.1080p.WEB-DL/Show.S01E02E03.1080p.WEB-DL.mkv":      true, // a multi-episode file holding the wanted one
		"Show.S01.1080p.WEB-DL/Show.S01.1080p.WEB-DL.nfo":            true, // names no episode: kept
		"Show.S01.1080p.WEB-DL/featurette.mkv":                       true,
	}
	for path, wanted := range cases {
		assert.Equal(t, wanted, want(path, 1), path)
	}
}

// TestSelectionAcceptsSceneAndAbsoluteNumbering proves the selection is the
// union of every numbering the command carries (official and scene episode
// numbers, official and scene absolutes), because a pack's release group
// chose one of them and nothing here can tell which; the persisted form is
// sorted and de-duplicated.
func TestSelectionAcceptsSceneAndAbsoluteNumbering(t *testing.T) {
	sel := selectionFrom(&schema.TransferSelection{
		// S01E04 (scene S01E03, absolute 16, scene absolute 15) and S01E05.
		Episodes:  []schema.EpisodeNo{{Season: 1, Number: 4}, {Season: 1, Number: 5}, {Season: 1, Number: 3}, {Season: 1, Number: 4}},
		Absolutes: []int32{16, 15, 16},
	})
	assert.Equal(t, []EpisodeNumber{{1, 3}, {1, 4}, {1, 5}}, sel.Episodes)
	assert.Equal(t, []int{15, 16}, sel.Absolutes)

	want := sel.selector()
	assert.True(t, want("Pack/Show.S01E03.mkv", 1), "scene number of a wanted episode")
	assert.True(t, want("Pack/Show.S01E05.mkv", 1), "official number of a wanted episode")
	assert.False(t, want("Pack/Show.S01E06.mkv", 1))
	assert.True(t, want("[Group] Show - 16 [1080p].mkv", 1), "official absolute")
	assert.True(t, want("[Group] Show - 15 [1080p].mkv", 1), "scene absolute")
	assert.False(t, want("[Group] Show - 17 [1080p].mkv", 1))
}

// TestSelectionMatchesDailyEpisodesByAirDate: a daily episode is matched by
// the air dates the command carries (the manager sends the UTC calendar day
// with a day either side).
func TestSelectionMatchesDailyEpisodesByAirDate(t *testing.T) {
	sel := selectionFrom(&schema.TransferSelection{
		Episodes: []schema.EpisodeNo{{Season: 2026, Number: 1}},
		AirDates: []string{"2026-01-02", "2025-12-31", "2026-01-01"},
	})
	assert.Equal(t, []string{"2025-12-31", "2026-01-01", "2026-01-02"}, sel.AirDates)

	want := sel.selector()
	assert.True(t, want("Pack/Show.2026.01.01.720p.HDTV.mkv", 1))
	assert.False(t, want("Pack/Show.2026.01.05.720p.HDTV.mkv", 1))
}

// TestResolveSelectionLeavesNonEpisodeTargetsWhole covers every "no
// selection" answer: a command with no selection (a movie, a pack with no
// covered episodes, an album) and an empty one.
func TestResolveSelectionLeavesNonEpisodeTargetsWhole(t *testing.T) {
	for name, sel := range map[string]*schema.TransferSelection{
		"no selection":    nil,
		"empty selection": {},
	} {
		assert.Nil(t, selectionFrom(sel), name)
		assert.Nil(t, selectionFrom(sel).selector(), name)
	}
}
