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

// Package kvledger keeps the usenet connection ledger in NATS KV: one
// entry per provider account in clustarr-usenet-connections, listing every
// engine pod's grants with their own expiry (spec 2026-10-16 §3). Every
// write is a compare-and-swap on the entry, so two pods taking at once
// never sum over the account's limit; a grant a pod stops renewing
// expires on its own, so a dead pod's connections return without anyone
// cleaning up.
package kvledger

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/mediactl/clustarr/pkg/download/usenet"
	"github.com/mediactl/clustarr/pkg/events"
)

// DefaultTTL is a grant's life without a renewal; the client renews every
// usenet.Config.GrantRenewInterval (30 s), so three misses lose a grant.
const DefaultTTL = 90 * time.Second

// maxTries bounds the compare-and-swap loop: a bus so contended that eight
// reads in a row are stale is reported, not spun on.
const maxTries = 8

// errContended is the CAS loop's give-up.
var errContended = errors.New("kvledger: the account entry kept changing")

// Ledger is the KV-backed usenet.Ledger for one pod.
type Ledger struct {
	kv  events.KV
	pod string
	ttl time.Duration
	now func() time.Time

	mu sync.Mutex
	// mine is every grant this pod holds, by account then holder, for
	// Renew: the entry alone cannot say which of its grants are ours once
	// another writer has pruned one.
	mine map[string]map[string]usenet.Grant
}

var _ usenet.Ledger = (*Ledger)(nil)

// New builds the ledger over kv (clustarr-usenet-connections) for pod,
// whose grants live ttl without a renewal; 0 means DefaultTTL.
func New(kv events.KV, pod string, ttl time.Duration) *Ledger {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Ledger{kv: kv, pod: pod, ttl: ttl, now: time.Now, mine: map[string]map[string]usenet.Grant{}}
}

// Take implements usenet.Ledger.
func (l *Ledger) Take(ctx context.Context, account string, limit int, g usenet.Grant, want, floor int) (int, error) {
	g.Pod, g.Limit = l.pod, limit
	var got int
	err := l.swap(ctx, account, func(a *usenet.Account, now time.Time) bool {
		if i := slices.IndexFunc(a.Grants, func(x usenet.Grant) bool { return x.Holder == g.Holder }); i >= 0 {
			got = a.Grants[i].Connections
			return false
		}
		got = decide(limit, want, floor, held(a.Grants, ""))
		if got == 0 {
			return false
		}
		g.Connections = got
		g.Expires = now.Add(l.ttl)
		a.Grants = append(a.Grants, g)
		return true
	})
	if err != nil {
		return 0, err
	}
	if got > 0 {
		g.Connections = got
		l.remember(account, g)
	}
	return got, nil
}

func (l *Ledger) remember(account string, g usenet.Grant) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.mine[account] == nil {
		l.mine[account] = map[string]usenet.Grant{}
	}
	l.mine[account][g.Holder] = g
}

func (l *Ledger) stillMine(account, holder string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.mine[account][holder]
	return ok
}

func (l *Ledger) forget(account, holder string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.mine[account], holder)
	if len(l.mine[account]) == 0 {
		delete(l.mine, account)
	}
}

// Give implements usenet.Ledger.
func (l *Ledger) Give(ctx context.Context, account, holder string) error {
	l.forget(account, holder)
	return l.swap(ctx, account, func(a *usenet.Account, _ time.Time) bool {
		before := len(a.Grants)
		a.Grants = slices.DeleteFunc(a.Grants, func(x usenet.Grant) bool { return x.Holder == holder })
		return len(a.Grants) != before
	})
}

// Held implements usenet.Ledger.
func (l *Ledger) Held(ctx context.Context, account string) (int, error) {
	a, _, err := l.read(ctx, account)
	if err != nil {
		return 0, err
	}
	return held(prune(a.Grants, l.now()), ""), nil
}

// Renew implements usenet.Ledger: every grant this pod holds gets a new
// expiry; one gone from its entry -- expired and pruned by another writer
// -- is taken back when the account still has room for it, else its
// holder is reported lost and forgotten.
func (l *Ledger) Renew(ctx context.Context) ([]string, error) {
	l.mu.Lock()
	accounts := make([]string, 0, len(l.mine))
	for a := range l.mine {
		accounts = append(accounts, a)
	}
	l.mu.Unlock()
	slices.Sort(accounts)

	var lost []string
	for _, account := range accounts {
		l.mu.Lock()
		mine := make([]usenet.Grant, 0, len(l.mine[account]))
		for _, g := range l.mine[account] {
			mine = append(mine, g)
		}
		l.mu.Unlock()
		slices.SortFunc(mine, func(a, b usenet.Grant) int { return cmp.Compare(a.Holder, b.Holder) })

		var gone []string
		err := l.swap(ctx, account, func(a *usenet.Account, now time.Time) bool {
			gone = gone[:0]
			changed := false
			for _, m := range mine {
				i := slices.IndexFunc(a.Grants, func(x usenet.Grant) bool { return x.Holder == m.Holder })
				switch {
				case i >= 0:
					a.Grants[i].Expires = now.Add(l.ttl)
					changed = true
				case !l.stillMine(account, m.Holder):
					// Given back while this swap was in flight: not ours to
					// put back.
				case held(a.Grants, "")+m.Connections <= m.Limit:
					m.Expires = now.Add(l.ttl)
					a.Grants = append(a.Grants, m)
					changed = true
				default:
					gone = append(gone, m.Holder)
				}
			}
			return changed
		})
		if err != nil {
			return lost, err
		}
		for _, h := range gone {
			l.forget(account, h)
		}
		lost = append(lost, gone...)
	}
	return lost, nil
}

// read loads an account's entry: a zero Account at revision 0 when absent.
func (l *Ledger) read(ctx context.Context, account string) (usenet.Account, uint64, error) {
	e, err := l.kv.Get(ctx, events.UsenetAccountKey(account))
	if errors.Is(err, events.ErrKeyNotFound) {
		return usenet.Account{}, 0, nil
	}
	if err != nil {
		return usenet.Account{}, 0, fmt.Errorf("kvledger: read %s: %w", account, err)
	}
	var a usenet.Account
	if len(e.Value) > 0 {
		if err := json.Unmarshal(e.Value, &a); err != nil {
			return usenet.Account{}, 0, fmt.Errorf("kvledger: decode %s: %w", account, err)
		}
	}
	return a, e.Revision, nil
}

// swap reads the account, prunes expired grants, applies fn and writes the
// result back under the read revision, deciding again on a mismatch. fn
// returns whether it changed anything; a pruned entry is written even when
// it did not.
func (l *Ledger) swap(ctx context.Context, account string, fn func(*usenet.Account, time.Time) bool) error {
	key := events.UsenetAccountKey(account)
	for try := 0; try < maxTries; try++ {
		a, rev, err := l.read(ctx, account)
		if err != nil {
			return err
		}
		now := l.now()
		kept := prune(a.Grants, now)
		pruned := len(kept) != len(a.Grants)
		a.Grants = kept
		if !fn(&a, now) && !pruned {
			return nil
		}
		val, err := json.Marshal(a)
		if err != nil {
			return fmt.Errorf("kvledger: encode %s: %w", account, err)
		}
		if rev == 0 {
			_, err = l.kv.Create(ctx, key, val)
		} else {
			_, err = l.kv.Update(ctx, key, val, rev)
		}
		switch {
		case err == nil:
			return nil
		case errors.Is(err, events.ErrKeyExists), errors.Is(err, events.ErrRevisionMismatch), errors.Is(err, events.ErrKeyNotFound):
			continue // the entry moved since the read: decide again
		default:
			return fmt.Errorf("kvledger: write %s: %w", account, err)
		}
	}
	return fmt.Errorf("%w: %s", errContended, account)
}

func prune(grants []usenet.Grant, now time.Time) []usenet.Grant {
	kept := make([]usenet.Grant, 0, len(grants))
	for _, g := range grants {
		if g.Expires.After(now) {
			kept = append(kept, g)
		}
	}
	return kept
}

func held(grants []usenet.Grant, except string) int {
	n := 0
	for _, g := range grants {
		if g.Holder != except {
			n += g.Connections
		}
	}
	return n
}

// decide is usenet's rule, restated: the free connections under limit,
// capped at want, or nothing when fewer than min are free.
func decide(limit, want, floor, used int) int {
	n := min(limit-used, want)
	if n < max(floor, 1) {
		return 0
	}
	return n
}
