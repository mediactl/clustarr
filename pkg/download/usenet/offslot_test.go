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
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

// A job whose transfer is over but whose missing articles are still being
// chased gives its transfer slot to the next queued job: the chase parks for
// another server's connections, and the queued job transfers and completes
// meanwhile. Until 2026-10-28 the slot was held through the retry's sleep and
// the whole chase, and nothing new started behind it.
func TestAChasingJobLeavesItsTransferSlotToTheNext(t *testing.T) {
	first := newStubServer(t)
	second := newStubServer(t)
	chasedNZB := buildNZBWithPrefix(t, first, "Chased", "chased-", []fileSpec{{name: "chased.mkv", parts: fourParts()}})
	nextNZB := buildNZBWithPrefix(t, first, "Next", "next-", []fileSpec{{name: "next.mkv", parts: manyParts(60, 2, 300)}})
	second.mirror(first)
	first.refuse["chased-f0-p2@clustarr.test"] = 430

	fp, sp := first.provider("first", 2, 1), second.provider("second", 2, 2)
	ledger := newGateLedger(sp.Account())
	c, _, _ := newTestClient(t, Config{
		Providers: []Provider{fp, sp}, Ledger: ledger, MaxActive: 1,
		HealthAction: downloadv1alpha1.HealthActionDelete,
	})
	ctx := context.Background()

	chasedID, err := c.Add(ctx, download.AddRequest{Name: "Chased", Payload: chasedNZB})
	require.NoError(t, err)
	waitQueuedForConnections(t, c, chasedID) // parked for the second server's grant

	nextID, err := c.Add(ctx, download.AddRequest{Name: "Next", Payload: nextNZB})
	require.NoError(t, err)
	next := waitForTerminal(t, c, nextID)
	require.Equal(t, download.StatusCompleted, next.Status, "message: %s", next.Message)

	chased, err := c.Get(ctx, chasedID)
	require.NoError(t, err)
	require.Equal(t, commonv1.DownloadStageTransferring, chased.Stage, "the chase is still parked")
	require.Zero(t, second.askedCount("chased-f0-p2@clustarr.test"))

	ledger.open(sp.Account())
	done := waitForTerminal(t, c, chasedID)
	require.Equal(t, download.StatusCompleted, done.Status, "message: %s", done.Message)
	require.Equal(t, 1, second.servedCount("chased-f0-p2@clustarr.test"))
}

// A job holding a transfer slot passes its turn check without the client
// lock. Every fetch worker asks before every batch, and on talos (2026-10-28)
// 92 of 115 of them sat behind about 700 queued jobs polling that lock.
func TestASlotHoldersTurnCheckTakesNoClientLock(t *testing.T) {
	c := &Client{jobs: map[string]*job{}, slots: map[*job]struct{}{}, scratch: map[*job]int64{}, maxActive: 1}
	j := &job{client: c, id: "holder"}
	c.jobs[j.id] = j
	c.mu.Lock()
	c.giveSlotLocked(j)
	c.mu.Unlock()
	c.highestTransferring() // the cache is warm, as it is between refreshes

	c.mu.Lock()
	defer c.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- j.waitTurn(context.Background(), false) }()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("a slot holder's turn check waited for the client lock")
	}
}

// A job waiting for a slot starts when the slot is given back, not at its
// next look: with the fallback look taken away, only the wake can start it.
func TestAFreedSlotWakesTheNextJob(t *testing.T) {
	saved := turnWaitInterval
	turnWaitInterval = time.Hour
	t.Cleanup(func() { turnWaitInterval = saved })

	srv := newStubServer(t)
	srv.bodyDelay = 20 * time.Millisecond
	firstNZB := buildNZBWithPrefix(t, srv, "First", "first-", []fileSpec{{name: "first.mkv", parts: manyParts(1, 8, 300)}})
	secondNZB := buildNZBWithPrefix(t, srv, "Second", "second-", []fileSpec{{name: "second.mkv", parts: manyParts(70, 2, 300)}})

	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 2, 1)}, MaxActive: 1})
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
		return err == nil && it.WaitingTurn
	}, 5*time.Second, 5*time.Millisecond)

	require.Equal(t, download.StatusCompleted, waitForTerminal(t, c, firstID).Status)
	second := waitForTerminal(t, c, secondID)
	require.Equal(t, download.StatusCompleted, second.Status, "message: %s", second.Message)
}
