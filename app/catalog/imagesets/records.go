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

// Package imagesets is the image-set remediation's protocol
// (docs/superpowers/specs/2026-10-22-mediux-image-sets-automation-design.md):
// the clustarr-image-sets record both halves share, the task the loop
// publishes, and the pure planner that decides when a Movie or Series is
// asked for a set and incorporates the worker's answer into
// status.imageSet.
package imagesets

import (
	"context"
	"time"
	"unicode/utf8"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
	"github.com/mediactl/clustarr/pkg/version"
)

// RecordSchema is RecordHeader.Schema on a clustarr-image-sets record.
var RecordSchema = schema.ImageSetRecord{}.Schema()

// Records is the loop's read half of clustarr-image-sets; the loop writes
// its requests as remediation.RecordWrite effects.
type Records struct {
	q *records.Requester[*schema.ImageSetRecord]
}

// NewRecords is the read half over bus's clustarr-image-sets bucket.
func NewRecords(bus events.Bus) *Records {
	return &Records{q: records.NewRequester(bus.KV(events.BucketImageSets), agentrecords.ImageSets())}
}

// Get reads uid's record, its revision, and whether there is one.
func (r *Records) Get(ctx context.Context, uid string) (*schema.ImageSetRecord, uint64, bool, error) {
	return r.q.Get(ctx, agentrecords.ImageSetKey(uid))
}

// Answers is the worker's half of clustarr-image-sets.
type Answers struct {
	a *records.Answerer[*schema.ImageSetRecord]
}

// NewAnswers is the worker's half, writing as writer (the pod).
func NewAnswers(bus events.Bus, writer string) *Answers {
	return &Answers{a: records.NewAnswerer(bus.KV(events.BucketImageSets), agentrecords.ImageSets(), writer, version.String())}
}

// Superseded reports whether t's answer would be dropped: a newer request,
// a withdrawal or an answer at its Seq.
func (a *Answers) Superseded(ctx context.Context, t schema.ImageSetTask) (bool, error) {
	return a.a.Superseded(ctx, agentrecords.ImageSetKey(t.Item.UID), t.Seq)
}

// Answer writes ans for t: state answered, or failed and transient with
// failure as the reason (the loop keeps the images it has and asks again
// after ErrorRetry).
func (a *Answers) Answer(ctx context.Context, t schema.ImageSetTask, ans schema.ImageSetAnswer, failure string) (records.Verdict, error) {
	rec := &schema.ImageSetRecord{Inputs: t.Inputs, Answer: &ans}
	item := t.Item
	rec.RecordHeader.Schema, rec.Item, rec.Seq, rec.State = RecordSchema, &item, t.Seq, records.StateAnswered
	if failure != "" {
		rec.State, rec.Failure, rec.Transient = records.StateFailed, clamp(failure, schema.MaxRecordFailure), true
	}
	return a.a.Answer(ctx, agentrecords.ImageSetKey(t.Item.UID), rec)
}

// TaskMessage is the subject, Msg-Id and envelope of rec's task.
func TaskMessage(rec schema.ImageSetRecord, now time.Time) (subject, msgID string, env *events.Envelope, err error) {
	t := schema.ImageSetTask{Seq: rec.Seq, Inputs: rec.Inputs}
	if rec.Item != nil {
		t.Item = *rec.Item
	}
	name, data, err := schema.Encode(t)
	if err != nil {
		return "", "", nil, err
	}
	id := events.MsgIDForImageSet(t.Item.UID, t.Seq)
	env = &events.Envelope{
		ID: id, Type: "catalog.ImageSetTask", Schema: name, Source: "catalog@" + version.String(),
		Key: t.Item.Namespace + "/" + t.Item.Name, Time: now.UTC(), Data: data,
	}
	return events.WorkImageSetSubject(events.MediaKey(t.Item.Kind, t.Item.Namespace, t.Item.Name)), id, env, nil
}

// clamp cuts s to n bytes on a rune boundary.
func clamp(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
