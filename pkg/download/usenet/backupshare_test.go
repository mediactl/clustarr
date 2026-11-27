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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

// backupShareRig is a post of parts articles on two primaries and a backup,
// each a stub server; the backup holds every article.
func backupShareRig(t *testing.T, name string, parts int) (first, second, backup *stubServer, c *Client, nzb []byte) {
	t.Helper()
	first, second, backup = newStubServer(t), newStubServer(t), newStubServer(t)
	payloads := make([][]byte, parts)
	for i := range payloads {
		payloads[i] = partPayload(byte(i+1), 64)
	}
	nzb = buildNZB(t, first, name, []fileSpec{{name: "episode.mkv", parts: payloads}})
	second.mirror(first)
	backup.mirror(first)
	bp := backup.provider("bonus", 2, 3)
	bp.Backup = true
	c, _, _ = newTestClient(t, Config{
		Providers: []Provider{first.provider("first", 2, 1), second.provider("second", 2, 2), bp},
		Ledger:    NewLocalLedger(), HealthAction: downloadv1alpha1.HealthActionDelete,
	})
	return first, second, backup, c, nzb
}

func refuseParts(parts []int, servers ...*stubServer) {
	for _, s := range servers {
		for _, i := range parts {
			s.refuse[fmt.Sprintf("f0-p%d@clustarr.test", i)] = 430
		}
	}
}

func upTo(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	return out
}

// A backup server fills a few articles, never the bulk of a download
// (owner, 2026-11-27: "ensure frugal bonus is reserved for downloads that
// have a *few* articles missing - **not** for the entire download. For
// downloads that will depend on frugal-bonus, blocklist and grab the
// next"). On talos thirteen episodes of one poster that none of the three
// primaries held were each fetched whole from frugal-bonus on its chase
// share, two connections, while every transfer slot and most of the
// primaries' connections stood idle behind them.
func TestABackupIsNotAskedForTheBulkOfADownload(t *testing.T) {
	const parts = 200

	t.Run("a post the primaries do not hold fails as missing, the backup unasked", func(t *testing.T) {
		first, second, backup, c, nzb := backupShareRig(t, "Gone", parts)
		refuseParts(upTo(parts), first, second)
		id, err := c.Add(context.Background(), download.AddRequest{Name: "Gone", Payload: nzb})
		require.NoError(t, err)
		it := waitForTerminal(t, c, id)
		require.Equal(t, download.StatusFailed, it.Status, "message: %s", it.Message)
		assert.Equal(t, commonv1alpha1.DownloadFailureMissingArticles, it.FailureReason, "it is blocklisted and the next release taken")
		// The first batch's failures are chased before the transfer has
		// asked for enough to say the post is not there.
		assert.Less(t, backup.askedTotal(), backupSample, "the backup was asked for the post")
		assert.Contains(t, it.Message, "a backup is not asked for")
	})
	t.Run("more than a tenth missing on the primaries is not the backup's", func(t *testing.T) {
		first, second, backup, c, nzb := backupShareRig(t, "Holed", parts)
		refuseParts(upTo(30), first, second)
		id, err := c.Add(context.Background(), download.AddRequest{Name: "Holed", Payload: nzb})
		require.NoError(t, err)
		it := waitForTerminal(t, c, id)
		require.Equal(t, download.StatusFailed, it.Status, "message: %s", it.Message)
		assert.LessOrEqual(t, backup.servedTotal(), parts/10, "the backup supplied more than the tenth the health floor lets go missing")
	})
	t.Run("a few missing articles are", func(t *testing.T) {
		first, second, backup, c, nzb := backupShareRig(t, "Filled", parts)
		refuseParts(upTo(12), first, second)
		id, err := c.Add(context.Background(), download.AddRequest{Name: "Filled", Payload: nzb})
		require.NoError(t, err)
		it := waitForTerminal(t, c, id)
		require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
		assert.Equal(t, 12, backup.servedTotal())
	})
	t.Run("what one primary lacks and the other holds never reaches the backup", func(t *testing.T) {
		first, _, backup, c, nzb := backupShareRig(t, "Elsewhere", parts)
		refuseParts(upTo(parts), first)
		id, err := c.Add(context.Background(), download.AddRequest{Name: "Elsewhere", Payload: nzb})
		require.NoError(t, err)
		it := waitForTerminal(t, c, id)
		require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
		assert.Zero(t, backup.askedTotal())
	})
}
