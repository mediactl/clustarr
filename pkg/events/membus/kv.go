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
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
)

// watchBuffer is how many entries a watcher may fall behind before updates
// are dropped for it. A dropped update never blocks a writer.
const watchBuffer = 64

// kvValue is one live key.
type kvValue struct {
	value   []byte
	rev     uint64
	created time.Time

	// expires is the key's own expiry. A zero value means the key lives
	// until the bucket TTL removes it.
	expires time.Time
}

type watcher struct {
	pattern string
	ch      chan events.Entry
	done    chan struct{}
	once    sync.Once
}

func (w *watcher) close() {
	w.once.Do(func() {
		close(w.done)
		close(w.ch)
	})
}

// bucket is one in-memory key/value bucket.
type bucket struct {
	mu       sync.Mutex
	spec     events.BucketSpec
	rev      uint64
	vals     map[string]*kvValue
	watchers []*watcher
}

func (b *bucket) closeWatchers() {
	b.mu.Lock()
	ws := b.watchers
	b.watchers = nil
	b.mu.Unlock()
	for _, w := range ws {
		w.close()
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
		select {
		case w.ch <- e:
		case <-w.done:
		default:
		}
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

// Watch streams the current value of every matching key and then every
// subsequent change. Unlike JetStream it sends no end-of-initial-values
// marker, because events.Entry has no nil form; callers that need one should
// read the current keys themselves before watching.
func (k *kvHandle) Watch(ctx context.Context, pattern string) (<-chan events.Entry, error) {
	b, err := k.resolve()
	if err != nil {
		return nil, err
	}
	w := &watcher{
		pattern: pattern,
		ch:      make(chan events.Entry, watchBuffer),
		done:    make(chan struct{}),
	}
	now := k.bus.clock.Now()
	b.mu.Lock()
	for key := range b.vals {
		v, ok := b.liveLocked(key, now)
		if !ok || !events.SubjectMatches(pattern, key) {
			continue
		}
		select {
		case w.ch <- events.Entry{
			Bucket:    b.spec.Name,
			Key:       key,
			Value:     append([]byte(nil), v.value...),
			Revision:  v.rev,
			Created:   v.created,
			Operation: events.KVPut,
		}:
		default:
		}
	}
	b.watchers = append(b.watchers, w)
	b.mu.Unlock()

	k.bus.wg.Add(1)
	go func() {
		defer k.bus.wg.Done()
		select {
		case <-ctx.Done():
		case <-k.bus.done:
		case <-w.done:
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
		w.close()
	}()
	return w.ch, nil
}
