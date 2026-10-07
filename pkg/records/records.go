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

// Package records is the compare-and-swap record protocol every remediation
// reports through (loop spec 2026-10-06 §4; ADR-0016): one record per
// MediaFile UID (and sub-key) in a Durable KV bucket per remediation; the
// loop the only issuer of Seq, writing status, then the record, then the
// task; workers answering by compare-and-swap after a fresh re-read; nobody
// deleting a record, whose bucket TTL retires it. Requester is the loop's
// half, Answerer the workers'. It imports pkg/events, its schema and the
// standard library only (TestRecordsLinksNoKubernetes), so the
// credential-less transcode and markers binaries link it.
package records

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// States of the records buckets the fold adds. The probe keeps its own
// (requested, probed, failed), and its Spec's Answered maps probed and failed
// to answered.
const (
	StateRequested = "requested" // the loop
	StateWithdrawn = "withdrawn" // the loop
	StateClaimed   = "claimed"   // a worker started (transcode, graft)
	StateDeferred  = "deferred"  // a worker postponed to DeferredUntil (markers)
	StateAnswered  = "answered"  // a worker's answer: a success or a remediation-level failure
	StateFailed    = "failed"    // no answer could be produced
)

const (
	// CASAttempts bounds how often a write re-reads and decides again.
	CASAttempts = 8
	// RepublishWindow is how long a requested, unclaimed record is
	// republished under its Msg-Id: inside the work streams' one-hour
	// Duplicates window, so a republish is absorbed (§4.7).
	RepublishWindow = 50 * time.Minute
	// MaxFactWindow caps FactWindow at the buckets' TTL.
	MaxFactWindow = 7 * 24 * time.Hour
	// NotYetDelay is how long a worker naks a task whose record is absent or
	// older: the loop's owed write recreates it within a pass (§4.8).
	NotYetDelay = 30 * time.Second
	// MaxFailure bounds RecordHeader.Failure; Answer clips to it.
	MaxFailure = schema.MaxRecordFailure
)

var (
	// ErrRaced is a compare-and-swap the loop lost: requeue and decide again
	// from a fresh read.
	ErrRaced = errors.New("records: the record changed since it was read")
	// ErrTooLarge is a record over its bucket's MaxValueSize.
	ErrTooLarge = errors.New("records: the record exceeds the bucket's value limit")
	// ErrUnincorporatedFact is a write that would replace an answer which
	// changed the file on disk before the caller incorporated it.
	ErrUnincorporatedFact = errors.New("records: the record holds a fact the caller has not incorporated")
)

// NextSeq is the Seq the loop issues for a new request: above the record's
// and the status's, and never below the clock's millisecond, so a Seq never
// restarts when the TTL retires a record (§4.6).
func NextSeq(recordSeq, statusSeq int64, now time.Time) int64 {
	return max(recordSeq+1, statusSeq+1, now.UnixMilli())
}

// FactWindow is how long the planner keeps reading a withdrawn dispatch's
// record for a fact (§4.10): the work's deadline plus its grace, at most the
// buckets' TTL.
func FactWindow(deadline, grace time.Duration) time.Duration {
	return min(deadline+grace, MaxFactWindow)
}

// Record is a records-bucket value: a pointer to a struct embedding
// schema.RecordHeader.
type Record interface {
	Header() *schema.RecordHeader
}

// Spec is one remediation's protocol.
type Spec[R Record] struct {
	// Remediation names it in errors and metrics: "probe", "transcode",
	// "graft", "subtitle", "markers".
	Remediation string
	// Bucket is the records bucket, for errors.
	Bucket string
	// Schema is RecordHeader.Schema on every value; "" for the probe.
	Schema string
	// MaxValue is the bucket's MaxValueSize; a larger write is ErrTooLarge.
	MaxValue int
	// New allocates an empty record. Required.
	New func() R
	// Answered reports a worker's terminal state; nil is TerminalAnswered.
	Answered func(state string) bool
	// Fact reports an answer recording a change already made on disk; nil
	// is none.
	Fact func(R) bool
	// Carry, when set, copies what a remediation carries forward from the
	// current record into the next before every write (the probe's
	// AbandonedCount, a failure counter).
	Carry func(cur R, ok bool, next R)
	// Errors, when set, counts a failed operation ("get", "request",
	// "withdraw", "decode", "claim", "answer"); the manager binds it to
	// clustarr_record_errors_total.
	Errors func(op string)
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// TerminalAnswered is the Answered of every bucket the fold adds.
func TerminalAnswered(state string) bool { return state == StateAnswered || state == StateFailed }

// Option qualifies one write.
type Option func(*writeOptions)

type writeOptions struct{ incorporated int64 }

// IncorporatedThrough states the highest Seq whose answer the caller has
// incorporated: a fact at or below it may be replaced (§4.7).
func IncorporatedThrough(seq int64) Option {
	return func(o *writeOptions) { o.incorporated = seq }
}

type store[R Record] struct {
	spec Spec[R]
	kv   events.KV
}

func newStore[R Record](kv events.KV, spec Spec[R]) store[R] {
	if spec.New == nil {
		panic("records: Spec.New is required (" + spec.Remediation + ")")
	}
	if spec.Answered == nil {
		spec.Answered = TerminalAnswered
	}
	if spec.Now == nil {
		spec.Now = time.Now
	}
	return store[R]{spec: spec, kv: kv}
}

func (s store[R]) now() time.Time { return s.spec.Now().UTC() }

func (s store[R]) count(op string) {
	if s.spec.Errors != nil {
		s.spec.Errors(op)
	}
}

// get reads key: (record, revision, ok). An undecodable value, another
// Schema, or a header naming another UID or sub-key than the key reads as
// absent at its revision, so a write replaces it there.
func (s store[R]) get(ctx context.Context, key string) (R, uint64, bool, error) {
	var zero R
	e, err := s.kv.Get(ctx, key)
	if errors.Is(err, events.ErrKeyNotFound) {
		return zero, 0, false, nil
	}
	if err != nil {
		s.count("get")
		return zero, 0, false, fmt.Errorf("records: get %s/%s: %w", s.spec.Bucket, key, err)
	}
	if e.Operation != events.KVPut || len(e.Value) == 0 {
		return zero, e.Revision, false, nil
	}
	rec := s.spec.New()
	if err := json.Unmarshal(e.Value, rec); err != nil {
		s.count("decode")
		return zero, e.Revision, false, nil
	}
	h := rec.Header()
	uid, sub, err := keyParts(key)
	if err != nil || h.Schema != s.spec.Schema || h.MediaFile.UID != uid || h.Sub != sub {
		s.count("decode")
		return zero, e.Revision, false, nil
	}
	return rec, e.Revision, true, nil
}

// at reads key and fails with ErrRaced unless it is still at rev.
func (s store[R]) at(ctx context.Context, key string, rev uint64) (R, bool, error) {
	cur, got, ok, err := s.get(ctx, key)
	if err != nil {
		return cur, false, err
	}
	if got != rev {
		var zero R
		return zero, false, fmt.Errorf("records: %s/%s is at revision %d, not %d: %w", s.spec.Bucket, key, got, rev, ErrRaced)
	}
	return cur, ok, nil
}

// write creates key when rev is 0 and replaces it at rev otherwise.
func (s store[R]) write(ctx context.Context, key string, rev uint64, rec R, op string) (uint64, error) {
	h := rec.Header()
	uid, sub, err := keyParts(key)
	if err != nil || h.MediaFile.UID != uid || h.Sub != sub {
		return 0, fmt.Errorf("records: a %s record for %q/%q cannot be written under key %q", s.spec.Remediation, h.MediaFile.UID, h.Sub, key)
	}
	h.Schema = s.spec.Schema
	b, err := json.Marshal(rec)
	if err != nil {
		return 0, fmt.Errorf("records: encode %s/%s: %w", s.spec.Bucket, key, err)
	}
	if s.spec.MaxValue > 0 && len(b) > s.spec.MaxValue {
		return 0, fmt.Errorf("records: %s/%s is %d bytes, over %d: %w", s.spec.Bucket, key, len(b), s.spec.MaxValue, ErrTooLarge)
	}
	var next uint64
	if rev == 0 {
		next, err = s.kv.Create(ctx, key, b)
	} else {
		next, err = s.kv.Update(ctx, key, b, rev)
	}
	switch {
	case errors.Is(err, events.ErrKeyExists), errors.Is(err, events.ErrRevisionMismatch), errors.Is(err, events.ErrKeyNotFound):
		return 0, fmt.Errorf("records: write %s/%s: %w", s.spec.Bucket, key, ErrRaced)
	case err != nil:
		s.count(op)
		return 0, fmt.Errorf("records: write %s/%s: %w", s.spec.Bucket, key, err)
	}
	return next, nil
}

// clone deep-copies r through its JSON, the only form every record shares.
func (s store[R]) clone(r R) (R, error) {
	b, err := json.Marshal(r)
	if err != nil {
		var zero R
		return zero, fmt.Errorf("records: copy a %s record: %w", s.spec.Remediation, err)
	}
	out := s.spec.New()
	if err := json.Unmarshal(b, out); err != nil {
		var zero R
		return zero, fmt.Errorf("records: copy a %s record: %w", s.spec.Remediation, err)
	}
	return out, nil
}

// refuseFact is ErrUnincorporatedFact when cur is an answered fact above what
// opts say the caller incorporated.
func (s store[R]) refuseFact(cur R, ok bool, opts []Option) error {
	if !ok || s.spec.Fact == nil {
		return nil
	}
	h := cur.Header()
	if !s.spec.Answered(h.State) || !s.spec.Fact(cur) {
		return nil
	}
	var o writeOptions
	for _, fn := range opts {
		fn(&o)
	}
	if h.Seq <= o.incorporated {
		return nil
	}
	return fmt.Errorf("records: %s %s seq %d: %w", s.spec.Bucket, h.MediaFile.UID, h.Seq, ErrUnincorporatedFact)
}

// keyParts reads the UID and sub-key back out of a records key.
func keyParts(key string) (uid, sub string, err error) {
	first, second, two := strings.Cut(key, ".")
	if uid, err = events.ParseKVKeyToken(first); err != nil {
		return "", "", err
	}
	if two {
		if sub, err = events.ParseKVKeyToken(second); err != nil {
			return "", "", err
		}
	}
	return uid, sub, nil
}

// clip cuts s to at most n bytes on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
