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

// Gap fix Y2: the two verdicts this package reaches over time -- a stall and
// a met seed goal -- and the write-error classification.
package torrent

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

// A magnet nobody can serve never gets its metadata: qBittorrent's metaDL
// with no one to ask. Once StallTimeout passes it fails as stalled, and
// stays failed.
func TestStall_AMagnetNobodyServesFailsAfterTheTimeout(t *testing.T) {
	cfg := loopbackConfig(t)
	cfg.StallTimeout = 300 * time.Millisecond
	raw, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	ctx := context.Background()

	id, err := raw.Add(ctx, download.AddRequest{
		Name:   "nobody-seeds-this",
		Magnet: "magnet:?xt=urn:btih:" + strings.Repeat("ab", 20),
	})
	require.NoError(t, err)

	item, err := raw.Get(ctx, id)
	require.NoError(t, err)
	require.NotEqual(t, download.StatusFailed, item.Status, "failed before the stall timeout")

	require.Eventually(t, func() bool {
		item, err = raw.Get(ctx, id)
		require.NoError(t, err)
		return item.Status == download.StatusFailed
	}, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, downloadv1alpha1.DownloadFailureStalled, item.FailureReason)
	assert.Contains(t, item.Message, "stalled")

	// Resume is not a way out of a failure.
	require.NoError(t, raw.Resume(ctx, id))
	item, err = raw.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, download.StatusFailed, item.Status)
}

// A paused transfer is making no progress on purpose, so it never stalls;
// Resume starts the clock again rather than counting the pause.
func TestStall_APausedTransferNeverStallsAndResumeRestartsTheClock(t *testing.T) {
	cfg := loopbackConfig(t)
	cfg.StallTimeout = 250 * time.Millisecond
	raw, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	ctx := context.Background()

	id, err := raw.Add(ctx, download.AddRequest{
		Name:   "paused-magnet",
		Magnet: "magnet:?xt=urn:btih:" + strings.Repeat("cd", 20),
		Paused: true,
	})
	require.NoError(t, err)

	time.Sleep(600 * time.Millisecond)
	item, err := raw.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, download.StatusPaused, item.Status, "a paused transfer stalled")

	require.NoError(t, raw.Resume(ctx, id))
	item, err = raw.Get(ctx, id)
	require.NoError(t, err)
	require.NotEqual(t, download.StatusFailed, item.Status, "the pause counted against the stall clock")

	require.Eventually(t, func() bool {
		item, err = raw.Get(ctx, id)
		require.NoError(t, err)
		return item.Status == download.StatusFailed
	}, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, downloadv1alpha1.DownloadFailureStalled, item.FailureReason)
}

// Zero disables stall detection: the Config every caller had before Y2.
func TestStall_ZeroTimeoutNeverStalls(t *testing.T) {
	s := &session{activeSince: time.Now().Add(-100 * 24 * time.Hour)}
	stopped := false
	s.checkStallLocked(func() { stopped = true }, 0, time.Now())
	assert.False(t, s.failed)
	assert.False(t, stopped)
}

// The window runs from the later of the last progress and activeSince, so a
// transfer that moves any byte inside it lives on.
func TestStall_ProgressRestartsTheWindow(t *testing.T) {
	t0 := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	s := &session{activeSince: t0, hasProgress: true, lastProgressAt: t0.Add(50 * time.Minute)}
	stopped := false
	stop := func() { stopped = true }

	s.checkStallLocked(stop, time.Hour, t0.Add(100*time.Minute))
	require.False(t, s.failed, "stalled inside the window that the last progress started")
	require.False(t, stopped)

	s.checkStallLocked(stop, time.Hour, t0.Add(111*time.Minute))
	require.True(t, s.failed)
	assert.True(t, stopped, "a stalled transfer must stop requesting data")
	assert.Equal(t, downloadv1alpha1.DownloadFailureStalled, s.failureReason)
}

// Seed criteria: any one limit meets the goal -- ratio, seed time, or
// inactive seeding time measured from the later of completion and the last
// upload.
func TestSeedGoal_InactiveTimeCountsFromTheLastUpload(t *testing.T) {
	t0 := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	s := &session{
		hasSeedCriteria: true,
		seedCriteria:    commonv1alpha1.SeedCriteria{InactiveTime: &metav1.Duration{Duration: 30 * time.Minute}},
		completedAt:     t0,
		lastUploadAt:    t0.Add(10 * time.Minute),
	}
	assert.False(t, s.seedGoalMetLocked(0, 100, t0.Add(30*time.Minute)), "idle only 20 minutes")
	assert.True(t, s.seedGoalMetLocked(0, 100, t0.Add(41*time.Minute)))
}

func TestSeedGoal_RatioOrSeedTime(t *testing.T) {
	t0 := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	ratio := resource.MustParse("1.5")
	s := &session{
		hasSeedCriteria: true,
		seedCriteria: commonv1alpha1.SeedCriteria{
			Ratio:    &ratio,
			SeedTime: &metav1.Duration{Duration: time.Hour},
		},
		completedAt:  t0,
		lastUploadAt: t0,
	}
	assert.False(t, s.seedGoalMetLocked(140, 100, t0.Add(time.Minute)))
	assert.True(t, s.seedGoalMetLocked(150, 100, t0.Add(time.Minute)), "ratio 1.5 reached")
	assert.True(t, s.seedGoalMetLocked(0, 100, t0.Add(time.Hour)), "seed time reached")
}

func TestChunkWriteFailureClassifiesDiskFull(t *testing.T) {
	wrap := func(errno syscall.Errno) error {
		return fmt.Errorf("storage: %w", &fs.PathError{Op: "write", Path: "/data/x", Err: errno})
	}
	assert.Equal(t, downloadv1alpha1.DownloadFailureDiskFull, chunkWriteFailure(wrap(syscall.ENOSPC)))
	assert.Equal(t, downloadv1alpha1.DownloadFailureDiskFull, chunkWriteFailure(wrap(syscall.EDQUOT)))
	assert.Equal(t, downloadv1alpha1.DownloadFailureWriteError, chunkWriteFailure(wrap(syscall.EIO)))
	assert.Equal(t, downloadv1alpha1.DownloadFailureWriteError, chunkWriteFailure(wrap(syscall.EROFS)))
}

// A completed torrent reports its seed goal on its own -- before any import
// -- and stops seeding once it is met (design spec §6.3); CanBeRemoved
// still waits for the import.
func TestSeedGoal_ACompletedTorrentReportsTheGoalAndStopsSeeding(t *testing.T) {
	content := []byte(strings.Repeat("seed goal content ", 2048))
	seeder, payload := newSeeder(t, content)
	cfg := loopbackConfig(t)
	cfg.Seed = true
	raw, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	c := raw.(*Client)
	ctx := context.Background()

	id, err := c.Add(ctx, download.AddRequest{
		Name:         "seed-goal",
		Payload:      payload,
		SeedCriteria: &commonv1alpha1.SeedCriteria{SeedTime: &metav1.Duration{Duration: 300 * time.Millisecond}},
	})
	require.NoError(t, err)
	connectPeer(t, c, id, seeder)

	var item download.Item
	require.Eventually(t, func() bool {
		item, err = c.Get(ctx, id)
		require.NoError(t, err)
		return item.SeedGoalMet
	}, 15*time.Second, 20*time.Millisecond, "the seed goal was never reported; last %+v", item)

	assert.Equal(t, download.StatusCompleted, item.Status)
	assert.Equal(t, downloadv1alpha1.DownloadStageDone, item.Stage, "a torrent past its seed goal must stop seeding")
	assert.GreaterOrEqual(t, item.SeedTime, 300*time.Millisecond)
	assert.False(t, item.CanBeRemoved, "not imported yet")

	require.NoError(t, c.MarkImported(ctx, id))
	item, err = c.Get(ctx, id)
	require.NoError(t, err)
	assert.True(t, item.CanBeRemoved)

	// Resume must not put a torrent past its goal back on the swarm.
	require.NoError(t, c.Resume(ctx, id))
	item, err = c.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, downloadv1alpha1.DownloadStageDone, item.Stage)
}
