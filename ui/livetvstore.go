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
	"io"
	"slices"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/iptv/livetvstore"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// liveTVStore reads the tuner's objects in the clustarr-livetv store for
// the Live TV pages (the direct-tuners and ui design §5.1): a provider's
// playlist, its guide's channels and its guide index. An object's digest
// is checked at most every recheck, and it is decoded again only when the
// digest moved, so the owner's 16 MB playlist is parsed once per change,
// never per request. A read that fails keeps the last good view. Reads
// only: Info and Get (TestUINeverWrites).
type liveTVStore struct {
	store   events.ObjectStore
	now     func() time.Time
	recheck time.Duration

	mu      sync.Mutex
	objects map[string]*storedObject
}

// storedObject is one object's last good view.
type storedObject struct {
	mu      sync.Mutex
	digest  string
	checked time.Time
	view    any
}

// liveTVRecheck is how often an object's digest is checked;
// liveTVReadTimeout bounds one read, the playlist's 16 MB among them.
const (
	liveTVRecheck     = 5 * time.Second
	liveTVReadTimeout = 30 * time.Second
)

// newLiveTVStore reads s; a nil s answers every read false.
func newLiveTVStore(s events.ObjectStore) *liveTVStore {
	return &liveTVStore{store: s, now: time.Now, recheck: liveTVRecheck, objects: map[string]*storedObject{}}
}

// read is name's view, decoded by decode when its digest moved.
func (l *liveTVStore) read(ctx context.Context, name string, decode func(io.Reader) (any, error)) (any, bool) {
	if l == nil || l.store == nil {
		return nil, false
	}
	l.mu.Lock()
	o := l.objects[name]
	if o == nil {
		o = &storedObject{}
		l.objects[name] = o
	}
	l.mu.Unlock()

	o.mu.Lock()
	defer o.mu.Unlock()
	now := l.now()
	if o.view != nil && now.Sub(o.checked) < l.recheck {
		return o.view, true
	}
	ctx, cancel := context.WithTimeout(ctx, liveTVReadTimeout)
	defer cancel()
	info, err := l.store.Info(ctx, name)
	if err != nil {
		o.checked = now
		return o.view, o.view != nil
	}
	if o.view != nil && info.Digest == o.digest {
		o.checked = now
		return o.view, true
	}
	_, rc, err := l.store.Get(ctx, name)
	if err != nil {
		logging.FromContext(ctx).Warn("live tv: read a store object; keeping the last", "object", name, "error", err)
		o.checked = now
		return o.view, o.view != nil
	}
	defer func() { _ = rc.Close() }()
	v, err := decode(rc)
	if err != nil {
		logging.FromContext(ctx).Warn("live tv: decode a store object; keeping the last", "object", name, "error", err)
		o.checked = now
		return o.view, o.view != nil
	}
	o.view, o.digest, o.checked = v, info.Digest, now
	return v, true
}

// playlistView is a provider's playlist, its stream URLs dropped.
type playlistView struct {
	Entries []livetvstore.PlaylistEntry
	ByKey   map[string]int
	Groups  []string
}

// Playlist is key's playlist; false before the tuner stored one.
func (l *liveTVStore) Playlist(ctx context.Context, key types.NamespacedName) (*playlistView, bool) {
	v, ok := l.read(ctx, livetvstore.PlaylistObject(key), func(r io.Reader) (any, error) {
		var pl livetvstore.Playlist
		if err := livetvstore.Decode(r, &pl); err != nil {
			return nil, err
		}
		pv := &playlistView{Entries: pl.Entries, ByKey: make(map[string]int, len(pl.Entries))}
		groups := map[string]bool{}
		for i := range pv.Entries {
			// The templated stream URL is the provider's: no page shows it.
			pv.Entries[i].URL, pv.Entries[i].UserAgent = "", ""
			pv.ByKey[pv.Entries[i].Key] = i
			if g := pv.Entries[i].Group; g != "" && !groups[g] {
				groups[g] = true
				pv.Groups = append(pv.Groups, g)
			}
		}
		slices.Sort(pv.Groups)
		return pv, nil
	})
	if !ok {
		return nil, false
	}
	return v.(*playlistView), true
}

// guideChannelsView is a provider's guide's channels, for the EPG picker.
type guideChannelsView struct {
	Channels []livetvstore.GuideChannel
	ByID     map[string]int
}

// GuideChannels is key's guide channels; false before the tuner stored
// them.
func (l *liveTVStore) GuideChannels(ctx context.Context, key types.NamespacedName) (*guideChannelsView, bool) {
	v, ok := l.read(ctx, livetvstore.GuideChannelsObject(key), func(r io.Reader) (any, error) {
		var cs []livetvstore.GuideChannel
		if err := livetvstore.Decode(r, &cs); err != nil {
			return nil, err
		}
		gv := &guideChannelsView{Channels: cs, ByID: make(map[string]int, len(cs))}
		for i, c := range cs {
			gv.ByID[c.ID] = i
		}
		return gv, nil
	})
	if !ok {
		return nil, false
	}
	return v.(*guideChannelsView), true
}

// guideView is a provider's guide index, its programmes by channel.
type guideView struct {
	Index     livetvstore.GuideIndex
	ByChannel map[string][]livetvstore.IndexProgramme
	Logos     map[string]string
}

// GuideIndex is key's guide index; false before the tuner stored one.
func (l *liveTVStore) GuideIndex(ctx context.Context, key types.NamespacedName) (*guideView, bool) {
	v, ok := l.read(ctx, livetvstore.GuideIndexObject(key), func(r io.Reader) (any, error) {
		var ix livetvstore.GuideIndex
		if err := livetvstore.Decode(r, &ix); err != nil {
			return nil, err
		}
		gv := &guideView{Index: ix, ByChannel: map[string][]livetvstore.IndexProgramme{}, Logos: map[string]string{}}
		for _, p := range ix.Programmes {
			gv.ByChannel[p.Channel] = append(gv.ByChannel[p.Channel], p)
		}
		for _, c := range ix.Channels {
			if c.Logo != "" {
				gv.Logos[c.Number] = c.Logo
			}
		}
		return gv, nil
	})
	if !ok {
		return nil, false
	}
	return v.(*guideView), true
}
