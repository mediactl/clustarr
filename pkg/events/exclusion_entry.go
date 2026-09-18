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

package events

import (
	"encoding/json"
	"fmt"
)

// ExclusionEntry is the value the ImportExclusion controller stores at an
// [ExclusionKey] inside [BucketImportExclusions], and what an import-list or
// search path decodes after a successful Get to learn which resource blocked
// a candidate and why.
//
// The lookup contract is: for every external id a candidate carries (tmdb,
// imdb, tvdb, ...), call
//
//	bus.KV(events.BucketImportExclusions).Get(ctx, events.ExclusionKey(key, value))
//
// [ErrKeyNotFound] means the id is not excluded; any other successful Get
// means it is, and DecodeExclusionEntry(entry.Value) gives the reason to
// surface to the user. Consulting the bucket rather than listing
// ImportExclusion resources per candidate is what makes a large import-list
// sync affordable.
//
// Kind is catalog.clustarr.io's ExclusionKind rendered as a plain string:
// like a bus payload, a KV value carries data only, so a consumer decodes it
// without importing the catalog API group.
type ExclusionEntry struct {
	// Namespace is the namespace of the ImportExclusion that blocked the id.
	Namespace string `json:"namespace"`

	// Name is the name of the ImportExclusion that blocked the id.
	Name string `json:"name"`

	// Kind is the catalog kind the exclusion suppresses ("movie",
	// "series", ...).
	Kind string `json:"kind"`

	// Reason is the user-supplied explanation, when the exclusion carried
	// one.
	Reason string `json:"reason,omitempty"`
}

// Encode marshals e for a KV Put.
func (e ExclusionEntry) Encode() ([]byte, error) {
	data, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("events: encode exclusion entry: %w", err)
	}
	return data, nil
}

// DecodeExclusionEntry unmarshals a KV value written by [ExclusionEntry.Encode].
func DecodeExclusionEntry(data []byte) (ExclusionEntry, error) {
	var e ExclusionEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return ExclusionEntry{}, fmt.Errorf("events: decode exclusion entry: %w", err)
	}
	return e, nil
}
