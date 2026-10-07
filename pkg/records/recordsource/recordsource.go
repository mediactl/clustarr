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

// Package recordsource wakes the remediation loop from a records bucket
// (loop spec 2026-10-06 §4.9): a controller source, so leader-only, that
// watches one bucket from its current end, enqueues the file a worker's
// record names and skips the loop's own writes. It writes nothing and decides
// nothing; every pass reads its records itself, and a dropped wake costs
// latency, never correctness. It links controller-runtime, so only the
// manager uses it; workers write through pkg/records.
package recordsource

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/controller/priorityqueue"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/records"
)

// Timers; the tests shorten them.
var (
	// openDeadline bounds Start's first open, retried with backoff.
	openDeadline = 60 * time.Second
	// reopenInitial and reopenMax bound the reopen backoff, doubling.
	reopenInitial = time.Second
	reopenMax     = 30 * time.Second
	// recreateCheck is how often an open watch checks that its bucket is
	// still the one it opened: nats.go's ordered consumer can outlive a
	// stream deleted and created again without closing (defect D8).
	recreateCheck = 30 * time.Second
)

// Bus is what a source needs of the bus. Every events.Bus is one.
type Bus interface {
	KV(bucket string) events.KV
}

// Decoder reads which file a record names, and its state.
type Decoder func(value []byte) (file schema.Ref, state string, ok bool)

// HeaderDecoder reads schema.RecordHeader's mediaFile and state: every
// records bucket but clustarr-segments, whose v2 records name their file
// under another key (F3.4 passes its own Decoder).
func HeaderDecoder(value []byte) (schema.Ref, string, bool) {
	var h struct {
		MediaFile schema.Ref `json:"mediaFile"`
		State     string     `json:"state"`
	}
	if err := json.Unmarshal(value, &h); err != nil || h.MediaFile.Name == "" {
		return schema.Ref{}, "", false
	}
	return h.MediaFile, h.State, true
}

// Option configures a Source.
type Option[K comparable] func(*Source[K])

// WithDecoder replaces HeaderDecoder.
func WithDecoder[K comparable](d Decoder) Option[K] { return func(s *Source[K]) { s.decode = d } }

// OnRecreated is called when the bucket was deleted and created again under
// the source; enqueue adds at low priority. The loop enqueues, from its cache,
// every file whose status shows an outstanding request of this remediation.
func OnRecreated[K comparable](f func(ctx context.Context, enqueue func(K))) Option[K] {
	return func(s *Source[K]) { s.recreated = f }
}

// Source is one bucket's waker.
type Source[K comparable] struct {
	bus       Bus
	bucket    string
	toKey     func(schema.Ref) (K, bool)
	decode    Decoder
	recreated func(ctx context.Context, enqueue func(K))

	// lastRev and created belong to the run goroutine once Start returns.
	lastRev uint64
	created time.Time
}

// New is bucket's waker: toKey maps a record's file to the loop's key, false
// to skip it.
func New[K comparable](bus Bus, bucket string, toKey func(schema.Ref) (K, bool), opts ...Option[K]) *Source[K] {
	s := &Source[K]{bus: bus, bucket: bucket, toKey: toKey, decode: HeaderDecoder}
	for _, o := range opts {
		o(s)
	}
	return s
}

func (s *Source[K]) String() string { return "records source " + s.bucket }

type watch struct {
	ch     <-chan events.Entry
	cancel context.CancelFunc
	// replayThrough marks a recreated bucket's replay: entries at or below
	// it are enqueued at low priority.
	replayThrough uint64
}

// Start opens the watch from the bucket's end, synchronously, retrying with
// backoff for up to openDeadline: controller-runtime starts workers only once
// every source started and every cache synced, so a record written before
// this open is read by the pass the informer's initial list enqueues, and one
// written after is delivered (§4.9). A goroutine forwards until ctx ends.
func (s *Source[K]) Start(ctx context.Context, q workqueue.TypedRateLimitingInterface[K]) error {
	kv := s.bus.KV(s.bucket)
	deadline := time.Now().Add(openDeadline)
	wait := reopenInitial
	for {
		w, _, err := s.open(ctx, kv, true)
		if err == nil {
			go s.run(ctx, kv, w, q)
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().Add(wait).After(deadline) {
			return fmt.Errorf("recordsource: watch %s: %w", s.bucket, err)
		}
		logging.FromContext(ctx).Warn("recordsource: could not open the records watch; retrying", "bucket", s.bucket, "error", err, "in", wait)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		wait = min(wait*2, reopenMax)
	}
}

// open reads the bucket's status, then watches: from its end on the first
// open, from lastRev+1 on a reopen, and from the start of a bucket that was
// recreated (its creation time changed, or its last revision fell below
// lastRev), which it reports.
func (s *Source[K]) open(ctx context.Context, kv events.KV, first bool) (watch, bool, error) {
	st, err := kv.Status(ctx)
	if err != nil {
		return watch{}, false, err
	}
	var opts []events.WatchOption
	var w watch
	recreated := false
	switch {
	case first:
		s.created, s.lastRev = st.Created, max(s.lastRev, st.LastRevision)
		opts = append(opts, events.WatchUpdatesOnly())
	case !st.Created.Equal(s.created) || st.LastRevision < s.lastRev:
		recreated = true
		s.created, s.lastRev, w.replayThrough = st.Created, 0, st.LastRevision
	default:
		opts = append(opts, events.WatchFromRevision(s.lastRev+1))
	}
	wctx, cancel := context.WithCancel(ctx)
	ch, err := kv.Watch(wctx, ">", opts...)
	if err != nil {
		cancel()
		return watch{}, false, err
	}
	w.ch, w.cancel = ch, cancel
	return w, recreated, nil
}

func (s *Source[K]) run(ctx context.Context, kv events.KV, w watch, q workqueue.TypedRateLimitingInterface[K]) {
	log := logging.FromContext(ctx).With("bucket", s.bucket)
	tick := time.NewTicker(recreateCheck)
	defer tick.Stop()
	defer func() { w.cancel() }()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-w.ch:
			if ok {
				s.handle(e, w.replayThrough, q)
				continue
			}
		case <-tick.C:
			st, err := kv.Status(ctx)
			if err != nil && !errors.Is(err, events.ErrBucketNotFound) {
				continue
			}
			if err == nil && st.Created.Equal(s.created) && st.LastRevision >= s.lastRev {
				continue
			}
		}
		w.cancel()
		next, ok := s.reopen(ctx, kv, q, log)
		if !ok {
			return
		}
		w = next
	}
}

// reopen opens the watch again after a backoff of reopenInitial doubling to
// reopenMax, until ctx ends.
func (s *Source[K]) reopen(ctx context.Context, kv events.KV, q workqueue.TypedRateLimitingInterface[K], log interface {
	Warn(msg string, args ...any)
},
) (watch, bool) {
	wait := reopenInitial
	for {
		select {
		case <-ctx.Done():
			return watch{}, false
		case <-time.After(wait):
		}
		w, recreated, err := s.open(ctx, kv, false)
		if err != nil {
			wait = min(wait*2, reopenMax)
			log.Warn("recordsource: could not reopen the records watch; retrying", "error", err, "in", wait)
			continue
		}
		if recreated {
			log.Warn("recordsource: the records bucket was recreated; replaying it and waking every outstanding request")
			if s.recreated != nil {
				s.recreated(ctx, func(k K) { addLow(q, k) })
			}
		}
		return w, true
	}
}

// handle enqueues the file of a worker's write; the loop's own writes
// (requested, withdrawn), deletes, purges and undecodable values only move
// lastRev.
func (s *Source[K]) handle(e events.Entry, replayThrough uint64, q workqueue.TypedRateLimitingInterface[K]) {
	s.lastRev = max(s.lastRev, e.Revision)
	if e.Operation != events.KVPut {
		return
	}
	ref, state, ok := s.decode(e.Value)
	if !ok || state == records.StateRequested || state == records.StateWithdrawn {
		return
	}
	k, ok := s.toKey(ref)
	if !ok {
		return
	}
	if e.Revision <= replayThrough {
		addLow(q, k)
		return
	}
	q.Add(k)
}

// addLow enqueues k at controller-runtime's LowPriority when the queue is a
// priority queue (the default since v0.23), else plainly.
func addLow[K comparable](q workqueue.TypedRateLimitingInterface[K], k K) {
	if pq, ok := q.(priorityqueue.PriorityQueue[K]); ok {
		low := handler.LowPriority
		pq.AddWithOpts(priorityqueue.AddOpts{Priority: &low}, k)
		return
	}
	q.Add(k)
}
