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
// clustarr-transcodes. It imports no Kubernetes client. A task of an older
// schema fails to decode and is dead-lettered like any undecodable payload.
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
	File            schema.Ref `json:"file"`
	Seq             int64      `json:"seq"`
	Class           string     `json:"class"`
	Profile         Profile    `json:"profile"`
	SourcePath      string     `json:"sourcePath"`
	SourceProbeHash string     `json:"sourceProbeHash"`
	SourceSizeBytes int64      `json:"sourceSizeBytes"`
	// OriginalLanguage is the file's movie's or series' original language
	// (TMDB's, "ja"), "" when unknown: the standard's "original" in a
	// profile's language lists, which no probe can read, so the worker plans
	// as the manager did (transcode.MediaInfo.OriginalLanguage).
	OriginalLanguage string          `json:"originalLanguage,omitempty"`
	OutputPath       string          `json:"outputPath"`
	Root             RootFolder      `json:"root"`
	OutputRoot       string          `json:"outputRoot,omitempty"`
	Engine           string          `json:"engine,omitempty"`
	PlanHash         string          `json:"planHash,omitempty"`
	Deadline         metav1.Duration `json:"deadline"`
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

// EngineFFgo is the engine every task runs on, in-process. (status.plan's
// engine enum still admits "ffmpeg": a plan recorded by the deleted argv
// engine, which dispatch plans again.)
const EngineFFgo = "ffgo"

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

// LeaseState is whether a lease is held by a worker or marks a withdrawal.
type LeaseState string

const (
	LeaseHeld      LeaseState = "held"
	LeaseCancelled LeaseState = "cancelled"
)
