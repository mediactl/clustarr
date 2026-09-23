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

package subtitles

import (
	"errors"
	"sync"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
)

var ErrDuplicateProvider = errors.New("subtitles: provider already registered")

// Registry holds the configured set of subtitle Providers.
//
// It has no production caller. captionarr's fetch worker used one, keyed by
// provider type, until gap fix X11b: that dedupe meant two SubtitleProvider
// objects of one type (two accounts) could never both be searched, so the
// worker now pools one entry per SubtitleProvider object itself
// (captionarr/worker/fetch). Kept for its tests; a prune candidate.
type Registry struct {
	mu     sync.RWMutex
	byName map[string]Provider
	order  []string
}

func NewRegistry() *Registry { return &Registry{byName: map[string]Provider{}} }

// Register adds p, keyed by its Name(). It returns ErrDuplicateProvider if a
// provider with the same name is already registered.
func (r *Registry) Register(p Provider) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.byName[p.Name()]; exists {
		return ErrDuplicateProvider
	}
	r.byName[p.Name()] = p
	r.order = append(r.order, p.Name())
	return nil
}

// Get returns the provider registered under name, if any.
func (r *Registry) Get(name string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byName[name]
	return p, ok
}

// All returns every registered provider, in registration order.
func (r *Registry) All() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Provider, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.byName[name])
	}
	return out
}

// For returns every registered provider whose Capabilities advertise
// support for kind.
func (r *Registry) For(kind common.MediaKind) []Provider {
	var out []Provider
	for _, p := range r.All() {
		c := p.Capabilities()
		if (kind == common.MediaKindMovie && c.Movies) || (kind == common.MediaKindEpisode && c.Episodes) {
			out = append(out, p)
		}
	}
	return out
}
