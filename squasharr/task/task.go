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

// Package task holds what crosses NATS between squasharr and its transcode
// pools (spec §6, §17.5, §18.1): the Task squasharr publishes, the
// StatusEvents a worker publishes on clustarr.work.transcode.result.<uid>,
// and the Lease a worker holds in clustarr-transcode-leases. It imports no
// Kubernetes client.
package task

import (
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// Profile is the TranscodeProfile snapshot a task was planned under.
type Profile struct {
	Name     string                                 `json:"name"`
	Hash     string                                 `json:"hash"`
	Spec     transcodev1alpha1.TranscodeProfileSpec `json:"spec"`
	Hardware *transcodev1alpha1.Hardware            `json:"hardware,omitempty"`
}

// RootFolder is the library root the source lives under, resolved by squasharr.
type RootFolder struct {
	Path       string `json:"path"`
	RecycleBin string `json:"recycleBin"`
}

// Task is one dispatch of one TranscodeJob.
type Task struct {
	Job             schema.Ref      `json:"job"`
	Attempt         int32           `json:"attempt"`
	Class           string          `json:"class"`
	Profile         Profile         `json:"profile"`
	SourcePath      string          `json:"sourcePath"`
	SourceProbeHash string          `json:"sourceProbeHash"`
	SourceSizeBytes int64           `json:"sourceSizeBytes"`
	SourceModifier  string          `json:"sourceModifier,omitempty"`
	OutputPath      string          `json:"outputPath"`
	Root            RootFolder      `json:"root"`
	OutputRoot      string          `json:"outputRoot,omitempty"`
	ArgsHash        string          `json:"argsHash,omitempty"`
	Deadline        metav1.Duration `json:"deadline"`
}

// Schema implements schema.Payload.
func (Task) Schema() string { return "transcode.Task.v1" }

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

	// Decided by squasharr, never reported by a worker.
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
type StatusEvent struct {
	Job        schema.Ref                  `json:"job"`
	Attempt    int32                       `json:"attempt"`
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
	At         time.Time                   `json:"at"`
}

// Schema implements schema.Payload.
func (StatusEvent) Schema() string { return "transcode.StatusEvent.v1" }

// LeaseState is whether a lease is held by a worker or marks a withdrawal.
type LeaseState string

const (
	LeaseHeld      LeaseState = "held"
	LeaseCancelled LeaseState = "cancelled"
)

// Lease is one TranscodeJob's claim. A cancelled lease applies to its own
// Attempt and earlier ones only (spec §6).
type Lease struct {
	Job     schema.Ref `json:"job"`
	Attempt int32      `json:"attempt"`
	State   LeaseState `json:"state"`
	Pod     string     `json:"pod,omitempty"`
	Node    string     `json:"node,omitempty"`
	Since   time.Time  `json:"since"`
}

// Schema implements schema.Payload.
func (Lease) Schema() string { return "transcode.Lease.v1" }
