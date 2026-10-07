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

package task

import (
	"time"

	"github.com/mediactl/clustarr/app/transcode/grafttask"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/version"
)

// Answer is a worker's answer to one dispatch (loop spec §4.12). Message is
// clamped to MaxAnswerMessage and StderrTail to MaxAnswerStderr before the
// compare-and-swap.
type Answer struct {
	Outcome    Outcome           `json:"outcome"`
	Reason     Reason            `json:"reason,omitempty"`
	Message    string            `json:"message,omitempty"`
	Class      string            `json:"class,omitempty"`
	Pod        string            `json:"pod,omitempty"`
	Node       string            `json:"node,omitempty"`
	StartedAt  *time.Time        `json:"startedAt,omitempty"`
	FinishedAt *time.Time        `json:"finishedAt,omitempty"`
	Result     *Result           `json:"result,omitempty"`
	StderrTail string            `json:"stderrTail,omitempty"`
	Graft      *grafttask.Result `json:"graft,omitempty"`
}

// The answer's bounds: the record's value fits clustarr-transcodes'
// MaxValueSize with room for the rest.
const (
	MaxAnswerMessage = 1024
	MaxAnswerStderr  = 4096
)

// RecordSchema is a transcode record's RecordHeader.Schema.
const RecordSchema = "records.transcode.v1"

// Record is one MediaFile's clustarr-transcodes value: the loop's request
// (the header and the dispatch's Inputs) and the worker's Answer.
type Record struct {
	schema.RecordHeader
	Inputs Inputs  `json:"inputs"`
	Answer *Answer `json:"answer,omitempty"`
}

// RecordSpec is the transcode remediation's records protocol: a succeeded
// answer is a fact -- the file already changed on disk -- which is never
// overwritten before the loop incorporated it, and which a worker writes
// even over a withdrawal at its Seq (§4.7, §4.8).
var RecordSpec = records.Spec[*Record]{
	Remediation: "transcode", Bucket: events.BucketTranscodes, Schema: RecordSchema, MaxValue: events.TranscodesMaxValueSize,
	New:      func() *Record { return &Record{} },
	Answered: records.TerminalAnswered,
	Fact: func(r *Record) bool {
		return r.State == records.StateAnswered && r.Answer != nil && r.Answer.Outcome == OutcomeSucceeded
	},
}

// Requests is the loop's half over kv, clustarr-transcodes.
func Requests(kv events.KV) *records.Requester[*Record] { return records.NewRequester(kv, RecordSpec) }

// Answers is a worker's half over kv, stamped with its pod.
func Answers(kv events.KV, pod string) *records.Answerer[*Record] {
	return records.NewAnswerer(kv, RecordSpec, pod, version.String())
}
