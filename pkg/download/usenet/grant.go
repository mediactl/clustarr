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
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// # Connection grants
//
// A transfer runs only under a grant from the [Ledger]: connections on one
// server, taken from the budget every pod on that account shares (spec
// 2026-10-16 §4). The grant bounds the job's concurrent fetches through
// its connLimiter, and names the server every fetch is scoped to. No
// primary with room means the job parks: Queued, WaitingTurn (the manager's
// stall clock stops), polling the ledger. A paused or outranked job gives
// its grant back; a grant the renewal finds gone is taken again before the
// next batch, never assumed.

// defaultGrantRenewInterval is how often the client renews its grants.
const defaultGrantRenewInterval = 30 * time.Second

// ledgerPollInterval is how often a parked job asks the ledger again. A
// variable so tests need not wait.
var ledgerPollInterval = 5 * time.Second

// errGrantLost is the limiter's answer to a worker whose grant went away
// between batches: go back through waitForTurn and take one again.
var errGrantLost = errors.New("usenet: the connection grant was given back")

// grant is the job's live grant: the ledger's record plus the account it
// is on.
type grant struct {
	Grant
	Account string
}

// connLimiter bounds a job's concurrent fetches to its grant. set changes
// the bound (0 is "no grant") and bumps the generation, so a worker that
// was admitted under an older grant leaves at its next acquire.
type connLimiter struct {
	mu   sync.Mutex
	cond *sync.Cond
	n    int
	used int
	gen  uint64
}

func newConnLimiter() *connLimiter {
	l := &connLimiter{}
	l.cond = sync.NewCond(&l.mu)
	return l
}

func (l *connLimiter) set(n int) {
	l.mu.Lock()
	l.n = n
	l.gen++
	l.mu.Unlock()
	l.cond.Broadcast()
}

// acquire takes one of the grant's connections. It returns errGrantLost
// when there is no grant (n == 0), so the caller re-takes, and ctx's error
// when cancelled. The wait is woken by set and release; ctx is watched by
// a goroutine that broadcasts on cancellation.
func (l *connLimiter) acquire(ctx context.Context) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	stop := context.AfterFunc(ctx, func() {
		l.mu.Lock()
		l.mu.Unlock() //nolint:staticcheck // taking the lock orders the wake after the Wait.
		l.cond.Broadcast()
	})
	defer stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if l.n == 0 {
			return errGrantLost
		}
		if l.used < l.n {
			l.used++
			return nil
		}
		l.cond.Wait()
	}
}

func (l *connLimiter) release() {
	l.mu.Lock()
	if l.used > 0 {
		l.used--
	}
	l.mu.Unlock()
	l.cond.Broadcast()
}

// holder is the ledger holder for j: "<engine>/<transfer id>".
func (j *job) holder() string { return j.client.holder + "/" + j.id }

// primaries lists the servers a first grant may be taken on: every
// non-backup provider, in the pool's order.
func (c *Client) primaries() []string {
	var out []string
	for _, p := range c.pool.Providers() {
		if !p.Backup {
			out = append(out, p.Name)
		}
	}
	return out
}

// takeGrant takes a grant on the first of candidates (pool order) with
// room, parking the job until one has it. suffix distinguishes a chase
// grant's holder from the transfer's. min is the smallest grant worth
// taking. The job's grant is set on success; the caller sets the limiter.
func (j *job) takeGrant(ctx context.Context, candidates []string, suffix string, min int) (grant, error) {
	log := logging.FromContext(ctx)
	parked := false
	for {
		if err := j.waitWhilePaused(ctx); err != nil {
			return grant{}, err
		}
		g, waits, err := j.tryTake(ctx, candidates, suffix, min)
		if err != nil {
			return grant{}, err
		}
		if g.Connections > 0 {
			j.waitingGrant.Store(false)
			j.mu.Lock()
			if suffix == "" {
				j.grant = &g
			}
			if parked {
				j.lastProgress = time.Now()
			}
			if j.status == download.StatusQueued {
				j.status = download.StatusDownloading
			}
			j.message = fmt.Sprintf("transferring on %s (%d connections)", g.Server, g.Connections)
			j.mu.Unlock()
			if parked {
				log.InfoContext(ctx, "usenet: connections granted after a wait", "download", j.id, "server", g.Server, "connections", g.Connections)
			}
			return g, nil
		}
		if !parked {
			parked = true
			log.InfoContext(ctx, "usenet: no primary server has free connections; waiting", "download", j.id, "servers", waits)
		}
		j.waitingGrant.Store(true)
		j.mu.Lock()
		j.status = download.StatusQueued
		j.message = "waiting for usenet connections (" + waits + ")"
		j.mu.Unlock()
		if err := sleepCtx(ctx, ledgerPollInterval); err != nil {
			return grant{}, err
		}
	}
}

// tryTake asks the ledger once per candidate, in order, and returns the
// first grant, or the "<name> <held>/<limit> in use" summary of every
// candidate when none had room.
func (j *job) tryTake(ctx context.Context, candidates []string, suffix string, min int) (grant, string, error) {
	c := j.client
	var waits []string
	for _, name := range candidates {
		p, ok := c.pool.Provider(name)
		if !ok {
			continue
		}
		want := share(p.Connections, c.maxActive)
		n, err := c.ledger.Take(ctx, p.Account(), p.Connections, Grant{Holder: j.holder() + suffix, Server: name}, want, min)
		if err != nil {
			return grant{}, "", fmt.Errorf("usenet: take connections on %s: %w", name, err)
		}
		if n > 0 {
			return grant{Grant: Grant{Holder: j.holder() + suffix, Server: name, Connections: n}, Account: p.Account()}, "", nil
		}
		held, err := c.ledger.Held(ctx, p.Account())
		if err != nil {
			return grant{}, "", fmt.Errorf("usenet: read the ledger for %s: %w", name, err)
		}
		waits = append(waits, fmt.Sprintf("%s %d/%d in use", name, held, p.Connections))
	}
	return grant{}, strings.Join(waits, ", "), nil
}

// giveGrant returns the job's transfer grant, if any, and closes the
// limiter so a worker still fetching leaves at its next acquire.
func (j *job) giveGrant(ctx context.Context) {
	j.mu.Lock()
	g := j.grant
	j.grant = nil
	j.mu.Unlock()
	j.limiter.set(0)
	if g == nil {
		return
	}
	j.give(ctx, *g)
}

// give returns g to the ledger. A give that fails is logged: the grant
// expires on its own within the TTL.
func (j *job) give(ctx context.Context, g grant) {
	if err := j.client.ledger.Give(ctx, g.Account, g.Holder); err != nil {
		logging.FromContext(ctx).WarnContext(ctx, "usenet: could not return a connection grant; it expires on its own",
			"download", j.id, "server", g.Server, "error", err)
	}
}

// ensureGrant takes the transfer grant when the job holds none and sizes
// the limiter to it. It is the step between admission and the first fetch
// of every batch, so a grant given back on a pause or an outranking is
// taken again before the next article.
func (j *job) ensureGrant(ctx context.Context) error {
	j.mu.Lock()
	has := j.grant != nil
	j.mu.Unlock()
	if has {
		return nil
	}
	g, err := j.takeGrant(ctx, j.client.primaries(), "", 1)
	if err != nil {
		return err
	}
	j.limiter.set(g.Connections)
	return nil
}

// grantServer is the server the transfer grant names, for scoping a fetch.
func (j *job) grantServer() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.grant == nil {
		return nil
	}
	return []string{j.grant.Server}
}

// renewLoop renews the client's grants every GrantRenewInterval until stop
// closes. A holder the ledger reports lost has its job's grant cleared, so
// the job takes a new one before its next batch.
func (c *Client) renewLoop(stop <-chan struct{}) {
	defer c.wg.Done()
	t := time.NewTicker(c.cfg.GrantRenewInterval)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
		}
		ctx, cancel := context.WithTimeout(c.baseCtx, c.cfg.GrantRenewInterval)
		lost, err := c.ledger.Renew(ctx)
		cancel()
		log := logging.FromContext(c.baseCtx)
		if err != nil {
			log.WarnContext(c.baseCtx, "usenet: renewing connection grants failed; trying again next tick", "error", err)
			continue
		}
		for _, h := range lost {
			c.dropGrant(h)
		}
	}
}

// dropGrant forgets the grant held under holder, without giving it back:
// the ledger no longer records it.
func (c *Client) dropGrant(holder string) {
	c.mu.Lock()
	jobs := make([]*job, 0, len(c.jobs))
	for _, j := range c.jobs {
		jobs = append(jobs, j)
	}
	c.mu.Unlock()
	for _, j := range jobs {
		j.mu.Lock()
		dropped := j.grant != nil && j.grant.Holder == holder
		if dropped {
			j.grant = nil
		}
		j.mu.Unlock()
		if dropped {
			j.limiter.set(0)
			logging.FromContext(c.baseCtx).WarnContext(c.baseCtx, "usenet: a connection grant expired under this pod; the transfer takes a new one",
				"download", j.id)
		}
	}
}
