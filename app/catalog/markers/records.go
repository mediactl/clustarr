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

package markers

import (
	"context"
	"fmt"
	"time"
	"unicode/utf8"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/version"
)

// markersRequestTimeout is how long a request (or a deferral past its
// reset) stands before the loop asks again (§4.12).
const markersRequestTimeout = 24 * time.Hour

// maxValue is clustarr-markers' MaxValueSize (§4.3).
const maxValue = events.MarkersMaxValueSize

// maxMessage is FileMarkers.Message's MaxLength and MarkersAnswer.Message's
// bound; maxAnswerSegments is MarkersAnswer.Segments'.
const (
	maxMessage        = 512
	maxAnswerSegments = 20
)

// spec is the clustarr-markers instantiation of pkg/records. No markers
// answer is a fact: nothing on disk changes.
func spec() records.Spec[*schema.MarkersRecord] {
	return records.Spec[*schema.MarkersRecord]{
		Remediation: "markers", Bucket: events.BucketMarkers, Schema: schema.MarkersRecordSchema, MaxValue: maxValue,
		New:      func() *schema.MarkersRecord { return &schema.MarkersRecord{} },
		Answered: records.TerminalAnswered,
		Fact:     func(*schema.MarkersRecord) bool { return false },
	}
}

// Records is the loop's read half of clustarr-markers; the loop writes its
// requests as remediation.RecordWrite effects.
type Records struct {
	q *records.Requester[*schema.MarkersRecord]
}

// NewRecords is the read half over bus's clustarr-markers bucket.
func NewRecords(bus events.Bus) *Records {
	return &Records{q: records.NewRequester(bus.KV(events.BucketMarkers), spec())}
}

// Get reads uid's record, its revision, and whether there is one.
func (r *Records) Get(ctx context.Context, uid string) (*schema.MarkersRecord, uint64, bool, error) {
	return r.q.Get(ctx, events.RecordKey(uid))
}

// Answers is the worker's half of clustarr-markers.
type Answers struct {
	a *records.Answerer[*schema.MarkersRecord]
}

// NewAnswers is the worker's half, writing as writer (the pod).
func NewAnswers(bus events.Bus, writer string) *Answers {
	return &Answers{a: records.NewAnswerer(bus.KV(events.BucketMarkers), spec(), writer, version.String())}
}

// Superseded reports whether t's answer would be dropped: a newer request,
// a withdrawal or an answer at its Seq.
func (a *Answers) Superseded(ctx context.Context, t schema.MarkersTask) (bool, error) {
	return a.a.Superseded(ctx, events.RecordKey(t.File.UID), t.Seq)
}

// Defer postpones t's record to until, a spent key's reset.
func (a *Answers) Defer(ctx context.Context, t schema.MarkersTask, until time.Time) (records.Verdict, error) {
	return a.a.Defer(ctx, events.RecordKey(t.File.UID), t.Seq, until)
}

// Answer writes ans for t (state answered; state failed, transient, with
// ans.Message as the failure when failed), clamping Message to 512 bytes and
// Segments to 20.
func (a *Answers) Answer(ctx context.Context, t schema.MarkersTask, ans schema.MarkersAnswer, failed bool) (records.Verdict, error) {
	ans.Message = clamp(ans.Message)
	if len(ans.Segments) > maxAnswerSegments {
		ans.Segments = ans.Segments[:maxAnswerSegments]
	}
	rec := &schema.MarkersRecord{Inputs: t.Inputs, SeriesKey: t.SeriesKey, Answer: &ans}
	rec.Schema, rec.MediaFile, rec.Seq, rec.State = schema.MarkersRecordSchema, t.File, t.Seq, records.StateAnswered
	if failed {
		rec.State, rec.Failure, rec.Transient = records.StateFailed, ans.Message, true
	}
	return a.a.Answer(ctx, events.RecordKey(t.File.UID), rec)
}

// TaskMessage is the subject, Msg-Id and envelope of rec's task.
func TaskMessage(rec schema.MarkersRecord, now time.Time) (subject, msgID string, env *events.Envelope, err error) {
	return taskMessage(schema.MarkersTask{File: rec.MediaFile, Seq: rec.Seq, Inputs: rec.Inputs, SeriesKey: rec.SeriesKey},
		events.MsgIDForMarkers(rec.MediaFile.UID, rec.Seq), now)
}

// DeferredTask is t republished for at, a spent key's reset.
func DeferredTask(t schema.MarkersTask, now, at time.Time) (subject, msgID string, env *events.Envelope, err error) {
	return taskMessage(t, events.MsgIDForMarkersAt(t.File.UID, t.Seq, at), now)
}

func taskMessage(t schema.MarkersTask, id string, now time.Time) (string, string, *events.Envelope, error) {
	name, data, err := schema.Encode(t)
	if err != nil {
		return "", "", nil, err
	}
	env := &events.Envelope{
		ID: id, Type: "catalog.MarkersTask", Schema: name, Source: "catalogarr@" + version.String(),
		Key: t.File.Namespace + "/" + t.File.Name, Time: now.UTC(), Data: data,
	}
	return events.WorkMarkersSubject(events.MediaKey("mediafile", t.File.Namespace, t.File.Name)), id, env, nil
}

// NotAskedMultiEpisode and NotAskedOrder are the NotFound messages of the
// queries the loop decides without asking, byte for byte what the handler
// wrote before the fold (TestTheLoopsNotAskedMessagesMatchTheWorkers).
func NotAskedMultiEpisode(others int) string {
	return fmt.Sprintf("the file holds %d more episodes, whose segments TheIntroDB times per episode%s", others, notAskedSuffix)
}

// NotAskedOrder is an episode in another order than the aired one's message.
func NotAskedOrder(order catalogv1alpha1.EpisodeOrder) string {
	return fmt.Sprintf("episode order %s is not the aired order TheIntroDB numbers episodes in%s", order, notAskedSuffix)
}

const notAskedSuffix = ": not asked: metadata: not found"

// clamp cuts s to maxMessage bytes on a rune boundary.
func clamp(s string) string {
	if len(s) <= maxMessage {
		return s
	}
	cut := maxMessage
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
