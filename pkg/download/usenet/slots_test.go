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

package usenet

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

// A job past Config.MaxActive waits Queued for a transfer slot, reporting
// WaitingTurn, and fetches no article until the job holding the slot has
// left its transfer. Before the cap every job ran at once, and an engine
// re-attaching a dozen ran out of memory at every start (2026-10-14).
func TestAJobPastMaxActiveWaitsForATransferSlot(t *testing.T) {
	srv := newStubServer(t)
	srv.bodyDelay = 40 * time.Millisecond
	firstNZB := buildNZBWithPrefix(t, srv, "First", "first-", []fileSpec{{name: "first.mkv", parts: manyParts(1, 16, 300)}})
	secondNZB := buildNZBWithPrefix(t, srv, "Second", "second-", []fileSpec{{name: "second.mkv", parts: manyParts(50, 2, 300)}})

	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 4, 1)}, MaxActive: 1})
	ctx := context.Background()

	firstID, err := c.Add(ctx, download.AddRequest{Name: "First", Payload: firstNZB})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		it, err := c.Get(ctx, firstID)
		return err == nil && it.Stage == commonv1.DownloadStageTransferring && it.Status == download.StatusDownloading
	}, 5*time.Second, 5*time.Millisecond)

	secondID, err := c.Add(ctx, download.AddRequest{Name: "Second", Payload: secondNZB})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		it, err := c.Get(ctx, secondID)
		return err == nil && it.Status == download.StatusQueued && it.WaitingTurn &&
			it.Stage == commonv1.DownloadStageTransferring
	}, 5*time.Second, 5*time.Millisecond, "a job past MaxActive must report Queued and WaitingTurn")

	for {
		first, err := c.Get(ctx, firstID)
		require.NoError(t, err)
		if first.Stage != commonv1.DownloadStageTransferring {
			break
		}
		for _, id := range []string{"second-f0-p0@clustarr.test", "second-f0-p1@clustarr.test"} {
			require.Zero(t, srv.servedCount(id), "the second job fetched %s while the first held the only slot", id)
		}
		time.Sleep(10 * time.Millisecond)
	}

	require.Equal(t, download.StatusCompleted, waitForTerminal(t, c, firstID).Status)
	second := waitForTerminal(t, c, secondID)
	require.Equal(t, download.StatusCompleted, second.Status, "message: %s", second.Message)
	require.False(t, second.WaitingTurn)
}

// A high job that finds every slot held by a normal one takes a slot as the
// normal one gives it back at its next batch: the higher class outranks the
// holder, never deadlocking behind it.
func TestAHigherClassTakesTheSlotOfALowerOne(t *testing.T) {
	srv := newStubServer(t)
	srv.bodyDelay = 40 * time.Millisecond
	normalNZB := buildNZBWithPrefix(t, srv, "Normal", "normal-", []fileSpec{{name: "normal.mkv", parts: manyParts(1, 40, 300)}})
	highNZB := buildNZBWithPrefix(t, srv, "High", "high-", []fileSpec{{name: "high.mkv", parts: manyParts(60, 2, 300)}})

	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 1, 1)}, MaxActive: 1})
	ctx := context.Background()

	normalID, err := c.Add(ctx, download.AddRequest{Name: "Normal", Payload: normalNZB})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		it, err := c.Get(ctx, normalID)
		return err == nil && it.Stage == commonv1.DownloadStageTransferring && it.Status == download.StatusDownloading
	}, 5*time.Second, 5*time.Millisecond)

	highID, err := c.Add(ctx, download.AddRequest{Name: "High", Payload: highNZB, Priority: commonv1.DownloadPriorityHigh})
	require.NoError(t, err)
	high := waitForTerminal(t, c, highID)
	require.Equal(t, download.StatusCompleted, high.Status, "message: %s", high.Message)

	normal, err := c.Get(ctx, normalID)
	require.NoError(t, err)
	require.NotEqual(t, download.StatusCompleted, normal.Status, "the high job finished first, on the normal one's slot")
	require.Equal(t, download.StatusCompleted, waitForTerminal(t, c, normalID).Status)
}

// A paused job gives its slot back, so a health-paused job never holds the
// only slot while another waits.
func TestAPausedJobGivesItsSlotBack(t *testing.T) {
	srv := newStubServer(t)
	srv.bodyDelay = 40 * time.Millisecond
	firstNZB := buildNZBWithPrefix(t, srv, "First", "first-", []fileSpec{{name: "first.mkv", parts: manyParts(1, 40, 300)}})
	secondNZB := buildNZBWithPrefix(t, srv, "Second", "second-", []fileSpec{{name: "second.mkv", parts: manyParts(60, 2, 300)}})

	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 1, 1)}, MaxActive: 1})
	ctx := context.Background()

	firstID, err := c.Add(ctx, download.AddRequest{Name: "First", Payload: firstNZB})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		it, err := c.Get(ctx, firstID)
		return err == nil && it.Stage == commonv1.DownloadStageTransferring && it.Status == download.StatusDownloading
	}, 5*time.Second, 5*time.Millisecond)
	secondID, err := c.Add(ctx, download.AddRequest{Name: "Second", Payload: secondNZB})
	require.NoError(t, err)

	require.NoError(t, c.Pause(ctx, firstID))
	second := waitForTerminal(t, c, secondID)
	require.Equal(t, download.StatusCompleted, second.Status, "message: %s", second.Message)

	first, err := c.Get(ctx, firstID)
	require.NoError(t, err)
	require.Equal(t, download.StatusPaused, first.Status)
	require.False(t, first.WaitingTurn, "a paused job is the operator's, not waiting its turn")
	require.NoError(t, c.Resume(ctx, firstID))
	require.Equal(t, download.StatusCompleted, waitForTerminal(t, c, firstID).Status)
}

// A finished transfer waits Queued, in the verifying stage, while
// Config.MaxPostProcess jobs post-process, and takes the token once one is
// given back.
func TestAJobWaitsItsTurnToPostProcess(t *testing.T) {
	srv := newStubServer(t)
	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 1, 1)}, MaxPostProcess: 1})
	j := c.newJob("waiting", "Waiting", "", t.TempDir(), &nzbJob{}, nil)
	c.post <- struct{}{} // another job is post-processing

	got := make(chan func(), 1)
	go func() {
		release, err := j.waitToPostProcess(context.Background())
		if err == nil {
			got <- release
		}
	}()
	require.Eventually(t, func() bool {
		it := j.item()
		return it.Status == download.StatusQueued && it.Stage == commonv1.DownloadStageVerifying && it.WaitingTurn
	}, 5*time.Second, 5*time.Millisecond)
	require.Empty(t, got, "the token is taken")

	<-c.post // the other job finishes
	select {
	case release := <-got:
		require.False(t, j.item().WaitingTurn)
		release()
	case <-time.After(5 * time.Second):
		t.Fatal("the waiting job never took the freed token")
	}
	require.Empty(t, c.post, "release gives the token back")
}
