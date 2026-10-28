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
	"strconv"
	"sync"
	"time"
)

// Account is the connection budget's key: host, port and username. Every
// pod and every DownloadClient configured with the same account shares one
// budget (spec 2026-10-16 §3.1), because the plan's cap is the account's,
// not the pod's.
func (p Provider) Account() string {
	return p.Host + ":" + strconv.Itoa(p.Port) + ":" + p.Username
}

// Grant is one holder's connections on an account, as the ledger records
// it. Holder is "<engine>/<transfer id>" (plus "/chase" for a chase grant);
// Server the provider name the grant was taken for, for anyone reading the
// ledger; Expires is set by the ledger and ignored past.
type Grant struct {
	Holder      string `json:"holder"`
	Pod         string `json:"pod,omitempty"`
	Server      string `json:"server"`
	Connections int    `json:"connections"`
	// Limit is the account limit the taker applied, so a renewal can tell
	// whether a grant it found gone still fits.
	Limit   int       `json:"limit,omitempty"`
	Expires time.Time `json:"expires"`
}

// Account is the ledger's value for one account: every live grant on it.
type Account struct {
	Grants []Grant `json:"grants"`
}

// Ledger is the shared record of connections in use per provider account.
// A transfer runs only under a grant from it, so pods on the same account
// stay under the plan's cap together (spec 2026-10-16 §3).
type Ledger interface {
	// Take grants g.Holder up to want connections on account, bounded by
	// limit minus every other live grant; it takes nothing and returns 0
	// when fewer than min are free. A holder that already holds a grant
	// on account gets that grant's size back unchanged.
	Take(ctx context.Context, account string, limit int, g Grant, want, min int) (int, error)
	// Give returns holder's grant on account; an absent grant is not an
	// error.
	Give(ctx context.Context, account, holder string) error
	// Held is the live connections on account.
	Held(ctx context.Context, account string) (int, error)
	// Renew extends every grant this ledger's pod holds and names the
	// holders whose grants it found gone: expired and pruned by another
	// writer, with no room to take them back.
	Renew(ctx context.Context) (lost []string, err error)
}

// share is one download's share of an account: max(1, limit/maxActive), so
// a pod's MaxActive transfers together never exceed one account's limit,
// and the free count bounds what several pods take together.
func share(limit, maxActive int) int {
	if maxActive <= 0 {
		maxActive = 1
	}
	return max(1, limit/maxActive)
}

// LocalLedger is the in-process Ledger: one pod, so no grant ever expires.
// It is the default when [Config.Ledger] is nil, which makes a one-pod
// install, the library and its tests behave exactly as a one-pod ledger.
type LocalLedger struct {
	mu     sync.Mutex
	grants map[string]map[string]Grant // account → holder → grant
}

// NewLocalLedger builds an empty LocalLedger.
func NewLocalLedger() *LocalLedger {
	return &LocalLedger{grants: map[string]map[string]Grant{}}
}

var _ Ledger = (*LocalLedger)(nil)

// Take implements Ledger.
func (l *LocalLedger) Take(_ context.Context, account string, limit int, g Grant, want, min int) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	held := l.grants[account]
	if cur, ok := held[g.Holder]; ok {
		return cur.Connections, nil
	}
	n := decide(limit, want, min, heldOthers(held, g.Holder))
	if n == 0 {
		return 0, nil
	}
	if held == nil {
		held = map[string]Grant{}
		l.grants[account] = held
	}
	g.Connections = n
	held[g.Holder] = g
	return n, nil
}

// Give implements Ledger.
func (l *LocalLedger) Give(_ context.Context, account, holder string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.grants[account], holder)
	return nil
}

// Held implements Ledger.
func (l *LocalLedger) Held(_ context.Context, account string) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return heldOthers(l.grants[account], ""), nil
}

// Renew implements Ledger: nothing expires in one process.
func (l *LocalLedger) Renew(context.Context) ([]string, error) { return nil, nil }

func heldOthers(held map[string]Grant, except string) int {
	n := 0
	for h, g := range held {
		if h != except {
			n += g.Connections
		}
	}
	return n
}

// decide is the one rule both ledgers apply: the free connections under
// limit, capped at want, or nothing when fewer than min are free.
func decide(limit, want, min, held int) int {
	free := limit - held
	n := free
	if want < n {
		n = want
	}
	if n < max(min, 1) {
		return 0
	}
	return n
}

// primaryShares is each primary server's share of its connections per
// transfer, so every primary carries transfers at once (owner, 2026-10-28)
// rather than each taking limit / MaxActive of the first primary with
// room: MaxActive's slots are divided among the primaries in proportion to
// their connections, rounded and at least one each, and a share is a
// server's connections over its slots. With one primary that is
// limit / MaxActive, as before.
func primaryShares(providers []Provider, maxActive int) map[string]int {
	maxActive = max(maxActive, 1)
	total := 0
	for _, p := range providers {
		if !p.Backup {
			total += max(p.Connections, 1)
		}
	}
	out := map[string]int{}
	for _, p := range providers {
		if p.Backup {
			continue
		}
		limit := max(p.Connections, 1)
		slots := max(1, (2*maxActive*limit+total)/(2*total))
		out[p.Name] = max(1, limit/slots)
	}
	return out
}
