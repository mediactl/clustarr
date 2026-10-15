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
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

// twoJobsBudget holds one small test job's peak in scratch -- twice its
// bytes plus the headroom -- but never two.
const twoJobsBudget = 2 * diskHeadroomBytes

// A job whose peak does not fit beside the started jobs' waits Queued for
// scratch space, fetching nothing, with transfer slots to spare: a local
// working area of a fixed size must never be promised to more releases than
// it holds, unpacked copies and par2 included (owner, 2026-10-15).
func TestAJobPastTheScratchBudgetWaitsForSpace(t *testing.T) {
	srv := newStubServer(t)
	srv.bodyDelay = 40 * time.Millisecond
	firstNZB := buildNZBWithPrefix(t, srv, "First", "first-", []fileSpec{{name: "first.mkv", parts: manyParts(1, 16, 300)}})
	secondNZB := buildNZBWithPrefix(t, srv, "Second", "second-", []fileSpec{{name: "second.mkv", parts: manyParts(50, 2, 300)}})

	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 4, 1)}, MaxActive: 4, ScratchBudget: twoJobsBudget})
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
		return err == nil && it.Status == download.StatusQueued && it.WaitingTurn
	}, 5*time.Second, 5*time.Millisecond, "a job past the scratch budget must report Queued and WaitingTurn")
	it, err := c.Get(ctx, secondID)
	require.NoError(t, err)
	require.Contains(t, it.Message, "waiting for scratch space")

	for {
		first, err := c.Get(ctx, firstID)
		require.NoError(t, err)
		if first.Status == download.StatusCompleted {
			break
		}
		for _, id := range []string{"second-f0-p0@clustarr.test", "second-f0-p1@clustarr.test"} {
			require.Zero(t, srv.servedCount(id), "the second job fetched %s before the first gave its scratch back", id)
		}
		time.Sleep(10 * time.Millisecond)
	}
	second := waitForTerminal(t, c, secondID)
	require.Equal(t, download.StatusCompleted, second.Status, "message: %s", second.Message)
}

// A release whose peak is more than the whole budget can never start: it
// fails as diskFull, a local fault, before it fetches a byte, rather than
// waiting for space that never comes.
func TestAReleaseLargerThanTheScratchBudgetFailsAsDiskFull(t *testing.T) {
	srv := newStubServer(t)
	nzb := buildNZB(t, srv, "Too.Big", []fileSpec{{name: "movie.mkv", parts: [][]byte{partPayload(1, 900), partPayload(2, 512)}}})
	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 2, 1)}, ScratchBudget: diskHeadroomBytes})
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Too.Big", Payload: nzb, Category: "movies"})
	require.NoError(t, err)
	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusFailed, it.Status)
	require.Equal(t, commonv1.DownloadFailureDiskFull, it.FailureReason)
	require.Contains(t, it.Message, "scratch budget")
	require.Zero(t, srv.servedCount("f0-p0@clustarr.test"), "nothing was fetched")
}

// A paused job gives its transfer slot back but not its scratch space: its
// partial files stay on disk. Removing it frees the space for the next job.
func TestAPausedJobKeepsItsScratchUntilRemoved(t *testing.T) {
	srv := newStubServer(t)
	srv.bodyDelay = 40 * time.Millisecond
	firstNZB := buildNZBWithPrefix(t, srv, "First", "first-", []fileSpec{{name: "first.mkv", parts: manyParts(1, 40, 300)}})
	secondNZB := buildNZBWithPrefix(t, srv, "Second", "second-", []fileSpec{{name: "second.mkv", parts: manyParts(60, 2, 300)}})

	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 1, 1)}, MaxActive: 4, ScratchBudget: twoJobsBudget})
	ctx := context.Background()

	firstID, err := c.Add(ctx, download.AddRequest{Name: "First", Payload: firstNZB})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		it, err := c.Get(ctx, firstID)
		return err == nil && it.Stage == commonv1.DownloadStageTransferring && it.DownloadedBytes > 0
	}, 5*time.Second, 5*time.Millisecond)
	require.NoError(t, c.Pause(ctx, firstID))
	secondID, err := c.Add(ctx, download.AddRequest{Name: "Second", Payload: secondNZB})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		it, err := c.Get(ctx, secondID)
		return err == nil && it.Status == download.StatusQueued && it.WaitingTurn
	}, 5*time.Second, 5*time.Millisecond, "the paused job's partial files still hold the scratch")
	time.Sleep(200 * time.Millisecond)
	require.Zero(t, srv.servedCount("second-f0-p0@clustarr.test"))

	require.NoError(t, c.Remove(ctx, firstID, true))
	second := waitForTerminal(t, c, secondID)
	require.Equal(t, download.StatusCompleted, second.Status, "message: %s", second.Message)
}

// After a restart, a re-attached job with data on disk holds its scratch
// before any new job is admitted: its files are already there.
func TestAReattachedJobHoldsItsScratchFirst(t *testing.T) {
	srv, cheese := swissCheese(t)
	small := buildNZBWithPrefix(t, srv, "Small", "small-", []fileSpec{{name: "small.mkv", parts: manyParts(80, 2, 300)}})
	root := t.TempDir()
	cfg := Config{
		Providers:     []Provider{srv.provider("solo", 2, 1)},
		ScratchDir:    filepath.Join(root, "scratch"),
		DataDir:       filepath.Join(root, "data"),
		MaxActive:     4,
		ScratchBudget: twoJobsBudget,
	}
	ctx := context.Background()

	first, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })
	cheeseID, err := first.Add(ctx, download.AddRequest{Name: "Swiss.Cheese", Payload: cheese})
	require.NoError(t, err)
	waitForHealthPause(t, first, cheeseID)
	require.NoError(t, first.Close())

	second, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })
	smallID, err := second.Add(ctx, download.AddRequest{Name: "Small", Payload: small})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		it, err := second.Get(ctx, smallID)
		return err == nil && it.Status == download.StatusQueued && it.WaitingTurn
	}, 5*time.Second, 5*time.Millisecond, "the re-attached job's files hold the scratch")

	require.NoError(t, second.Remove(ctx, cheeseID, true))
	it := waitForTerminal(t, second, smallID)
	require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
}
