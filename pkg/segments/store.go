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

package segments

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
)

// Store reads and writes clustarr-segments by compare-and-swap (loop spec
// 2026-10-06 §4.12): cmd/markers writes each file's analysis through it, the
// remediation loop reads it.
type Store struct{ kv events.KV }

// NewStore is the store over kv, the clustarr-segments bucket.
func NewStore(kv events.KV) *Store { return &Store{kv: kv} }

// Get reads uid's record, its revision and whether there is one. A v1
// record (no Schema: every record written before F3.4) reads as v2 with
// File.UID from its key; a record of another schema, or one that does not
// decode, reads as absent at its revision.
func (s *Store) Get(ctx context.Context, uid string) (Record, uint64, bool, error) {
	e, err := s.kv.Get(ctx, events.RecordKey(uid))
	if errors.Is(err, events.ErrKeyNotFound) {
		return Record{}, 0, false, nil
	}
	if err != nil {
		return Record{}, 0, false, err
	}
	if e.Operation != events.KVPut || len(e.Value) == 0 {
		return Record{}, e.Revision, false, nil
	}
	var r Record
	if json.Unmarshal(e.Value, &r) != nil || (r.Schema != "" && r.Schema != RecordSchema) {
		return Record{}, e.Revision, false, nil
	}
	if r.Schema == "" {
		r.File.UID = uid
	}
	return r, e.Revision, true, nil
}

// Update applies fn to uid's current record (nil when absent) and writes the
// result by CAS, re-reading and re-applying on a lost race, at most
// records.CASAttempts times (the lost-update rule: fn always sees the record
// it replaces); fn's false writes nothing. It reports whether it wrote.
func (s *Store) Update(ctx context.Context, uid string, fn func(cur *Record) (Record, bool)) (bool, error) {
	for range records.CASAttempts {
		cur, rev, ok, err := s.Get(ctx, uid)
		if err != nil {
			return false, err
		}
		var curp *Record
		if ok {
			curp = &cur
		}
		next, write := fn(curp)
		if !write {
			return false, nil
		}
		b, err := json.Marshal(next)
		if err != nil {
			return false, err
		}
		if rev == 0 {
			_, err = s.kv.Create(ctx, events.RecordKey(uid), b)
		} else {
			_, err = s.kv.Update(ctx, events.RecordKey(uid), b, rev)
		}
		if errors.Is(err, events.ErrKeyExists) || errors.Is(err, events.ErrRevisionMismatch) {
			continue
		}
		return err == nil, err
	}
	return false, records.ErrRaced
}

// WakeRef is S21's recordsource.Decoder: a v2 record's file ref and no state
// (every put wakes); a v1 record is skipped (none is written after F3.4).
func WakeRef(value []byte) (schema.Ref, string, bool) {
	var r struct {
		Schema string     `json:"schema"`
		File   schema.Ref `json:"file"`
	}
	if json.Unmarshal(value, &r) != nil || r.Schema != RecordSchema || r.File.Name == "" {
		return schema.Ref{}, "", false
	}
	return r.File, "", true
}
