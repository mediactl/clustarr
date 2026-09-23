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

package natsbus

import (
	"context"
	"errors"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/mediactl/clustarr/pkg/events"
)

// KV binds to a bucket. The binding is lazy and cached: the first call
// resolves the bucket, so a bus created before Ensure still works.
func (b *Bus) KV(name string) events.KV { return &kvHandle{bus: b, name: name} }

type kvHandle struct {
	bus  *Bus
	name string
}

var _ events.KV = (*kvHandle)(nil)

func (k *kvHandle) resolve(ctx context.Context) (jetstream.KeyValue, error) {
	k.bus.mu.Lock()
	if k.bus.closed {
		k.bus.mu.Unlock()
		return nil, events.ErrClosed
	}
	if kv, ok := k.bus.buckets[k.name]; ok {
		k.bus.mu.Unlock()
		return kv, nil
	}
	k.bus.mu.Unlock()

	kv, err := k.bus.js.KeyValue(ctx, k.name)
	if err != nil {
		if errors.Is(err, jetstream.ErrBucketNotFound) {
			return nil, fmt.Errorf("natsbus: bucket %q: %w", k.name, events.ErrBucketNotFound)
		}
		return nil, fmt.Errorf("natsbus: bind bucket %q: %w", k.name, err)
	}
	k.bus.mu.Lock()
	k.bus.buckets[k.name] = kv
	k.bus.mu.Unlock()
	return kv, nil
}

// kvError maps JetStream key/value failures onto the package sentinels.
//
// The order matters. JetStream reports both "key already exists" and "the
// revision you passed is stale" with wrong-last-sequence, so jetstream
// .ErrKeyExists matches a compare-and-swap conflict too. Only Create can
// legitimately mean "exists"; for every other operation the same code means
// the revision moved.
func kvError(op, bucket, key string, err error) error {
	switch {
	case errors.Is(err, jetstream.ErrKeyRevisionMismatch):
		return fmt.Errorf("natsbus: %s %s/%s: %w", op, bucket, key, events.ErrRevisionMismatch)
	case errors.Is(err, jetstream.ErrKeyExists) && op != "create":
		return fmt.Errorf("natsbus: %s %s/%s: %w", op, bucket, key, events.ErrRevisionMismatch)
	case errors.Is(err, jetstream.ErrKeyNotFound):
		return fmt.Errorf("natsbus: %s %s/%s: %w", op, bucket, key, events.ErrKeyNotFound)
	case errors.Is(err, jetstream.ErrKeyExists):
		return fmt.Errorf("natsbus: %s %s/%s: %w", op, bucket, key, events.ErrKeyExists)
	case errors.Is(err, jetstream.ErrBucketNotFound):
		return fmt.Errorf("natsbus: %s %s: %w", op, bucket, events.ErrBucketNotFound)
	default:
		return fmt.Errorf("natsbus: %s %s/%s: %w", op, bucket, key, err)
	}
}

func entryOf(bucket string, e jetstream.KeyValueEntry) events.Entry {
	op := events.KVPut
	switch e.Operation() {
	case jetstream.KeyValueDelete:
		op = events.KVDelete
	case jetstream.KeyValuePurge:
		op = events.KVPurge
	}
	return events.Entry{
		Bucket:    bucket,
		Key:       e.Key(),
		Value:     e.Value(),
		Revision:  e.Revision(),
		Created:   e.Created(),
		Delta:     e.Delta(),
		Operation: op,
	}
}

// Get returns the current revision of key.
func (k *kvHandle) Get(ctx context.Context, key string) (events.Entry, error) {
	kv, err := k.resolve(ctx)
	if err != nil {
		return events.Entry{}, err
	}
	e, err := kv.Get(ctx, key)
	if err != nil {
		return events.Entry{}, kvError("get", k.name, key, err)
	}
	return entryOf(k.name, e), nil
}

// Create writes key only if it is absent. WithTTL needs the bucket to have a
// non-zero LimitMarkerTTL, which events.Default sets on every bucket.
func (k *kvHandle) Create(ctx context.Context, key string, val []byte,
	opts ...events.KVOption,
) (uint64, error) {
	kv, err := k.resolve(ctx)
	if err != nil {
		return 0, err
	}
	o := events.ResolveKVOptions(opts)
	var copts []jetstream.KVCreateOpt
	if o.TTL > 0 {
		copts = append(copts, jetstream.KeyTTL(o.TTL))
	}
	rev, err := kv.Create(ctx, key, val, copts...)
	if err != nil {
		return 0, kvError("create", k.name, key, err)
	}
	return rev, nil
}

// Update writes key only if its current revision is rev.
func (k *kvHandle) Update(ctx context.Context, key string, val []byte,
	rev uint64,
) (uint64, error) {
	kv, err := k.resolve(ctx)
	if err != nil {
		return 0, err
	}
	next, err := kv.Update(ctx, key, val, rev)
	if err != nil {
		return 0, kvError("update", k.name, key, err)
	}
	return next, nil
}

// Put writes key unconditionally.
func (k *kvHandle) Put(ctx context.Context, key string, val []byte) (uint64, error) {
	kv, err := k.resolve(ctx)
	if err != nil {
		return 0, err
	}
	rev, err := kv.Put(ctx, key, val)
	if err != nil {
		return 0, kvError("put", k.name, key, err)
	}
	return rev, nil
}

// Delete places a delete marker on key.
func (k *kvHandle) Delete(ctx context.Context, key string) error {
	kv, err := k.resolve(ctx)
	if err != nil {
		return err
	}
	if err := kv.Delete(ctx, key); err != nil {
		if errors.Is(err, jetstream.ErrKeyNotFound) {
			return nil
		}
		return kvError("delete", k.name, key, err)
	}
	return nil
}

// DeleteRevision places a delete marker on key only if key's last revision is
// rev. JetStream enforces it with the expected-last-subject-sequence header,
// so a key rewritten, deleted or expired since rev fails the same way.
func (k *kvHandle) DeleteRevision(ctx context.Context, key string, rev uint64) error {
	if rev == 0 {
		// jetstream.LastRevision(0) means "no expectation": an
		// unconditional delete. No stored revision is 0.
		return fmt.Errorf("natsbus: delete %s/%s at revision 0: %w",
			k.name, key, events.ErrRevisionMismatch)
	}
	kv, err := k.resolve(ctx)
	if err != nil {
		return err
	}
	if err := kv.Delete(ctx, key, jetstream.LastRevision(rev)); err != nil {
		return kvError("delete", k.name, key, err)
	}
	return nil
}

// Watch streams the current value of every key matching pattern and then
// every subsequent change. The JetStream end-of-initial-values marker is
// swallowed, because events.Entry has no nil form.
func (k *kvHandle) Watch(ctx context.Context, pattern string) (<-chan events.Entry, error) {
	kv, err := k.resolve(ctx)
	if err != nil {
		return nil, err
	}
	w, err := kv.Watch(ctx, pattern)
	if err != nil {
		return nil, kvError("watch", k.name, pattern, err)
	}
	out := make(chan events.Entry, 64)
	go func() {
		defer close(out)
		defer func() { _ = w.Stop() }()
		for {
			select {
			case <-ctx.Done():
				return
			case e, ok := <-w.Updates():
				if !ok {
					return
				}
				if e == nil {
					continue
				}
				select {
				case out <- entryOf(k.name, e):
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out, nil
}
