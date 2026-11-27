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
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

func TestOverSeedCap(t *testing.T) {
	t0 := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	s := func(id string, seeded time.Duration, completedAfter time.Duration) seeder {
		return seeder{id: id, seeded: seeded, completedAt: t0.Add(completedAfter)}
	}
	for _, tc := range []struct {
		name    string
		seeding []seeder
		limit   int
		want    []string
	}{
		{name: "no cap", seeding: []seeder{s("a", time.Hour, 0), s("b", 0, time.Hour)}, limit: 0},
		{name: "under the cap", seeding: []seeder{s("a", time.Hour, 0), s("b", 0, time.Hour)}, limit: 3},
		{name: "at the cap", seeding: []seeder{s("a", time.Hour, 0), s("b", 0, time.Hour), s("c", time.Minute, 0)}, limit: 3},
		{
			name:    "the ones that have seeded longest give up their places",
			seeding: []seeder{s("week", 168*time.Hour, 0), s("new", 0, 4*time.Hour), s("day", 24*time.Hour, 0), s("hour", time.Hour, 3*time.Hour), s("days", 72*time.Hour, 0)},
			limit:   3,
			want:    []string{"days", "week"},
		},
		{
			// After a restart every torrent's completedAt is this process's,
			// and its seed time is what was persisted: the order holds.
			name:    "seed time decides, not when this process saw it complete",
			seeding: []seeder{s("old", 10*time.Hour, time.Minute), s("new", time.Hour, 0)},
			limit:   1,
			want:    []string{"old"},
		},
		{
			name:    "among equals the later completed keeps its place, then the id decides",
			seeding: []seeder{s("b", 0, 0), s("a", 0, 0), s("later", 0, time.Minute)},
			limit:   1,
			want:    []string{"a", "b"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]seeder(nil), tc.seeding...)
			assert.ElementsMatch(t, tc.want, overSeedCap(tc.seeding, tc.limit))
			assert.Equal(t, before, tc.seeding, "the caller's slice is not reordered")
		})
	}
}

// The owner, 2026-11-27: "We should only allow a max of 3 seeding torrents
// at a time." Four real torrents complete one after another on a client
// that allows two: the two that have seeded longest have their goal counted
// as met and stop seeding, the two newest go on, and what the caller does
// with a met goal is unchanged -- an imported one may be removed, one not
// imported may not. A fifth takes its place from the older of the two left.
func TestMaxSeeding_TheLongestSeededGiveUpTheirPlaces(t *testing.T) {
	cfg := loopbackConfig(t)
	cfg.Seed = true
	cfg.MaxSeeding = 2
	raw, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	c := raw.(*Client)
	ctx := context.Background()

	add := func(n int) string {
		t.Helper()
		content := []byte(strings.Repeat(fmt.Sprintf("seeding cap content %d ", n), 2048))
		seeder, payload := newSeeder(t, content)
		id, err := c.Add(ctx, download.AddRequest{
			Name:         fmt.Sprintf("seed-cap-%d", n),
			Payload:      payload,
			SeedCriteria: &commonv1alpha1.SeedCriteria{SeedTime: &metav1.Duration{Duration: time.Hour}},
		})
		require.NoError(t, err)
		connectPeer(t, c, id, seeder)
		require.Eventually(t, func() bool {
			item, err := c.Get(ctx, id)
			require.NoError(t, err)
			return item.CanMoveFiles
		}, 15*time.Second, 20*time.Millisecond, "torrent %d never completed", n)
		// Each has seeded measurably longer than the next one will have.
		time.Sleep(30 * time.Millisecond)
		return id
	}
	list := func() map[string]download.Item {
		t.Helper()
		items, err := c.List(ctx)
		require.NoError(t, err)
		out := map[string]download.Item{}
		for _, it := range items {
			out[it.ID] = it
		}
		return out
	}

	first, second := add(1), add(2)
	got := list()
	assert.False(t, got[first].SeedGoalMet, "two may seed")
	assert.False(t, got[second].SeedGoalMet)
	assert.Equal(t, commonv1alpha1.DownloadStageSeeding, got[first].Stage)

	require.NoError(t, c.MarkImported(ctx, first))
	third, fourth := add(3), add(4)
	got = list()
	assert.True(t, got[first].SeedGoalMet, "the longest seeded gives up its place")
	assert.True(t, got[second].SeedGoalMet)
	assert.False(t, got[third].SeedGoalMet, "the newest two go on seeding")
	assert.False(t, got[fourth].SeedGoalMet)
	assert.Equal(t, commonv1alpha1.DownloadStageDone, got[first].Stage, "a torrent over the cap stops seeding")
	assert.Equal(t, commonv1alpha1.DownloadStageSeeding, got[fourth].Stage)
	assert.True(t, got[first].CanBeRemoved, "imported and off the swarm: its copy may go")
	assert.False(t, got[second].CanBeRemoved, "not imported yet: its files stay for the importer")
	assert.Less(t, got[first].SeedTime, time.Hour, "its own goal was an hour away")

	// The verdict is sticky, as any met goal is: nothing puts it back.
	require.NoError(t, c.Resume(ctx, first))
	got = list()
	assert.True(t, got[first].SeedGoalMet)
	assert.False(t, got[third].SeedGoalMet, "and a place freed is not taken from the ones seeding")

	fifth := add(5)
	got = list()
	assert.True(t, got[third].SeedGoalMet, "the older of the two gives its place to the new one")
	assert.False(t, got[fourth].SeedGoalMet)
	assert.False(t, got[fifth].SeedGoalMet)
}

// Without the setting nothing is capped: every completed torrent seeds to
// its own goal, as before.
func TestMaxSeeding_UnsetCapsNothing(t *testing.T) {
	cfg := loopbackConfig(t)
	cfg.Seed = true
	raw, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	c := raw.(*Client)
	ctx := context.Background()

	var ids []string
	for n := range 3 {
		seeder, payload := newSeeder(t, []byte(strings.Repeat(fmt.Sprintf("uncapped content %d ", n), 2048)))
		id, err := c.Add(ctx, download.AddRequest{
			Name: fmt.Sprintf("uncapped-%d", n), Payload: payload,
			SeedCriteria: &commonv1alpha1.SeedCriteria{SeedTime: &metav1.Duration{Duration: time.Hour}},
		})
		require.NoError(t, err)
		connectPeer(t, c, id, seeder)
		ids = append(ids, id)
	}
	require.Eventually(t, func() bool {
		items, err := c.List(ctx)
		require.NoError(t, err)
		done := 0
		for _, it := range items {
			if it.CanMoveFiles {
				done++
			}
		}
		return done == len(ids)
	}, 15*time.Second, 20*time.Millisecond)
	items, err := c.List(ctx)
	require.NoError(t, err)
	for _, it := range items {
		assert.False(t, it.SeedGoalMet, it.ID)
	}
}
