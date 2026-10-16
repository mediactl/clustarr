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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

// gateLedger is a LocalLedger whose named accounts give nothing until the
// test opens them: another pod holding every connection.
type gateLedger struct {
	*LocalLedger
	mu     sync.Mutex
	closed map[string]bool
	takes  atomic.Int32
}

func newGateLedger(closedAccounts ...string) *gateLedger {
	g := &gateLedger{LocalLedger: NewLocalLedger(), closed: map[string]bool{}}
	for _, a := range closedAccounts {
		g.closed[a] = true
	}
	return g
}

func (g *gateLedger) open(account string) {
	g.mu.Lock()
	delete(g.closed, account)
	g.mu.Unlock()
}

func (g *gateLedger) Take(ctx context.Context, account string, limit int, gr Grant, want, min int) (int, error) {
	g.takes.Add(1)
	g.mu.Lock()
	closed := g.closed[account]
	g.mu.Unlock()
	if closed {
		return 0, nil
	}
	return g.LocalLedger.Take(ctx, account, limit, gr, want, min)
}

// lostLedger reports every holder it has granted as lost once, on its first
// Renew: the grant expired under the pod.
type lostLedger struct {
	*LocalLedger
	mu       sync.Mutex
	holders  []string
	reported bool
	takes    atomic.Int32
}

func (l *lostLedger) Take(ctx context.Context, account string, limit int, g Grant, want, min int) (int, error) {
	l.takes.Add(1)
	n, err := l.LocalLedger.Take(ctx, account, limit, g, want, min)
	if n > 0 {
		l.mu.Lock()
		l.holders = append(l.holders, g.Holder)
		l.mu.Unlock()
	}
	return n, err
}

func (l *lostLedger) Renew(ctx context.Context) ([]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.reported || len(l.holders) == 0 {
		return nil, nil
	}
	l.reported = true
	lost := append([]string(nil), l.holders...)
	for _, h := range lost {
		_ = l.LocalLedger.Give(ctx, "", h)
	}
	return lost, nil
}

func waitQueuedForConnections(t *testing.T, c *Client, id string) {
	t.Helper()
	require.Eventually(t, func() bool {
		it, err := c.Get(context.Background(), id)
		return err == nil && it.Status == download.StatusQueued && it.WaitingTurn &&
			strings.Contains(it.Message, "waiting for usenet connections")
	}, 5*time.Second, 5*time.Millisecond, "the job must park Queued, WaitingTurn, naming the wait")
}

// With every connection of the account held elsewhere, a transfer parks
// Queued -- WaitingTurn, so the manager's stall clock stops -- fetching
// nothing, and starts once the ledger has room (spec 2026-10-16 §4.1).
func TestATransferParksUntilTheLedgerHasRoom(t *testing.T) {
	srv := newStubServer(t)
	nzb := buildNZB(t, srv, "Parked", []fileSpec{{name: "parked.mkv", parts: manyParts(1, 4, 300)}})
	prov := srv.provider("solo", 4, 1)
	ledger := newGateLedger(prov.Account())
	c, _, _ := newTestClient(t, Config{Providers: []Provider{prov}, Ledger: ledger})
	ctx := context.Background()

	id, err := c.Add(ctx, download.AddRequest{Name: "Parked", Payload: nzb})
	require.NoError(t, err)
	waitQueuedForConnections(t, c, id)
	time.Sleep(50 * time.Millisecond)
	_, accepted, _ := srv.stats()
	require.Zero(t, accepted, "a parked job must open no connection")

	ledger.open(prov.Account())
	it := waitForTerminal(t, c, id)
	require.Equal(t, download.StatusCompleted, it.Status, "message: %s", it.Message)
	require.False(t, it.WaitingTurn)
	require.Nil(t, it.Grant, "a finished transfer holds no grant")
}

// A transfer's concurrent fetches are its grant, a share of the account
// (limit / MaxActive), not the sum of every provider's allowance.
func TestTheWorkersMatchTheGrant(t *testing.T) {
	srv := newStubServer(t)
	srv.bodyDelay = 20 * time.Millisecond
	nzb := buildNZB(t, srv, "Shared", []fileSpec{{name: "shared.mkv", parts: manyParts(1, 40, 300)}})
	prov := srv.provider("solo", 8, 1)
	c, _, _ := newTestClient(t, Config{Providers: []Provider{prov}, MaxActive: 2, PipelineDepth: 1, Workers: 8})
	ctx := context.Background()

	id, err := c.Add(ctx, download.AddRequest{Name: "Shared", Payload: nzb})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		it, err := c.Get(ctx, id)
		return err == nil && it.Grant != nil
	}, 5*time.Second, 5*time.Millisecond)
	it, err := c.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, &download.Grant{Server: "solo", Connections: 4}, it.Grant)
	require.Contains(t, it.Message, "solo")

	require.Equal(t, download.StatusCompleted, waitForTerminal(t, c, id).Status)
	peak, _, _ := srv.stats()
	require.LessOrEqual(t, peak, 4, "the transfer opened more connections than its grant")
}

// A backup server fills; it never carries a transfer. With the primary
// full the job parks rather than start on the backup.
func TestABackupServerNeverGetsTheFirstGrant(t *testing.T) {
	primary := newStubServer(t)
	backup := newStubServer(t)
	nzb := buildNZB(t, primary, "Backup", []fileSpec{{name: "b.mkv", parts: manyParts(1, 2, 300)}})
	pp := primary.provider("primary", 4, 1)
	bp := backup.provider("backup", 4, 2)
	bp.Backup = true
	ledger := newGateLedger(pp.Account())
	c, _, _ := newTestClient(t, Config{Providers: []Provider{pp, bp}, Ledger: ledger})
	ctx := context.Background()

	id, err := c.Add(ctx, download.AddRequest{Name: "Backup", Payload: nzb})
	require.NoError(t, err)
	waitQueuedForConnections(t, c, id)
	time.Sleep(50 * time.Millisecond)
	_, accepted, _ := backup.stats()
	require.Zero(t, accepted, "the backup must not carry the transfer")
}

// A paused job holds no connections: its grant goes back to the ledger,
// and it takes a new one when resumed.
func TestAPausedJobGivesItsGrantBack(t *testing.T) {
	srv := newStubServer(t)
	srv.bodyDelay = 20 * time.Millisecond
	nzb := buildNZB(t, srv, "Paused", []fileSpec{{name: "p.mkv", parts: manyParts(1, 30, 300)}})
	prov := srv.provider("solo", 4, 1)
	ledger := NewLocalLedger()
	c, _, _ := newTestClient(t, Config{Providers: []Provider{prov}, Ledger: ledger, PipelineDepth: 1})
	ctx := context.Background()

	id, err := c.Add(ctx, download.AddRequest{Name: "Paused", Payload: nzb})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		held, _ := ledger.Held(ctx, prov.Account())
		return held > 0
	}, 5*time.Second, 5*time.Millisecond)

	require.NoError(t, c.Pause(ctx, id))
	require.Eventually(t, func() bool {
		held, _ := ledger.Held(ctx, prov.Account())
		it, err := c.Get(ctx, id)
		return held == 0 && err == nil && it.Grant == nil
	}, 5*time.Second, 5*time.Millisecond, "a paused job must give its grant back")

	require.NoError(t, c.Resume(ctx, id))
	require.Equal(t, download.StatusCompleted, waitForTerminal(t, c, id).Status)
	held, _ := ledger.Held(ctx, prov.Account())
	require.Zero(t, held)
}

// A grant the renewal finds gone is never assumed: the transfer takes a
// new one before its next batch.
func TestALostGrantIsRetaken(t *testing.T) {
	srv := newStubServer(t)
	srv.bodyDelay = 10 * time.Millisecond
	nzb := buildNZB(t, srv, "Lost", []fileSpec{{name: "l.mkv", parts: manyParts(1, 40, 300)}})
	prov := srv.provider("solo", 4, 1)
	ledger := &lostLedger{LocalLedger: NewLocalLedger()}
	c, _, _ := newTestClient(t, Config{Providers: []Provider{prov}, Ledger: ledger, PipelineDepth: 1, GrantRenewInterval: 20 * time.Millisecond})
	ctx := context.Background()

	id, err := c.Add(ctx, download.AddRequest{Name: "Lost", Payload: nzb})
	require.NoError(t, err)
	require.Equal(t, download.StatusCompleted, waitForTerminal(t, c, id).Status)
	require.GreaterOrEqual(t, ledger.takes.Load(), int32(2), "the lost grant must have been taken again")
	it, err := c.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, commonv1.DownloadStageDone, it.Stage)
}
