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
	toolscache "k8s.io/client-go/tools/cache"
)

// Row is what an [Informer] stores for one object: its JSON, compressed,
// beside the few fields a cache needs without decoding it. A row is
// immutable once built; an update stores a new row.
//
// A row is not a runtime.Object or a metav1.Object on purpose: nothing may
// hand one to code expecting the kind, and everything that leaves the
// informer -- a Get, a List, a handler notification -- is a fresh decode.
type Row struct {
	namespace, name string
	// labels is the object's own map, kept for List's label selectors.
	labels map[string]string
	// body is the object's JSON as one zstd frame, compressed with the
	// dictionary dict names (0: none).
	body []byte
	dict uint32
	// size is the JSON's length before compression.
	size int
	// index holds the values every registered index function gave for the
	// object, computed while the object was still decoded.
	index []indexed
}

type indexed struct {
	name   string
	values []string
}

func (r *Row) key() string {
	if r.namespace == "" {
		return r.name
	}
	return r.namespace + "/" + r.name
}

// indexValues reports the precomputed values of the index name, if the row
// was built while that index was registered.
func (r *Row) indexValues(name string) ([]string, bool) {
	for i := range r.index {
		if r.index[i].name == name {
			return r.index[i].values, true
		}
	}
	return nil, false
}

// rowKey keys a store of rows: a row by namespace/name, a tombstone by the
// key it carries, and anything else -- an object the DeltaFIFO keys before
// it is transformed -- as client-go does.
func rowKey(obj any) (string, error) {
	switch o := obj.(type) {
	case *Row:
		return o.key(), nil
	case *rowEvent:
		return o.row.key(), nil
	case toolscache.DeletedFinalStateUnknown:
		return o.Key, nil
	}
	return toolscache.MetaNamespaceKeyFunc(obj)
}
