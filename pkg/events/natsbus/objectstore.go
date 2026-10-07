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
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"sort"
	"strings"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/internal/evqueue"
)

// ObjectStore binds to bucket, created by Ensure. The binding is lazy and
// cached, like KV's, so a bus created before Ensure still works.
func (b *Bus) ObjectStore(bucket string) events.ObjectStore {
	return &objectHandle{bus: b, name: bucket}
}

type objectHandle struct {
	bus  *Bus
	name string
}

var _ events.ObjectStore = (*objectHandle)(nil)

func (o *objectHandle) resolve(ctx context.Context) (jetstream.ObjectStore, error) {
	o.bus.mu.Lock()
	if o.bus.closed {
		o.bus.mu.Unlock()
		return nil, events.ErrClosed
	}
	if store, ok := o.bus.objectStores[o.name]; ok {
		o.bus.mu.Unlock()
		return store, nil
	}
	o.bus.mu.Unlock()

	store, err := o.bus.js.ObjectStore(ctx, o.name)
	if err != nil {
		if errors.Is(err, jetstream.ErrBucketNotFound) {
			return nil, fmt.Errorf("natsbus: object store %q: %w", o.name, events.ErrBucketNotFound)
		}
		return nil, fmt.Errorf("natsbus: bind object store %q: %w", o.name, err)
	}
	o.bus.mu.Lock()
	o.bus.objectStores[o.name] = store
	o.bus.mu.Unlock()
	return store, nil
}

// objectError maps JetStream object-store failures onto the package
// sentinels.
//
// nats.go's ErrUpdateMetaDeleted (SetMeta on a deleted object) is
// ErrObjectNotFound too; ErrCantGetBucket (a bucket link, which nothing in
// clustarr creates) is named rather than left a bare cause (artwork design
// §B.1 as amended 2026-10-07).
func objectError(op, bucket, name string, err error) error {
	if errors.Is(err, jetstream.ErrObjectNotFound) || errors.Is(err, jetstream.ErrUpdateMetaDeleted) {
		return fmt.Errorf("natsbus: %s %s/%s: %w", op, bucket, name, events.ErrObjectNotFound)
	}
	if errors.Is(err, jetstream.ErrCantGetBucket) {
		return fmt.Errorf("natsbus: %s %s/%s: the object is a bucket link, which clustarr never creates: %w",
			op, bucket, name, err)
	}
	if errors.Is(err, jetstream.ErrBucketNotFound) {
		return fmt.Errorf("natsbus: %s %s: %w", op, bucket, events.ErrBucketNotFound)
	}
	return fmt.Errorf("natsbus: %s %s/%s: %w", op, bucket, name, err)
}

// objectDigestHex converts JetStream's "SHA-256=<base64url>" digest form
// into the hex-encoded form events.ObjectInfo.Digest documents. An empty
// digest (a freshly created, still-uploading object) stays empty; anything
// else that fails to decode is malformed and is reported as an error, never
// silently turned into "".
func objectDigestHex(digest string) (string, error) {
	if digest == "" {
		return "", nil
	}
	raw, err := jetstream.DecodeObjectDigest(digest)
	if err != nil {
		return "", fmt.Errorf("decode digest %q: %w", digest, err)
	}
	return hex.EncodeToString(raw), nil
}

func headerToMap(h nats.Header) map[string]string {
	if len(h) == 0 {
		return nil
	}
	m := make(map[string]string, len(h))
	for k, v := range h {
		if len(v) > 0 {
			m[k] = v[0]
		}
	}
	return m
}

func mapToHeader(h map[string]string) nats.Header {
	if len(h) == 0 {
		return nil
	}
	out := make(nats.Header, len(h))
	for k, v := range h {
		out[k] = []string{v}
	}
	return out
}

func objectInfoOf(bucket string, info *jetstream.ObjectInfo) (events.ObjectInfo, error) {
	digest, err := objectDigestHex(info.Digest)
	if err != nil {
		return events.ObjectInfo{}, fmt.Errorf("natsbus: object %s/%s: %w", bucket, info.Name, err)
	}
	return events.ObjectInfo{
		Name:     info.Name,
		Size:     int64(info.Size),
		Digest:   digest,
		ModTime:  info.ModTime,
		Headers:  headerToMap(info.Headers),
		Metadata: maps.Clone(info.Metadata),
	}, nil
}

// Get returns name's current info and its content. The caller must close the
// reader. A missing object is ErrObjectNotFound.
func (o *objectHandle) Get(ctx context.Context, name string) (events.ObjectInfo, io.ReadCloser, error) {
	store, err := o.resolve(ctx)
	if err != nil {
		return events.ObjectInfo{}, nil, err
	}
	res, err := store.Get(ctx, name)
	if err != nil {
		return events.ObjectInfo{}, nil, objectError("get", o.name, name, err)
	}
	info, err := res.Info()
	if err != nil {
		_ = res.Close()
		return events.ObjectInfo{}, nil, objectError("get", o.name, name, err)
	}
	oi, err := objectInfoOf(o.name, info)
	if err != nil {
		_ = res.Close()
		return events.ObjectInfo{}, nil, err
	}
	return oi, res, nil
}

// Put writes name unconditionally, reading r to completion. JetStream chunks
// the object into stream messages under max_payload (128 KiB per chunk by
// default), so an object far larger than the connection's max_payload still
// round-trips whole. meta replaces the object's whole meta, headers and
// metadata alike (research E3).
func (o *objectHandle) Put(ctx context.Context, name string, r io.Reader,
	meta events.ObjectMeta,
) (events.ObjectInfo, error) {
	store, err := o.resolve(ctx)
	if err != nil {
		return events.ObjectInfo{}, err
	}
	info, err := store.Put(ctx, jetstream.ObjectMeta{
		Name: name, Headers: mapToHeader(meta.Headers), Metadata: maps.Clone(meta.Metadata),
	}, r)
	if err != nil {
		return events.ObjectInfo{}, objectError("put", o.name, name, err)
	}
	return objectInfoOf(o.name, info)
}

// SetMeta is NATS UpdateMeta with the name unchanged: no rename can happen
// (a rename purges the old name with no tombstone, E5). It rewrites no chunk.
// It has no compare-and-swap (publishMeta sends no expected sequence), so only
// the object's one writer calls it, under its per-item lock (artwork design
// §B.3 as amended).
func (o *objectHandle) SetMeta(ctx context.Context, name string, meta events.ObjectMeta) error {
	store, err := o.resolve(ctx)
	if err != nil {
		return err
	}
	err = store.UpdateMeta(ctx, name, jetstream.ObjectMeta{
		Name: name, Headers: mapToHeader(meta.Headers), Metadata: maps.Clone(meta.Metadata),
	})
	if err != nil {
		return objectError("set meta", o.name, name, err)
	}
	return nil
}

// Delete removes name. An absent object -- never written, or already
// deleted -- is ErrObjectNotFound.
//
// jetstream.ObjectStore.Delete does not itself enforce that: it treats an
// object that is already deleted (a soft-delete tombstone still on record)
// as an idempotent no-op and returns nil, reserving ErrObjectNotFound for a
// name that was never Put at all (verified against a real embedded server).
// The events.ObjectStore contract wants ErrObjectNotFound for both, matching
// membus, which has no tombstone to distinguish them. GetInfo hides deleted
// objects exactly like Get does, so checking visibility first turns both
// "never written" and "already deleted" into ErrObjectNotFound before
// Delete's own, looser, idempotent-no-op behaviour ever runs.
func (o *objectHandle) Delete(ctx context.Context, name string) error {
	store, err := o.resolve(ctx)
	if err != nil {
		return err
	}
	if _, err := store.GetInfo(ctx, name); err != nil {
		return objectError("delete", o.name, name, err)
	}
	if err := store.Delete(ctx, name); err != nil {
		return objectError("delete", o.name, name, err)
	}
	return nil
}

// Info returns name's current metadata without its content. A missing
// object is ErrObjectNotFound.
func (o *objectHandle) Info(ctx context.Context, name string) (events.ObjectInfo, error) {
	store, err := o.resolve(ctx)
	if err != nil {
		return events.ObjectInfo{}, err
	}
	info, err := store.GetInfo(ctx, name)
	if err != nil {
		return events.ObjectInfo{}, objectError("info", o.name, name, err)
	}
	return objectInfoOf(o.name, info)
}

// List returns the info of every object whose name starts with prefix,
// sorted by name. JetStream's object store has no server-side prefix
// listing, so this lists the whole bucket and filters client-side; the
// artwork bucket is capped at 5 GiB (spec §B.2) so one bucket lists in one
// call, and the reaper (spec §B.5) is the only caller that lists at all.
func (o *objectHandle) List(ctx context.Context, prefix string) ([]events.ObjectInfo, error) {
	store, err := o.resolve(ctx)
	if err != nil {
		return nil, err
	}
	all, err := store.List(ctx)
	if err != nil {
		if errors.Is(err, jetstream.ErrNoObjectsFound) {
			return nil, nil
		}
		return nil, objectError("list", o.name, prefix, err)
	}
	out := make([]events.ObjectInfo, 0, len(all))
	for _, info := range all {
		if !strings.HasPrefix(info.Name, prefix) {
			continue
		}
		oi, err := objectInfoOf(o.name, info)
		if err != nil {
			return nil, err
		}
		out = append(out, oi)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Watch wraps nats.go's ObjectStore.Watch, an ordered push consumer on
// $O.<bucket>.M.> (artwork design §B.1 as amended 2026-10-07): the replay of
// every object's latest meta, tombstones included, then Synced, then one event
// per Put, SetMeta or Delete. WatchFromRevision is refused: nats.go accepts
// ResumeFromRevision on an object store and silently replays from the start
// (research E6).
func (o *objectHandle) Watch(ctx context.Context, opts ...events.WatchOption) (<-chan events.ObjectEvent, error) {
	wo := events.ResolveWatchOptions(opts)
	if wo.FromRevision != 0 {
		return nil, fmt.Errorf("natsbus: watch %s from revision %d: %w", o.name, wo.FromRevision, events.ErrWatchOptionUnsupported)
	}
	store, err := o.resolve(ctx)
	if err != nil {
		return nil, err
	}
	var jo []jetstream.WatchOpt
	if wo.UpdatesOnly {
		jo = append(jo, jetstream.UpdatesOnly())
	}
	w, err := store.Watch(ctx, jo...)
	if err != nil {
		return nil, objectError("watch", o.name, "", err)
	}
	out := make(chan events.ObjectEvent)
	q := evqueue.New[events.ObjectEvent]()
	// The pump drains nats.go's channel at once -- its callback blocks on 32
	// slots and would stall the ordered consumer -- into an unbounded queue;
	// the relay hands it on at the reader's pace. An Updates channel that
	// closes while ctx is live means nats.go gave up resetting the consumer:
	// the queue closes, out closes once drained, and the reader reopens.
	go func() {
		defer q.Close()
		defer func() { _ = w.Stop() }()
		for {
			select {
			case <-ctx.Done():
				return
			case info, ok := <-w.Updates():
				if !ok {
					return
				}
				switch {
				case info == nil:
					q.Push(events.ObjectEvent{Synced: true})
				case info.Deleted:
					q.Push(events.ObjectEvent{Deleted: true, Info: events.ObjectInfo{Name: info.Name}})
				default:
					// A malformed digest is skipped, as List would refuse it;
					// the next change to the name delivers it again.
					if oi, err := objectInfoOf(o.name, info); err == nil {
						q.Push(events.ObjectEvent{Info: oi})
					}
				}
			}
		}
	}()
	go q.Relay(ctx, out)
	return out, nil
}

// Status reports the bucket's backing stream: its creation time, which
// changes when the bucket is deleted and created again (research E9), and
// its size.
func (o *objectHandle) Status(ctx context.Context) (events.ObjectStoreStatus, error) {
	store, err := o.resolve(ctx)
	if err != nil {
		return events.ObjectStoreStatus{}, err
	}
	st, err := store.Status(ctx)
	if err != nil {
		return events.ObjectStoreStatus{}, objectError("status", o.name, "", err)
	}
	out := events.ObjectStoreStatus{Bucket: o.name, Bytes: st.Size()}
	// ObjectStoreStatus, the interface, carries no stream info; nats.go's
	// one implementation does.
	if bs, ok := st.(*jetstream.ObjectBucketStatus); ok && bs.StreamInfo() != nil {
		out.Created = bs.StreamInfo().Created
	}
	return out, nil
}
