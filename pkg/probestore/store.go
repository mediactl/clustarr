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
// It imports pkg/events and its schema, and nothing that probes.
package probestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/version"
)

// Bus is what the store needs of the bus: the bucket and the probe stream.
// Every events.Bus is one.
type Bus interface {
	events.Publisher
	KV(bucket string) events.KV
}

// ErrConflict is a compare-and-swap that lost to another writer: re-read and
// decide again.
var ErrConflict = errors.New("probestore: the record changed since it was read")

// maxCAS bounds how often Answer and Seed redo a lost compare-and-swap.
const maxCAS = 5

// Store reads and writes probe records.
type Store struct {
	bus Bus
	kv  events.KV
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// New is the store over bus's clustarr-probes bucket.
func New(bus Bus) *Store { return &Store{bus: bus, kv: bus.KV(events.BucketProbes)} }

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
	e, err := s.kv.Get(ctx, events.ProbeKey(uid))
	if errors.Is(err, events.ErrKeyNotFound) {
		return Current{}, nil
	}
	if err != nil {
		return Current{}, fmt.Errorf("probestore: get %s: %w", uid, err)
	}
	rec, ok := Decode(e)
	return Current{Record: rec, OK: ok, Revision: e.Revision}, nil
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
	return s.kv.Watch(ctx, ">")
}

// TaskOf is the task that asks for rec.
func TaskOf(rec schema.ProbeRecord) schema.ProbeTask {
	return schema.ProbeTask{
		MediaFile: rec.MediaFile, Path: rec.Path, ProbeHash: rec.ProbeHash,
		ProbeVersion: rec.RequestedVersion, Seq: rec.Seq, Lane: rec.Lane,
	}
}

// Request writes a requested record for want at prev's revision: sequence
// prev's plus one (1 when there is none), carrying prev's AbandonedCount when
// prev describes the same path and hash. A lost compare-and-swap is
// ErrConflict. The caller then Publishes the record's task.
func (s *Store) Request(ctx context.Context, want Want, prev Current) (schema.ProbeRecord, error) {
	rec := schema.ProbeRecord{
		MediaFile: want.MediaFile, Seq: 1, State: schema.ProbeRequested,
		Path: want.Path, ProbeHash: want.ProbeHash, RequestedVersion: want.ProbeVersion,
		Lane: lane(want.Lane), RequestedAt: s.now(),
	}
	if prev.OK {
		rec.Seq = prev.Record.Seq + 1
		if samePathAndHash(prev.Record, rec) {
			rec.AbandonedCount = prev.Record.AbandonedCount
		}
	}
	if err := s.write(ctx, want.MediaFile.UID, prev.Revision, rec); err != nil {
		return schema.ProbeRecord{}, err
	}
	return rec, nil
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
	cur, err := s.Get(ctx, t.MediaFile.UID)
	if err != nil {
		return false, err
	}
	return !answerable(cur, t), nil
}

// Answer records ans, a probed or failed answer to t, re-reading the record
// immediately before each compare-and-swap: written when the record is
// absent, requested at or below t.Seq, or answered below t.Seq; dropped
// (false, nil) otherwise. An abandoned answer counts one more than the
// record's AbandonedCount for the same path and hash; any other resets it.
func (s *Store) Answer(ctx context.Context, t schema.ProbeTask, ans schema.ProbeRecord) (bool, error) {
	if ans.State != schema.ProbeProbed && ans.State != schema.ProbeFailed {
		return false, fmt.Errorf("probestore: an answer is %q or %q, not %q", schema.ProbeProbed, schema.ProbeFailed, ans.State)
	}
	for range maxCAS {
		cur, err := s.Get(ctx, t.MediaFile.UID)
		if err != nil {
			return false, err
		}
		if !answerable(cur, t) {
			return false, nil
		}
		rec := ans
		rec.MediaFile, rec.Seq, rec.RequestedVersion, rec.Lane = t.MediaFile, t.Seq, t.ProbeVersion, t.Lane
		rec.Source = schema.ProbeSourceProbe
		rec.Failure = clip(rec.Failure, schema.MaxProbeFailure)
		if cur.OK && cur.Record.Seq == t.Seq {
			rec.RequestedAt = cur.Record.RequestedAt
		}
		rec.AbandonedCount = 0
		if rec.Abandoned {
			rec.AbandonedCount = 1
			if cur.OK && samePathAndHash(cur.Record, rec) {
				rec.AbandonedCount = cur.Record.AbandonedCount + 1
			}
		}
		err = s.write(ctx, t.MediaFile.UID, cur.Revision, rec)
		if errors.Is(err, ErrConflict) {
			continue
		}
		return err == nil, err
	}
	return false, fmt.Errorf("probestore: answer for %s: %w", t.MediaFile, ErrConflict)
}

// Seed records a probe an importer ran as a probed record answering no
// request (RequestedVersion 0, the current sequence), unless the record is
// already probed for the same path and hash at seed's version or newer.
func (s *Store) Seed(ctx context.Context, seed Seed) error {
	if seed.MediaFile.UID == "" || seed.MediaInfo == nil {
		return errors.New("probestore: a seed needs the MediaFile's UID and a MediaInfo")
	}
	for range maxCAS {
		cur, err := s.Get(ctx, seed.MediaFile.UID)
		if err != nil {
			return err
		}
		if cur.OK && cur.Record.State == schema.ProbeProbed && cur.Record.Path == seed.Path &&
			cur.Record.ProbeHash == seed.ProbeHash && cur.Record.ProbeVersion >= seed.ProbeVersion {
			return nil
		}
		rec := schema.ProbeRecord{
			MediaFile: seed.MediaFile, State: schema.ProbeProbed, Path: seed.Path, ProbeHash: seed.ProbeHash,
			ProbeVersion: seed.ProbeVersion, ProbedAt: s.now(), MediaInfo: seed.MediaInfo,
			Source: schema.ProbeSourceImport, Prober: seed.Prober,
		}
		if cur.OK {
			rec.Seq = cur.Record.Seq
		}
		err = s.write(ctx, seed.MediaFile.UID, cur.Revision, rec)
		if errors.Is(err, ErrConflict) {
			continue
		}
		return err
	}
	return fmt.Errorf("probestore: seed for %s: %w", seed.MediaFile, ErrConflict)
}

// write creates uid's record when rev is 0 and replaces it at rev otherwise.
func (s *Store) write(ctx context.Context, uid string, rev uint64, rec schema.ProbeRecord) error {
	b, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("probestore: encode the record of %s: %w", uid, err)
	}
	key := events.ProbeKey(uid)
	if rev == 0 {
		_, err = s.kv.Create(ctx, key, b)
	} else {
		_, err = s.kv.Update(ctx, key, b, rev)
	}
	switch {
	case errors.Is(err, events.ErrKeyExists), errors.Is(err, events.ErrRevisionMismatch), errors.Is(err, events.ErrKeyNotFound):
		return ErrConflict
	case err != nil:
		return fmt.Errorf("probestore: write the record of %s: %w", uid, err)
	}
	return nil
}

// answerable is §6.5.2's answer rule.
func answerable(cur Current, t schema.ProbeTask) bool {
	if !cur.OK {
		return true
	}
	switch cur.Record.State {
	case schema.ProbeRequested:
		return cur.Record.Seq <= t.Seq
	case schema.ProbeProbed, schema.ProbeFailed:
		return cur.Record.Seq < t.Seq
	}
	return true
}

func samePathAndHash(a, b schema.ProbeRecord) bool {
	return a.Path == b.Path && a.ProbeHash == b.ProbeHash
}

// lane is the record's lane: "high" or "low".
func lane(p events.Priority) string {
	if p == events.PriorityHigh {
		return string(events.PriorityHigh)
	}
	return string(events.PriorityLow)
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
