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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/internal/evqueue"
)

// memObject is one stored object, spec §B.1.
type memObject struct {
	data     []byte
	digest   string
	headers  map[string]string
	metadata map[string]string
	modTime  time.Time
}

// objectBucket is one in-memory object-store bucket.
type objectBucket struct {
	mu      sync.Mutex
	spec    events.ObjectStoreSpec
	objects map[string]*memObject
	// created is when Ensure created the bucket (ObjectStore.Status).
	created time.Time
	// watchers are the open Watch queues. A change is pushed to each under
	// mu, so every watcher sees the bucket's own order; a push never
	// blocks, so no writer waits on a reader.
	watchers []*evqueue.Queue[events.ObjectEvent]
}

// notifyLocked hands e to every watcher. The caller holds b.mu.
func (b *objectBucket) notifyLocked(e events.ObjectEvent) {
	for _, q := range b.watchers {
		q.Push(e)
	}
}

// objectHandle is the events.ObjectStore view of one bucket. A handle for a
// bucket that Ensure never created carries a nil bucket and fails every call
// with ErrBucketNotFound, matching kvHandle.
type objectHandle struct {
	bus    *Bus
	bucket *objectBucket
	name   string
}

var _ events.ObjectStore = (*objectHandle)(nil)

func (o *objectHandle) resolve() (*objectBucket, error) {
	if o.bucket == nil {
		return nil, fmt.Errorf("membus: object store %q: %w", o.name, events.ErrBucketNotFound)
	}
	if o.bus.isClosed() {
		return nil, events.ErrClosed
	}
	return o.bucket, nil
}

// infoOf renders obj as the events.ObjectInfo view. The caller holds the
// bucket's mutex.
func infoOf(name string, obj *memObject) events.ObjectInfo {
	return events.ObjectInfo{
		Name:     name,
		Size:     int64(len(obj.data)),
		Digest:   obj.digest,
		ModTime:  obj.modTime,
		Headers:  cloneHeaders(obj.headers),
		Metadata: cloneHeaders(obj.metadata),
	}
}

func cloneHeaders(h map[string]string) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, v := range h {
		out[k] = v
	}
	return out
}

// Get returns name's current info and its content. A missing object is
// ErrObjectNotFound.
func (o *objectHandle) Get(ctx context.Context, name string) (events.ObjectInfo, io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return events.ObjectInfo{}, nil, err
	}
	b, err := o.resolve()
	if err != nil {
		return events.ObjectInfo{}, nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	obj, ok := b.objects[name]
	if !ok {
		return events.ObjectInfo{}, nil, fmt.Errorf("membus: get %s/%s: %w", o.name, name, events.ErrObjectNotFound)
	}
	data := append([]byte(nil), obj.data...)
	return infoOf(name, obj), io.NopCloser(bytes.NewReader(data)), nil
}

// Put writes name unconditionally, computing the hex SHA-256 digest of the
// content read from r. meta replaces the object's headers and metadata
// whole, as a NATS Put does.
func (o *objectHandle) Put(ctx context.Context, name string, r io.Reader,
	meta events.ObjectMeta,
) (events.ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return events.ObjectInfo{}, err
	}
	b, err := o.resolve()
	if err != nil {
		return events.ObjectInfo{}, err
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return events.ObjectInfo{}, fmt.Errorf("membus: put %s/%s: %w", o.name, name, err)
	}
	sum := sha256.Sum256(data)
	obj := &memObject{
		data:     data,
		digest:   hex.EncodeToString(sum[:]),
		headers:  cloneHeaders(meta.Headers),
		metadata: cloneHeaders(meta.Metadata),
		modTime:  o.bus.clock.Now(),
	}
	b.mu.Lock()
	b.objects[name] = obj
	b.notifyLocked(events.ObjectEvent{Info: infoOf(name, obj)})
	b.mu.Unlock()
	return infoOf(name, obj), nil
}

// SetMeta replaces a live object's headers and metadata, keeping its bytes
// and digest; its modTime becomes now, as a NATS meta publish's does. A
// missing object is ErrObjectNotFound.
func (o *objectHandle) SetMeta(ctx context.Context, name string, meta events.ObjectMeta) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b, err := o.resolve()
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	old, ok := b.objects[name]
	if !ok {
		return fmt.Errorf("membus: set meta %s/%s: %w", o.name, name, events.ErrObjectNotFound)
	}
	// A new memObject rather than an in-place edit: an info rendered
	// earlier shares nothing with it.
	obj := &memObject{
		data:     old.data,
		digest:   old.digest,
		headers:  cloneHeaders(meta.Headers),
		metadata: cloneHeaders(meta.Metadata),
		modTime:  o.bus.clock.Now(),
	}
	b.objects[name] = obj
	b.notifyLocked(events.ObjectEvent{Info: infoOf(name, obj)})
	return nil
}

// Delete removes name. Unlike KV's Delete, deleting an absent object is
// ErrObjectNotFound, matching jetstream.ObjectStore.Delete.
func (o *objectHandle) Delete(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b, err := o.resolve()
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.objects[name]; !ok {
		return fmt.Errorf("membus: delete %s/%s: %w", o.name, name, events.ErrObjectNotFound)
	}
	delete(b.objects, name)
	b.notifyLocked(events.ObjectEvent{Deleted: true, Info: events.ObjectInfo{Name: name}})
	return nil
}

// Info returns name's current metadata without its content. A missing
// object is ErrObjectNotFound.
func (o *objectHandle) Info(ctx context.Context, name string) (events.ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return events.ObjectInfo{}, err
	}
	b, err := o.resolve()
	if err != nil {
		return events.ObjectInfo{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	obj, ok := b.objects[name]
	if !ok {
		return events.ObjectInfo{}, fmt.Errorf("membus: info %s/%s: %w", o.name, name, events.ErrObjectNotFound)
	}
	return infoOf(name, obj), nil
}

// List returns the info of every object whose name starts with prefix,
// sorted by name.
func (o *objectHandle) List(ctx context.Context, prefix string) ([]events.ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b, err := o.resolve()
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]events.ObjectInfo, 0, len(b.objects))
	for name, obj := range b.objects {
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		out = append(out, infoOf(name, obj))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Watch replays the live objects in name order (membus keeps no
// tombstones), then Synced unless WatchUpdatesOnly, then every Put, SetMeta
// and Delete in the bucket's order, through an unbounded queue that never
// drops one. WatchFromRevision is ErrWatchOptionUnsupported, as on natsbus.
// The channel closes on ctx or Close.
func (o *objectHandle) Watch(ctx context.Context, opts ...events.WatchOption) (<-chan events.ObjectEvent, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	wo := events.ResolveWatchOptions(opts)
	if wo.FromRevision != 0 {
		return nil, fmt.Errorf("membus: watch %s from revision %d: %w", o.name, wo.FromRevision, events.ErrWatchOptionUnsupported)
	}
	b, err := o.resolve()
	if err != nil {
		return nil, err
	}
	q := evqueue.New[events.ObjectEvent]()
	b.mu.Lock()
	if !wo.UpdatesOnly {
		names := make([]string, 0, len(b.objects))
		for name := range b.objects {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			q.Push(events.ObjectEvent{Info: infoOf(name, b.objects[name])})
		}
		q.Push(events.ObjectEvent{Synced: true})
	}
	b.watchers = append(b.watchers, q)
	b.mu.Unlock()

	wctx, cancel := context.WithCancel(ctx)
	out := make(chan events.ObjectEvent)
	go q.Relay(wctx, out)
	o.bus.wg.Add(1)
	go func() {
		defer o.bus.wg.Done()
		select {
		case <-ctx.Done():
		case <-o.bus.done:
		}
		b.mu.Lock()
		kept := b.watchers[:0]
		for _, x := range b.watchers {
			if x != q {
				kept = append(kept, x)
			}
		}
		b.watchers = kept
		b.mu.Unlock()
		q.Close()
		cancel()
	}()
	return out, nil
}

// Status reports when Ensure created the bucket and the summed sizes of its
// live objects.
func (o *objectHandle) Status(ctx context.Context) (events.ObjectStoreStatus, error) {
	if err := ctx.Err(); err != nil {
		return events.ObjectStoreStatus{}, err
	}
	b, err := o.resolve()
	if err != nil {
		return events.ObjectStoreStatus{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var size uint64
	for _, obj := range b.objects {
		size += uint64(len(obj.data))
	}
	return events.ObjectStoreStatus{Bucket: o.name, Created: b.created, Bytes: size}, nil
}

var _ events.ObjectStoreAdmin = (*Bus)(nil)

// PurgeOrphanChunks implements events.ObjectStoreAdmin: membus stores each
// object whole, so no chunk can be orphaned and nothing is purged.
func (b *Bus) PurgeOrphanChunks(context.Context, string, time.Duration) (int, uint64, error) {
	return 0, 0, nil
}
