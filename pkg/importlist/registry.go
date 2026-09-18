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

package importlist

import (
	"context"
	"errors"
	"sort"
	"sync"
)

// ErrDuplicateName is returned by Registry.Register when an ImportList with
// the same Name() is already registered.
var ErrDuplicateName = errors.New("importlist: name already registered")

// FetchResult is one ImportList's outcome from Registry.FetchAll.
type FetchResult struct {
	Name  string
	Items []Item
	Err   error
}

// Registry holds a name-keyed set of ImportList instances, so a service can
// fetch every configured list by name without depending on any one
// provider package directly.
type Registry struct {
	mu    sync.RWMutex
	lists map[string]ImportList
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{lists: make(map[string]ImportList)}
}

// Register adds l, keyed by l.Name(). It returns ErrDuplicateName if a list
// with the same name is already registered.
func (r *Registry) Register(l ImportList) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.lists[l.Name()]; exists {
		return ErrDuplicateName
	}
	r.lists[l.Name()] = l
	return nil
}

// Get returns the ImportList registered under name, and whether it exists.
func (r *Registry) Get(name string) (ImportList, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	l, ok := r.lists[name]
	return l, ok
}

// Names returns every registered name, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.lists))
	for name := range r.lists {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// FetchAll calls Fetch on every registered list, in sorted name order, and
// returns one FetchResult per list. One list's error does not stop the
// others from being fetched.
func (r *Registry) FetchAll(ctx context.Context) []FetchResult {
	names := r.Names()
	results := make([]FetchResult, 0, len(names))
	for _, name := range names {
		l, _ := r.Get(name)
		items, err := l.Fetch(ctx)
		results = append(results, FetchResult{Name: name, Items: items, Err: err})
	}
	return results
}
