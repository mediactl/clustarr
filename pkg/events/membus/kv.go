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

package membus

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/internal/evqueue"
)

// kvValue is one live key.
type kvValue struct {
	value   []byte
	rev     uint64
	created time.Time

	// expires is the key's own expiry. A zero value means the key lives
	// until the bucket TTL removes it.
	expires time.Time
}

// watcher is one Watch: an unbounded queue (evqueue) a relay drains into the
// reader's channel, in order, so a slow reader neither loses an update nor
// blocks a writer -- natsbus's parity, which relays nats.go's watch through
// the same queue (loop spec 2026-10-06 §4.15: membus Watch never drops).
type watcher struct {
	pattern string
	q       *evqueue.Queue[events.Entry]
}

// bucket is one in-memory key/value bucket.
type bucket struct {
	mu       sync.Mutex
	spec     events.BucketSpec
	rev      uint64
	vals     map[string]*kvValue
	watchers []*watcher
	// created is when Ensure created it (events.KVStatus.Created).
	created time.Time
}

func (b *bucket) closeWatchers() {
	b.mu.Lock()
	ws := b.watchers
	b.watchers = nil
	b.mu.Unlock()
	for _, w := range ws {
		w.q.Close()
	}
}

// liveLocked returns the value for key if it exists and has not expired,
// removing it if it has. The caller holds b.mu.
func (b *bucket) liveLocked(key string, now time.Time) (*kvValue, bool) {
	v, ok := b.vals[key]
	if !ok {
		return nil, false
	}
	ttl := v.expires
	if ttl.IsZero() && b.spec.TTL > 0 {
		ttl = v.created.Add(b.spec.TTL)
	}
	if !ttl.IsZero() && !now.Before(ttl) {
		delete(b.vals, key)
		return nil, false
	}
	return v, true
}

// notifyLocked fans an entry out to matching watchers. The caller holds b.mu.
func (b *bucket) notifyLocked(e events.Entry) {
	for _, w := range b.watchers {
		if !events.SubjectMatches(w.pattern, e.Key) {
			continue
		}
		w.q.Push(e)
	}
}

// kvHandle is the events.KV view of one bucket. A handle for a bucket that
// Ensure never created carries a nil bucket and fails every call with
// ErrBucketNotFound.
type kvHandle struct {
	bus    *Bus
	bucket *bucket
	name   string
}

var _ events.KV = (*kvHandle)(nil)

func (k *kvHandle) resolve() (*bucket, error) {
	if k.bucket == nil {
		return nil, fmt.Errorf("membus: bucket %q: %w", k.name, events.ErrBucketNotFound)
	}
	if k.bus.isClosed() {
		return nil, events.ErrClosed
	}
	return k.bucket, nil
}

// Get returns the current revision of key.
func (k *kvHandle) Get(ctx context.Context, key string) (events.Entry, error) {
	if err := ctx.Err(); err != nil {
		return events.Entry{}, err
	}
	b, err := k.resolve()
	if err != nil {
		return events.Entry{}, err
	}
	now := k.bus.clock.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.liveLocked(key, now)
	if !ok {
		return events.Entry{}, fmt.Errorf("membus: %q: %w", key, events.ErrKeyNotFound)
	}
	return events.Entry{
		Bucket:    b.spec.Name,
		Key:       key,
		Value:     append([]byte(nil), v.value...),
		Revision:  v.rev,
		Created:   v.created,
		Operation: events.KVPut,
	}, nil
}

// Create writes key only if it is absent.
func (k *kvHandle) Create(ctx context.Context, key string, val []byte,
	opts ...events.KVOption,
) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	b, err := k.resolve()
	if err != nil {
		return 0, err
	}
	o := events.ResolveKVOptions(opts)
	now := k.bus.clock.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.liveLocked(key, now); ok {
		return 0, fmt.Errorf("membus: %q: %w", key, events.ErrKeyExists)
	}
	return b.writeLocked(key, val, now, o.TTL), nil
}

// Update writes key only if its current revision is rev.
func (k *kvHandle) Update(ctx context.Context, key string, val []byte,
	rev uint64,
) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	b, err := k.resolve()
	if err != nil {
		return 0, err
	}
	now := k.bus.clock.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.liveLocked(key, now)
	if !ok {
		return 0, fmt.Errorf("membus: %q: %w", key, events.ErrKeyNotFound)
	}
	if v.rev != rev {
		return 0, fmt.Errorf("membus: %q is at revision %d, not %d: %w",
			key, v.rev, rev, events.ErrRevisionMismatch)
	}
	// An update clears any per-key TTL, matching JetStream.
	return b.writeLocked(key, val, now, 0), nil
}

// Put writes key unconditionally.
func (k *kvHandle) Put(ctx context.Context, key string, val []byte) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	b, err := k.resolve()
	if err != nil {
		return 0, err
	}
	now := k.bus.clock.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.writeLocked(key, val, now, 0), nil
}

// writeLocked stores a new revision and notifies watchers. The caller holds
// b.mu.
func (b *bucket) writeLocked(key string, val []byte, now time.Time,
	ttl time.Duration,
) uint64 {
	b.rev++
	v := &kvValue{
		value:   append([]byte(nil), val...),
		rev:     b.rev,
		created: now,
	}
	if ttl > 0 {
		v.expires = now.Add(ttl)
	}
	b.vals[key] = v
	b.notifyLocked(events.Entry{
		Bucket:    b.spec.Name,
		Key:       key,
		Value:     append([]byte(nil), v.value...),
		Revision:  v.rev,
		Created:   v.created,
		Operation: events.KVPut,
	})
	return v.rev
}

// Delete places a delete marker on key.
func (k *kvHandle) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b, err := k.resolve()
	if err != nil {
		return err
	}
	now := k.bus.clock.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.liveLocked(key, now); !ok {
		return nil
	}
	delete(b.vals, key)
	b.rev++
	b.notifyLocked(events.Entry{
		Bucket:    b.spec.Name,
		Key:       key,
		Revision:  b.rev,
		Created:   now,
		Operation: events.KVDelete,
	})
	return nil
}

// DeleteRevision places a delete marker on key only if its current revision is
// rev. A key that is absent -- deleted or expired since rev was read -- is a
// mismatch, as it is on JetStream, where the key's last revision is then its
// delete marker.
func (k *kvHandle) DeleteRevision(ctx context.Context, key string, rev uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b, err := k.resolve()
	if err != nil {
		return err
	}
	now := k.bus.clock.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.liveLocked(key, now)
	if !ok {
		return fmt.Errorf("membus: %q is absent, not at revision %d: %w",
			key, rev, events.ErrRevisionMismatch)
	}
	if v.rev != rev {
		return fmt.Errorf("membus: %q is at revision %d, not %d: %w",
			key, v.rev, rev, events.ErrRevisionMismatch)
	}
	delete(b.vals, key)
	b.rev++
	b.notifyLocked(events.Entry{
		Bucket:    b.spec.Name,
		Key:       key,
		Revision:  b.rev,
		Created:   now,
		Operation: events.KVDelete,
	})
	return nil
}

// Watch streams the current value of every matching key, in revision order,
// then every subsequent change (events.KV.Watch), through an unbounded queue
// that never drops one. Like JetStream it sends no end-of-initial-values
// marker, because events.Entry has no nil form. WatchUpdatesOnly skips the
// current values; WatchFromRevision replays only the live values written at
// or after it (membus keeps no history or tombstones).
func (k *kvHandle) Watch(ctx context.Context, pattern string, opts ...events.WatchOption) (<-chan events.Entry, error) {
	b, err := k.resolve()
	if err != nil {
		return nil, err
	}
	o := events.ResolveWatchOptions(opts)
	w := &watcher{pattern: pattern, q: evqueue.New[events.Entry]()}
	now := k.bus.clock.Now()
	b.mu.Lock()
	if !o.UpdatesOnly {
		var initial []events.Entry
		for key := range b.vals {
			v, ok := b.liveLocked(key, now)
			if !ok || !events.SubjectMatches(pattern, key) || v.rev < o.FromRevision {
				continue
			}
			initial = append(initial, events.Entry{
				Bucket: b.spec.Name, Key: key, Value: append([]byte(nil), v.value...),
				Revision: v.rev, Created: v.created, Operation: events.KVPut,
			})
		}
		slices.SortFunc(initial, func(x, y events.Entry) int { return cmp.Compare(x.Revision, y.Revision) })
		for _, e := range initial {
			w.q.Push(e)
		}
	}
	b.watchers = append(b.watchers, w)
	b.mu.Unlock()

	wctx, cancel := context.WithCancel(ctx)
	out := make(chan events.Entry)
	go w.q.Relay(wctx, out)
	k.bus.wg.Add(1)
	go func() {
		defer k.bus.wg.Done()
		select {
		case <-ctx.Done():
		case <-k.bus.done:
		}
		b.mu.Lock()
		kept := b.watchers[:0]
		for _, x := range b.watchers {
			if x != w {
				kept = append(kept, x)
			}
		}
		b.watchers = kept
		b.mu.Unlock()
		w.q.Close()
		cancel()
	}()
	return out, nil
}

// Keys lists every live key, sorted.
func (k *kvHandle) Keys(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, err := k.resolve()
	if err != nil {
		return nil, err
	}
	now := k.bus.clock.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for key := range b.vals {
		if _, ok := b.liveLocked(key, now); ok {
			out = append(out, key)
		}
	}
	slices.Sort(out)
	return out, nil
}

// Status reads the bucket's last revision and creation time.
func (k *kvHandle) Status(ctx context.Context) (events.KVStatus, error) {
	if err := ctx.Err(); err != nil {
		return events.KVStatus{}, err
	}
	b, err := k.resolve()
	if err != nil {
		return events.KVStatus{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return events.KVStatus{Bucket: b.spec.Name, LastRevision: b.rev, Created: b.created}, nil
}
