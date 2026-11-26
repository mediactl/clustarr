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

package records

import (
	"context"
	"fmt"
	"strings"

	"github.com/mediactl/clustarr/pkg/events"
)

// Reader is the manager's read half of an agent-written bucket (ADR-0019
// §4.3, ruling R6): it never writes, requests or withdraws there.
type Reader[R Record] struct{ s store[R] }

// NewReader binds spec to kv, the agent-written bucket.
func NewReader[R Record](kv events.KV, spec Spec[R]) *Reader[R] {
	return &Reader[R]{s: newStore(kv, spec)}
}

// Get reads key: the record, its revision (0 when absent), and whether it
// decoded. An undecodable record, another Schema, or one whose KeyOf names
// another key reads as absent at its revision.
func (r *Reader[R]) Get(ctx context.Context, key string) (R, uint64, bool, error) {
	return r.s.get(ctx, key)
}

// Keys lists the bucket's live keys that start with prefix, sorted; an empty
// prefix lists them all. It reads every key of the bucket, so a caller pages
// nothing and calls it rarely (a resync, a rebuild).
func (r *Reader[R]) Keys(ctx context.Context, prefix string) ([]string, error) {
	keys, err := r.s.kv.Keys(ctx)
	if err != nil {
		r.s.count("get")
		return nil, fmt.Errorf("records: keys %s: %w", r.s.spec.Bucket, err)
	}
	if prefix == "" {
		return keys, nil
	}
	out := keys[:0]
	for _, k := range keys {
		if strings.HasPrefix(k, prefix) {
			out = append(out, k)
		}
	}
	return out, nil
}

// Stored is one record of a bucket with the revision it was read at.
type Stored[R Record] struct {
	Record   R
	Revision uint64
}

// All reads every record of the bucket in one pass (events.KV.Entries), in
// revision order: what Keys and a Get of each key answer, without a request
// to the broker per key. A value Get reads as absent is left out.
func (r *Reader[R]) All(ctx context.Context) ([]Stored[R], error) {
	entries, err := r.s.kv.Entries(ctx)
	if err != nil {
		r.s.count("get")
		return nil, fmt.Errorf("records: read %s: %w", r.s.spec.Bucket, err)
	}
	out := make([]Stored[R], 0, len(entries))
	for _, e := range entries {
		if rec, ok := r.s.decode(e); ok {
			out = append(out, Stored[R]{Record: rec, Revision: e.Revision})
		}
	}
	return out, nil
}
