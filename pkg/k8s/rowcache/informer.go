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

package rowcache

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"sync"
	"time"
	"unique"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/apimachinery/pkg/watch"
	toolscache "k8s.io/client-go/tools/cache"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// Informer is a client-go SharedIndexInformer whose store holds each
// object as a [Row]: its JSON compressed, not a decoded Go object. It runs
// client-go's own reflector and DeltaFIFO; the FIFO's transform turns every
// object into a row as it arrives (the decoded object lives only for the
// watch decode), the one writer applies the deltas to the store, and
// everything that leaves -- a handler notification, an index value for a
// row stored before its index was registered -- is decoded from the row
// again. [Cache] answers Get and List from the rows directly.
//
// Handler notifications carry rows and are decoded in each handler's own
// goroutine, so a late handler's replay of the whole store (every
// controller's, since controllers start after the cache has synced) runs
// off the writer's lock; the handlers that register together share one
// replay and one decode of each row ([replay]).
type Informer struct {
	// lw is the cache's lister-watcher with the watch stream rowified:
	// see listerWatcher.
	lw      toolscache.ListerWatcher
	resync  time.Duration
	codec   *codec
	indexer toolscache.Indexer

	// mu is the writer's lock: process, the dictionary sweep and a late
	// handler's replay hold it, so the store and the order of
	// notifications move under one hand. It is taken before state.
	mu sync.Mutex
	// state guards the lifecycle and the registrations.
	state      sync.Mutex
	started    bool
	stopped    bool
	ctx        context.Context
	log        *slog.Logger
	transform  toolscache.TransformFunc
	watchErr   toolscache.WatchErrorHandlerWithContext
	controller toolscache.Controller
	indexFuncs map[string]toolscache.IndexFunc
	listeners  map[*listener]struct{}
	// dictionaryOnSync builds the kind's dictionary once the initial list
	// is in; a test turns it off to build one by hand.
	dictionaryOnSync bool

	// synced closes once the initial list is in the store; every listener
	// registered by then is told after its last initial notification.
	synced     chan struct{}
	syncedOnce sync.Once

	// Under mu: what the store holds, mirrored into the gauges.
	rows, bytes, objectBytes int64
	// gen counts the store's writes, under mu: a replay opened at one
	// generation is joined only while the store is still at it.
	gen uint64
	// replay is the latest late-handler replay, joinable until it starts.
	replay *replay
	// The replay's tuning (replayQuiet, replayWindow, replayLead); fields so
	// a test can shorten them.
	replayQuiet, replayWindow time.Duration
	replayLead                int
	// wire is the content type the kind streams in when the row cache
	// built the lister-watcher itself (cbor.go); "" for controller-runtime's.
	wire string
}

var _ toolscache.SharedIndexInformer = (*Informer)(nil)

func newInformer(lw toolscache.ListerWatcher, _ runtime.Object, k *kind, resync time.Duration, indexers toolscache.Indexers) *Informer {
	s := &Informer{
		resync:     resync,
		codec:      k.codec,
		indexFuncs: make(map[string]toolscache.IndexFunc, len(indexers)),
		listeners:  map[*listener]struct{}{},
		synced:     make(chan struct{}),

		dictionaryOnSync: true,
		replayQuiet:      replayQuiet,
		replayWindow:     replayWindow,
		replayLead:       replayLead,
	}
	s.lw = s.listerWatcher(lw)
	adapters := make(toolscache.Indexers, len(indexers))
	for name, fn := range indexers {
		s.indexFuncs[name] = fn
		adapters[name] = s.adapter(name, fn)
	}
	s.indexer = toolscache.NewIndexer(rowKey, adapters)
	return s
}

// rowEvent is a watch event's object once the watch stream has turned it
// into a row: the metadata the reflector reads (name, namespace, resource
// version, labels) beside the row the FIFO's transform unwraps. A
// watch-list's temporary store in the reflector holds these, about 300
// bytes each, where it held the decoded objects until the whole initial
// list was in (2026-11-03: 230 MB of the manager's start-up peak).
type rowEvent struct {
	metav1.TypeMeta
	metav1.ObjectMeta
	row *Row
}

func (e *rowEvent) DeepCopyObject() runtime.Object { return e }

// listerWatcher wraps the cache's lister-watcher so every object on the
// watch stream becomes a row before the reflector stores it. A list (the
// fallback when the apiserver cannot stream, and a relist) still decodes
// the collection whole; the FIFO's transform turns its items into rows.
func (s *Informer) listerWatcher(inner toolscache.ListerWatcher) toolscache.ListerWatcher {
	lw := toolscache.ToListerWatcherWithContext(inner)
	return &toolscache.ListWatch{
		ListWithContextFunc: lw.ListWithContext,
		WatchFuncWithContext: func(ctx context.Context, opts metav1.ListOptions) (watch.Interface, error) {
			w, err := lw.WatchWithContext(ctx, opts)
			if err != nil {
				return nil, err
			}
			return watch.Filter(w, s.rowifyEvent), nil
		},
	}
}

// rowifyEvent turns an Added, Modified or Deleted event's object into a
// rowEvent; a bookmark or an error passes as it is. An object that cannot
// become a row passes too, for the FIFO's transform to report.
func (s *Informer) rowifyEvent(e watch.Event) (watch.Event, bool) {
	switch e.Type {
	case watch.Added, watch.Modified, watch.Deleted:
	default:
		return e, true
	}
	m, err := meta.Accessor(e.Object)
	if err != nil {
		return e, true
	}
	rv := m.GetResourceVersion()
	obj, err := s.rowify(e.Object)
	if err != nil {
		return e, true
	}
	r, ok := obj.(*Row)
	if !ok {
		return e, true
	}
	e.Object = &rowEvent{
		ObjectMeta: metav1.ObjectMeta{Name: r.name, Namespace: r.namespace, ResourceVersion: rv, Labels: r.labels},
		row:        r,
	}
	return e, true
}

// adapter runs an index function registered for the kind's objects on the
// rows the store holds: the values a row carries from when it was built,
// else the object decoded again (a row stored before the index existed).
func (s *Informer) adapter(name string, fn toolscache.IndexFunc) toolscache.IndexFunc {
	return func(obj any) ([]string, error) {
		r, ok := obj.(*Row)
		if !ok {
			return fn(obj)
		}
		if vals, ok := r.indexValues(name); ok {
			return vals, nil
		}
		o, err := s.codec.decode(r, reasonIndex)
		if err != nil {
			return nil, err
		}
		return fn(o)
	}
}

// rowify is the DeltaFIFO's transform: the informer's own transform first
// (the manager's strips managedFields), then the object becomes a row with
// every registered index's values computed while it is still decoded.
func (s *Informer) rowify(obj any) (any, error) {
	// Already a row: the watch stream rowified it, or a resync carries the
	// stored row.
	switch o := obj.(type) {
	case *rowEvent:
		return o.row, nil
	case *Row:
		return o, nil
	}
	s.state.Lock()
	transform := s.transform
	type namedIndex struct {
		name string
		fn   toolscache.IndexFunc
	}
	funcs := make([]namedIndex, 0, len(s.indexFuncs))
	for name, fn := range s.indexFuncs {
		funcs = append(funcs, namedIndex{name, fn})
	}
	s.state.Unlock()

	if transform != nil {
		var err error
		if obj, err = transform(obj); err != nil {
			return nil, err
		}
	}
	o, ok := obj.(runtime.Object)
	if !ok {
		return nil, fmt.Errorf("rowcache: %s: the watch delivered a %T", s.codec.kind, obj)
	}
	m, err := meta.Accessor(o)
	if err != nil {
		return nil, fmt.Errorf("rowcache: %s: %w", s.codec.kind, err)
	}
	body, dict, size, err := s.codec.encode(o)
	if err != nil {
		return nil, err
	}
	r := &Row{
		namespace: m.GetNamespace(), name: m.GetName(), k: storeKey(m.GetNamespace(), m.GetName()),
		labels: m.GetLabels(), body: body, dict: dict, size: size,
	}
	if len(funcs) > 0 {
		r.index = make([]indexed, 0, len(funcs))
		for _, f := range funcs {
			vals, err := f.fn(o)
			if err != nil {
				return nil, fmt.Errorf("rowcache: %s: index %s of %s: %w", s.codec.kind, f.name, r.key(), err)
			}
			// An index value is shared by every row that carries it and by
			// the store's index map, which keys on the string the adapter
			// hands it: interned, they are one string (2026-11-03: 22 MB of
			// namespaced index keys, a copy per row).
			for i := range vals {
				vals[i] = unique.Make(vals[i]).Value()
			}
			r.index = append(r.index, indexed{name: f.name, values: vals})
		}
	}
	return r, nil
}

// RunWithContext implements SharedInformer.
func (s *Informer) RunWithContext(ctx context.Context) {
	s.state.Lock()
	if s.started {
		s.state.Unlock()
		return
	}
	s.started = true
	s.ctx = ctx
	s.log = logging.FromContext(ctx).With("informer", s.codec.kind)
	if s.wire != "" {
		wire := s.wire
		if !cborAllowed() {
			wire = runtime.ContentTypeJSON + " (ClientsAllowCBOR is off)"
		}
		s.log.Info("rowcache: the kind lists and watches over its own stream", "contentType", wire)
	}
	fifo := toolscache.NewDeltaFIFOWithOptions(toolscache.DeltaFIFOOptions{
		KeyFunction:           rowKey,
		KnownObjects:          s.indexer,
		EmitDeltaTypeReplaced: true,
		Transformer:           s.rowify,
	})
	// No ObjectType: the reflector would check every watch event's type
	// against it, and the watch stream carries rowEvents.
	s.controller = toolscache.New(&toolscache.Config{
		Queue:                        fifo,
		ListerWatcher:                s.lw,
		ObjectDescription:            s.codec.kind,
		FullResyncPeriod:             s.resync,
		ShouldResync:                 s.shouldResync,
		Process:                      s.process,
		WatchErrorHandlerWithContext: s.watchErr,
	})
	ctrl := s.controller
	for l := range s.listeners {
		l.start(ctx)
	}
	dictionary := s.dictionaryOnSync
	s.state.Unlock()

	go s.watchSynced(ctx)
	if dictionary {
		go s.buildDictionaryWhenSynced(ctx)
	}
	defer func() {
		s.state.Lock()
		s.stopped = true
		s.state.Unlock()
	}()
	ctrl.RunWithContext(ctx)
}

// Run implements SharedInformer.
func (s *Informer) Run(stopCh <-chan struct{}) { s.RunWithContext(wait.ContextForChannel(stopCh)) }

// watchSynced marks the informer synced once the controller reports its
// initial list processed. The FIFO reports it only once the last initial
// delta's process has returned (its lock is held through the call), so the
// marker always follows the initial notifications; process marks it
// earlier when a later delta arrives first.
func (s *Informer) watchSynced(ctx context.Context) {
	_ = wait.PollUntilContextCancel(ctx, 50*time.Millisecond, true, func(context.Context) (bool, error) {
		if !s.controller.HasSynced() {
			return false, nil
		}
		s.mu.Lock()
		s.markSyncedLocked()
		s.mu.Unlock()
		return true, nil
	})
}

func (s *Informer) markSyncedLocked() {
	s.syncedOnce.Do(func() {
		close(s.synced)
		for _, l := range s.listenerSnapshot() {
			l.add(notification{op: opSynced})
		}
	})
}

func (s *Informer) isSynced() bool {
	select {
	case <-s.synced:
		return true
	default:
		return false
	}
}

func (s *Informer) isStarted() bool {
	s.state.Lock()
	defer s.state.Unlock()
	return s.started
}

func (s *Informer) listenerSnapshot() []*listener {
	s.state.Lock()
	defer s.state.Unlock()
	out := make([]*listener, 0, len(s.listeners))
	for l := range s.listeners {
		out = append(out, l)
	}
	return out
}

func (s *Informer) shouldResync() bool {
	s.state.Lock()
	defer s.state.Unlock()
	return s.resync > 0 && len(s.listeners) > 0
}

// process applies one object's deltas to the store and tells the
// listeners, as client-go's processDeltas does for a shared informer.
func (s *Informer) process(obj any, isInInitialList bool) error {
	deltas, ok := obj.(toolscache.Deltas)
	if !ok {
		return fmt.Errorf("rowcache: %s: the queue handed a %T, not deltas", s.codec.kind, obj)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// The FIFO holds its own lock while process runs, and its HasSynced
	// takes that lock, so the sync is read from the flag it passes: the
	// first delta past the initial list means the list is in. The poller
	// in watchSynced covers a list nothing follows.
	if !isInInitialList {
		s.markSyncedLocked()
	}
	for _, d := range deltas {
		switch d.Type {
		case toolscache.Sync:
			// A resync carries the stored row itself: nothing to write.
			r, ok := d.Object.(*Row)
			if !ok {
				return fmt.Errorf("rowcache: %s: a %s delta carried a %T, not a row", s.codec.kind, d.Type, d.Object)
			}
			if stored, exists, _ := s.indexer.Get(r); exists {
				sr := stored.(*Row)
				s.notifyLocked(notification{op: opUpdate, row: sr, old: sr})
			}
		case toolscache.Replaced, toolscache.Added, toolscache.Updated:
			r, ok := d.Object.(*Row)
			if !ok {
				return fmt.Errorf("rowcache: %s: a %s delta carried a %T, not a row", s.codec.kind, d.Type, d.Object)
			}
			old, exists, err := s.indexer.Get(r)
			if err != nil {
				return err
			}
			if exists {
				if err := s.indexer.Update(r); err != nil {
					return err
				}
				s.gen++
				prev := old.(*Row)
				s.accountLocked(prev, r)
				s.notifyLocked(notification{op: opUpdate, row: r, old: prev})
			} else {
				if err := s.indexer.Add(r); err != nil {
					return err
				}
				s.gen++
				s.accountLocked(nil, r)
				s.notifyLocked(notification{op: opAdd, row: r, initial: isInInitialList})
			}
		case toolscache.Deleted:
			switch o := d.Object.(type) {
			case *Row:
				prev, _, _ := s.indexer.GetByKey(o.key())
				if err := s.indexer.Delete(o); err != nil {
					return err
				}
				s.gen++
				if pr, ok := prev.(*Row); ok {
					s.accountLocked(pr, nil)
				}
				s.notifyLocked(notification{op: opDelete, row: o})
			case toolscache.DeletedFinalStateUnknown:
				prev, _, _ := s.indexer.GetByKey(o.Key)
				if err := s.indexer.Delete(o); err != nil {
					return err
				}
				s.gen++
				if pr, ok := prev.(*Row); ok {
					s.accountLocked(pr, nil)
				}
				last, _ := o.Obj.(*Row)
				s.notifyLocked(notification{op: opDelete, row: last, tombstone: true, key: o.Key})
			default:
				return fmt.Errorf("rowcache: %s: a Deleted delta carried a %T", s.codec.kind, d.Object)
			}
		case toolscache.Bookmark:
			info, ok := d.Object.(toolscache.BookmarkInfo)
			if !ok {
				return fmt.Errorf("rowcache: %s: a Bookmark delta carried a %T", s.codec.kind, d.Object)
			}
			s.indexer.Bookmark(info.ResourceVersion)
		default:
			return fmt.Errorf("rowcache: %s: unexpected delta type %s", s.codec.kind, d.Type)
		}
	}
	return nil
}

func (s *Informer) accountLocked(old, cur *Row) {
	if old != nil {
		s.rows--
		s.bytes -= int64(len(old.body))
		s.objectBytes -= int64(old.size)
	}
	if cur != nil {
		s.rows++
		s.bytes += int64(len(cur.body))
		s.objectBytes += int64(cur.size)
	}
	kind := s.codec.kind
	metrics.RowCacheRows.WithLabelValues(kind).Set(float64(s.rows))
	metrics.RowCacheBytes.WithLabelValues(kind).Set(float64(s.bytes))
	metrics.RowCacheObjectBytes.WithLabelValues(kind).Set(float64(s.objectBytes))
}

func (s *Informer) notifyLocked(n notification) {
	n = withCells(n)
	for _, l := range s.listenerSnapshot() {
		l.add(n)
	}
}

// buildDictionaryWhenSynced builds the kind's dictionary from the initial
// list and compresses every row again with it.
func (s *Informer) buildDictionaryWhenSynced(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-s.synced:
	}
	if err := s.buildDictionary(ctx); err != nil && !errors.Is(err, context.Canceled) {
		s.log.Warn("rowcache: the kind's rows stay without a dictionary", "err", err)
	}
}

func (s *Informer) buildDictionary(ctx context.Context) error {
	items := s.indexer.List()
	if len(items) < minDictionaryRows {
		return nil
	}
	rand.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] })
	n := min(len(items), dictionarySamples)
	samples := make([][]byte, 0, n)
	for _, item := range items[:n] {
		js, err := s.codec.decodeJSON(item.(*Row))
		if err != nil {
			return err
		}
		samples = append(samples, js)
	}
	s.codec.decodes[reasonSample].Add(float64(n))
	items = nil
	if err := s.codec.buildDictionary(samples); err != nil {
		return err
	}
	samples = nil

	// The sweep: each row compressed before the dictionary is compressed
	// again with it, one at a time under the writer's lock, so neither the
	// watch stream nor a reader waits for more than one row. It waits for
	// the listeners' replays to drain first: a queued notification holds
	// its row, and sweeping under a replay kept the old rows alive beside
	// the new ones (2026-11-03: a second copy of every row at the start-up
	// peak).
	s.awaitDrained(ctx, sweepDrainWait)
	started := time.Now()
	var before, after int64
	for _, key := range s.indexer.ListKeys() {
		if err := ctx.Err(); err != nil {
			return err
		}
		s.mu.Lock()
		item, ok, _ := s.indexer.GetByKey(key)
		if r, isRow := item.(*Row); ok && isRow && r.dict == 0 {
			nr, err := s.codec.recompress(r)
			if err != nil {
				s.log.Warn("rowcache: a row stays compressed without the dictionary", "key", key, "err", err)
			} else if err := s.indexer.Update(nr); err != nil {
				s.log.Warn("rowcache: a row stays compressed without the dictionary", "key", key, "err", err)
			} else {
				s.gen++
				before += int64(len(r.body))
				after += int64(len(nr.body))
				s.accountLocked(r, nr)
			}
		}
		s.mu.Unlock()
	}
	s.log.Info("rowcache: rows compressed with the kind's dictionary",
		"dictionaryBytes", len(s.codec.dict), "bytesBefore", before, "bytesAfter", after, "took", time.Since(started).Round(time.Millisecond))
	return nil
}

// sweepDrainWait is the longest the dictionary sweep waits for the
// listeners' queues to drain.
const sweepDrainWait = 2 * time.Minute

// awaitDrained waits until every listener's queue is empty, or for at most
// limit.
func (s *Informer) awaitDrained(ctx context.Context, limit time.Duration) {
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		busy := false
		for _, l := range s.listenerSnapshot() {
			if l.pending() > 0 {
				busy = true
				break
			}
		}
		if !busy {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// AddEventHandler implements SharedInformer.
func (s *Informer) AddEventHandler(handler toolscache.ResourceEventHandler) (toolscache.ResourceEventHandlerRegistration, error) {
	return s.AddEventHandlerWithOptions(handler, toolscache.HandlerOptions{})
}

// AddEventHandlerWithResyncPeriod implements SharedInformer. A handler's
// own resync period is not honoured: every handler resyncs at the
// informer's period, if it has one.
func (s *Informer) AddEventHandlerWithResyncPeriod(handler toolscache.ResourceEventHandler, resyncPeriod time.Duration) (toolscache.ResourceEventHandlerRegistration, error) {
	return s.AddEventHandlerWithOptions(handler, toolscache.HandlerOptions{ResyncPeriod: &resyncPeriod})
}

// AddEventHandlerWithOptions implements SharedInformer. A handler added
// after the informer started is told of every object in the store as an
// add in the initial list, then of the sync, before anything newer, as a
// shared informer does.
func (s *Informer) AddEventHandlerWithOptions(handler toolscache.ResourceEventHandler, _ toolscache.HandlerOptions) (toolscache.ResourceEventHandlerRegistration, error) {
	l := newListener(s, handler)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Lock()
	if s.stopped {
		s.state.Unlock()
		return nil, fmt.Errorf("rowcache: %s: the informer has stopped", s.codec.kind)
	}
	s.listeners[l] = struct{}{}
	started, ctx := s.started, s.ctx
	s.state.Unlock()
	if !started {
		return l, nil
	}
	// The store as of now, delivered through a replay shared with the
	// handlers registering alongside; the sync marker follows it.
	rep := s.replay
	if rep == nil || !rep.join(l, s.gen) {
		rep = s.newReplay(l, s.isSynced())
		s.replay = rep
	}
	l.rep = rep
	l.start(ctx)
	return l, nil
}

// RemoveEventHandler implements SharedInformer.
func (s *Informer) RemoveEventHandler(handle toolscache.ResourceEventHandlerRegistration) error {
	l, ok := handle.(*listener)
	if !ok || l.inf != s {
		return fmt.Errorf("rowcache: %s: the registration is not this informer's", s.codec.kind)
	}
	s.state.Lock()
	delete(s.listeners, l)
	s.state.Unlock()
	l.end()
	// Its replay's other members wait for it no longer, whatever its
	// handler is doing.
	if rep := l.replay(); rep != nil {
		rep.leave(l)
	}
	return nil
}

// GetStore implements SharedInformer: the store of rows. Its items are
// *Row, never the kind.
func (s *Informer) GetStore() toolscache.Store { return s.indexer }

// GetIndexer implements SharedIndexInformer: the store of rows.
func (s *Informer) GetIndexer() toolscache.Indexer { return s.indexer }

// GetController implements SharedInformer; nil before Run.
func (s *Informer) GetController() toolscache.Controller {
	s.state.Lock()
	defer s.state.Unlock()
	return s.controller
}

// HasSynced implements SharedInformer.
func (s *Informer) HasSynced() bool { return s.isSynced() }

// HasSyncedChecker implements SharedInformer.
func (s *Informer) HasSyncedChecker() toolscache.DoneChecker {
	return doneChecker{name: s.codec.kind + " row informer", done: s.synced}
}

// LastSyncResourceVersion implements SharedInformer.
func (s *Informer) LastSyncResourceVersion() string {
	c := s.GetController()
	if c == nil {
		return ""
	}
	return c.LastSyncResourceVersion()
}

// SetWatchErrorHandler implements SharedInformer.
func (s *Informer) SetWatchErrorHandler(handler toolscache.WatchErrorHandler) error {
	return s.SetWatchErrorHandlerWithContext(func(_ context.Context, r *toolscache.Reflector, err error) { handler(r, err) })
}

// SetWatchErrorHandlerWithContext implements SharedInformer.
func (s *Informer) SetWatchErrorHandlerWithContext(handler toolscache.WatchErrorHandlerWithContext) error {
	s.state.Lock()
	defer s.state.Unlock()
	if s.started {
		return fmt.Errorf("rowcache: %s: the informer has already started", s.codec.kind)
	}
	s.watchErr = handler
	return nil
}

// SetTransform implements SharedInformer: the transform runs on each
// object before it becomes a row.
func (s *Informer) SetTransform(handler toolscache.TransformFunc) error {
	s.state.Lock()
	defer s.state.Unlock()
	if s.started {
		return fmt.Errorf("rowcache: %s: the informer has already started", s.codec.kind)
	}
	s.transform = handler
	return nil
}

// IsStopped implements SharedInformer.
func (s *Informer) IsStopped() bool {
	s.state.Lock()
	defer s.state.Unlock()
	return s.stopped
}

// AddIndexers implements SharedIndexInformer. The functions take the
// kind's objects, as controller-runtime registers them; rows stored
// before the index existed are decoded once to index them.
func (s *Informer) AddIndexers(indexers toolscache.Indexers) error {
	s.state.Lock()
	defer s.state.Unlock()
	adapters := make(toolscache.Indexers, len(indexers))
	for name, fn := range indexers {
		if _, dup := s.indexFuncs[name]; dup {
			return fmt.Errorf("rowcache: %s: indexer conflict: %s", s.codec.kind, name)
		}
		adapters[name] = s.adapter(name, fn)
	}
	if err := s.indexer.AddIndexers(adapters); err != nil {
		return err
	}
	maps.Copy(s.indexFuncs, indexers)
	return nil
}

type doneChecker struct {
	name string
	done <-chan struct{}
}

func (d doneChecker) Name() string          { return d.name }
func (d doneChecker) Done() <-chan struct{} { return d.done }

type op uint8

const (
	opAdd op = iota + 1
	opUpdate
	opDelete
	opSynced
)

// notification is one event for a listener: rows, decoded once by the
// first listener to dispatch the event and shared with the rest.
type notification struct {
	op       op
	row, old *Row
	// obj and prev are the decoded row and old, shared by every listener
	// the event went to; prev is obj when old is row.
	obj, prev *decoded
	initial   bool
	// tombstone marks a DeletedFinalStateUnknown of key; row is its last
	// known state, or nil.
	tombstone bool
	key       string
}

// withCells gives a notification its decode cells.
func withCells(n notification) notification {
	if n.row != nil {
		n.obj = &decoded{}
	}
	switch {
	case n.old == nil:
	case n.old == n.row:
		n.prev = n.obj
	default:
		n.prev = &decoded{}
	}
	return n
}

// decoded is one event's object: decoded by the first listener that
// dispatches the event, handed to every listener by the same pointer, as a
// shared informer hands one object to all its handlers. It lives until the
// last listener has dispatched the event.
type decoded struct {
	once sync.Once
	obj  runtime.Object
	err  error
}

func (d *decoded) get(c *codec, r *Row) (runtime.Object, error) {
	d.once.Do(func() { d.obj, d.err = c.decode(r, reasonEvent) })
	return d.obj, d.err
}

// listener is one registered handler with its unbounded queue, drained by
// its own goroutine so a slow handler never holds the writer.
type listener struct {
	inf     *Informer
	handler toolscache.ResourceEventHandler

	mu    sync.Mutex
	queue []notification
	wake  chan struct{}
	// rep is the store's replay this handler reads first, when it
	// registered on a started informer; nil once read.
	rep *replay

	synced     chan struct{}
	syncedOnce sync.Once
	stop       chan struct{}
	stopOnce   sync.Once
}

var _ toolscache.ResourceEventHandlerRegistration = (*listener)(nil)

func newListener(inf *Informer, handler toolscache.ResourceEventHandler) *listener {
	return &listener{inf: inf, handler: handler, wake: make(chan struct{}, 1), synced: make(chan struct{}), stop: make(chan struct{})}
}

func (l *listener) add(n notification) {
	l.mu.Lock()
	l.queue = append(l.queue, n)
	l.mu.Unlock()
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

func (l *listener) start(ctx context.Context) { go l.run(ctx) }

// pending is how many notifications wait: the replay's rows not yet
// dispatched, then the queue's.
func (l *listener) pending() int {
	l.mu.Lock()
	n, rep := len(l.queue), l.rep
	l.mu.Unlock()
	if rep != nil {
		n += rep.remaining(l)
	}
	return n
}

// replay is the replay l is reading, or nil.
func (l *listener) replay() *replay {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rep
}

func (l *listener) end() { l.stopOnce.Do(func() { close(l.stop) }) }

func (l *listener) run(ctx context.Context) {
	// The replay first, in the order every member reads it; the sync
	// marker once it is read, when the informer had synced by its opening;
	// then the queue, which holds what arrived since the registration.
	if rep := l.replay(); rep != nil {
		defer rep.leave(l)
		for {
			row, cell, status := rep.next(ctx, l)
			if status == replayStopped {
				return
			}
			if status == replayDone {
				break
			}
			l.dispatch(notification{op: opAdd, row: row, obj: cell, initial: true})
			rep.advance(l)
		}
		rep.leave(l)
		l.mu.Lock()
		l.rep = nil
		l.mu.Unlock()
		if rep.synced {
			l.syncedOnce.Do(func() { close(l.synced) })
		}
	}
	for {
		l.mu.Lock()
		if len(l.queue) == 0 {
			l.mu.Unlock()
			select {
			case <-l.wake:
				continue
			case <-l.stop:
				return
			case <-ctx.Done():
				return
			}
		}
		n := l.queue[0]
		l.queue[0] = notification{}
		l.queue = l.queue[1:]
		if len(l.queue) == 0 {
			l.queue = nil
		}
		l.mu.Unlock()
		select {
		case <-l.stop:
			return
		default:
		}
		l.dispatch(n)
	}
}

func (l *listener) dispatch(n notification) {
	c := l.inf.codec
	switch n.op {
	case opSynced:
		l.syncedOnce.Do(func() { close(l.synced) })
	case opAdd:
		obj, err := n.obj.get(c, n.row)
		if err != nil {
			l.inf.log.Error("rowcache: an add was not delivered", "key", n.row.key(), "err", err)
			return
		}
		l.handler.OnAdd(obj, n.initial)
	case opUpdate:
		obj, err := n.obj.get(c, n.row)
		if err != nil {
			l.inf.log.Error("rowcache: an update was not delivered", "key", n.row.key(), "err", err)
			return
		}
		old, err := n.prev.get(c, n.old)
		if err != nil {
			l.inf.log.Error("rowcache: an update was not delivered", "key", n.row.key(), "err", err)
			return
		}
		l.handler.OnUpdate(old, obj)
	case opDelete:
		var obj any
		if n.row != nil {
			o, err := n.obj.get(c, n.row)
			if err != nil {
				l.inf.log.Error("rowcache: a delete was not delivered", "key", n.row.key(), "err", err)
				return
			}
			obj = o
		}
		if n.tombstone {
			l.handler.OnDelete(toolscache.DeletedFinalStateUnknown{Key: n.key, Obj: obj})
			return
		}
		l.handler.OnDelete(obj)
	}
}

// HasSynced implements ResourceEventHandlerRegistration: the informer has
// synced and this handler has been told of every object of the initial
// list.
func (l *listener) HasSynced() bool {
	select {
	case <-l.synced:
		return true
	default:
		return false
	}
}

// HasSyncedChecker implements ResourceEventHandlerRegistration.
func (l *listener) HasSyncedChecker() toolscache.DoneChecker {
	return doneChecker{name: l.inf.codec.kind + " row informer handler", done: l.synced}
}
