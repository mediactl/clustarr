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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

// Queued jobs cost nothing while nothing changes: each waits on its own
// ticket, and the queue looks at them only when something changes. On talos
// (2026-10-28) about 720 queued jobs each looked under the client lock at
// every wake and every two seconds, walking every job, and the engine's
// commands -- two removes among them -- waited behind them for minutes.
func TestQueuedJobsCostNothingWhileNothingChanges(t *testing.T) {
	saved := turnWaitInterval
	turnWaitInterval = 50 * time.Millisecond
	t.Cleanup(func() { turnWaitInterval = saved })

	srv := newStubServer(t)
	srv.bodyDelay = 50 * time.Millisecond
	first := buildNZBWithPrefix(t, srv, "First", "first-", []fileSpec{{name: "first.mkv", parts: manyParts(1, 200, 300)}})
	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 1, 1)}, MaxActive: 1, PipelineDepth: 1})
	ctx := context.Background()

	firstID, err := c.Add(ctx, download.AddRequest{Name: "First", Payload: first})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		it, err := c.Get(ctx, firstID)
		return err == nil && it.Stage == commonv1.DownloadStageTransferring && it.Status == download.StatusDownloading
	}, 5*time.Second, 5*time.Millisecond)

	var queued []string
	for i := range 100 {
		name := fmt.Sprintf("Queued%03d", i)
		nzb := buildNZBWithPrefix(t, srv, name, name+"-", []fileSpec{{name: name + ".mkv", parts: manyParts(byte(i), 1, 300)}})
		id, err := c.Add(ctx, download.AddRequest{Name: name, Payload: nzb})
		require.NoError(t, err)
		queued = append(queued, id)
	}
	require.Eventually(t, func() bool {
		for _, id := range queued {
			if it, err := c.Get(ctx, id); err != nil || !it.WaitingTurn {
				return false
			}
		}
		return true
	}, 10*time.Second, 10*time.Millisecond, "every queued job waits its turn")

	before := c.queueLooks.Load()
	time.Sleep(500 * time.Millisecond) // ten of the old fallback looks
	looks := c.queueLooks.Load() - before
	require.Less(t, looks, int64(len(queued)), "the queue looked at %d queued jobs in half a second with nothing changing", looks)
}
