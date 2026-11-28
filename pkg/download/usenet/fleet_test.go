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
	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/download"
)

// A pool is one account's hosts under the account's cap: both hosts' keys
// are the pool's, so their grants are counted together, and a host capped
// below its pool holds no more than its own share of it.
func TestAPoolCapsItsHostsTogether(t *testing.T) {
	us := Provider{Name: "us", Host: "news.example", Port: 563, Username: "me", Connections: 100, Pool: "plan", PoolConnections: 100}
	eu := Provider{Name: "eu", Host: "eunews.example", Port: 563, Username: "me", Connections: 25, Pool: "plan", PoolConnections: 100, Backup: true}
	alone := Provider{Name: "other", Host: "other.example", Port: 563, Username: "me", Connections: 30}
	require.Equal(t, us.Account(), eu.Account(), "a pool's hosts share one ledger entry")
	require.NotEqual(t, us.Account(), alone.Account())
	require.Equal(t, "other.example:563:me", alone.Account(), "a provider on its own keeps its key")
	require.Equal(t, 100, eu.AccountLimit())
	require.Equal(t, 25, eu.HostLimit())
	require.Equal(t, 30, alone.AccountLimit())

	ctx := context.Background()
	l := NewLocalLedger()
	take := func(p Provider, holder string, want int) int {
		g := Grant{Holder: holder, Server: p.Name, Host: p.Host}
		if p.HostLimit() < p.AccountLimit() {
			g.HostLimit = p.HostLimit()
		}
		n, err := l.Take(ctx, p.Account(), p.AccountLimit(), g, want, 1)
		require.NoError(t, err)
		return n
	}
	require.Equal(t, 20, take(eu, "a", 20))
	require.Equal(t, 5, take(eu, "b", 20), "the EU host holds 25 of the pool at most")
	require.Equal(t, 0, take(eu, "c", 1))
	require.Equal(t, 75, take(us, "d", 90), "the pool's 100 less the 25 on the other host")
	require.Equal(t, 0, take(us, "e", 1), "the pool is spent across both hosts")
	held, err := l.Held(ctx, us.Account())
	require.NoError(t, err)
	require.Equal(t, 100, held)
	require.NoError(t, l.Give(ctx, eu.Account(), "a"))
	require.Equal(t, 20, take(us, "e", 50), "what one host gives back the other may take")
}

func TestGrantRoom(t *testing.T) {
	grants := []Grant{
		{Holder: "a", Pod: "p1", Host: "h1", Connections: 30},
		{Holder: "b", Pod: "p1", Host: "h2", Connections: 10},
		{Holder: "c", Pod: "p2", Host: "h1", Connections: 20},
	}
	assert.Equal(t, 40, GrantRoom(grants, Grant{Holder: "x", Pod: "p3", Host: "h3"}, 100), "the account's limit alone")
	assert.Equal(t, 10, GrantRoom(grants, Grant{Holder: "x", Pod: "p3", Host: "h1", HostLimit: 60}, 100), "50 of 60 on its host")
	assert.Equal(t, 10, GrantRoom(grants, Grant{Holder: "x", Pod: "p1", Host: "h3", PodLimit: 50}, 100), "40 of 50 in its pod")
	assert.Equal(t, 70, GrantRoom(grants, Grant{Holder: "a", Pod: "p1", Host: "h1"}, 100), "its own grant is not counted")
	assert.Equal(t, -10, GrantRoom(grants, Grant{Holder: "x", Pod: "p1", Host: "h3", PodLimit: 30}, 100), "over its pod's share: no room")
}

// A pod's share of an account follows its weight among the live members;
// a pod alone, or the only one with any weight, has it all.
func TestEntitlementFollowsTheWeights(t *testing.T) {
	assert.Equal(t, 100, Entitlement(100, 1000, 1000), "alone")
	assert.Equal(t, 80, Entitlement(100, 2000, 2500))
	assert.Equal(t, 20, Entitlement(100, 500, 2500))
	assert.Equal(t, 1, Entitlement(3, 1, 1000), "never under one connection")
	assert.Equal(t, 100, Entitlement(100, 1000, 0), "a total never read is the pod alone")
}

// A month's count starts again when the month turns; a block's never does.
func TestUsageAfter(t *testing.T) {
	nov := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	dec := time.Date(2026, 12, 1, 0, 0, 1, 0, time.UTC)
	u := UsageAfter(Usage{}, QuotaMonth, 100, nov)
	assert.Equal(t, Usage{PeriodStart: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), Bytes: 100}, u)
	u = UsageAfter(u, QuotaMonth, 50, nov)
	assert.Equal(t, int64(150), u.Bytes)
	u = UsageAfter(u, QuotaMonth, 7, dec)
	assert.Equal(t, Usage{PeriodStart: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), Bytes: 7}, u, "the month turned")

	b := UsageAfter(Usage{}, QuotaNever, 100, nov)
	b = UsageAfter(b, QuotaNever, 7, dec)
	assert.Equal(t, Usage{PeriodStart: nov, Bytes: 107}, b, "a block is counted until it is gone")
	assert.Equal(t, int64(150), UsageAfter(Usage{Bytes: 150, PeriodStart: nov}, "", 0, nov).Bytes, "reading adds nothing")
}

// A backup that shares a pool with a primary stands in for it while the
// primary cannot be reached, carrying the whole transfer; a backup outside
// the pool never does.
func TestAPooledBackupStandsInWhileItsPrimaryCannotBeReached(t *testing.T) {
	for _, tc := range []struct {
		name   string
		pooled bool
	}{{"in the primary's pool", true}, {"outside it", false}} {
		t.Run(tc.name, func(t *testing.T) {
			us, eu := newStubServer(t), newStubServer(t)
			nzb := buildNZBWithPrefix(t, eu, "One", "one-", []fileSpec{{name: "one.mkv", parts: manyParts(1, 40, 300)}})
			us.mirror(eu)
			us.mu.Lock()
			us.greetBusy = true // every connection is refused: the host is down
			us.mu.Unlock()
			pus, peu := us.provider("us", 8, 1), eu.provider("eu", 4, 2)
			peu.Backup = true
			if tc.pooled {
				pus.Pool, pus.PoolConnections = "plan", 8
				peu.Pool, peu.PoolConnections = "plan", 8
			}
			c, _, _ := newTestClient(t, Config{Providers: []Provider{pus, peu}, PipelineDepth: 1, ProviderRetryDelay: 20 * time.Millisecond})
			id, err := c.Add(context.Background(), download.AddRequest{Name: "One", Payload: nzb})
			require.NoError(t, err)
			if !tc.pooled {
				require.Never(t, func() bool { return eu.servedTotal() > 0 }, 500*time.Millisecond, 20*time.Millisecond,
					"a backup that is no stand-in is not asked for the bulk of a download")
				return
			}
			require.Equal(t, download.StatusCompleted, waitForTerminal(t, c, id).Status)
			require.Equal(t, 40, eu.servedTotal(), "the stand-in served the whole transfer")
		})
	}
}

// A provider's quota is the account's, counted in the ledger by every pod:
// what another pod has read counts here, and a spent quota skips the
// provider.
func TestAQuotaSpentByAnotherPodSkipsTheProvider(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spent int64
		asked bool
	}{{"under the quota", 10, true}, {"spent by another pod", 1 << 20, false}} {
		t.Run(tc.name, func(t *testing.T) {
			primary, block := newStubServer(t), newStubServer(t)
			nzb := buildNZBWithPrefix(t, block, "One", "one-", []fileSpec{{name: "one.mkv", parts: manyParts(1, 40, 300)}})
			primary.mirror(block)
			primary.mu.Lock()
			primary.refuse = map[string]int{"one-f0-p3@clustarr.test": 430}
			primary.mu.Unlock()
			pb := block.provider("block", 2, 2)
			pb.Backup, pb.QuotaBytes, pb.QuotaPeriod = true, 1<<20, QuotaNever
			ledger := NewLocalLedger()
			_, err := ledger.Use(context.Background(), pb.Account(), QuotaNever, tc.spent)
			require.NoError(t, err)
			c, _, _ := newTestClient(t, Config{
				Providers: []Provider{primary.provider("primary", 4, 1), pb}, PipelineDepth: 1, Ledger: ledger,
			})
			id, err := c.Add(context.Background(), download.AddRequest{Name: "One", Payload: nzb})
			require.NoError(t, err)
			if !tc.asked {
				require.Eventually(t, func() bool { return primary.askedTotal() >= 40 }, 5*time.Second, 10*time.Millisecond)
				require.Never(t, func() bool { return block.askedTotal() > 0 }, 500*time.Millisecond, 20*time.Millisecond,
					"a provider whose account's quota is spent is not asked")
				return
			}
			waitForTerminal(t, c, id)
			require.Positive(t, block.askedTotal())
			{
				// What this pod read is added to the account's count.
				c.syncUsage(context.Background())
				u, err := ledger.Use(context.Background(), pb.Account(), QuotaNever, 0)
				require.NoError(t, err)
				require.Greater(t, u.Bytes, tc.spent)
			}
		})
	}
}

// With GrowGrants a transfer asks for its server's whole budget divided
// among the transfers running there, and gives way when another arrives:
// one transfer uses what four would, two share it.
func TestGrowGrantsUseWhatTheSlotsLeaveFree(t *testing.T) {
	srv := newStubServer(t)
	srv.bodyDelay = 20 * time.Millisecond
	one := buildNZBWithPrefix(t, srv, "One", "one-", []fileSpec{{name: "one.mkv", parts: manyParts(1, 400, 300)}})
	two := buildNZBWithPrefix(t, srv, "Two", "two-", []fileSpec{{name: "two.mkv", parts: manyParts(2, 400, 300)}})
	c, _, _ := newTestClient(t, Config{
		Providers: []Provider{srv.provider("solo", 8, 1)}, MaxActive: 4, PipelineDepth: 1, GrowGrants: true,
		GrantRenewInterval: 50 * time.Millisecond,
	})
	ctx := context.Background()
	id1, err := c.Add(ctx, download.AddRequest{Name: "One", Payload: one})
	require.NoError(t, err)
	require.Equal(t, []download.Grant{{Server: "solo", Connections: 8}}, waitGrants(t, c, id1),
		"one transfer of four slots holds the whole server, not a quarter of it")

	id2, err := c.Add(ctx, download.AddRequest{Name: "Two", Payload: two})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		a, errA := c.Get(ctx, id1)
		b, errB := c.Get(ctx, id2)
		return errA == nil && errB == nil && a.Grant != nil && b.Grant != nil && a.Grant.Connections == 4 && b.Grant.Connections == 4
	}, 5*time.Second, 10*time.Millisecond, "the first gives way: two transfers hold half each")
}

// othersLedger is a LocalLedger that reports other pods' weight on every
// account, as a shared ledger with other members would.
type othersLedger struct {
	*LocalLedger
	others int64
}

func (l *othersLedger) Join(_ context.Context, _ string, weight int64) (int64, error) {
	return weight + l.others, nil
}

// A pod takes its share of an account, by its weight among the members,
// and a larger budget raises it: every grant is bounded by the pod's
// entitlement.
func TestAPodHoldsItsShareOfAnAccount(t *testing.T) {
	srv := newStubServer(t)
	srv.bodyDelay = 20 * time.Millisecond
	nzb := buildNZBWithPrefix(t, srv, "One", "one-", []fileSpec{{name: "one.mkv", parts: manyParts(1, 600, 300)}})
	c, _, _ := newTestClient(t, Config{
		Providers: []Provider{srv.provider("solo", 20, 1)}, MaxActive: 1, PipelineDepth: 1, GrowGrants: true,
		Ledger: &othersLedger{LocalLedger: NewLocalLedger(), others: 3000}, GrantRenewInterval: 50 * time.Millisecond,
	})
	ctx := context.Background()
	id, err := c.Add(ctx, download.AddRequest{Name: "One", Payload: nzb})
	require.NoError(t, err)
	require.Equal(t, []download.Grant{{Server: "solo", Connections: 5}}, waitGrants(t, c, id),
		"a weight of 1000 among 4000 is a quarter of the account's twenty")

	c.SetBudget(Budget{Weight: 3000})
	require.Eventually(t, func() bool {
		it, err := c.Get(ctx, id)
		return err == nil && it.Grant != nil && it.Grant.Connections == 10
	}, 5*time.Second, 10*time.Millisecond, "3000 among 6000 is half")
}

// The engine's budget sets how many jobs transfer at once, below the
// client's MaxActive and never above it.
func TestABudgetSetsTheTransferSlots(t *testing.T) {
	srv := newStubServer(t)
	srv.bodyDelay = 20 * time.Millisecond
	var ids []string
	c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 8, 1)}, MaxActive: 3, PipelineDepth: 1})
	c.SetBudget(Budget{Transfers: 1})
	ctx := context.Background()
	for i, name := range []string{"One", "Two", "Three"} {
		nzb := buildNZBWithPrefix(t, srv, name, name+"-", []fileSpec{{name: name + ".mkv", parts: manyParts(byte(i+1), 200, 300)}})
		id, err := c.Add(ctx, download.AddRequest{Name: name, Payload: nzb})
		require.NoError(t, err)
		ids = append(ids, id)
	}
	holding := func() int {
		c.mu.Lock()
		defer c.mu.Unlock()
		return len(c.slots)
	}
	require.Eventually(t, func() bool { return holding() == 1 }, 5*time.Second, 5*time.Millisecond)
	require.Never(t, func() bool { return holding() > 1 }, 300*time.Millisecond, 5*time.Millisecond, "one transfer at once under the budget")

	c.SetBudget(Budget{Transfers: 9})
	require.Eventually(t, func() bool { return holding() == 3 }, 5*time.Second, 5*time.Millisecond,
		"a larger budget fills the slots at once, up to MaxActive")
	for _, id := range ids {
		require.Equal(t, download.StatusCompleted, waitForTerminal(t, c, id).Status)
	}
}

// The limiter holds what the client reads to the engine's budget.
func TestTheLimiterHoldsTheReadRate(t *testing.T) {
	run := func(lim *rate.Limiter) (time.Duration, int64) {
		srv := newStubServer(t)
		nzb := buildNZBWithPrefix(t, srv, "One", "one-", []fileSpec{{name: "one.mkv", parts: manyParts(1, 60, 1000)}})
		c, _, _ := newTestClient(t, Config{Providers: []Provider{srv.provider("solo", 4, 1)}, PipelineDepth: 1, Limiter: lim})
		start := time.Now()
		id, err := c.Add(context.Background(), download.AddRequest{Name: "One", Payload: nzb})
		require.NoError(t, err)
		require.Equal(t, download.StatusCompleted, waitForTerminal(t, c, id).Status)
		return time.Since(start), c.WireBytes()
	}
	free, read := run(nil)
	require.Greater(t, read, int64(60_000), "sixty articles of a thousand bytes and their yEnc lines")
	// A third of the bytes a second, a burst of a tenth: at least two
	// seconds for what is past the burst.
	perSecond := read / 3
	held, _ := run(rate.NewLimiter(rate.Limit(perSecond), int(read/10)))
	require.Greater(t, held, 2*time.Second, "unbounded it took %s", free)
	require.Less(t, free, time.Second)
}
