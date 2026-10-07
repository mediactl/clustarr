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

// Package probestore is the MediaFile probe record protocol (spec 2026-10-06
// §6.5.2): one record per MediaFile UID in the clustarr-probes bucket, every
// write a compare-and-swap, nobody deleting one (the bucket's TTL does).
// catalogarr's MediaFile reconciler requests and publishes; the import
// domain's probe worker answers; an importer seeds the probe it already ran.
// Since the fold it is pkg/records' probe remediation (loop spec 2026-10-06
// §4.12): the protocol is records', and this package keeps the probe's own
// names, its Carry and its task. It imports pkg/events, its schema and
// pkg/records, and nothing that probes.
package probestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/version"
)

// Bus is what the store needs of the bus: the bucket and the probe stream.
// Every events.Bus is one.
type Bus interface {
	events.Publisher
	KV(bucket string) events.KV
}

// ErrConflict is a compare-and-swap that lost to another writer: re-read and
// decide again. It is records.ErrRaced, so errors.Is holds under either name.
var ErrConflict = records.ErrRaced

// MaxValue is clustarr-probes' MaxValueSize (loop spec §4.3): a probe answer's
// MediaInfo runs to about 153 KB at the schema's worst case.
const MaxValue = events.ProbesMaxValueSize

// Option configures a Store.
type Option func(*Store)

// WithErrors counts a failed records operation ("get", "decode", "request",
// "answer"); the manager binds it to clustarr_record_errors_total.
func WithErrors(count func(op string)) Option { return func(s *Store) { s.errs = count } }

// Store reads and writes probe records through pkg/records (loop spec §4.12).
type Store struct {
	bus  Bus
	errs func(op string)
	q    *records.Requester[*schema.ProbeRecord]
	a    *records.Answerer[*schema.ProbeRecord]
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// New is the store over bus's clustarr-probes bucket.
func New(bus Bus, opts ...Option) *Store {
	s := &Store{bus: bus}
	for _, o := range opts {
		o(s)
	}
	spec := records.Spec[*schema.ProbeRecord]{
		Remediation: "probe", Bucket: events.BucketProbes, MaxValue: MaxValue,
		New:      func() *schema.ProbeRecord { return new(schema.ProbeRecord) },
		Answered: func(st string) bool { return st == schema.ProbeProbed || st == schema.ProbeFailed },
		Carry:    carryAbandoned,
		Errors:   s.errs,
		Now:      s.now,
	}
	kv := bus.KV(events.BucketProbes)
	s.q = records.NewRequester(kv, spec)
	s.a = records.NewAnswerer(kv, spec, "", "")
	return s
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Current is one read of a MediaFile's record. Revision is the key's revision,
// 0 when the key is absent and non-zero with OK false for an undecodable
// value, which a write replaces at that revision.
type Current struct {
	Record   schema.ProbeRecord
	OK       bool
	Revision uint64
}

// Want is the probe a reconcile asks for.
type Want struct {
	MediaFile    schema.Ref
	Path         string
	ProbeHash    string
	ProbeVersion int32
	Lane         events.Priority
}

// Seed is a probe an importer ran on the file it just placed.
type Seed struct {
	MediaFile    schema.Ref
	Path         string
	ProbeHash    string
	ProbeVersion int32
	MediaInfo    *commonv1.MediaInfo
	Prober       string
}

// Get reads uid's record.
func (s *Store) Get(ctx context.Context, uid string) (Current, error) {
	rec, rev, ok, err := s.q.Get(ctx, events.ProbeKey(uid))
	if err != nil {
		return Current{}, fmt.Errorf("probestore: get %s: %w", uid, err)
	}
	cur := Current{OK: ok, Revision: rev}
	if ok {
		cur.Record = *rec
	}
	return cur, nil
}

// Decode reads a record from a bucket entry. A delete, a purge or an
// undecodable value is no record.
func Decode(e events.Entry) (schema.ProbeRecord, bool) {
	if e.Operation == events.KVDelete || e.Operation == events.KVPurge || len(e.Value) == 0 {
		return schema.ProbeRecord{}, false
	}
	var rec schema.ProbeRecord
	if err := json.Unmarshal(e.Value, &rec); err != nil {
		return schema.ProbeRecord{}, false
	}
	return rec, true
}

// Watch streams every record now in the bucket and then every change.
func (s *Store) Watch(ctx context.Context) (<-chan events.Entry, error) {
	return s.bus.KV(events.BucketProbes).Watch(ctx, ">")
}

// TaskOf is the task that asks for rec.
func TaskOf(rec schema.ProbeRecord) schema.ProbeTask {
	return schema.ProbeTask{
		MediaFile: rec.MediaFile, Path: rec.Path, ProbeHash: rec.ProbeHash,
		ProbeVersion: rec.RequestedVersion, Seq: rec.Seq, Lane: rec.Lane,
	}
}

// Request writes a requested record for want at prev's revision, with Seq
// records.NextSeq(prev's, 0, now) (loop spec §4.6, §4.12) and prev's
// AbandonedCount when prev describes the same path and hash. A lost
// compare-and-swap is ErrConflict. The caller then Publishes the task.
func (s *Store) Request(ctx context.Context, want Want, prev Current) (schema.ProbeRecord, error) {
	var prevSeq int64
	if prev.OK {
		prevSeq = prev.Record.Seq
	}
	now := s.now()
	rec := &schema.ProbeRecord{
		RecordHeader: schema.RecordHeader{MediaFile: want.MediaFile, Seq: records.NextSeq(prevSeq, 0, now), RequestedAt: now},
		Path:         want.Path, ProbeHash: want.ProbeHash, RequestedVersion: want.ProbeVersion, Lane: lane(want.Lane),
	}
	if _, err := s.q.Request(ctx, events.ProbeKey(want.MediaFile.UID), prev.Revision, rec); err != nil {
		return schema.ProbeRecord{}, err
	}
	return *rec, nil
}

// Publish puts rec's task on its lane under its Msg-Id. A republish inside
// the stream's one-hour window is absorbed; a full queue is events.ErrQueueFull.
func (s *Store) Publish(ctx context.Context, rec schema.ProbeRecord) error {
	task := TaskOf(rec)
	name, data, err := schema.Encode(task)
	if err != nil {
		return err
	}
	id := events.MsgIDForProbe(rec.MediaFile.UID, rec.ProbeHash, rec.RequestedVersion, rec.Seq)
	env := &events.Envelope{
		ID: id, Type: "importarr.ProbeTask", Schema: name, Source: "catalogarr@" + version.String(),
		Key: rec.MediaFile.Key(), Time: s.now(), Data: data,
	}
	subject := events.WorkProbeSubject(events.Priority(rec.Lane),
		events.MediaKey("mediafile", rec.MediaFile.Namespace, rec.MediaFile.Name))
	if _, err := s.bus.Publish(ctx, subject, env, events.WithMsgID(id), events.WithExpectStream(events.StreamWorkProbe)); err != nil {
		return fmt.Errorf("probestore: publish the probe of %s: %w", rec.MediaFile, err)
	}
	return nil
}

// Superseded reports whether t's answer would be dropped: a newer request
// exists, or t is answered already (by the agent or a seed).
func (s *Store) Superseded(ctx context.Context, t schema.ProbeTask) (bool, error) {
	return s.a.Superseded(ctx, events.ProbeKey(t.MediaFile.UID), t.Seq)
}

// Answer records ans, a probed or failed answer to t (§6.5.2's rule, which is
// records' §4.8 table for the probe's states), re-reading the record
// immediately before each compare-and-swap; dropped (false, nil) when a newer
// request exists or t is answered already. An answer over MaxValue is
// recorded as a failure instead.
func (s *Store) Answer(ctx context.Context, t schema.ProbeTask, ans schema.ProbeRecord) (bool, error) {
	if ans.State != schema.ProbeProbed && ans.State != schema.ProbeFailed {
		return false, fmt.Errorf("probestore: an answer is %q or %q, not %q", schema.ProbeProbed, schema.ProbeFailed, ans.State)
	}
	key := events.ProbeKey(t.MediaFile.UID)
	task := func(r *schema.ProbeRecord) *schema.ProbeRecord {
		r.MediaFile, r.Seq, r.RequestedVersion, r.Lane, r.Source = t.MediaFile, t.Seq, t.ProbeVersion, t.Lane, schema.ProbeSourceProbe
		return r
	}
	rec := ans
	v, err := s.a.Answer(ctx, key, task(&rec))
	if errors.Is(err, records.ErrTooLarge) {
		over := &schema.ProbeRecord{
			RecordHeader: schema.RecordHeader{State: schema.ProbeFailed, Failure: fmt.Sprintf("answer exceeds %d bytes", MaxValue)},
			Path:         ans.Path, ProbeHash: ans.ProbeHash, ProbeVersion: ans.ProbeVersion, ProbedAt: ans.ProbedAt, Prober: ans.Prober,
		}
		v, err = s.a.Answer(ctx, key, task(over))
	}
	if err != nil {
		return false, fmt.Errorf("probestore: answer for %s: %w", t.MediaFile, err)
	}
	return v == records.Wrote, nil
}

// Seed records a probe an importer ran: probed, answering no request
// (RequestedVersion 0, the current Seq), unless the record is already probed
// for the same path and hash at seed's version or newer.
func (s *Store) Seed(ctx context.Context, seed Seed) error {
	if seed.MediaFile.UID == "" || seed.MediaInfo == nil {
		return errors.New("probestore: a seed needs the MediaFile's UID and a MediaInfo")
	}
	rec := &schema.ProbeRecord{
		RecordHeader: schema.RecordHeader{MediaFile: seed.MediaFile, State: schema.ProbeProbed},
		Path:         seed.Path, ProbeHash: seed.ProbeHash, ProbeVersion: seed.ProbeVersion, ProbedAt: s.now(),
		MediaInfo: seed.MediaInfo, Source: schema.ProbeSourceImport, Prober: seed.Prober,
	}
	_, err := s.a.Seed(ctx, events.ProbeKey(seed.MediaFile.UID), rec, func(cur *schema.ProbeRecord, ok bool) bool {
		return ok && cur.State == schema.ProbeProbed && cur.Path == seed.Path &&
			cur.ProbeHash == seed.ProbeHash && cur.ProbeVersion >= seed.ProbeVersion
	})
	if err != nil {
		return fmt.Errorf("probestore: seed for %s: %w", seed.MediaFile, err)
	}
	return nil
}

// carryAbandoned is the probe's Carry: a request keeps the count of the same
// path and hash, an abandoned answer counts one more than it, any other
// answer resets it.
func carryAbandoned(cur *schema.ProbeRecord, ok bool, next *schema.ProbeRecord) {
	same := ok && cur.Path == next.Path && cur.ProbeHash == next.ProbeHash
	switch {
	case next.State == schema.ProbeRequested:
		next.AbandonedCount = 0
		if same {
			next.AbandonedCount = cur.AbandonedCount
		}
	case next.Abandoned:
		next.AbandonedCount = 1
		if same {
			next.AbandonedCount = cur.AbandonedCount + 1
		}
	default:
		next.AbandonedCount = 0
	}
}

// lane is the record's lane: "high" or "low".
func lane(p events.Priority) string {
	if p == events.PriorityHigh {
		return string(events.PriorityHigh)
	}
	return string(events.PriorityLow)
}
