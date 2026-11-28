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
	"sync"
	"time"

	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// # One engine among several (spec 2026-11-27)
//
// A client is one pod's. Several pods on one provider account share its
// connections through the [Ledger]; what divides them is each pod's
// membership: a weight written into the account's entry at every renewal
// ([Ledger.Join]), zero while the pod has nothing to transfer. A pod's
// entitlement on an account is the account's limit times its weight over
// every live member's ([Entitlement]), and every grant it takes is bounded
// by it (Grant.PodLimit), so two busy pods of weights 2000 and 500 hold 80
// and 20 of a pool of 100, and the busy one of an idle pair holds it all.
//
// The engine's bandwidth budget sets the weight, and with it how many
// transfers the client runs at once ([Client.SetBudget]); the rate itself
// is the engine's limiter ([Config.Limiter]).

// defaultWeight is a pod's weight with no budget: every such pod is equal.
const defaultWeight = 1000

// Budget is what the engine's bandwidth budget changes while the client
// runs.
type Budget struct {
	// Transfers is how many jobs transfer at once, in place of
	// [Config.MaxActive]; 0 leaves it.
	Transfers int
	// Weight is the pod's claim on every provider account among the pods
	// sharing it; 0 means the default, equal to every other pod without
	// one.
	Weight int64
}

// Budgeted is the part of the client an engine with a bandwidth budget
// drives; the [download.Client] New returns implements it.
type Budgeted interface {
	SetBudget(Budget)
	// WireBytes is every body byte the client has read from its providers
	// since it started, for the engine's measured rate.
	WireBytes() int64
	// ConnectionsHeld is the connections the client's transfers hold in
	// the ledger now.
	ConnectionsHeld() int
}

var _ Budgeted = (*Client)(nil)

// membership is the pod's standing on its accounts.
type membership struct {
	mu     sync.Mutex
	weight int64
	// busy is what the last Join wrote: the weight, or zero. joined is
	// false until the first Join.
	busy, joined bool
	// others is every other live member's weight per account at the last
	// Join.
	others map[string]int64
}

// SetBudget applies an engine budget: the transfer slots and the pod's
// weight. More slots are filled at once; fewer are reached as running
// transfers finish, never by stopping one.
func (c *Client) SetBudget(b Budget) {
	if b.Transfers > 0 {
		n := min(b.Transfers, c.maxActiveCap)
		if int(c.maxActive.Swap(int32(n))) != n {
			shares := primaryShares(c.cfg.Providers, n)
			c.shares.Store(&shares)
			c.turnChanged()
		}
	}
	w := b.Weight
	if w <= 0 {
		w = defaultWeight
	}
	c.members.mu.Lock()
	changed := c.members.weight != w
	c.members.weight = w
	c.members.mu.Unlock()
	if changed {
		c.kickRebalance()
	}
}

// WireBytes implements Budgeted.
func (c *Client) WireBytes() int64 {
	var n int64
	for _, s := range c.pool.servers {
		n += s.used.Load()
	}
	return n
}

// ConnectionsHeld implements Budgeted.
func (c *Client) ConnectionsHeld() int {
	c.mu.Lock()
	jobs := make([]*job, 0, len(c.jobs))
	for _, j := range c.jobs {
		jobs = append(jobs, j)
	}
	c.mu.Unlock()
	n := 0
	for _, j := range jobs {
		j.mu.Lock()
		if j.grant != nil {
			n += j.grant.Connections
		}
		j.mu.Unlock()
	}
	return n
}

// accounts lists each distinct provider account once, in pool order.
func (c *Client) accounts() []Provider {
	seen := map[string]bool{}
	var out []Provider
	for _, p := range c.pool.Providers() {
		if a := p.Account(); !seen[a] {
			seen[a] = true
			out = append(out, p)
		}
	}
	return out
}

// hasWork reports a job holding or waiting for a transfer slot: the pod
// then counts among its accounts' members.
func (c *Client) hasWork() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.slots) > 0 || len(c.waiting) > 0
}

// join writes the pod's weight -- zero with nothing to transfer -- into
// every account it uses and remembers the other members' weights.
func (c *Client) join(ctx context.Context) {
	busy := c.hasWork()
	c.members.mu.Lock()
	w := c.members.weight
	idle := c.members.joined && !c.members.busy
	c.members.mu.Unlock()
	if !busy && idle {
		// Already written as zero, and a zero that lapses counts the same:
		// an idle engine writes nothing.
		return
	}
	written := w
	if !busy {
		written = 0
	}
	for _, p := range c.accounts() {
		total, err := c.ledger.Join(ctx, p.Account(), written)
		if err != nil {
			logging.FromContext(ctx).WarnContext(ctx, "usenet: could not write the pod's share of a provider account; keeping the last one", "server", p.Name, "err", err)
			continue
		}
		c.members.mu.Lock()
		c.members.others[p.Account()] = max(total-written, 0)
		c.members.mu.Unlock()
	}
	c.members.mu.Lock()
	c.members.busy, c.members.joined = busy, true
	c.members.mu.Unlock()
}

// joined makes sure the pod's weight is written before its first grant
// after an idle spell, so the other pods give way within a renewal.
func (c *Client) joined(ctx context.Context) {
	c.members.mu.Lock()
	busy := c.members.busy
	c.members.mu.Unlock()
	if !busy {
		c.join(ctx)
	}
}

// entitlement is the pod's share of p's account.
func (c *Client) entitlement(p Provider) int {
	c.members.mu.Lock()
	defer c.members.mu.Unlock()
	return Entitlement(p.AccountLimit(), c.members.weight, c.members.others[p.Account()]+c.members.weight)
}

// scaled is n of p's account's connections as the pod's entitlement
// scales it, at least one.
func (c *Client) scaled(p Provider, n int) int {
	limit := p.AccountLimit()
	ent := c.entitlement(p)
	if ent >= limit {
		return max(n, 1)
	}
	return max(1, n*ent/limit)
}

// ask is the grant a job would take on p: its host and pod limits. A
// transfer is held to the primary's budget, on its host across every pod
// and, scaled, within this pod's entitlement; a chase may take what is
// left above it.
func (c *Client) ask(p Provider, holder string, transfer bool) Grant {
	g := Grant{Holder: holder, Server: p.Name, Host: p.Host, PodLimit: c.entitlement(p)}
	if host := p.HostLimit(); host < p.AccountLimit() {
		g.HostLimit = host
	}
	if b, ok := c.budgets[p.Name]; ok && transfer {
		g.HostLimit = b
		g.PodLimit = c.scaled(p, b)
	}
	return g
}

// want is the share a job asks of p. For a chase, or without
// [Config.GrowGrants], it is the server's static share of a transfer slot.
// With it a transfer asks for the pod's whole budget on the server divided
// among the transfers that would then run there: three transfers use what
// eight would.
func (c *Client) want(p Provider, transfer bool, running int) int {
	static, ok := (*c.shares.Load())[p.Name]
	if !ok {
		// A backup, asked for a chase or standing in for its pool's primary.
		static = share(p.HostLimit(), int(c.maxActive.Load()))
	}
	static = c.scaled(p, static)
	if !transfer || !c.cfg.GrowGrants {
		return static
	}
	budget, ok := c.budgets[p.Name]
	if !ok {
		budget = p.HostLimit()
	}
	return max(static, c.scaled(p, budget)/max(running, 1))
}

// transfersOn counts the pod's jobs holding a transfer grant on each
// server, and the jobs parked for one.
func (c *Client) transfersOn() (on map[string]int, parked int) {
	on = map[string]int{}
	c.mu.Lock()
	jobs := make([]*job, 0, len(c.jobs))
	for _, j := range c.jobs {
		jobs = append(jobs, j)
	}
	c.mu.Unlock()
	for _, j := range jobs {
		j.mu.Lock()
		if j.grant != nil {
			on[j.grant.Server]++
		}
		j.mu.Unlock()
		if j.waitingGrant.Load() && j.wantsTransferGrant.Load() {
			parked++
		}
	}
	return on, parked
}

// rebalance sizes every transfer grant the pod holds to what it would be
// asked for now: smaller when the pod's entitlement shrank (another pod
// joined) or a job is parked for connections, larger when fewer transfers
// run than the server has room for. Shrinks go first, so what one grant
// gives up another can take in the same pass.
func (c *Client) rebalance(ctx context.Context) {
	on, parked := c.transfersOn()
	c.mu.Lock()
	jobs := make([]*job, 0, len(c.jobs))
	for _, j := range c.jobs {
		jobs = append(jobs, j)
	}
	c.mu.Unlock()
	type sized struct {
		j      *job
		p      Provider
		target int
	}
	var shrink, grow []sized
	for _, j := range jobs {
		j.mu.Lock()
		g := j.grant
		var cur int
		var server string
		if g != nil {
			cur, server = g.Connections, g.Server
		}
		j.mu.Unlock()
		if g == nil {
			continue
		}
		p, ok := c.pool.Provider(server)
		if !ok {
			continue
		}
		target := c.want(p, true, on[server]+parked)
		switch {
		case target < cur:
			shrink = append(shrink, sized{j, p, target})
		case target > cur && c.cfg.GrowGrants:
			grow = append(grow, sized{j, p, target})
		}
	}
	gaveWay := false
	defer func() {
		if gaveWay {
			c.wakeParked()
		}
	}()
	for _, s := range append(shrink, grow...) {
		s.j.mu.Lock()
		g := s.j.grant
		if g == nil || g.Server != s.p.Name {
			s.j.mu.Unlock()
			continue
		}
		holder, account := g.Holder, g.Account
		s.j.mu.Unlock()
		n, err := c.ledger.Resize(ctx, account, s.p.AccountLimit(), c.ask(s.p, holder, true), s.target)
		if err != nil {
			logging.FromContext(ctx).WarnContext(ctx, "usenet: could not resize a connection grant; it stays as it is", "download", s.j.id, "server", s.p.Name, "err", err)
			continue
		}
		if n == 0 {
			continue
		}
		s.j.mu.Lock()
		if s.j.grant != nil && s.j.grant.Holder == holder && s.j.grant.Connections != n {
			gaveWay = gaveWay || n < s.j.grant.Connections
			s.j.grant.Connections = n
			s.j.limiter.resize(n)
			if s.j.message == s.j.grantMessage {
				s.j.message = grantMessageOf(s.p.Name, n)
				s.j.grantMessage = s.j.message
			}
		}
		s.j.mu.Unlock()
	}
}

// parkSignal is closed the next time a transfer of this pod gives
// connections back by shrinking: a job parked for connections asks the
// ledger again then, not at its next poll.
func (c *Client) parkSignal() <-chan struct{} {
	c.parkMu.Lock()
	defer c.parkMu.Unlock()
	if c.parkCh == nil {
		c.parkCh = make(chan struct{})
	}
	return c.parkCh
}

func (c *Client) wakeParked() {
	c.parkMu.Lock()
	defer c.parkMu.Unlock()
	if c.parkCh != nil {
		close(c.parkCh)
		c.parkCh = nil
	}
}

// kickRebalance asks the renewal loop for a join and a rebalance now.
func (c *Client) kickRebalance() {
	select {
	case c.rebalanceKick <- struct{}{}:
	default:
	}
}

// standIns are the backups a transfer grant may be taken on because every
// primary of their pool cannot be asked now (penalised: unreachable or
// refusing the login): a backup that shares a pool with a primary stands
// in for it, for whole transfers (spec 2026-11-27 §4.3).
func (c *Client) standIns(now time.Time) []string {
	providers := c.pool.Providers()
	var out []string
	for _, b := range providers {
		if !b.Backup || b.Pool == "" {
			continue
		}
		primaries, down := 0, 0
		for _, p := range providers {
			if p.Backup || p.Pool != b.Pool {
				continue
			}
			primaries++
			if !c.pool.Available(p.Name, now) {
				down++
			}
		}
		if primaries > 0 && down == primaries {
			out = append(out, b.Name)
		}
	}
	return out
}

// syncUsage adds what each metered provider has read since the last sync
// to its account's count in the ledger, and reads the count back: every
// pod's bytes, across restarts. A provider whose account has reached its
// quota is then skipped as if it were down ([serverPool.available]).
func (c *Client) syncUsage(ctx context.Context) {
	for _, s := range c.pool.servers {
		if s.p.QuotaBytes <= 0 {
			continue
		}
		delta := s.used.Load() - s.reported.Load()
		u, err := c.ledger.Use(ctx, s.p.Account(), s.p.QuotaPeriod, delta)
		if err != nil {
			logging.FromContext(ctx).WarnContext(ctx, "usenet: could not count a provider's quota in the ledger; counting on from the last total", "server", s.p.Name, "err", err)
			continue
		}
		s.reported.Add(delta)
		s.counted.Store(u.Bytes)
	}
}
