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
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go/jetstream"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/internal/evqueue"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// The watch of a bucket's metadata index (events.ObjectStoreSpec.MetaIndex).
//
// A bucket's stream holds each object's chunks and, after them, its one
// metadata record, so the records lie throughout the stream. nats-server
// starts a last-per-subject consumer by walking every block of the stream
// (fileStore.MultiLastSeqs loads each block's subject table, which reads the
// block), and delivering the records reads the blocks again: the replay of
// 15 MB of records costs the server the whole bucket in memory. The index
// stream holds the records alone, so its replay costs their size.
const (
	// objectIndexWait is how long a watch waits for an index that exists
	// but has not yet copied the bucket's latest record -- one the manager
	// created a moment ago and the server is still filling -- before it
	// watches the bucket itself.
	objectIndexWait = time.Minute
	objectIndexPoll = 250 * time.Millisecond
	// objectWatchOpenTimeout bounds the creation of a watch on the bucket's
	// own stream. nats.go gives a consumer's creation 5 s when the context
	// has no deadline, the server takes longer than that to start a
	// last-per-subject consumer on a large bucket, and creates it all the
	// same: on talos each ui start asked two to four times, every one a
	// whole read of the 1.46 GiB artwork bucket (2026-11-17).
	objectWatchOpenTimeout = 2 * time.Minute
	// objectIndexIdle is how long a replay of the index for List waits for
	// its next record before it gives the index up and reads the bucket.
	objectIndexIdle = 30 * time.Second
	// streamSourceHeader is the header nats-server adds to a sourced
	// message: "<stream> <sequence> <filter> <transform> <subject>".
	streamSourceHeader = "Nats-Stream-Source"
)

// errObjectIndexUnusable is an index a watch may not replay: missing, older
// than its bucket, or behind it past objectIndexWait.
var errObjectIndexUnusable = errors.New("the object metadata index is unusable")

// watchIndex is Watch over the bucket's metadata index stream: the replay of
// every record the index holds, Synced, then every record the server copies
// in. It returns errObjectIndexUnusable, wrapped, when the bucket itself must
// be watched. An event's ModTime is when its record reached the index, which
// for a record older than the index is when the index was filled.
func (o *objectHandle) watchIndex(ctx context.Context) (<-chan events.ObjectEvent, error) {
	name := events.ObjectMetaIndexStream(o.name)
	cons, created, err := o.openIndex(ctx)
	if err != nil {
		return nil, err
	}

	out := make(chan events.ObjectEvent)
	q := evqueue.New[events.ObjectEvent]()
	synced := false
	if info := cons.CachedInfo(); info != nil && info.NumPending == 0 {
		synced = true
		q.Push(events.ObjectEvent{Synced: true})
	}
	// lost is signalled when nats.go finds the consumer gone. It makes a new
	// one from the next sequence by itself, which is right for the same
	// stream and wrong for an index deleted and built again under the watch,
	// whose sequences start over: the checker below closes the watch then,
	// and the reader's reopen replays the new index.
	lost := make(chan struct{}, 1)
	cc, err := cons.Consume(func(m jetstream.Msg) {
		md, err := m.Metadata()
		if err != nil {
			return
		}
		var info jetstream.ObjectInfo
		if err := json.Unmarshal(m.Data(), &info); err == nil {
			info.ModTime = md.Timestamp
			pushObjectInfo(q, o.name, &info)
		}
		if !synced && md.NumPending == 0 {
			synced = true
			q.Push(events.ObjectEvent{Synced: true})
		}
	}, jetstream.ConsumeErrHandler(func(_ jetstream.ConsumeContext, err error) {
		if errors.Is(err, jetstream.ErrConsumerDeleted) || errors.Is(err, jetstream.ErrNoHeartbeat) {
			select {
			case lost <- struct{}{}:
			default:
			}
		}
	}))
	if err != nil {
		q.Close()
		return nil, fmt.Errorf("natsbus: watch %s: %w", name, err)
	}
	go func() {
		defer q.Close()
		defer cc.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-cc.Closed():
				return
			case <-lost:
				if !o.indexUnchanged(ctx, name, created) {
					return
				}
			}
		}
	}()
	go q.Relay(ctx, out)
	return out, nil
}

// openIndex returns a consumer of everything the bucket's index holds, made
// once the index holds the bucket's latest record (awaitIndex), and the
// index's creation time. The index keeps one record per object, so
// everything in it is the latest of each: no last-per-subject scan.
func (o *objectHandle) openIndex(ctx context.Context) (jetstream.Consumer, time.Time, error) {
	name := events.ObjectMetaIndexStream(o.name)
	index, err := o.bus.js.Stream(ctx, name)
	if err != nil {
		if errors.Is(err, jetstream.ErrStreamNotFound) {
			return nil, time.Time{}, fmt.Errorf("%w: stream %s does not exist", errObjectIndexUnusable, name)
		}
		return nil, time.Time{}, fmt.Errorf("natsbus: stream %s: %w", name, err)
	}
	created, err := o.awaitIndex(ctx, index)
	if err != nil {
		return nil, time.Time{}, err
	}
	cons, err := index.OrderedConsumer(ctx, jetstream.OrderedConsumerConfig{
		DeliverPolicy: jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("natsbus: read %s: %w", name, err)
	}
	return cons, created, nil
}

// listIndex is the bucket's live objects as its metadata index holds them,
// which is as the bucket held them when the index was found caught up: the
// latest record of every object, tombstones left out. It returns
// errObjectIndexUnusable, wrapped, when the bucket itself must be listed --
// an index that is missing, stale or behind, or one that stops delivering
// mid-replay.
//
// Two things differ from a list of the bucket. An object's ModTime is when
// its record reached the index, never earlier than its write, so a caller
// that spares young objects spares a few more. And in a bucket that expires
// its objects (ObjectStoreSpec.MaxAge) a record outlives its object by as
// long as it was copied after it, so for up to MaxAge after the index is
// first filled the list may name objects that have expired: reading one is
// ErrObjectNotFound, as for an object deleted meanwhile.
func (o *objectHandle) listIndex(ctx context.Context) ([]*jetstream.ObjectInfo, error) {
	name := events.ObjectMetaIndexStream(o.name)
	cons, _, err := o.openIndex(ctx)
	if err != nil {
		return nil, err
	}
	if info := cons.CachedInfo(); info != nil && info.NumPending == 0 {
		return nil, nil
	}
	var (
		out  []*jetstream.ObjectInfo
		done = make(chan struct{})
		beat = make(chan struct{}, 1)
		over bool
	)
	cc, err := cons.Consume(func(m jetstream.Msg) {
		if over {
			return
		}
		md, err := m.Metadata()
		if err != nil {
			return
		}
		var info jetstream.ObjectInfo
		if err := json.Unmarshal(m.Data(), &info); err == nil && !info.Deleted {
			info.ModTime = md.Timestamp
			out = append(out, &info)
		}
		select {
		case beat <- struct{}{}:
		default:
		}
		if md.NumPending == 0 {
			over = true
			close(done)
		}
	})
	if err != nil {
		return nil, fmt.Errorf("natsbus: read %s: %w", name, err)
	}
	idle := time.NewTimer(objectIndexIdle)
	defer idle.Stop()
	for {
		select {
		case <-done:
			// The handler appends nothing once it has closed done.
			cc.Stop()
			return out, nil
		case <-beat:
			idle.Reset(objectIndexIdle)
		case <-idle.C:
			cc.Stop()
			return nil, fmt.Errorf("%w: %s delivered nothing for %s", errObjectIndexUnusable, name, objectIndexIdle)
		case <-cc.Closed():
			return nil, fmt.Errorf("%w: the read of %s ended early", errObjectIndexUnusable, name)
		case <-ctx.Done():
			cc.Stop()
			return nil, ctx.Err()
		}
	}
}

// listObjects is the bucket's live objects, and whether they were read from
// its metadata index (listIndex) or, for a bucket without a usable one, from
// the bucket's own stream, which costs the server a read of every chunk.
func (o *objectHandle) listObjects(ctx context.Context, store jetstream.ObjectStore) (infos []*jetstream.ObjectInfo, fromIndex bool, err error) {
	if o.bus.objectStoreSpec(o.name).MetaIndex {
		infos, err := o.listIndex(ctx)
		if err == nil {
			return infos, true, nil
		}
		if !errors.Is(err, errObjectIndexUnusable) {
			return nil, false, err
		}
		logIndexFallback(ctx, o.name, err)
	}
	infos, err = store.List(ctx)
	if err != nil && !errors.Is(err, jetstream.ErrNoObjectsFound) {
		return nil, false, err
	}
	return infos, false, nil
}

// indexUnchanged reports whether the index stream still is the one created
// at created. A failed lookup other than "not found" leaves the watch as it
// is: the consumer's own reset is still trying.
func (o *objectHandle) indexUnchanged(ctx context.Context, name string, created time.Time) bool {
	s, err := o.bus.js.Stream(ctx, name)
	if err != nil {
		return !errors.Is(err, jetstream.ErrStreamNotFound)
	}
	return s.CachedInfo().Created.Equal(created)
}

// awaitIndex waits until index holds the bucket's latest metadata record,
// and returns index's creation time. Every record up to that one is then in
// the index, since the server copies them in order, and every later one
// arrives as a live event.
func (o *objectHandle) awaitIndex(ctx context.Context, index jetstream.Stream) (time.Time, error) {
	bucketName := events.ObjectStoreStream(o.name)
	bucket, err := o.bus.js.Stream(ctx, bucketName)
	if err != nil {
		return time.Time{}, fmt.Errorf("natsbus: stream %s: %w", bucketName, err)
	}
	created := index.CachedInfo().Created
	if created.Before(bucket.CachedInfo().Created) {
		return time.Time{}, fmt.Errorf("%w: %s is older than its bucket, which was created again",
			errObjectIndexUnusable, index.CachedInfo().Config.Name)
	}
	var want uint64
	last, err := bucket.GetLastMsgForSubject(ctx, events.ObjectMetaSubjects(o.name))
	switch {
	case err == nil:
		want = last.Sequence
	case errors.Is(err, jetstream.ErrMsgNotFound):
		return created, nil // an empty bucket
	default:
		return time.Time{}, fmt.Errorf("natsbus: last metadata record of %s: %w", bucketName, err)
	}

	deadline := time.NewTimer(objectIndexWait)
	defer deadline.Stop()
	var have uint64
	for {
		have, err = o.indexedThrough(ctx, index, bucketName)
		if err != nil {
			return time.Time{}, err
		}
		if have >= want {
			return created, nil
		}
		select {
		case <-ctx.Done():
			return time.Time{}, ctx.Err()
		case <-deadline.C:
			return time.Time{}, fmt.Errorf("%w: %s holds the bucket's records through sequence %d of %d after %s",
				errObjectIndexUnusable, index.CachedInfo().Config.Name, have, want, objectIndexWait)
		case <-time.After(objectIndexPoll):
		}
	}
}

// indexedThrough is the bucket sequence of the last record index holds, from
// the source header of its last message; 0 for an index that holds none.
func (o *objectHandle) indexedThrough(ctx context.Context, index jetstream.Stream, bucketStream string) (uint64, error) {
	last, err := index.GetLastMsgForSubject(ctx, events.ObjectMetaSubjects(o.name))
	if err != nil {
		if errors.Is(err, jetstream.ErrMsgNotFound) {
			return 0, nil
		}
		return 0, fmt.Errorf("natsbus: last record of %s: %w", index.CachedInfo().Config.Name, err)
	}
	fields := strings.Fields(last.Header.Get(streamSourceHeader))
	if len(fields) < 2 || fields[0] != bucketStream {
		return 0, fmt.Errorf("%w: a record of %s names no sequence of %s in its %s header (%q)",
			errObjectIndexUnusable, index.CachedInfo().Config.Name, bucketStream, streamSourceHeader,
			last.Header.Get(streamSourceHeader))
	}
	seq, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: a record of %s carries the %s header %q",
			errObjectIndexUnusable, index.CachedInfo().Config.Name, streamSourceHeader,
			last.Header.Get(streamSourceHeader))
	}
	return seq, nil
}

// openContext is a context for opening something that then lives as long as
// the parent: it carries a deadline, and ends at it, only until opened is
// called; from then on it ends when the parent does.
//
// nats.go's ObjectStore.Watch needs it. It gives the consumer's creation the
// context's deadline, or 5 s when the context has none, and it unsubscribes
// the watch when the same context ends -- so a plain context.WithTimeout
// would end the watch at the deadline, and none bounds the creation at 5 s.
type openContext struct {
	context.Context
	deadline time.Time
	done     chan struct{}
	release  chan struct{}
	quit     chan struct{}

	mu       sync.Mutex
	err      error
	released bool
}

func withOpenDeadline(parent context.Context, d time.Duration) *openContext {
	c := &openContext{
		Context:  parent,
		deadline: time.Now().Add(d),
		done:     make(chan struct{}),
		release:  make(chan struct{}),
		quit:     make(chan struct{}),
	}
	go func() {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-parent.Done():
			c.end(parent.Err())
			return
		case <-c.quit:
			c.end(context.Canceled)
			return
		case <-timer.C:
			c.end(context.DeadlineExceeded)
			return
		case <-c.release:
		}
		select {
		case <-parent.Done():
			c.end(parent.Err())
		case <-c.quit:
			c.end(context.Canceled)
		}
	}()
	return c
}

func (c *openContext) end(err error) {
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
	close(c.done)
}

// opened lifts the deadline: the open succeeded.
func (c *openContext) opened() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.released {
		c.released = true
		close(c.release)
	}
}

// abandon ends the context: the open failed.
func (c *openContext) abandon() {
	select {
	case <-c.quit:
	default:
		close(c.quit)
	}
}

func (c *openContext) Deadline() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.released {
		return c.Context.Deadline()
	}
	return c.deadline, true
}

func (c *openContext) Done() <-chan struct{} { return c.done }

func (c *openContext) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// pushObjectInfo queues info as a watch event: a tombstone as Deleted, by
// name alone. A malformed digest is skipped, as List would refuse it; the
// next change to the name delivers it again.
func pushObjectInfo(q *evqueue.Queue[events.ObjectEvent], bucket string, info *jetstream.ObjectInfo) {
	if info.Deleted {
		q.Push(events.ObjectEvent{Deleted: true, Info: events.ObjectInfo{Name: info.Name}})
		return
	}
	if oi, err := objectInfoOf(bucket, info); err == nil {
		q.Push(events.ObjectEvent{Info: oi})
	}
}

// logIndexFallback says why a watch reads the bucket's own stream.
func logIndexFallback(ctx context.Context, bucket string, err error) {
	logging.FromContext(ctx).Warn("bus: reading the object store's own stream; its metadata index is unusable",
		"bucket", bucket, "err", err)
}
