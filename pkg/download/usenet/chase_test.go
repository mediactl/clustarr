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
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

func fourParts() [][]byte {
	return [][]byte{partPayload(1, 400), partPayload(2, 400), partPayload(3, 400), partPayload(4, 400)}
}

// An article the granted server lacks is fetched from the next server in
// order, under a grant of that server's own: the job parks for it rather
// than fail, and takes nothing else from there (spec 2026-10-16 §4.2).
func TestAMissingArticleIsChasedOnTheNextServerUnderAGrant(t *testing.T) {
	first := newStubServer(t)
	second := newStubServer(t)
	nzb := buildNZB(t, first, "Chased", []fileSpec{{name: "movie.mkv", parts: fourParts()}})
	second.mirror(first)
	first.refuse["f0-p2@clustarr.test"] = 430

	fp, sp := first.provider("first", 2, 1), second.provider("second", 2, 2)
	ledger := newGateLedger(sp.Account())
	c, _, _ := newTestClient(t, Config{Providers: []Provider{fp, sp}, Ledger: ledger, HealthAction: downloadv1alpha1.HealthActionDelete})
	ctx := context.Background()

	id, err := c.Add(ctx, download.AddRequest{Name: "Chased", Payload: nzb})
	require.NoError(t, err)
	waitQueuedForConnections(t, c, id)
	require.Zero(t, second.askedCount("f0-p2@clustarr.test"), "the second server was asked before it granted connections")

	ledger.open(sp.Account())
	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
	require.Equal(t, 1, second.servedCount("f0-p2@clustarr.test"))
	require.Equal(t, 1, second.servedTotal(), "the chase fetches only the missing articles")
	held, _ := ledger.Held(ctx, sp.Account())
	require.Zero(t, held, "a chase grant is given back")
}

// The job fails as missing only once every configured server has been
// asked for the articles still missing.
func TestAJobFailsAsMissingOnlyAfterEveryServerWasTried(t *testing.T) {
	first := newStubServer(t)
	second := newStubServer(t)
	nzb := buildNZB(t, first, "Gone", []fileSpec{{name: "movie.mkv", parts: fourParts()}})
	second.mirror(first)
	for _, srv := range []*stubServer{first, second} {
		srv.refuse["f0-p1@clustarr.test"] = 430
		srv.refuse["f0-p2@clustarr.test"] = 430
	}
	c, _, _ := newTestClient(t, Config{
		Providers:    []Provider{first.provider("first", 2, 1), second.provider("second", 2, 2)},
		HealthAction: downloadv1alpha1.HealthActionDelete,
	})
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Gone", Payload: nzb})
	require.NoError(t, err)
	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusFailed, it.Status)
	require.Equal(t, commonv1.DownloadFailureMissingArticles, it.FailureReason)
	require.GreaterOrEqual(t, first.askedCount("f0-p1@clustarr.test"), 1)
	require.GreaterOrEqual(t, second.askedCount("f0-p1@clustarr.test"), 1, "the verdict came before the second server was tried")
}

// With one server there is nothing to chase: the job fails as missing at
// once, taking no second grant and never parking.
func TestASingleServerJobFailsAsMissingWithoutParking(t *testing.T) {
	srv := newStubServer(t)
	nzb := buildNZB(t, srv, "Solo.Gone", []fileSpec{{name: "movie.mkv", parts: fourParts()}})
	srv.refuse["f0-p1@clustarr.test"] = 430
	srv.refuse["f0-p2@clustarr.test"] = 430
	ledger := newGateLedger()
	c, _, _ := newTestClient(t, Config{
		Providers: []Provider{srv.provider("solo", 2, 1)}, Ledger: ledger,
		HealthAction: downloadv1alpha1.HealthActionDelete,
	})
	start := time.Now()
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Solo.Gone", Payload: nzb})
	require.NoError(t, err)
	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusFailed, it.Status)
	require.Equal(t, commonv1.DownloadFailureMissingArticles, it.FailureReason)
	require.Less(t, time.Since(start), ledgerPollInterval, "the job parked for a server it had already tried")
	require.Equal(t, int32(1), ledger.takes.Load(), "only the transfer's own grant is taken")
}

// A backup server fills exactly the missing articles, under a grant.
func TestABackupFillsOnlyTheMissingArticles(t *testing.T) {
	primary := newStubServer(t)
	backup := newStubServer(t)
	nzb := buildNZB(t, primary, "Filled", []fileSpec{{name: "movie.mkv", parts: fourParts()}})
	backup.mirror(primary)
	primary.refuse["f0-p2@clustarr.test"] = 430
	bp := backup.provider("backup", 2, 2)
	bp.Backup = true
	ledger := NewLocalLedger()
	c, _, _ := newTestClient(t, Config{Providers: []Provider{primary.provider("primary", 2, 1), bp}, Ledger: ledger})
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Filled", Payload: nzb})
	require.NoError(t, err)
	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
	require.Equal(t, 1, backup.servedTotal())
	require.Equal(t, 3, primary.servedTotal())
}

// The pre-check judges "missing everywhere": a shortfall on the granted
// server is STATed on the next server under a one-connection grant, parking
// for it, and a release that server holds is not refused.
func TestThePreCheckChasesTheShortfallBeforeJudging(t *testing.T) {
	first := newStubServer(t)
	second := newStubServer(t)
	parts := manyParts(1, 12, 300)
	nzb := buildNZB(t, first, "Checked", []fileSpec{{name: "movie.mkv", parts: parts}})
	second.mirror(first)
	for i := range 7 {
		first.refuse[fmt.Sprintf("f0-p%d@clustarr.test", i)] = 430
	}
	fp, sp := first.provider("first", 2, 1), second.provider("second", 2, 2)
	ledger := newGateLedger(sp.Account())
	c, _, _ := newTestClient(t, Config{Providers: []Provider{fp, sp}, Ledger: ledger, PreCheck: true, HealthAction: downloadv1alpha1.HealthActionDelete})
	ctx := context.Background()

	id, err := c.Add(ctx, download.AddRequest{Name: "Checked", Payload: nzb})
	require.NoError(t, err)
	waitQueuedForConnections(t, c, id)
	require.Zero(t, first.servedTotal(), "the pre-check must park before any article moves")

	ledger.open(sp.Account())
	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
	require.Equal(t, 7, second.servedTotal())
	require.Equal(t, 5, first.servedTotal())
}
