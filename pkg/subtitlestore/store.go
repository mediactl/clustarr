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

package subtitlestore

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/version"
)

const (
	// Remediation names the subtitle remediation in records metrics and is
	// its records-pacer class (records.PaceSubtitle).
	Remediation = records.PaceSubtitle
	// MaxValue is clustarr-subtitles' MaxValueSize.
	MaxValue = events.SubtitlesMaxValueSize
)

// Bus is what the store needs of the bus.
type Bus interface{ KV(bucket string) events.KV }

// Entry is one read of a language's record. Revision is the key's
// revision, 0 when absent and non-zero with OK false for an undecodable or
// foreign value, which a write replaces at that revision.
type Entry struct {
	Record   schema.SubtitleRecord
	OK       bool
	Revision uint64
}

// spec is the clustarr-subtitles protocol. Only a download changed the
// disk: it is the one answer a withdrawal must not swallow and a new
// request must not overwrite (§4.7, §4.8).
func spec(now func() time.Time) records.Spec[*schema.SubtitleRecord] {
	return records.Spec[*schema.SubtitleRecord]{
		Remediation: Remediation, Bucket: events.BucketSubtitles, Schema: schema.SubtitleRecordSchema, MaxValue: MaxValue,
		New:      func() *schema.SubtitleRecord { return &schema.SubtitleRecord{} },
		Answered: records.TerminalAnswered,
		Fact:     func(r *schema.SubtitleRecord) bool { return r.Verdict == schema.SubtitleVerdictDownloaded },
		Errors:   func(op string) { metrics.RecordErrorsTotal.WithLabelValues(Remediation, op).Inc() },
		Now:      now,
	}
}

// Store is the loop's and the caption domain's view of clustarr-subtitles.
type Store struct {
	req *records.Requester[*schema.SubtitleRecord]
	ans *records.Answerer[*schema.SubtitleRecord]
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// New is the store over bus's clustarr-subtitles bucket; writer names the
// answering pod in the records it writes ("" for the loop, which answers
// nothing).
func New(bus Bus, writer string) *Store {
	s := &Store{}
	kv := bus.KV(events.BucketSubtitles)
	sp := spec(s.now)
	s.req = records.NewRequester(kv, sp)
	s.ans = records.NewAnswerer(kv, sp, writer, version.String())
	return s
}

func (s *Store) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

// Key is fileUID's langKey record key.
func Key(fileUID, langKey string) string { return events.RecordSubKey(fileUID, langKey) }

// Requested is the record the loop writes for t: its inputs, so an answer
// is matched by them as well as by Seq.
func Requested(t schema.FetchTask, at time.Time) schema.SubtitleRecord {
	var r schema.SubtitleRecord
	r.Schema, r.MediaFile, r.Sub, r.Seq = schema.SubtitleRecordSchema, t.MediaFile, t.LangKey, t.Seq
	r.State, r.RequestedAt = records.StateRequested, at.UTC()
	r.ProbeHash, r.ProfileGeneration = t.ProbeHash, t.ProfileGeneration
	return r
}

// Get reads fileUID's langKey record.
func (s *Store) Get(ctx context.Context, fileUID, langKey string) (Entry, error) {
	rec, rev, ok, err := s.req.Get(ctx, Key(fileUID, langKey))
	if err != nil {
		return Entry{}, fmt.Errorf("subtitlestore: get %s %s: %w", fileUID, langKey, err)
	}
	e := Entry{OK: ok && rec != nil, Revision: rev}
	if e.OK {
		e.Record = *rec
	}
	return e, nil
}

// Request writes rec at rev (0 creates), refusing to overwrite an answer
// above incorporatedThrough (records.ErrUnincorporatedFact); a CAS miss is
// records.ErrRaced.
func (s *Store) Request(ctx context.Context, rec schema.SubtitleRecord, rev uint64, incorporatedThrough int64) error {
	r := rec
	_, err := s.req.Request(ctx, Key(rec.MediaFile.UID, rec.Sub), rev, &r, records.IncorporatedThrough(incorporatedThrough))
	return err
}

// Withdraw is records.Withdraw (§4.7's table) on one language.
func (s *Store) Withdraw(ctx context.Context, fileUID, langKey string, seq int64, rev uint64, incorporatedThrough int64) error {
	_, err := s.req.Withdraw(ctx, Key(fileUID, langKey), rev, seq, records.IncorporatedThrough(incorporatedThrough))
	return err
}

// Superseded is checked before any provider request (§6.6): a newer
// request, a withdrawal or an answer at the task's Seq.
func (s *Store) Superseded(ctx context.Context, t schema.FetchTask) (bool, error) {
	return s.ans.Superseded(ctx, Key(t.MediaFile.UID, t.LangKey), t.Seq)
}

// Answer records ans for t, re-reading immediately before its CAS; it
// reports false when §4.8's rules dropped it (a newer request, a
// withdrawal, or an answer already at t's Seq). ans.State defaults to
// answered.
func (s *Store) Answer(ctx context.Context, t schema.FetchTask, ans schema.SubtitleRecord) (bool, error) {
	ans.Schema, ans.MediaFile, ans.Sub, ans.Seq = schema.SubtitleRecordSchema, t.MediaFile, t.LangKey, t.Seq
	if ans.State == "" {
		ans.State = records.StateAnswered
	}
	ans.ProbeHash, ans.ProfileGeneration = t.ProbeHash, t.ProfileGeneration
	at := s.now()
	ans.AnsweredAt = &at
	ans.LastError = Clip(ans.LastError, schema.MaxSubtitleLastError)
	v, err := s.ans.Answer(ctx, Key(t.MediaFile.UID, t.LangKey), &ans)
	if err != nil {
		return false, fmt.Errorf("subtitlestore: answer %s %s: %w", t.MediaFile.UID, t.LangKey, err)
	}
	return v == records.Wrote, nil
}

// Clip cuts s to at most n bytes on a rune boundary.
func Clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
