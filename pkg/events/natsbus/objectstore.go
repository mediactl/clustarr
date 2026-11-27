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
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/internal/evqueue"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// defaultTopology is events.Default(), built once: the object store specs a
// bus that never ensured a topology (an agent, markers) puts with.
var defaultTopology = sync.OnceValue(events.Default)

// objectStoreSpec is bucket's spec in the topology this bus ensured, else in
// the default topology every agent awaits; the zero spec for a bucket in
// neither.
func (b *Bus) objectStoreSpec(bucket string) events.ObjectStoreSpec {
	b.mu.Lock()
	top := b.topology
	b.mu.Unlock()
	if spec, ok := top.ObjectStore(bucket); ok {
		return spec
	}
	spec, _ := defaultTopology().ObjectStore(bucket)
	return spec
}

// objectStoreTimeout is how long an object store's requests and, above
// all, a Put's acknowledgements are waited for when the caller's context
// has no deadline, in place of nats.go's 5 s (owner, 2026-11-27: "Start
// with this for now").
//
// A stream stalls for as long as nats-server takes to read what was written
// to a bucket since an object was last written, every time that object is
// written again (docs/claude/gotchas.md, "Writing an object again ..."): 6
// to 10 s on talos's artwork bucket several times a day, 14.2 s the longest
// in twelve days. nats.go's Put gives each publish the JetStream context's
// default timeout, and one that runs out is "nats: timeout waiting for
// ack" -- after which it purges the chunks it wrote and leaves the record
// (dropHollowAfterPut). The record and the chunks were stored; only the
// answer was late. With the wait past the stall, that Put succeeds.
//
// The stall is not shortened: every reader and writer of the bucket still
// waits it out. A context with a deadline is unaffected, and nats.go then
// answers its end with nats.ErrTimeout and purges nothing.
const objectStoreTimeout = 30 * time.Second

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

	store, err := o.bus.jsObjects.ObjectStore(ctx, o.name)
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
	return oi, &objectReader{ReadCloser: res, ctx: ctx, bucket: o.name, name: name}, nil
}

// objectReader is the io.ReadCloser Get hands out over nats.go's
// ObjectResult, so that a read bounded by the caller's ctx which outlives it
// fails with ctx.Err(). nats.go names that end two ways at random:
// objResult.Read copies the context's deadline onto its net.Pipe, and when
// the pipe's deadline fires before the context's Done is observed it returns
// the pipe's bare os.ErrDeadlineExceeded (its `err, ok := err.(net.Error)`
// shadows the error it then means to clear), else its private
// nats.ErrTimeout, a bare errors.New no caller matches without importing
// nats.go. /art's retry (ui/art.go readArt) classified the first and not the
// second, so an object overwritten while it was read -- Put purges the old
// chunks, and the ordered consumer the read is bound to has nothing more to
// deliver, so the pipe stalls to the deadline (research E14) -- answered 500
// on about four reads in ten (2026-11-10). Once ctx is done, a failed read
// is ctx.Err(), wrapped, with nats.go's own words kept in the message; so is
// a timeout once ctx's deadline has passed by the clock, since the pipe's
// timer and the context's fire at one instant and either may run first;
// io.EOF passes through bare (io.ReadAll compares it by value), and a failure
// while ctx lives is named by objectError.
type objectReader struct {
	io.ReadCloser
	ctx          context.Context
	bucket, name string
}

func (r *objectReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if err == nil || errors.Is(err, io.EOF) {
		return n, err
	}
	if cerr := r.ctx.Err(); cerr != nil {
		return n, fmt.Errorf("natsbus: read %s/%s: %w (%v)", r.bucket, r.name, cerr, err)
	}
	if d, ok := r.ctx.Deadline(); ok && isReadTimeout(err) && !time.Now().Before(d) {
		return n, fmt.Errorf("natsbus: read %s/%s: %w (%v)", r.bucket, r.name, context.DeadlineExceeded, err)
	}
	return n, objectError("read", r.bucket, r.name, err)
}

// isReadTimeout reports whether err is a read that ran out of time, in either
// of nats.go's spellings: its own nats.ErrTimeout, or the pipe's net.Error
// (os.ErrDeadlineExceeded).
func isReadTimeout(err error) bool {
	if errors.Is(err, nats.ErrTimeout) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// Put writes name unconditionally, reading r to completion. JetStream chunks
// the object into stream messages under max_payload, so an object far larger
// than the connection's max_payload still round-trips whole. The chunk size
// is meta's override, else the store's ObjectStoreSpec.ChunkSize, else
// nats.go's 128 KiB (ADR-0020 §5). meta replaces the object's whole meta,
// headers and metadata alike (research E3).
func (o *objectHandle) Put(ctx context.Context, name string, r io.Reader,
	meta events.ObjectMeta,
) (events.ObjectInfo, error) {
	chunk, err := events.ObjectChunkSize(o.bus.objectStoreSpec(o.name), meta)
	if err != nil {
		return events.ObjectInfo{}, fmt.Errorf("natsbus: put %s/%s: %w", o.name, name, err)
	}
	store, err := o.resolve(ctx)
	if err != nil {
		return events.ObjectInfo{}, err
	}
	jm := jetstream.ObjectMeta{
		Name: name, Headers: mapToHeader(meta.Headers), Metadata: maps.Clone(meta.Metadata),
	}
	if chunk > 0 {
		jm.Opts = &jetstream.ObjectMetaOptions{ChunkSize: chunk}
	}
	info, err := store.Put(ctx, jm, r)
	if err != nil {
		o.dropHollowAfterPut(ctx, store, name)
		return events.ObjectInfo{}, objectError("put", o.name, name, err)
	}
	return objectInfoOf(o.name, info)
}

// hollowCheckTimeout bounds the look a failed Put takes at what it left. It
// is the context's own time no longer: the Put that failed usually failed
// on a stream too slow to answer, or on a context that is done.
const hollowCheckTimeout = 10 * time.Second

// dropHollowAfterPut removes name's record when a failed Put left it naming
// content the store does not hold.
//
// nats.go's Put (v1.56.0, and its main on 2026-11-27) is not atomic: it
// publishes the chunks and the record without waiting, then waits for every
// acknowledgement, and on any error purges the chunks it wrote -- although
// the record may be stored by then. An acknowledgement that takes longer
// than the 5 s a publish is given ("nats: timeout waiting for ack"), or a
// chunk a full bucket refuses while the small record still fits, leaves a
// live record whose chunks are gone: Info answers, Get stalls to its
// deadline, and every writer that asks "is it there, from these inputs"
// reads yes and never writes it again. On talos that was 31 poster overlays
// in ten days, each written within seconds of a 6 to 10 s stall of the
// artwork stream, and each a poster /art answered 500 for (owner,
// 2026-11-27, of Beef: "has no image").
//
// Removing the record is what a failed Put should have left of a new
// object, and for a replaced one it is the nearest thing: the previous
// content's chunks are not purged by a failed Put, but nothing names them
// any more (the orphan purge reclaims them). The next writer finds no
// object and writes it. Best effort: a store still too slow to answer is
// logged, and PurgeOrphanChunks removes the record on its next pass.
func (o *objectHandle) dropHollowAfterPut(ctx context.Context, store jetstream.ObjectStore, name string) {
	log := logging.FromContext(ctx)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), hollowCheckTimeout)
	defer cancel()
	info, err := store.GetInfo(ctx, name)
	if err != nil {
		if !errors.Is(err, jetstream.ErrObjectNotFound) {
			log.Warn("bus: could not read back the record a failed put may have left",
				"bucket", o.name, "object", name, "err", err)
		}
		return
	}
	removed, err := o.dropIfHollow(ctx, store, info)
	if err != nil {
		log.Warn("bus: could not check the record a failed put may have left",
			"bucket", o.name, "object", name, "err", err)
		return
	}
	if removed {
		log.Warn("bus: a failed put left a record whose content the store does not hold; removed the record",
			"bucket", o.name, "object", name)
	}
}

// dropIfHollow deletes info's object when the store holds fewer chunks than
// its record names, and reports whether it did. The chunks are counted by
// the stream's leader (a subject-filtered stream info, which carries no
// payload): a direct get is answered by any replica, and one that trails
// the record's would call a whole object hollow.
//
// The delete is nats.go's, with no compare-and-swap: a Put of the same name
// that completes between the count and the delete is deleted with it, and
// its writer's next pass writes it again.
func (o *objectHandle) dropIfHollow(ctx context.Context, store jetstream.ObjectStore, info *jetstream.ObjectInfo) (bool, error) {
	if info.Deleted || info.Size == 0 || info.Chunks == 0 {
		return false, nil
	}
	s, err := o.bus.lookupStream(ctx, events.ObjectStoreStream(o.name))
	if err != nil {
		return false, err
	}
	subject := "$O." + o.name + ".C." + info.NUID
	si, err := s.Info(ctx, jetstream.WithSubjectFilter(subject))
	if err != nil {
		return false, fmt.Errorf("natsbus: chunks of %s/%s: %w", o.name, info.Name, err)
	}
	if si.State.Subjects[subject] >= uint64(info.Chunks) {
		return false, nil
	}
	if err := store.Delete(ctx, info.Name); err != nil {
		return false, objectError("delete", o.name, info.Name, err)
	}
	metrics.ObjectHollowRecordsRemovedTotal.WithLabelValues(o.name).Inc()
	return true, nil
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
// listing, so this lists the whole bucket and filters client-side. A bucket
// with a metadata index is listed from the index (listIndex, which says how
// its ModTime and an expiring bucket's names differ); any other from the
// bucket's own stream, which costs the server a read of every chunk.
func (o *objectHandle) List(ctx context.Context, prefix string) ([]events.ObjectInfo, error) {
	store, err := o.resolve(ctx)
	if err != nil {
		return nil, err
	}
	all, _, err := o.listObjects(ctx, store)
	if err != nil {
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

// Watch replays every object's latest meta, tombstones included, then Synced,
// then one event per Put, SetMeta or Delete (artwork design §B.1 as amended
// 2026-10-07). A bucket with a metadata index (ObjectStoreSpec.MetaIndex) is
// replayed from the index stream (watchIndex); any other, an updates-only
// watch, and a bucket whose index is missing, older than the bucket or behind
// it, through nats.go's ObjectStore.Watch, an ordered push consumer on
// $O.<bucket>.M.> of the bucket's own stream, which costs the server a read
// of the whole bucket. WatchFromRevision is refused: nats.go accepts
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
	if !wo.UpdatesOnly && o.bus.objectStoreSpec(o.name).MetaIndex {
		ch, err := o.watchIndex(ctx)
		if err == nil {
			return ch, nil
		}
		if !errors.Is(err, errObjectIndexUnusable) {
			return nil, err
		}
		logIndexFallback(ctx, o.name, err)
	}
	var jo []jetstream.WatchOpt
	if wo.UpdatesOnly {
		jo = append(jo, jetstream.UpdatesOnly())
	}
	octx := withOpenDeadline(ctx, objectWatchOpenTimeout)
	w, err := store.Watch(octx, jo...)
	if err != nil {
		octx.abandon()
		return nil, objectError("watch", o.name, "", err)
	}
	octx.opened()
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
				if info == nil {
					q.Push(events.ObjectEvent{Synced: true})
					continue
				}
				pushObjectInfo(q, o.name, info)
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

var _ events.ObjectStoreAdmin = (*Bus)(nil)

// Purge guard: a list read from the metadata index is trusted for a purge
// only while it leaves few chunks orphaned. Orphans are what two racing Puts
// of one name leak, a handful at most; an index that named few of the
// bucket's objects would call most of its chunks orphans. Past both bounds
// the pass is decided again from the bucket's own stream.
const (
	// orphanGuardMin is the fewest orphaned chunk subjects that trip it.
	orphanGuardMin = 100
	// orphanGuardShare trips it at more than one in orphanGuardShare of the
	// bucket's chunk subjects.
	orphanGuardShare = 4
)

// orphanGuardTrips reports whether orphans of subjects chunk subjects are
// too many to purge on the index's word.
func orphanGuardTrips(orphans, subjects int) bool {
	return orphans >= orphanGuardMin && orphans*orphanGuardShare > subjects
}

// PurgeOrphanChunks implements events.ObjectStoreAdmin (artwork design §B.5 as
// amended 2026-10-07). It needs the object store's stream layout ($O.<bucket>.C.
// <nuid> chunks, the OBJ_<bucket> stream; ADR-20), which is why it lives only
// here. The meta-level List cannot see an orphan, since no meta names it.
//
// The live objects are read from the bucket's metadata index when it has a
// usable one, and from the bucket itself when the index would orphan too
// many chunks (orphanGuardTrips).
//
// A List read before a racing Put replaced X with Y lists X; Y's chunks are
// then not live here but younger than the grace, so they stay.
func (b *Bus) PurgeOrphanChunks(ctx context.Context, bucket string, grace time.Duration) (int, uint64, error) {
	o := &objectHandle{bus: b, name: bucket}
	store, err := o.resolve(ctx)
	if err != nil {
		return 0, 0, err
	}
	all, fromIndex, err := o.listObjects(ctx, store)
	if err != nil {
		return 0, 0, objectError("list", bucket, "", err)
	}
	s, err := b.lookupStream(ctx, events.ObjectStoreStream(bucket))
	if err != nil {
		return 0, 0, err
	}
	prefix := "$O." + bucket + ".C."
	info, err := s.Info(ctx, jetstream.WithSubjectFilter(prefix+">"))
	if err != nil {
		return 0, 0, fmt.Errorf("natsbus: chunk subjects of %s: %w", bucket, err)
	}
	orphans := orphanSubjects(info.State.Subjects, prefix, all)
	if fromIndex && orphanGuardTrips(len(orphans), len(info.State.Subjects)) {
		logging.FromContext(ctx).Warn("bus: the metadata index leaves too many chunks orphaned to purge on its word; reading the object store's own stream",
			"bucket", bucket, "orphans", len(orphans), "chunkSubjects", len(info.State.Subjects), "objects", len(all))
		all, err = store.List(ctx)
		if err != nil && !errors.Is(err, jetstream.ErrNoObjectsFound) {
			return 0, 0, objectError("list", bucket, "", err)
		}
		orphans = orphanSubjects(info.State.Subjects, prefix, all)
	}
	var purged int
	var freed uint64
	var errs []error
	for _, subject := range orphans {
		last, err := s.GetLastMsgForSubject(ctx, subject)
		if err != nil {
			if errors.Is(err, jetstream.ErrMsgNotFound) {
				continue // purged meanwhile, by a Delete or a Put's own cleanup
			}
			errs = append(errs, fmt.Errorf("natsbus: last chunk of %s: %w", subject, err))
			continue
		}
		if time.Since(last.Time) < grace {
			continue // a Put in progress: chunks precede their meta
		}
		if err := s.Purge(ctx, jetstream.WithPurgeSubject(subject)); err != nil {
			errs = append(errs, fmt.Errorf("natsbus: purge %s: %w", subject, err))
			continue
		}
		purged++
		freed += info.State.Subjects[subject] * uint64(len(last.Data)) // the chunk size; the last chunk may be shorter
	}
	metrics.ObjectOrphanChunksPurgedTotal.WithLabelValues(bucket).Add(float64(purged))
	metrics.ObjectOrphanBytesPurgedTotal.WithLabelValues(bucket).Add(float64(freed))
	if err := o.dropHollowRecords(ctx, store, all, info.State.Subjects, prefix, grace); err != nil {
		errs = append(errs, err)
	}
	return purged, freed, errors.Join(errs...)
}

// dropHollowRecords is the purge's other half: where an orphan is chunks no
// record names, a hollow record is a record whose chunks are gone
// (dropHollowAfterPut says how one comes about). It removes every record of
// live that the listing found no chunk subject for and that is older than
// grace, each read again from the bucket and its chunks counted again
// first, so a list that trails the bucket removes nothing: a record
// replaced since the list is judged by what it is now.
//
// This is the net under dropHollowAfterPut, whose look comes while the
// stream is at its slowest, and what removes a record left before
// 2026-11-27. An item whose object is gone is written again by its stage.
func (o *objectHandle) dropHollowRecords(ctx context.Context, store jetstream.ObjectStore, live []*jetstream.ObjectInfo,
	subjects map[string]uint64, prefix string, grace time.Duration,
) error {
	var errs []error
	removed := 0
	for _, listed := range live {
		if listed.Deleted || listed.Size == 0 || listed.Chunks == 0 || subjects[prefix+listed.NUID] >= uint64(listed.Chunks) {
			continue
		}
		if time.Since(listed.ModTime) < grace {
			continue
		}
		now, err := store.GetInfo(ctx, listed.Name)
		if err != nil {
			if !errors.Is(err, jetstream.ErrObjectNotFound) { // expired or deleted since the list
				errs = append(errs, objectError("info", o.name, listed.Name, err))
			}
			continue
		}
		ok, err := o.dropIfHollow(ctx, store, now)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if ok {
			removed++
		}
	}
	if removed > 0 {
		logging.FromContext(ctx).Warn("bus: removed records whose content the object store does not hold",
			"bucket", o.name, "records", removed)
	}
	return errors.Join(errs...)
}

// orphanSubjects is the chunk subjects, sorted, that no object of live
// names.
func orphanSubjects(subjects map[string]uint64, prefix string, live []*jetstream.ObjectInfo) []string {
	nuids := make(map[string]bool, len(live))
	for _, info := range live {
		nuids[info.NUID] = true
	}
	var out []string
	for subject := range subjects {
		if !nuids[strings.TrimPrefix(subject, prefix)] {
			out = append(out, subject)
		}
	}
	sort.Strings(out)
	return out
}
