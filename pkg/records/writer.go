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

	"github.com/mediactl/clustarr/pkg/events"
)

// Writer is the one writer of a key in an agent-written bucket (ADR-0019
// §4.3, ruling R6): a compare-and-swap after a fresh read that drops a
// record whose Seq is below the stored one (Dropped) and allows a repeated
// Seq (a transfer rewrites its counters at one command's Seq). It never
// writes requested or withdrawn, and never deletes: the bucket TTL retires
// records.
type Writer[R Record] struct {
	s               store[R]
	writer, version string
}

// NewWriter binds spec to kv. writer and writerVersion (the pod name and
// version.String()) are stamped on every write.
func NewWriter[R Record](kv events.KV, spec Spec[R], writer, writerVersion string) *Writer[R] {
	return &Writer[R]{s: newStore(kv, spec), writer: writer, version: writerVersion}
}

// Write stores rec at key unless the stored record's Seq is above rec's
// (Dropped, nothing written). It stamps Writer, WriterVersion and Schema,
// clips Failure to MaxFailure, refuses a value over the bucket's MaxValue
// (ErrTooLarge), and re-reads and retries a lost compare-and-swap up to
// CASAttempts times.
func (w *Writer[R]) Write(ctx context.Context, key string, rec R) (Verdict, error) {
	seq := rec.Header().Seq
	return w.Update(ctx, key, func(cur R, ok bool) (R, bool) {
		if ok && cur.Header().Seq > seq {
			return cur, false
		}
		return rec, true
	})
}

// Update is the read-modify-write form two writers in one process share
// (the index agent's fan-out and RSS poll, ADR-0019 §7.5): mutate gets a
// copy of the stored record (ok false when absent) and returns the record to
// write, or false to write nothing (Dropped). A returned record whose Seq is
// below the stored one is Dropped too. Each attempt re-reads, so a lost
// compare-and-swap decides again on the newer record.
func (w *Writer[R]) Update(ctx context.Context, key string, mutate func(cur R, ok bool) (R, bool)) (Verdict, error) {
	for range CASAttempts {
		cur, rev, ok, err := w.s.get(ctx, key)
		if err != nil {
			return 0, err
		}
		if ok {
			if cur, err = w.s.clone(cur); err != nil {
				return 0, err
			}
		}
		next, write := mutate(cur, ok)
		if !write {
			return Dropped, nil
		}
		if ok && next.Header().Seq < cur.Header().Seq {
			return Dropped, nil
		}
		if next, err = w.s.clone(next); err != nil {
			return 0, err
		}
		nh := next.Header()
		nh.Failure = clip(nh.Failure, MaxFailure)
		nh.Writer, nh.WriterVersion = w.writer, w.version
		_, err = w.s.write(ctx, key, rev, next, "answer")
		if err == nil {
			return Wrote, nil
		}
		if !errorsIsRaced(err) {
			return 0, err
		}
	}
	return 0, fmt.Errorf("records: write %s/%s: %w", w.s.spec.Bucket, key, ErrRaced)
}
