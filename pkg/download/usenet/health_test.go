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

// swissCheese is a twelve-article release seven of whose articles the only
// provider refuses: 41% health, below the default floor and past the few
// bad articles SABnzbd tolerates, and with no par2 hopeless too.
func swissCheese(t *testing.T) (*stubServer, []byte) {
	t.Helper()
	srv := newStubServer(t)
	parts := make([][]byte, 12)
	for i := range parts {
		parts[i] = partPayload(byte(i+1), 400)
	}
	nzb := buildNZB(t, srv, "Swiss.Cheese", []fileSpec{{name: "movie.mkv", parts: parts}})
	for _, i := range []int{1, 2, 4, 6, 8, 9, 10} {
		srv.refuse[fmt.Sprintf("f0-p%d@clustarr.test", i)] = 430
	}
	return srv, nzb
}

func waitForHealthPause(t *testing.T, c download.Client, id string) download.Item {
	t.Helper()
	var it download.Item
	require.Eventually(t, func() bool {
		var err error
		it, err = c.Get(context.Background(), id)
		require.NoError(t, err)
		require.NotEqual(t, download.StatusFailed, it.Status, "healthAction pause failed the job: %s", it.Message)
		return it.HealthPaused
	}, 20*time.Second, 10*time.Millisecond, "the job never reported a health pause")
	return it
}

// healthAction pause, the CRD default: a breach holds the job paused with a
// clear status for an operator, rather than failing and so blocklisting it.
// Resume alone -- which the engine calls whenever spec.paused is false --
// must not lift it; the operator's Pause then Resume carries on without the
// health check, which is NZBGet's HealthCheck=pause.
func TestHealthActionPauseHoldsTheJobForAnOperator(t *testing.T) {
	srv, nzb := swissCheese(t)
	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 2, 1)}})
	ctx := context.Background()

	id, err := c.Add(ctx, download.AddRequest{Name: "Swiss.Cheese", Payload: nzb})
	require.NoError(t, err)

	it := waitForHealthPause(t, c, id)
	require.Equal(t, download.StatusPaused, it.Status)
	require.Contains(t, it.Message, "health 41%")
	require.Contains(t, it.Message, "healthAction pause")
	require.False(t, it.FailureReason.IsFailure())

	require.NoError(t, c.Resume(ctx, id))
	time.Sleep(3 * turnPollInterval)
	it, err = c.Get(ctx, id)
	require.NoError(t, err)
	require.True(t, it.HealthPaused, "Resume lifted a health pause; the engine would undo it on every poll")
	require.Equal(t, download.StatusPaused, it.Status)

	require.NoError(t, c.Pause(ctx, id))
	it, err = c.Get(ctx, id)
	require.NoError(t, err)
	require.False(t, it.HealthPaused, "Pause is the operator's answer: the health pause becomes an ordinary one")
	require.NoError(t, c.Resume(ctx, id))

	it = waitForTerminal(t, c, id)
	require.Equal(t, download.StatusCompleted, it.Status,
		"after the operator's answer the job carries on without the health check: %s", it.Message)
	require.False(t, it.HealthPaused)
}

// A health pause, and the operator's answer to one, survive a restart: the
// job must neither resume by itself nor pause again once told to carry on.
func TestHealthPauseSurvivesARestart(t *testing.T) {
	srv, nzb := swissCheese(t)
	root := t.TempDir()
	cfg := Config{
		Providers:  []Provider{srv.provider("solo", 2, 1)},
		ScratchDir: filepath.Join(root, "scratch"),
		DataDir:    filepath.Join(root, "data"),
	}
	ctx := context.Background()

	first, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = first.Close() })
	id, err := first.Add(ctx, download.AddRequest{Name: "Swiss.Cheese", Payload: nzb})
	require.NoError(t, err)
	waitForHealthPause(t, first, id)
	require.NoError(t, first.Close())

	second, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })
	it, err := second.Get(ctx, id)
	require.NoError(t, err)
	require.True(t, it.HealthPaused, "a restart forgot the health pause")
	require.Equal(t, download.StatusPaused, it.Status)

	require.NoError(t, second.Pause(ctx, id))
	require.NoError(t, second.Close())

	third, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = third.Close() })
	require.NoError(t, third.Resume(ctx, id))
	it = waitForTerminal(t, third, id)
	require.Equal(t, download.StatusCompleted, it.Status,
		"a restart forgot the operator's answer and paused again: %s", it.Message)
}

// The pre-check applies the same action: under pause it holds the job
// before any article is fetched, reporting Paused although the pre-check
// runs as Queued.
func TestHealthActionPauseAppliesToThePreCheck(t *testing.T) {
	srv, nzb := swissCheese(t)
	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 2, 1)}, PreCheck: true})

	id, err := c.Add(context.Background(), download.AddRequest{Name: "Swiss.Cheese", Payload: nzb})
	require.NoError(t, err)

	it := waitForHealthPause(t, c, id)
	require.Equal(t, download.StatusPaused, it.Status)
	require.Contains(t, it.Message, "pre-check found 7 of 12 articles missing")
	require.Zero(t, it.DownloadedBytes, "the job fetched articles past a failed pre-check")
}

// SetPriority reaches a job after its Add: a low job waiting behind a high
// one starts transferring once it is raised, and the class is persisted.
func TestSetPriorityRaisesAWaitingJob(t *testing.T) {
	srv := newStubServer(t)
	srv.bodyDelay = 40 * time.Millisecond
	highNZB := buildNZBWithPrefix(t, srv, "High", "high-", []fileSpec{{name: "high.mkv", parts: manyParts(1, 40, 300)}})
	lowNZB := buildNZBWithPrefix(t, srv, "Low", "low-", []fileSpec{{name: "low.mkv", parts: manyParts(90, 4, 300)}})

	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 2, 1)}})
	ctx := context.Background()

	highID, err := c.Add(ctx, download.AddRequest{Name: "High", Payload: highNZB, Priority: downloadv1alpha1.DownloadPriorityHigh})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		it, err := c.Get(ctx, highID)
		return err == nil && it.Status == download.StatusDownloading
	}, 5*time.Second, 5*time.Millisecond)

	lowID, err := c.Add(ctx, download.AddRequest{Name: "Low", Payload: lowNZB, Priority: downloadv1alpha1.DownloadPriorityLow})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		it, err := c.Get(ctx, lowID)
		return err == nil && it.Status == download.StatusQueued && it.Stage == downloadv1alpha1.DownloadStageTransferring
	}, 5*time.Second, 5*time.Millisecond, "the low job must be waiting behind the high one")

	require.NoError(t, c.SetPriority(ctx, lowID, downloadv1alpha1.DownloadPriorityHigh))
	require.Eventually(t, func() bool {
		high, err := c.Get(ctx, highID)
		require.NoError(t, err)
		require.Equal(t, downloadv1alpha1.DownloadStageTransferring, high.Stage,
			"the high job finished first; the test cannot tell whether the raise did anything")
		return srv.servedCount("low-f0-p1@clustarr.test") > 0
	}, 5*time.Second, 5*time.Millisecond, "a raised job still waited for the job it now shares a class with")

	j, err := c.lookup(lowID)
	require.NoError(t, err)
	require.Equal(t, downloadv1alpha1.DownloadPriorityHigh, j.getPriority())

	require.NoError(t, c.SetPriority(ctx, lowID, downloadv1alpha1.DownloadPriorityHigh), "the same class again is a no-op")
	require.ErrorIs(t, c.SetPriority(ctx, "no-such-job", downloadv1alpha1.DownloadPriorityLow), download.ErrNotFound)
}
