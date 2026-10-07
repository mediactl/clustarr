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

package grafttask

import (
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/version"
)

// RecordSchema marks a clustarr-grafts record (loop spec §4.5).
const RecordSchema = "records.graft.v1"

// MaxRecordBytes is clustarr-grafts' MaxValueSize (loop spec §4.3).
const MaxRecordBytes = int(events.GraftsMaxValueSize)

// Record is one file's clustarr-grafts record (loop spec §4.5, §4.12): the
// loop's request (the header and the dispatch's Inputs) and the graft pod's
// Answer. A *Record satisfies records.Record through the embedded header.
type Record struct {
	schema.RecordHeader
	Inputs Inputs  `json:"inputs"`
	Answer *Result `json:"answer,omitempty"`
}

// Fact reports a graft that swapped the file: Succeeded with reason Grafted.
// It is written over a withdrawal and never overwritten before the loop
// incorporated it (loop spec §4.7, §4.8).
func Fact(r *Record) bool {
	return r != nil && r.Answer != nil && r.Answer.Phase == PhaseSucceeded && r.Answer.Reason == ReasonGrafted
}

// Spec is the graft remediation's records protocol.
var Spec = records.Spec[*Record]{
	Remediation: "graft", Bucket: events.BucketGrafts, Schema: RecordSchema, MaxValue: MaxRecordBytes,
	New:      func() *Record { return &Record{} },
	Answered: records.TerminalAnswered,
	Fact:     func(r *Record) bool { return r.State == records.StateAnswered && Fact(r) },
}

// NewRequester is the loop's half of clustarr-grafts (loop spec §4.5: the
// bucket opens only through its store).
func NewRequester(bus events.Bus) *records.Requester[*Record] {
	return records.NewRequester(bus.KV(events.BucketGrafts), Spec)
}

// NewAnswerer is a graft pod's half of clustarr-grafts, stamped with its pod.
func NewAnswerer(bus events.Bus, writer string) *records.Answerer[*Record] {
	return records.NewAnswerer(bus.KV(events.BucketGrafts), Spec, writer, version.String())
}
