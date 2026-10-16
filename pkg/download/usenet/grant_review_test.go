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

// The final review's findings on the connection ledger (2026-10-16), each
// reproduced here before its fix.

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

// A server the first chase asked for some articles is asked again for the
// articles a later breach finds missing: "tried" is per chase, not per
// transfer (review C1).
func TestASecondBreachIsChasedOnAServerTriedForEarlierArticles(t *testing.T) {
	primary := newStubServer(t)
	backup := newStubServer(t)
	nzb := buildNZB(t, primary, "Scattered", []fileSpec{{name: "movie.mkv", parts: manyParts(1, 60, 300)}})
	backup.mirror(primary)
	for i := 0; i < 60; i += 3 {
		primary.refuse[fmt.Sprintf("f0-p%d@clustarr.test", i)] = 430
	}
	bp := backup.provider("backup", 1, 2)
	bp.Backup = true
	c, _, _ := newTestClient(t, Config{
		Providers:     []Provider{primary.provider("primary", 1, 1), bp},
		PipelineDepth: 1, HealthAction: downloadv1alpha1.HealthActionDelete,
	})
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Scattered", Payload: nzb})
	require.NoError(t, err)
	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
	require.Equal(t, 20, backup.servedTotal())
}

// A primary that cannot be asked (502 on connect) is not granted: the
// transfer takes the next primary instead of waiting on it forever
// (review C2).
func TestATransferTakesTheNextPrimaryWhenTheFirstCannotBeAsked(t *testing.T) {
	first := newStubServer(t)
	second := newStubServer(t)
	nzb := buildNZB(t, first, "Failover", []fileSpec{{name: "movie.mkv", parts: fourParts()}})
	second.mirror(first)
	first.greetBusy = true
	c, _, _ := newTestClient(t, Config{
		Providers:          []Provider{first.provider("first", 2, 1), second.provider("second", 2, 2)},
		ProviderRetryDelay: 20 * time.Millisecond,
	})
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Failover", Payload: nzb})
	require.NoError(t, err)
	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
	require.Equal(t, 4, second.servedTotal())
}

// Workers that breach at once run one chase between them, so each missing
// article is fetched once from the next server (review I3).
func TestConcurrentBreachesRunOneChase(t *testing.T) {
	primary := newStubServer(t)
	backup := newStubServer(t)
	nzb := buildNZB(t, primary, "Dup", []fileSpec{{name: "movie.mkv", parts: manyParts(1, 40, 300)}})
	backup.mirror(primary)
	backup.bodyDelay = 30 * time.Millisecond
	primary.bodyDelay = 5 * time.Millisecond
	for i := range 10 {
		primary.refuse[fmt.Sprintf("f0-p%d@clustarr.test", i)] = 430
	}
	bp := backup.provider("backup", 4, 2)
	bp.Backup = true
	c, _, _ := newTestClient(t, Config{
		Providers:     []Provider{primary.provider("primary", 4, 1), bp},
		PipelineDepth: 1, MaxActive: 1, HealthAction: downloadv1alpha1.HealthActionDelete,
	})
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Dup", Payload: nzb})
	require.NoError(t, err)
	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
	require.Equal(t, 10, backup.servedTotal(), "each missing article is fetched once from the backup")
}

// A job the pre-check health-paused holds neither its slot nor its
// connections: another job transfers meanwhile (review I4).
func TestAHealthPausedPreCheckHoldsNeitherSlotNorConnections(t *testing.T) {
	srv := newStubServer(t)
	bad := buildNZBWithPrefix(t, srv, "Bad", "bad-", []fileSpec{{name: "bad.mkv", parts: manyParts(1, 12, 300)}})
	good := buildNZBWithPrefix(t, srv, "Good", "good-", []fileSpec{{name: "good.mkv", parts: manyParts(60, 4, 300)}})
	for i := range 8 {
		srv.refuse[fmt.Sprintf("bad-f0-p%d@clustarr.test", i)] = 430
	}
	prov := srv.provider("solo", 4, 1)
	ledger := NewLocalLedger()
	c, _, _ := newTestClient(t, Config{Providers: []Provider{prov}, Ledger: ledger, PreCheck: true, MaxActive: 1})
	ctx := context.Background()

	badID, err := c.Add(ctx, download.AddRequest{Name: "Bad", Payload: bad})
	require.NoError(t, err)
	require.Eventually(t, func() bool { it, _ := c.Get(ctx, badID); return it.HealthPaused }, 5*time.Second, 5*time.Millisecond)
	require.Eventually(t, func() bool { held, _ := ledger.Held(ctx, prov.Account()); return held == 0 },
		2*time.Second, 5*time.Millisecond, "a health-paused job holds connections")

	goodID, err := c.Add(ctx, download.AddRequest{Name: "Good", Payload: good})
	require.NoError(t, err)
	require.Equal(t, download.StatusCompleted, waitForTerminal(t, c, goodID).Status, "the paused job holds the only slot")
}

// A job paused while parked for connections gives its slot up, and one
// outranked while parked gives way (review I5).
func TestAPausedOrOutrankedParkedJobGivesUpItsSlot(t *testing.T) {
	srv := newStubServer(t)
	a := buildNZBWithPrefix(t, srv, "A", "a-", []fileSpec{{name: "a.mkv", parts: manyParts(1, 4, 300)}})
	b := buildNZBWithPrefix(t, srv, "B", "b-", []fileSpec{{name: "b.mkv", parts: manyParts(30, 4, 300)}})
	prov := srv.provider("solo", 4, 1)
	ledger := newGateLedger(prov.Account())
	c, _, _ := newTestClient(t, Config{Providers: []Provider{prov}, Ledger: ledger, MaxActive: 1})
	ctx := context.Background()

	aID, err := c.Add(ctx, download.AddRequest{Name: "A", Payload: a})
	require.NoError(t, err)
	waitQueuedForConnections(t, c, aID)
	require.NoError(t, c.Pause(ctx, aID))
	ledger.open(prov.Account())
	bID, err := c.Add(ctx, download.AddRequest{Name: "B", Payload: b})
	require.NoError(t, err)
	require.Equal(t, download.StatusCompleted, waitForTerminal(t, c, bID).Status, "a paused, parked job must not hold the slot")

	// Outranked: A (normal) parks again; a high job must take the slot.
	ledger2 := newGateLedger(prov.Account())
	c2, _, _ := newTestClient(t, Config{Providers: []Provider{prov}, Ledger: ledger2, MaxActive: 1})
	lowID, err := c2.Add(ctx, download.AddRequest{Name: "A", Payload: a})
	require.NoError(t, err)
	waitQueuedForConnections(t, c2, lowID)
	highID, err := c2.Add(ctx, download.AddRequest{Name: "B", Payload: b, Priority: commonv1.DownloadPriorityHigh})
	require.NoError(t, err)
	ledger2.open(prov.Account())
	require.Equal(t, download.StatusCompleted, waitForTerminal(t, c2, highID).Status, "a parked lower job must give way")
	require.Equal(t, download.StatusCompleted, waitForTerminal(t, c2, lowID).Status)
}

// flakyLedger fails its first few ledger calls: a bus that is briefly
// unreachable.
type flakyLedger struct {
	*LocalLedger
	failures atomic.Int32
}

func (f *flakyLedger) Take(ctx context.Context, account string, limit int, g Grant, want, min int) (int, error) {
	if f.failures.Add(-1) >= 0 {
		return 0, errors.New("nats: timeout")
	}
	return f.LocalLedger.Take(ctx, account, limit, g, want, min)
}

// A ledger that cannot be reached parks the job; it never fails it
// (review I6).
func TestALedgerErrorParksTheJobInsteadOfFailingIt(t *testing.T) {
	srv := newStubServer(t)
	nzb := buildNZB(t, srv, "Flaky", []fileSpec{{name: "movie.mkv", parts: fourParts()}})
	ledger := &flakyLedger{LocalLedger: NewLocalLedger()}
	ledger.failures.Store(2)
	old := ledgerPollInterval
	ledgerPollInterval = 20 * time.Millisecond
	t.Cleanup(func() { ledgerPollInterval = old })
	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 2, 1)}, Ledger: ledger})
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Flaky", Payload: nzb})
	require.NoError(t, err)
	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
}

// lostAndFailingLedger reports a lost holder together with an error, as
// a Renew that lost one account's grants and then could not read another.
type lostAndFailingLedger struct {
	lostLedger
}

func (l *lostAndFailingLedger) Renew(ctx context.Context) ([]string, error) {
	lost, _ := l.lostLedger.Renew(ctx)
	if len(lost) > 0 {
		return lost, errors.New("nats: timeout on a later account")
	}
	return nil, nil
}

// Lost holders reported beside an error are still dropped, so the job
// takes a new grant rather than transfer on one nothing records (review I7).
func TestLostGrantsAreDroppedEvenWhenRenewAlsoFails(t *testing.T) {
	srv := newStubServer(t)
	srv.bodyDelay = 10 * time.Millisecond
	nzb := buildNZB(t, srv, "Lost2", []fileSpec{{name: "l.mkv", parts: manyParts(1, 40, 300)}})
	prov := srv.provider("solo", 4, 1)
	ledger := &lostAndFailingLedger{lostLedger{LocalLedger: NewLocalLedger()}}
	c, _, _ := newTestClient(t, Config{Providers: []Provider{prov}, Ledger: ledger, PipelineDepth: 1, GrantRenewInterval: 20 * time.Millisecond})
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Lost2", Payload: nzb})
	require.NoError(t, err)
	require.Equal(t, download.StatusCompleted, waitForTerminal(t, c, id).Status)
	require.GreaterOrEqual(t, ledger.takes.Load(), int32(2), "the lost grant must have been taken again")
}

// A provider configured with no connection count still gets a grant of
// one, as the pool floors it (review minor 14).
func TestAProviderWithoutAConnectionCountGetsOneConnection(t *testing.T) {
	srv := newStubServer(t)
	nzb := buildNZB(t, srv, "Zero", []fileSpec{{name: "movie.mkv", parts: fourParts()}})
	p := srv.provider("solo", 0, 1)
	c, _, _ := newTestClient(t, Config{Providers: []Provider{p}})
	id, err := c.Add(context.Background(), download.AddRequest{Name: "Zero", Payload: nzb})
	require.NoError(t, err)
	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
}
