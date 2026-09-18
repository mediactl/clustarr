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

package schema

import "time"

// JobEvent reports a transition of a TranscodeJob. Subject:
// clustarr.evt.transcode.job.<queued|started|succeeded|failed|skipped>.<uid>.
type JobEvent struct {
	// JobRef is the TranscodeJob the event is about.
	JobRef Ref `json:"jobRef"`

	// Action is one of queued, started, succeeded, failed or skipped.
	Action string `json:"action"`

	// ProfileRef is the TranscodeProfile the job runs under.
	ProfileRef *Ref `json:"profileRef,omitempty"`

	// InputPath is the source file.
	InputPath string `json:"inputPath,omitempty"`

	// OutputPath is the encoded file, once one exists.
	OutputPath string `json:"outputPath,omitempty"`

	// Mode is the plan mode chosen, e.g. "transcode" or "remux".
	Mode string `json:"mode,omitempty"`

	// Encoder is the encoder the plan selected.
	Encoder string `json:"encoder,omitempty"`

	// Reason explains a skip or a failure.
	Reason string `json:"reason,omitempty"`

	// InputBytes is the source size.
	InputBytes int64 `json:"inputBytes,omitempty"`

	// OutputBytes is the encoded size.
	OutputBytes int64 `json:"outputBytes,omitempty"`

	// SavedPercent is the size reduction as a whole percentage, 0..100.
	SavedPercent int32 `json:"savedPercent,omitempty"`

	// DurationSeconds is how long the encode ran.
	DurationSeconds int64 `json:"durationSeconds,omitempty"`

	// At is when the transition happened.
	At time.Time `json:"at"`
}

// Schema implements Payload.
func (JobEvent) Schema() string { return "transcode.JobEvent.v1" }

// TranscodeProgress is 1 Hz telemetry published on core NATS at
// clustarr.progress.transcode.<uid> and mirrored into the clustarr-progress
// key/value bucket. It is never persisted to a stream.
type TranscodeProgress struct {
	// JobRef is the TranscodeJob this telemetry is for.
	JobRef Ref `json:"jobRef"`

	// PercentMilli is completion in thousandths of a percent, 0..100000.
	PercentMilli int32 `json:"percentMilli"`

	// Frame is the frame number ffmpeg last reported.
	Frame int64 `json:"frame,omitempty"`

	// FPSCentis is the encode rate in hundredths of a frame per second, so
	// 23.98 fps is 2398.
	FPSCentis int32 `json:"fpsCentis,omitempty"`

	// SpeedCentis is the encode speed relative to realtime, in hundredths,
	// so 1.25x is 125.
	SpeedCentis int32 `json:"speedCentis,omitempty"`

	// OutTimeMillis is the output timestamp reached.
	OutTimeMillis int64 `json:"outTimeMillis,omitempty"`

	// TotalMillis is the total duration to encode.
	TotalMillis int64 `json:"totalMillis,omitempty"`

	// OutputBytes is how much has been written so far.
	OutputBytes int64 `json:"outputBytes,omitempty"`

	// WorkerRef is the worker Pod producing the sample.
	WorkerRef *Ref `json:"workerRef,omitempty"`

	// At is when the sample was taken.
	At time.Time `json:"at"`
}

// Schema implements Payload.
func (TranscodeProgress) Schema() string { return "transcode.Progress.v1" }
