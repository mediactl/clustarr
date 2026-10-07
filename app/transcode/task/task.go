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

// Package task holds what crosses NATS between the manager and the transcode
// pools (spec §6, §17.5, §18.1; loop spec §4.12): the Task the remediation
// loop publishes for one MediaFile's dispatch, the Lease a worker holds in
// clustarr-transcode-leases, and the Record a worker answers through in
// clustarr-transcodes. It imports no Kubernetes client. TaskV1, LeaseV1 and
// StatusEvent are the TranscodeJob controller's, until F6.6 deletes it.
package task

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/grafttask"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// Profile is the TranscodeProfile snapshot a task was planned under.
type Profile struct {
	Name     string                                 `json:"name"`
	Hash     string                                 `json:"hash"`
	Spec     transcodev1alpha1.TranscodeProfileSpec `json:"spec"`
	Hardware *transcodev1alpha1.Hardware            `json:"hardware,omitempty"`
}

// RootFolder is the library root the source lives under, resolved by transcode.
type RootFolder struct {
	Path       string `json:"path"`
	RecycleBin string `json:"recycleBin"`
}

// Inputs are what a transcode record is matched by (loop spec §4.10): the
// record answers this dispatch only when they are the block's.
type Inputs struct {
	ProfileHash string `json:"profileHash"`
	PlanHash    string `json:"planHash"`
	Class       string `json:"class"`
	SourcePath  string `json:"sourcePath"`
	ProbeHash   string `json:"probeHash"`
	OutputPath  string `json:"outputPath"`
}

// Task is one dispatch of one MediaFile's transcode (loop spec §4.12): File
// replaces v1's Job, Seq its Attempt, and Inputs are what the record is
// matched by.
type Task struct {
	File            schema.Ref      `json:"file"`
	Seq             int64           `json:"seq"`
	Class           string          `json:"class"`
	Profile         Profile         `json:"profile"`
	SourcePath      string          `json:"sourcePath"`
	SourceProbeHash string          `json:"sourceProbeHash"`
	SourceSizeBytes int64           `json:"sourceSizeBytes"`
	SourceModifier  string          `json:"sourceModifier,omitempty"`
	OutputPath      string          `json:"outputPath"`
	Root            RootFolder      `json:"root"`
	OutputRoot      string          `json:"outputRoot,omitempty"`
	Engine          string          `json:"engine,omitempty"`
	PlanHash        string          `json:"planHash,omitempty"`
	Deadline        metav1.Duration `json:"deadline"`
	// Graft is an audio graft riding along with this transcode (anime
	// dual-audio, phase 4 addendum; joined again by F7.2).
	Graft  *grafttask.Task `json:"graft,omitempty"`
	Inputs Inputs          `json:"inputs"`
}

// Schema implements schema.Payload.
func (Task) Schema() string { return "transcode.Task.v2" }

// Lease is one MediaFile's transcode claim in clustarr-transcode-leases at
// events.TranscodeLeaseKey(file UID); a cancelled lease applies to its Seq
// and earlier ones.
type Lease struct {
	File  schema.Ref `json:"file"`
	Seq   int64      `json:"seq"`
	State LeaseState `json:"state"`
	Pod   string     `json:"pod,omitempty"`
	Node  string     `json:"node,omitempty"`
	Since time.Time  `json:"since"`
}

// Schema implements schema.Payload.
func (Lease) Schema() string { return "transcode.Lease.v2" }

// TaskV1 is one dispatch of one TranscodeJob (the doomed controller's).
type TaskV1 struct {
	Job             schema.Ref `json:"job"`
	Attempt         int32      `json:"attempt"`
	Class           string     `json:"class"`
	Profile         Profile    `json:"profile"`
	SourcePath      string     `json:"sourcePath"`
	SourceProbeHash string     `json:"sourceProbeHash"`
	SourceSizeBytes int64      `json:"sourceSizeBytes"`
	SourceModifier  string     `json:"sourceModifier,omitempty"`
	OutputPath      string     `json:"outputPath"`
	Root            RootFolder `json:"root"`
	OutputRoot      string     `json:"outputRoot,omitempty"`
	// Engine is the engine the recorded plan was made for: EngineFFgo.
	Engine string `json:"engine,omitempty"`
	// PlanHash is the recorded plan's hash; the worker compares its own
	// plan's with it.
	PlanHash string          `json:"planHash,omitempty"`
	Deadline metav1.Duration `json:"deadline"`

	// Graft is an audio graft riding along with this transcode (anime
	// dual-audio, phase 4 addendum): the worker aligns the donor before it
	// encodes and muxes the dub in with the transcode. Its Target is the
	// source; nil for a plain transcode.
	Graft *grafttask.Task `json:"graft,omitempty"`
}

// EngineFFgo is the engine every task runs on, in-process. (status.plan's
// engine enum still admits "ffmpeg": a plan recorded by the deleted argv
// engine, which dispatch plans again.)
const EngineFFgo = "ffgo"

// Schema implements schema.Payload.
func (TaskV1) Schema() string { return "transcode.Task.v1" }

// Outcome is how an attempt ended.
type Outcome string

const (
	OutcomeSucceeded Outcome = "succeeded"
	OutcomeSkipped   Outcome = "skipped"
	OutcomeFailed    Outcome = "failed"
	OutcomeCancelled Outcome = "cancelled"
)

// Reason qualifies an Outcome (spec §18.1, §18.3).
type Reason string

const (
	ReasonInvalidSource    Reason = "InvalidSource"
	ReasonSourceChanged    Reason = "SourceChanged"
	ReasonVerifyFailed     Reason = "VerifyFailed"
	ReasonDeadlineExceeded Reason = "DeadlineExceeded"
	ReasonRetriable        Reason = "Retriable"
	ReasonGPUUnavailable   Reason = "GPUUnavailable"
	ReasonGPUEncodeFailed  Reason = "GPUEncodeFailed"
	ReasonCancelled        Reason = "Cancelled"

	// Decided by transcode, never reported by a worker.
	ReasonRetriesExhausted Reason = "RetriesExhausted"
	ReasonDeadLettered     Reason = "DeadLettered"
)

// EventKind is what a StatusEvent reports.
type EventKind string

const (
	EventClaimed  EventKind = "claimed"
	EventProgress EventKind = "progress"
	EventFinished EventKind = "finished"
)

// StatusEvent is one report from the worker running a delivery of an
// attempt. Seq counts from 1 within one delivery; the Msg-Id
// <uid>/<attempt>/<delivery>/<seq> makes a re-publish a duplicate.
//
// Class is the hardware class of the pool the task was taken from. With
// Attempt it proves which dispatch the worker is running, so transcode can
// adopt an attempt whose Queued write was lost after the publish, and correct
// the class when a re-dispatch's publish was absorbed as a duplicate of an
// earlier one to another pool.
type StatusEvent struct {
	Job        schema.Ref                  `json:"job"`
	Attempt    int32                       `json:"attempt"`
	Class      transcodev1alpha1.Hardware  `json:"class,omitempty"`
	Delivery   uint64                      `json:"delivery"`
	Seq        uint64                      `json:"seq"`
	Kind       EventKind                   `json:"kind"`
	Pod        string                      `json:"pod,omitempty"`
	Node       string                      `json:"node,omitempty"`
	Progress   *transcodev1alpha1.Progress `json:"progress,omitempty"`
	Outcome    Outcome                     `json:"outcome,omitempty"`
	Reason     Reason                      `json:"reason,omitempty"`
	Message    string                      `json:"message,omitempty"`
	Result     *transcodev1alpha1.Result   `json:"result,omitempty"`
	StderrTail string                      `json:"stderrTail,omitempty"`
	// Graft is what came of the task's graft, when it carried one.
	Graft *grafttask.Result `json:"graft,omitempty"`
	At    time.Time         `json:"at"`
}

// Schema implements schema.Payload.
func (StatusEvent) Schema() string { return "transcode.StatusEvent.v1" }

// LeaseState is whether a lease is held by a worker or marks a withdrawal.
type LeaseState string

const (
	LeaseHeld      LeaseState = "held"
	LeaseCancelled LeaseState = "cancelled"
)

// LeaseV1 is one TranscodeJob's claim (the doomed controller's). A cancelled
// lease applies to its own Attempt and earlier ones only (spec §6).
type LeaseV1 struct {
	Job     schema.Ref `json:"job"`
	Attempt int32      `json:"attempt"`
	State   LeaseState `json:"state"`
	Pod     string     `json:"pod,omitempty"`
	Node    string     `json:"node,omitempty"`
	Since   time.Time  `json:"since"`
}

// Schema implements schema.Payload.
func (LeaseV1) Schema() string { return "transcode.Lease.v1" }
