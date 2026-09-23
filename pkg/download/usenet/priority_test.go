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
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

func manyParts(seed byte, n, size int) [][]byte {
	parts := make([][]byte, n)
	for i := range parts {
		parts[i] = partPayload(seed+byte(i), size)
	}
	return parts
}

// TestClientTransfersStrictlyByPriorityClass proves ruling R-12's usenet
// half: a low-priority job added while a high-priority one is transferring
// fetches no article until the high one has left its transfer stage, and
// reports Queued (not Downloading) while it waits. Before this,
// AddRequest.Priority was accepted and never read.
func TestClientTransfersStrictlyByPriorityClass(t *testing.T) {
	srv := newStubServer(t)
	// One connection and a slow BODY make the high job's transfer last long
	// enough to observe the low one waiting behind it.
	srv.bodyDelay = 40 * time.Millisecond
	highNZB := buildNZBWithPrefix(t, srv, "High", "high-", []fileSpec{{name: "high.mkv", parts: manyParts(1, 16, 300)}})
	lowNZB := buildNZBWithPrefix(t, srv, "Low", "low-", []fileSpec{{name: "low.mkv", parts: manyParts(50, 2, 300)}})

	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 1, 1)}})
	ctx := context.Background()

	highID, err := c.Add(ctx, download.AddRequest{Name: "High", Payload: highNZB, Priority: downloadv1alpha1.DownloadPriorityHigh})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		it, err := c.Get(ctx, highID)
		return err == nil && it.Stage == downloadv1alpha1.DownloadStageTransferring && it.Status == download.StatusDownloading
	}, 5*time.Second, 5*time.Millisecond, "the high job must be transferring before the low one is added")

	lowID, err := c.Add(ctx, download.AddRequest{Name: "Low", Payload: lowNZB, Priority: downloadv1alpha1.DownloadPriorityLow})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		it, err := c.Get(ctx, lowID)
		return err == nil && it.Status == download.StatusQueued && it.Stage == downloadv1alpha1.DownloadStageTransferring
	}, 5*time.Second, 5*time.Millisecond, "an outranked job must report Queued while it waits its turn")

	lowArticles := []string{"low-f0-p0@clustarr.test", "low-f0-p1@clustarr.test"}
	for {
		high, err := c.Get(ctx, highID)
		require.NoError(t, err)
		if high.Stage != downloadv1alpha1.DownloadStageTransferring {
			break
		}
		for _, id := range lowArticles {
			require.Zero(t, srv.servedCount(id), "the low job fetched %s while the high job was still transferring", id)
		}
		time.Sleep(10 * time.Millisecond)
	}

	require.Equal(t, download.StatusCompleted, waitForTerminal(t, c, highID).Status)
	low := waitForTerminal(t, c, lowID)
	require.Equal(t, download.StatusCompleted, low.Status, "message: %s", low.Message)
}

// TestPriorityRankTreatsEmptyAsNormal pins the default: spec.priority's CRD
// default is normal, and an object built without apiserver defaulting
// carries "".
func TestPriorityRankTreatsEmptyAsNormal(t *testing.T) {
	require.Equal(t, priorityRank(downloadv1alpha1.DownloadPriorityNormal), priorityRank(""))
	require.Greater(t, priorityRank(downloadv1alpha1.DownloadPriorityHigh), priorityRank(downloadv1alpha1.DownloadPriorityNormal))
	require.Greater(t, priorityRank(downloadv1alpha1.DownloadPriorityNormal), priorityRank(downloadv1alpha1.DownloadPriorityLow))
}

// TestClientKeepsAddedAtAcrossARestart proves Item.AddedAt is a true age:
// the manifest carries it, so a restarted engine reports the original add
// time rather than the restart -- which is what lets an orphan reaper stop
// re-extending an orphan's grace period on every restart.
func TestClientKeepsAddedAtAcrossARestart(t *testing.T) {
	srv := newStubServer(t)
	nzb := buildNZB(t, srv, "Aged", []fileSpec{{name: "movie.mkv", parts: [][]byte{partPayload(3, 500)}}})
	root := t.TempDir()
	cfg := Config{
		Providers:  []Provider{srv.provider("solo", 2, 1)},
		ScratchDir: filepath.Join(root, "scratch"),
		DataDir:    filepath.Join(root, "data"),
	}

	first, err := New(cfg)
	require.NoError(t, err)
	// Close is idempotent; registering it keeps a failed assertion from
	// leaving the pool's connections open, which would block the stub
	// server's own cleanup forever.
	t.Cleanup(func() { _ = first.Close() })
	before := time.Now()
	id, err := first.Add(context.Background(), download.AddRequest{Name: "Aged", Payload: nzb})
	require.NoError(t, err)
	it := waitForTerminal(t, first, id)
	require.False(t, it.AddedAt.IsZero())
	require.False(t, it.AddedAt.Before(before.Add(-time.Second)))
	addedAt := it.AddedAt
	require.NoError(t, first.Close())

	second, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })
	again, err := second.Get(context.Background(), id)
	require.NoError(t, err)
	require.True(t, addedAt.Equal(again.AddedAt), "restart reported %v, want the original %v", again.AddedAt, addedAt)
}

// TestRemoveForgetsDurablyAcrossARestart is the other half of the reaper
// fix: Remove with deleteData=false -- what every orphan reaper passes --
// must still discard the job's scratch manifest, or the next restart
// re-attaches the transfer it was told to forget. The published content
// survives, because deleteData=false is about the data.
func TestRemoveForgetsDurablyAcrossARestart(t *testing.T) {
	srv := newStubServer(t)
	nzb := buildNZB(t, srv, "Forgotten", []fileSpec{{name: "movie.mkv", parts: [][]byte{partPayload(4, 500)}}})
	root := t.TempDir()
	cfg := Config{
		Providers:  []Provider{srv.provider("solo", 2, 1)},
		ScratchDir: filepath.Join(root, "scratch"),
		DataDir:    filepath.Join(root, "data"),
	}

	first, err := New(cfg)
	require.NoError(t, err)
	// Close is idempotent; registering it keeps a failed assertion from
	// leaving the pool's connections open, which would block the stub
	// server's own cleanup forever.
	t.Cleanup(func() { _ = first.Close() })
	id, err := first.Add(context.Background(), download.AddRequest{Name: "Forgotten", Payload: nzb, Category: "movies"})
	require.NoError(t, err)
	it := waitForTerminal(t, first, id)
	require.Equal(t, download.StatusCompleted, it.Status)

	require.NoError(t, first.Remove(context.Background(), id, false))
	require.NoDirExists(t, filepath.Join(cfg.ScratchDir, "movies", id), "forgetting must take the scratch manifest with it")
	require.FileExists(t, filepath.Join(it.OutputPath, "movie.mkv"), "deleteData=false keeps the published content")
	require.NoError(t, first.Close())

	second, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })
	_, err = second.Get(context.Background(), id)
	require.ErrorIs(t, err, download.ErrNotFound, "a restart must not resurrect a removed transfer")
	items, err := second.List(context.Background())
	require.NoError(t, err)
	require.Empty(t, items, fmt.Sprintf("unexpected re-attached transfers: %v", items))
}
