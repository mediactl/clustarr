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

// Z1: the seed counters and the met goal carried across an engine restart
// (design spec §6.3's "persisted cumulative counters"), and a priority
// change after the Add.
package torrent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

// The seed time towards the goal is what the torrent had seeded before the
// restart plus what it seeds now, so a restart does not start the clock
// again.
func TestSeedGoal_SeedTimeCountsOnFromTheHistory(t *testing.T) {
	t0 := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	s := &session{
		hasSeedCriteria: true,
		seedCriteria:    commonv1alpha1.SeedCriteria{SeedTime: &metav1.Duration{Duration: time.Hour}},
		completedAt:     t0,
	}
	s.restoreSeedHistoryLocked(&download.SeedHistory{SeedTime: 50 * time.Minute, UploadedBytes: 7})

	assert.False(t, s.seedGoalMetLocked(0, 100, t0.Add(9*time.Minute)))
	assert.True(t, s.seedGoalMetLocked(0, 100, t0.Add(10*time.Minute)), "50m before the restart plus 10m after is the hour")
	assert.Equal(t, 55*time.Minute, s.seedTimeLocked(t0.Add(5*time.Minute)))
	assert.EqualValues(t, 7, s.lastUploadBytes, "the restored upload must not read as a fresh upload")
}

// A torrent whose goal was met before the restart comes back met: it reports
// the goal and the seed time it had, and stays off the swarm (Stage done,
// not seeding) -- before, a restarted engine seeded it again until the goal
// was re-met. Its upload counts on from the restored figure.
func TestAdd_SeedHistoryWithAMetGoalKeepsTheTorrentOffTheSwarm(t *testing.T) {
	content := []byte(strings.Repeat("seed history content ", 2048))
	seeder, payload := newSeeder(t, content)
	cfg := loopbackConfig(t)
	cfg.Seed = true
	raw, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	c := raw.(*Client)
	ctx := context.Background()

	id, err := c.Add(ctx, download.AddRequest{
		Name:    "seed-history",
		Payload: payload,
		// A goal that could not be met in this test on its own.
		SeedCriteria: &commonv1alpha1.SeedCriteria{SeedTime: &metav1.Duration{Duration: 24 * time.Hour}},
		SeedHistory:  &download.SeedHistory{UploadedBytes: 5 << 20, SeedTime: 3 * time.Hour, GoalMet: true},
	})
	require.NoError(t, err)
	connectPeer(t, c, id, seeder)

	var item download.Item
	require.Eventually(t, func() bool {
		item, err = c.Get(ctx, id)
		require.NoError(t, err)
		return item.Status == download.StatusCompleted
	}, 15*time.Second, 20*time.Millisecond, "the transfer never completed; last %+v", item)

	assert.True(t, item.SeedGoalMet, "a goal met before the restart must stay met")
	assert.Equal(t, downloadv1alpha1.DownloadStageDone, item.Stage, "a torrent past its goal must not seed again")
	assert.Equal(t, 3*time.Hour, item.SeedTime, "a met goal adds no seed time after the restart")
	assert.GreaterOrEqual(t, item.UploadedBytes, int64(5<<20), "the upload must count on from the history")

	require.NoError(t, c.Resume(ctx, id))
	item, err = c.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, downloadv1alpha1.DownloadStageDone, item.Stage, "Resume put a torrent past its goal back on the swarm")
}

// SetPriority moves a running torrent's share of the connection budget, the
// same lever Add uses, and back to anacrolix's default for normal.
func TestSetPriority_MovesTheConnectionBudget(t *testing.T) {
	_, payload := newSeeder(t, []byte("priority content, repeated to fill a piece"))
	c := newTestClient(t)
	ctx := context.Background()

	id, err := c.Add(ctx, download.AddRequest{Name: "priority", Payload: payload})
	require.NoError(t, err)
	tr, _, err := c.lookup(id)
	require.NoError(t, err)

	// SetMaxEstablishedConns returns the previous budget, which is the only
	// read anacrolix offers; each probe puts the value straight back.
	budget := func() int {
		prev := tr.SetMaxEstablishedConns(1)
		tr.SetMaxEstablishedConns(prev)
		return prev
	}
	require.Equal(t, c.defaultConns, budget())

	require.NoError(t, c.SetPriority(ctx, id, downloadv1alpha1.DownloadPriorityHigh))
	assert.Equal(t, highPriorityConns, budget())
	require.NoError(t, c.SetPriority(ctx, id, downloadv1alpha1.DownloadPriorityLow))
	assert.Equal(t, lowPriorityConns, budget())
	require.NoError(t, c.SetPriority(ctx, id, ""))
	assert.Equal(t, c.defaultConns, budget(), `"" is normal, anacrolix's own default`)

	require.ErrorIs(t, c.SetPriority(ctx, strings.Repeat("0", 40), downloadv1alpha1.DownloadPriorityHigh), download.ErrNotFound)
}
