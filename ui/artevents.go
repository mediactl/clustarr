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

package ui

import (
	"context"
	"strings"
	"sync"

	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/objindex"
)

// artEvent is one SSE art event (artwork design §B.8 as amended 2026-10-07,
// step 4; owner decision (b)): the image key "<kind>/<uid>/<type>" now
// serves digest V, so ui/static/art.js points every img[data-art=Key] at
// /art/<Key>?v=<V>.
type artEvent struct {
	Key string `json:"key"`
	V   string `json:"v"`
}

// artSubscriberBuffer is each SSE connection's art buffer. A full one loses
// events; the next projection tick re-renders the same URL anyway.
const artSubscriberBuffer = 64

// artEvents maps the artwork index's digest changes to the digest /art
// serves for each image -- the overlay if present, else the original -- and
// fans an event out to every open stream only when that served digest
// changes. An original re-Put under an existing overlay serves nothing new
// and emits nothing until the renderer Puts the overlay. It reads the index
// and writes nothing; its fan-out is broadcast, never Publish
// (TestUINeverWrites).
type artEvents struct {
	index *objindex.Index

	mu sync.Mutex
	// last is the served digest last emitted per key.
	last map[string]string
	subs map[chan artEvent]struct{}
}

func newArtEvents(index *objindex.Index) *artEvents {
	return &artEvents{index: index, last: map[string]string{}, subs: map[chan artEvent]struct{}{}}
}

// run follows the index's changes until ctx ends.
func (a *artEvents) run(ctx context.Context) {
	ch, cancel := a.index.Subscribe()
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case c, ok := <-ch:
			if !ok {
				return
			}
			a.changed(c)
		}
	}
}

// changed recomputes the served digest of the image c's object belongs to
// and broadcasts it if it moved.
func (a *artEvents) changed(c objindex.Change) {
	kind, uid, t, variant, ok := parseArtName(c.Name)
	if !ok {
		return
	}
	key := string(kind) + "/" + string(uid) + "/" + string(t)
	overlayEntry, hasOverlay := objindex.Entry{}, false
	if overlaid(kind, t) {
		overlayEntry, hasOverlay = a.index.Lookup(events.ArtworkKey(kind, uid, string(t), events.ArtworkVariantOverlay))
	}
	original, hasOriginal := a.index.Lookup(events.ArtworkKey(kind, uid, string(t), events.ArtworkVariantOriginal))
	served := ""
	switch {
	case hasOverlay:
		served = overlayEntry.Digest
	case hasOriginal:
		served = original.Digest
	}

	a.mu.Lock()
	before, known := a.last[key]
	if !known {
		// What the image served before this change, read off the change.
		switch {
		case variant == events.ArtworkVariantOriginal && hasOverlay:
			before = overlayEntry.Digest // the overlay hid the original, and still does
		case c.OldDigest != "":
			before = c.OldDigest
		case variant == events.ArtworkVariantOverlay && hasOriginal:
			before = original.Digest // a new overlay replaces the original
		}
	}
	if served == "" {
		delete(a.last, key)
		a.mu.Unlock()
		return // nothing to point an img at: the next tick renders the placeholder
	}
	a.last[key] = served
	a.mu.Unlock()
	if served != before {
		a.broadcast(artEvent{Key: key, V: served})
	}
}

// parseArtName reads an ArtworkKey back: "<kind>/<uid>/<type>/<variant>".
func parseArtName(name string) (commonv1.MediaKind, types.UID, catalogv1.ImageType, string, bool) {
	parts := strings.Split(name, "/")
	if len(parts) != 4 || parts[1] == "" {
		return "", "", "", "", false
	}
	kind, t, variant := commonv1.MediaKind(parts[0]), catalogv1.ImageType(parts[2]), parts[3]
	if !validArtKinds[kind] || !validArtTypes[t] ||
		(variant != events.ArtworkVariantOriginal && variant != events.ArtworkVariantOverlay) {
		return "", "", "", "", false
	}
	return kind, types.UID(parts[1]), t, variant, true
}

// parseArtKey validates an image key, "<kind>/<uid>/<type>", as /art
// validates its path.
func parseArtKey(key string) bool {
	parts := strings.Split(key, "/")
	return len(parts) == 3 && validArtKinds[commonv1.MediaKind(parts[0])] && parts[1] != "" &&
		validArtTypes[catalogv1.ImageType(parts[2])]
}

// itemOfArtKey is an image key's "<kind>/<uid>", the item it shows.
func itemOfArtKey(key string) string {
	if i := strings.LastIndex(key, "/"); i >= 0 {
		return key[:i]
	}
	return key
}

// subscribe registers a stream and returns its channel and a cancel func.
// A nil artEvents (no artwork store) returns a channel that never delivers.
func (a *artEvents) subscribe() (<-chan artEvent, func()) {
	if a == nil {
		return nil, func() {}
	}
	ch := make(chan artEvent, artSubscriberBuffer)
	a.mu.Lock()
	a.subs[ch] = struct{}{}
	a.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			a.mu.Lock()
			delete(a.subs, ch)
			a.mu.Unlock()
		})
	}
}

// broadcast hands e to every stream without blocking.
func (a *artEvents) broadcast(e artEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for ch := range a.subs {
		select {
		case ch <- e:
		default:
		}
	}
}
