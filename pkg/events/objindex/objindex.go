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

// Package objindex is a read-only, in-memory index of one object-store
// bucket, kept current by a single ObjectStore.Watch (artwork design §B.8 as
// amended 2026-10-07, "The index"). The ui serves /art from it: a lookup, a
// synced flag that says when a miss may be trusted, and a stream of digest
// changes for the SSE art event.
//
// It reopens its watch when the channel closes (after a backoff of 1 s
// doubling to 30 s, reset by a successful replay) and when the bucket's
// creation time changes, which is how it notices a bucket deleted and
// created again under a running watch: nats.go's ordered consumer skips
// such a bucket silently (research E9). A reopen builds the new map aside
// and swaps it in at the replay's end, so lookups keep answering from the
// old map meanwhile.
//
// It costs about 250 bytes per object: some 2.5 MB at 10,000 objects.
//
// It writes nothing and imports only pkg/events (and logging), so ui/ may
// import it.
package objindex

import (
	"context"
	"errors"
	"maps"
	"sync"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// Entry is what the index knows of one live object.
type Entry struct {
	Digest      string
	Size        int64
	ContentType string // the Content-Type header
	ModTime     time.Time
	// Metadata is the object's metadata map. It is shared with the index:
	// read it, never modify it.
	Metadata map[string]string
}

// Change is one name whose digest changed; NewDigest is "" on a delete and
// OldDigest "" for a name the index did not hold.
type Change struct{ Name, OldDigest, NewDigest string }

// Option configures an Index.
type Option func(*Index)

// WithCheckEvery sets how often the index compares the bucket's creation
// time; 60 s by default.
func WithCheckEvery(d time.Duration) Option {
	return func(x *Index) {
		if d > 0 {
			x.checkEvery = d
		}
	}
}

const (
	defaultCheckEvery = 60 * time.Second
	minBackoff        = time.Second
	maxBackoff        = 30 * time.Second
	// subscriberBuffer is each subscriber's channel size. A full subscriber
	// loses changes rather than block the index.
	subscriberBuffer = 256
)

// Index is a read-only index of one bucket. Build it with New and run it
// with Run.
type Index struct {
	store      events.ObjectStore
	checkEvery time.Duration

	mu      sync.RWMutex
	entries map[string]Entry
	synced  bool

	subMu sync.Mutex
	subs  map[chan Change]struct{}
}

// New returns an index of store. It holds nothing until Run's first replay
// ends.
func New(store events.ObjectStore, opts ...Option) *Index {
	x := &Index{
		store:      store,
		checkEvery: defaultCheckEvery,
		entries:    map[string]Entry{},
		subs:       map[chan Change]struct{}{},
	}
	for _, o := range opts {
		o(x)
	}
	return x
}

// Lookup returns name's entry, if the index holds it. A miss is
// authoritative only while Synced is true.
func (x *Index) Lookup(name string) (Entry, bool) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	e, ok := x.entries[name]
	return e, ok
}

// Synced reports whether the index has finished a replay and its watch is
// open: true from the replay's end marker until a reopen.
func (x *Index) Synced() bool {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.synced
}

// Subscribe returns a buffered channel of changes and a func that cancels
// the subscription and closes the channel. A full subscriber loses changes
// rather than block the index.
func (x *Index) Subscribe() (<-chan Change, func()) {
	ch := make(chan Change, subscriberBuffer)
	x.subMu.Lock()
	x.subs[ch] = struct{}{}
	x.subMu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			x.subMu.Lock()
			delete(x.subs, ch)
			x.subMu.Unlock()
			close(ch)
		})
	}
}

// Run keeps the index until ctx ends, then returns nil.
func (x *Index) Run(ctx context.Context) error {
	log := logging.FromContext(ctx).With("component", "objindex")
	backoff := minBackoff
	for {
		replayed, recreated, err := x.session(ctx)
		if ctx.Err() != nil {
			return nil
		}
		x.setUnsynced()
		if replayed {
			backoff = minBackoff
		}
		if recreated {
			log.Info("object store re-created under the watch; reopening")
			continue // the new bucket's replay replaces the map at once
		}
		log.Warn("object watch closed; reopening", "after", backoff, "err", err)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// session runs one watch: the replay into a fresh map, the swap, then live
// changes until the channel closes, the bucket's creation time changes, or
// ctx ends. replayed reports that the replay reached its end marker.
func (x *Index) session(ctx context.Context) (replayed, recreated bool, err error) {
	st, err := x.store.Status(ctx)
	if err != nil {
		return false, false, err
	}
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, err := x.store.Watch(wctx)
	if err != nil {
		return false, false, err
	}
	check := time.NewTicker(x.checkEvery)
	defer check.Stop()

	fresh := map[string]Entry{}
	for {
		select {
		case <-ctx.Done():
			return replayed, false, ctx.Err()
		case <-check.C:
			now, err := x.store.Status(ctx)
			if err != nil {
				continue // a blip: the open watch still serves
			}
			if !now.Created.Equal(st.Created) {
				return replayed, true, nil
			}
		case e, ok := <-ch:
			if !ok {
				return replayed, false, errWatchClosed
			}
			if !replayed {
				if e.Synced {
					x.swap(fresh)
					fresh = nil
					replayed = true
					continue
				}
				if e.Deleted {
					delete(fresh, e.Info.Name)
				} else {
					fresh[e.Info.Name] = entryOf(e.Info)
				}
				continue
			}
			x.apply(e)
		}
	}
}

// errWatchClosed is a watch channel that closed while ctx was live.
var errWatchClosed = errors.New("objindex: the watch channel closed")

func entryOf(info events.ObjectInfo) Entry {
	return Entry{
		Digest:      info.Digest,
		Size:        info.Size,
		ContentType: info.Headers[events.HeaderContentType],
		ModTime:     info.ModTime,
		Metadata:    maps.Clone(info.Metadata),
	}
}

// swap installs a replay's map, marks the index synced and emits a change
// for every name whose digest differs between the old map and the new.
func (x *Index) swap(fresh map[string]Entry) {
	x.mu.Lock()
	old := x.entries
	x.entries = fresh
	x.synced = true
	x.mu.Unlock()
	var changes []Change
	for name, e := range fresh {
		if o, ok := old[name]; !ok || o.Digest != e.Digest {
			changes = append(changes, Change{Name: name, OldDigest: o.Digest, NewDigest: e.Digest})
		}
	}
	for name, o := range old {
		if _, ok := fresh[name]; !ok {
			changes = append(changes, Change{Name: name, OldDigest: o.Digest})
		}
	}
	for _, c := range changes {
		x.emit(c)
	}
}

// apply folds one live event into the map, emitting a change when a digest
// moved. A SetMeta (same digest) updates the entry and emits nothing.
func (x *Index) apply(e events.ObjectEvent) {
	if e.Synced {
		return
	}
	name := e.Info.Name
	x.mu.Lock()
	old, had := x.entries[name]
	if e.Deleted {
		delete(x.entries, name)
		x.mu.Unlock()
		if had {
			x.emit(Change{Name: name, OldDigest: old.Digest})
		}
		return
	}
	n := entryOf(e.Info)
	x.entries[name] = n
	x.mu.Unlock()
	if !had || old.Digest != n.Digest {
		x.emit(Change{Name: name, OldDigest: old.Digest, NewDigest: n.Digest})
	}
}

func (x *Index) setUnsynced() {
	x.mu.Lock()
	x.synced = false
	x.mu.Unlock()
}

// emit hands c to every subscriber without blocking.
func (x *Index) emit(c Change) {
	x.subMu.Lock()
	defer x.subMu.Unlock()
	for ch := range x.subs {
		select {
		case ch <- c:
		default:
		}
	}
}
