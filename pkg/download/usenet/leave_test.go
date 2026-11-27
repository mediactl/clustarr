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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

// A transfer leaves a primary that does not hold its post (owner,
// 2026-11-27: "frugal gets blacklisted when it's missing too many articles
// and then the download has to use easynews - reconciling the connections
// used"). On talos thirteen episodes that no primary held each asked its
// granted server for every article, then every other server for every
// article in turn, holding all eight slots and most of three providers'
// connections for an hour.
func TestATransferLeavesAPrimaryThatDoesNotHoldThePost(t *testing.T) {
	const parts = 600

	t.Run("the post is on the next primary: the first is left after a sample and its connections go back", func(t *testing.T) {
		// The next primary has no room yet, as easynews had none on talos:
		// the transfer used to go on asking the first for every article
		// under its grant while its chase waited.
		first, second, backup := newStubServer(t), newStubServer(t), newStubServer(t)
		payloads := make([][]byte, parts)
		for i := range payloads {
			payloads[i] = partPayload(byte(i+1), 64)
		}
		nzb := buildNZB(t, first, "Elsewhere", []fileSpec{{name: "episode.mkv", parts: payloads}})
		second.mirror(first)
		backup.mirror(first)
		refuseParts(upTo(parts), first)
		fp, sp, bp := first.provider("first", 4, 1), second.provider("second", 4, 2), backup.provider("bonus", 2, 3)
		bp.Backup = true
		ledger := newGateLedger(sp.Account())
		old := ledgerPollInterval
		ledgerPollInterval = 20 * time.Millisecond
		t.Cleanup(func() { ledgerPollInterval = old })
		c, _, _ := newTestClient(t, Config{
			Providers: []Provider{fp, sp, bp}, Ledger: ledger,
			// One transfer at a time, so its grant is all four connections:
			// one worker's chase waits for the second server while the
			// others fetch on, as on talos.
			MaxActive: 1, PipelineDepth: 4, HealthAction: downloadv1alpha1.HealthActionDelete,
		})
		id, err := c.Add(context.Background(), download.AddRequest{Name: "Elsewhere", Payload: nzb})
		require.NoError(t, err)

		left := func() bool {
			held, err := ledger.Held(context.Background(), fp.Account())
			return err == nil && held == 0 && first.askedTotal() >= lackSample
		}
		for deadline := time.Now().Add(5 * time.Second); !left() && time.Now().Before(deadline); {
			time.Sleep(5 * time.Millisecond)
		}
		if !left() {
			held, _ := ledger.Held(context.Background(), fp.Account())
			it, _ := c.Get(context.Background(), id)
			require.FailNowf(t, "the transfer kept its grant on the server that refuses everything",
				"first: %d connections held, asked for %d articles; the job: %s %q", held, first.askedTotal(), it.Status, it.Message)
		}
		time.Sleep(10 * ledgerPollInterval) // nothing more is asked of the first while it waits for the second
		assert.Less(t, first.askedTotal(), parts/4, "the first was asked for %d of %d articles it does not hold", first.askedTotal(), parts)
		assert.Zero(t, second.askedTotal(), "the second was asked before it granted connections")

		ledger.open(sp.Account())
		it := waitForTerminal(t, c, id)
		require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
		assert.Equal(t, parts, second.servedTotal(), "the next primary serves every article once")
		assert.Zero(t, backup.askedTotal())
	})
	t.Run("the post is on no primary: it fails as missing after a sample of each", func(t *testing.T) {
		first, second, backup, c, nzb := backupShareRig(t, "Nowhere", parts)
		refuseParts(upTo(parts), first, second)
		id, err := c.Add(context.Background(), download.AddRequest{Name: "Nowhere", Payload: nzb})
		require.NoError(t, err)
		it := waitForTerminal(t, c, id)
		require.Equal(t, download.StatusFailed, it.Status, "message: %s", it.Message)
		assert.Equal(t, commonv1alpha1.DownloadFailureMissingArticles, it.FailureReason)
		// The verdict is the leave's ("no primary server holds the post")
		// or, when the first batch's failures were chased and judged before
		// the sample was in, the health floor's over the primaries.
		assert.Contains(t, it.Message, "primary server")
		assert.Less(t, first.askedTotal(), parts/4, "the first was asked for %d of %d", first.askedTotal(), parts)
		assert.Less(t, second.askedTotal(), parts/4, "the second was asked for %d of %d", second.askedTotal(), parts)
		assert.Less(t, backup.askedTotal(), backupSample, "the backup was asked for the post")
	})
	t.Run("a post whose head is missing everywhere is not given up", func(t *testing.T) {
		first, second, backup, c, nzb := backupShareRig(t, "Headless", parts)
		refuseParts(upTo(48), first, second)
		id, err := c.Add(context.Background(), download.AddRequest{Name: "Headless", Payload: nzb})
		require.NoError(t, err)
		it := waitForTerminal(t, c, id)
		require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
		assert.Equal(t, 48, backup.servedTotal(), "the backup fills the head")
		assert.Equal(t, parts-48, first.servedTotal()+second.servedTotal())
	})
	t.Run("a primary that holds the post with holes is not left for them", func(t *testing.T) {
		// One article in twenty: far under the half that says the post is
		// not there. (The transfer may still move to the server its chase
		// recovers the holes from, moveGrant.)
		first, second, backup, c, nzb := backupShareRig(t, "Holes", parts)
		holes := make([]int, 0, parts/20)
		for i := 5; i < parts; i += 20 {
			holes = append(holes, i)
		}
		refuseParts(holes, first)
		id, err := c.Add(context.Background(), download.AddRequest{Name: "Holes", Payload: nzb})
		require.NoError(t, err)
		it := waitForTerminal(t, c, id)
		require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
		assert.Equal(t, parts, first.servedTotal()+second.servedTotal(), "each article is served once, by a primary")
		assert.Greater(t, first.servedTotal(), lackSample, "the first primary went on serving past the sample")
		assert.Zero(t, backup.askedTotal())
	})
}
