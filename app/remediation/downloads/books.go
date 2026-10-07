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

package downloads

import (
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/app/grab/lifecycle"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// BlockBook records the release index's replies to the owed blocklist
// calls (ruling R9): by entry uid for a block, by nonce for an unblock.
// It is leader-local and safe to lose: a lost reply re-calls, and the row
// write is idempotent and seq-fenced.
type BlockBook struct {
	mu      sync.Mutex
	entries map[types.UID]lifecycle.BlockReply
	nonces  map[string]bool
}

// NewBlockBook returns an empty book.
func NewBlockBook() *BlockBook {
	return &BlockBook{entries: map[types.UID]lifecycle.BlockReply{}, nonces: map[string]bool{}}
}

// record stores a reply.
func (b *BlockBook) record(uid types.UID, nonce string, r lifecycle.BlockReply) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if nonce != "" {
		b.nonces[nonce] = true
		return
	}
	if cur, ok := b.entries[uid]; ok && cur.Seq > r.Seq {
		return
	}
	b.entries[uid] = r
}

// replies are the book's replies for uids, and every confirmed nonce.
func (b *BlockBook) replies(uids []types.UID) (map[types.UID]lifecycle.BlockReply, map[string]bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[types.UID]lifecycle.BlockReply{}
	for _, u := range uids {
		if r, ok := b.entries[u]; ok {
			out[u] = r
		}
	}
	nonces := make(map[string]bool, len(b.nonces))
	for n := range b.nonces {
		nonces[n] = true
	}
	return out, nonces
}

// removalBook records the output paths the stage's SafeRemove removed, so
// an entry waiting on its data's removal is dropped on the next pass.
type removalBook struct {
	mu    sync.Mutex
	paths map[string]time.Time
}

func newRemovalBook() *removalBook { return &removalBook{paths: map[string]time.Time{}} }

func (b *removalBook) done(path string, now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.paths[path] = now
	// Bounded: a path is read on the next pass and never again.
	for p, at := range b.paths {
		if now.Sub(at) > time.Hour {
			delete(b.paths, p)
		}
	}
}

func (b *removalBook) snapshot(paths []string) map[string]bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[string]bool{}
	for _, p := range paths {
		if _, ok := b.paths[p]; ok {
			out[p] = true
		}
	}
	return out
}

// commandBook is what the stage last commanded each entry, by seq and
// desired-state hash (lifecycle.IssuedCommand): it tells a changed standing
// intent from an unchanged one. Lost with the leader, which costs one extra
// command per entry.
type commandBook struct {
	mu sync.Mutex
	m  map[types.UID]lifecycle.IssuedCommand
}

func newCommandBook() *commandBook {
	return &commandBook{m: map[types.UID]lifecycle.IssuedCommand{}}
}

func (b *commandBook) get(uids []types.UID) map[types.UID]lifecycle.IssuedCommand {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := map[types.UID]lifecycle.IssuedCommand{}
	for _, u := range uids {
		if c, ok := b.m[u]; ok {
			out[u] = c
		}
	}
	return out
}

func (b *commandBook) set(m map[types.UID]lifecycle.IssuedCommand) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for u, c := range m {
		b.m[u] = c
	}
}

func (b *commandBook) forget(uids []types.UID) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, u := range uids {
		delete(b.m, u)
	}
}

// ownerKey is a claim owner's identity in the leader-local indexes.
type ownerKey struct{ Kind, Namespace, Name string }

func keyOfRef(r schema.ItemRef) ownerKey {
	return ownerKey{Kind: r.Kind, Namespace: r.Namespace, Name: r.Name}
}

// Unclaimed is S27's leader-local index of transfer records whose entry no
// owner holds (claimed: false), keyed by the claim's owner. It is safe to
// lose: a transfer record is rewritten at least daily, and the stage seeds
// the index from the bucket once per leader.
type Unclaimed struct {
	mu sync.Mutex
	m  map[ownerKey]map[string]lifecycle.Transfer // by entry uid
}

// NewUnclaimed returns an empty index.
func NewUnclaimed() *Unclaimed {
	return &Unclaimed{m: map[ownerKey]map[string]lifecycle.Transfer{}}
}

// observe files rec under its claim's owner while it reads claimed: false
// and present, and drops it otherwise.
func (u *Unclaimed) observe(rec *schema.TransferRecord, revision uint64) (ownerKey, bool) {
	if rec == nil || rec.Claim == nil || rec.Entry.UID == "" {
		return ownerKey{}, false
	}
	k := keyOfRef(rec.Claim.Owner)
	u.mu.Lock()
	defer u.mu.Unlock()
	if rec.Claimed || rec.State != schema.TransferStatePresent {
		if byUID, ok := u.m[k]; ok {
			delete(byUID, rec.Entry.UID)
			if len(byUID) == 0 {
				delete(u.m, k)
			}
		}
		return k, false
	}
	byUID, ok := u.m[k]
	if !ok {
		byUID = map[string]lifecycle.Transfer{}
		u.m[k] = byUID
	}
	byUID[rec.Entry.UID] = lifecycle.Transfer{Record: *rec, Revision: revision}
	return k, true
}

// forOwner lists the owner's unclaimed transfers.
func (u *Unclaimed) forOwner(k ownerKey) []lifecycle.Transfer {
	u.mu.Lock()
	defer u.mu.Unlock()
	out := make([]lifecycle.Transfer, 0, len(u.m[k]))
	for _, t := range u.m[k] {
		out = append(out, t)
	}
	return out
}

// holds reports an unclaimed transfer naming the owner.
func (u *Unclaimed) holds(k ownerKey) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.m[k]) > 0
}

// bootBook remembers each engine's boot and when it first reported
// reattached at it, so the stage knows ReattachedAt and an engine record's
// source can tell a new boot.
type bootBook struct {
	mu sync.Mutex
	m  map[string]bootState // by engine name
}

type bootState struct {
	bootID       string
	reattachedAt time.Time
}

func newBootBook() *bootBook { return &bootBook{m: map[string]bootState{}} }

// see records an engine record; it reports a new boot (a bootID the book had
// before and that changed).
func (b *bootBook) see(engine, bootID string, reattached bool, at time.Time) (newBoot bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	cur, ok := b.m[engine]
	if !ok || cur.bootID != bootID {
		newBoot = ok && cur.bootID != bootID
		cur = bootState{bootID: bootID}
	}
	if reattached && cur.reattachedAt.IsZero() {
		cur.reattachedAt = at
	}
	b.m[engine] = cur
	return newBoot
}

// reattachedAt is when engine first reported reattached at bootID.
func (b *bootBook) reattachedAt(engine, bootID string) time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	if cur, ok := b.m[engine]; ok && cur.bootID == bootID {
		return cur.reattachedAt
	}
	return time.Time{}
}
