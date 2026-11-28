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

	"github.com/mediactl/clustarr/pkg/download/usenetaccount"
)

// Account is the connection budget's key ([usenetaccount.Key]): host, port
// and username for a provider on its own, the pool's name and the username
// for one in a pool, so every pod, every DownloadClient and every host of
// a pool on one login shares one budget.
func (p Provider) Account() string {
	return usenetaccount.Key(p.Pool, p.Host, p.Port, p.Username)
}

// AccountLimit is the cap on the provider's account: its pool's
// connections, or its own.
func (p Provider) AccountLimit() int {
	if p.Pool != "" && p.PoolConnections > 0 {
		return p.PoolConnections
	}
	return max(p.Connections, 1)
}

// HostLimit is the most the provider's one host may hold of its account:
// its own connections, never more than the account's.
func (p Provider) HostLimit() int {
	return min(max(p.Connections, 1), p.AccountLimit())
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
	Limit int `json:"limit,omitempty"`
	// Host is the server's hostname, and HostLimit the most every grant on
	// that host may hold together when this one is taken (0: no bound
	// beside the account's): a pool's member capped below its pool, or a
	// transfer leaving a share of its primary to chases.
	Host      string `json:"host,omitempty"`
	HostLimit int    `json:"hostLimit,omitempty"`
	// PodLimit is the most the taking pod's grants on the account may hold
	// together when this one is taken (0: no bound): the pod's entitlement
	// among the pods sharing the account ([Entitlement]).
	PodLimit int       `json:"podLimit,omitempty"`
	Expires  time.Time `json:"expires"`
}

// Member is one pod's claim on an account's connections: its weight among
// the pods using the account now. A pod with nothing to transfer writes a
// weight of zero, so the others divide the whole account.
type Member struct {
	Weight  int64     `json:"weight"`
	Expires time.Time `json:"expires"`
}

// Account is the ledger's value for one account: every live grant on it,
// and every pod's membership.
type Account struct {
	Grants  []Grant           `json:"grants"`
	Members map[string]Member `json:"members,omitempty"`
}

// Usage is what every pod has read from an account in the current quota
// period.
type Usage = usenetaccount.Usage

// QuotaPeriod is when an account's usage starts again.
type QuotaPeriod string

// The quota periods (downloadv1alpha1.QuotaPeriod's values).
const (
	QuotaMonth QuotaPeriod = "Month"
	QuotaNever QuotaPeriod = "Never"
)

// Ledger is the shared record of connections in use per provider account.
// A transfer runs only under a grant from it, so pods on the same account
// stay under the plan's cap together (spec 2026-10-16 §3).
type Ledger interface {
	// Take grants g.Holder up to want connections on account, bounded by
	// limit minus every other live grant, and by g's host and pod limits
	// ([GrantRoom]); it takes nothing and returns 0 when fewer than min
	// are free. A holder that already holds a grant on account gets that
	// grant's size back unchanged.
	Take(ctx context.Context, account string, limit int, g Grant, want, min int) (int, error)
	// Give returns holder's grant on account; an absent grant is not an
	// error.
	Give(ctx context.Context, account, holder string) error
	// Resize changes holder's grant on account to want connections: down
	// freely (never under one), up as far as limit and g's host and pod
	// limits leave room. It returns the grant's new size, 0 when the
	// holder has no grant.
	Resize(ctx context.Context, account string, limit int, g Grant, want int) (int, error)
	// Held is the live connections on account.
	Held(ctx context.Context, account string) (int, error)
	// Renew extends every grant this ledger's pod holds and names the
	// holders whose grants it found gone: expired and pruned by another
	// writer, with no room to take them back.
	Renew(ctx context.Context) (lost []string, err error)
	// Join records this pod's weight on account and returns the sum of
	// every live member's weight, this pod's included.
	Join(ctx context.Context, account string, weight int64) (total int64, err error)
	// Use adds bytes to what has been read from account in its current
	// quota period and returns the period's total; a Month period starts
	// again when the calendar month (UTC) turns.
	Use(ctx context.Context, account string, period QuotaPeriod, bytes int64) (Usage, error)
}

// Entitlement is a pod's share of an account of limit connections: its
// weight over every live member's, at least one connection, and all of
// them when it is the only member with any weight.
func Entitlement(limit int, weight, total int64) int {
	if weight <= 0 || total <= weight {
		return max(limit, 1)
	}
	return max(1, int(int64(limit)*weight/total))
}

// GrantRoom is how many connections g could hold on an account of limit
// connections beside grants, g's own holder's grant not counted: what is
// free under the account's limit, under g's host limit among the grants on
// its host, and under g's pod limit among its pod's grants.
func GrantRoom(grants []Grant, g Grant, limit int) int {
	var all, host, pod int
	for _, x := range grants {
		if x.Holder == g.Holder {
			continue
		}
		all += x.Connections
		if x.Host == g.Host {
			host += x.Connections
		}
		if x.Pod == g.Pod {
			pod += x.Connections
		}
	}
	room := limit - all
	if g.HostLimit > 0 {
		room = min(room, g.HostLimit-host)
	}
	if g.PodLimit > 0 {
		room = min(room, g.PodLimit-pod)
	}
	return room
}

// UsageAfter is u with bytes added at now: a Month period that began in an
// earlier calendar month (UTC) starts again first.
func UsageAfter(u Usage, period QuotaPeriod, bytes int64, now time.Time) Usage {
	now = now.UTC()
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	switch {
	case u.PeriodStart.IsZero():
		u.PeriodStart = now
		if period != QuotaNever {
			u.PeriodStart = month
		}
	case period != QuotaNever && u.PeriodStart.Before(month):
		u = Usage{PeriodStart: month}
	}
	u.Bytes += max(bytes, 0)
	return u
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
	usage  map[string]Usage
	now    func() time.Time
}

// NewLocalLedger builds an empty LocalLedger.
func NewLocalLedger() *LocalLedger {
	return &LocalLedger{grants: map[string]map[string]Grant{}, usage: map[string]Usage{}, now: time.Now}
}

var _ Ledger = (*LocalLedger)(nil)

func (l *LocalLedger) list(account string) []Grant {
	out := make([]Grant, 0, len(l.grants[account]))
	for _, g := range l.grants[account] {
		out = append(out, g)
	}
	return out
}

// Take implements Ledger.
func (l *LocalLedger) Take(_ context.Context, account string, limit int, g Grant, want, min int) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	held := l.grants[account]
	if cur, ok := held[g.Holder]; ok {
		return cur.Connections, nil
	}
	n := decide(GrantRoom(l.list(account), g, limit), want, min)
	if n == 0 {
		return 0, nil
	}
	if held == nil {
		held = map[string]Grant{}
		l.grants[account] = held
	}
	g.Connections, g.Limit = n, limit
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

// Resize implements Ledger.
func (l *LocalLedger) Resize(_ context.Context, account string, limit int, g Grant, want int) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cur, ok := l.grants[account][g.Holder]
	if !ok {
		return 0, nil
	}
	cur.HostLimit, cur.PodLimit, cur.Limit = g.HostLimit, g.PodLimit, limit
	cur.Connections = resized(cur.Connections, want, GrantRoom(l.list(account), cur, limit))
	l.grants[account][g.Holder] = cur
	return cur.Connections, nil
}

// Held implements Ledger.
func (l *LocalLedger) Held(_ context.Context, account string) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, g := range l.grants[account] {
		n += g.Connections
	}
	return n, nil
}

// Renew implements Ledger: nothing expires in one process.
func (l *LocalLedger) Renew(context.Context) ([]string, error) { return nil, nil }

// Join implements Ledger: one process is the only member.
func (l *LocalLedger) Join(_ context.Context, _ string, weight int64) (int64, error) {
	return weight, nil
}

// Use implements Ledger, counting in this process alone.
func (l *LocalLedger) Use(_ context.Context, account string, period QuotaPeriod, bytes int64) (Usage, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	u := UsageAfter(l.usage[account], period, bytes, l.now())
	l.usage[account] = u
	return u, nil
}

// decide is the one rule both ledgers apply: the room, capped at want, or
// nothing when less than min of it is free.
func decide(room, want, min int) int {
	n := room
	if want < n {
		n = want
	}
	if n < max(min, 1) {
		return 0
	}
	return n
}

// Decide is [decide] for the ledgers outside this package.
func Decide(room, want, min int) int { return decide(room, want, min) }

// resized is a grant of cur connections asked to hold want, with room for
// it (its own not counted): down freely, up as far as the room, never
// under one.
func resized(cur, want, room int) int {
	want = max(want, 1)
	if want <= cur {
		return want
	}
	return max(cur, min(want, room))
}

// Resized is [resized] for the ledgers outside this package.
func Resized(cur, want, room int) int { return resized(cur, want, room) }

// primaryShares is each primary server's share per transfer, so every
// primary carries transfers at once (owner, 2026-10-28) rather than each
// taking limit / MaxActive of the first primary with room: MaxActive's
// slots are divided among the primaries in proportion to their
// [transferBudgets], rounded and at least one each, and a share is a
// server's budget over its slots. With one primary that is
// limit / MaxActive, as before.
func primaryShares(providers []Provider, maxActive int) map[string]int {
	maxActive = max(maxActive, 1)
	budgets := transferBudgets(providers)
	total := 0
	for _, b := range budgets {
		total += b
	}
	out := map[string]int{}
	for name, b := range budgets {
		slots := max(1, (2*maxActive*b+total)/(2*total))
		out[name] = max(1, b/slots)
	}
	return out
}

// transferBudgets is what transfers may hold of each primary. With one
// primary that is every connection: its chases go to the backups. With
// several, transfers leave a tenth of each primary (at least one) to
// chases: a job's mid-transfer chase parks until the server it asks has
// room while the job keeps its own grant, so two primaries whose every
// connection a transfer held could each wait on the other's chase for
// good.
func transferBudgets(providers []Provider) map[string]int {
	primaries := 0
	for _, p := range providers {
		if !p.Backup {
			primaries++
		}
	}
	out := map[string]int{}
	for _, p := range providers {
		if p.Backup {
			continue
		}
		limit := p.HostLimit()
		if primaries > 1 && limit > 1 {
			limit -= max(1, limit/10)
		}
		out[p.Name] = limit
	}
	return out
}
