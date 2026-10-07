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

// JobEvent reports a transition of a MediaFile's transcode (loop spec
// §5.16). Subject:
// clustarr.evt.transcode.job.<queued|started|succeeded|failed|skipped|withdrawn>.<mfUID>,
// Msg-Id <mfUID>/transcode/<seq>/<action>.
type JobEvent struct {
	// File is the MediaFile the transcode is of.
	File Ref `json:"file"`

	// Seq is the dispatch the transition is of (status.transcode.dispatch.seq).
	Seq int64 `json:"seq"`

	// Action is one of queued, started, succeeded, failed, skipped or
	// withdrawn.
	Action string `json:"action"`

	// ProfileRef is the TranscodeProfile the file is transcoded under.
	ProfileRef *Ref `json:"profileRef,omitempty"`

	// InputPath is the source file.
	InputPath string `json:"inputPath,omitempty"`

	// OutputPath is the encoded file, once one exists.
	OutputPath string `json:"outputPath,omitempty"`

	// Mode is the plan mode chosen, e.g. "transcode" or "remuxOnly".
	Mode string `json:"mode,omitempty"`

	// Encoder is the encoder the plan selected.
	Encoder string `json:"encoder,omitempty"`

	// Reason explains a skip, a failure or a withdrawal.
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
func (JobEvent) Schema() string { return "transcode.JobEvent.v2" }

// TranscodeProgress is 1 Hz telemetry a pool worker writes into the
// clustarr-progress key/value bucket at transcode.<mfUID> (loop spec
// §4.12). It is never persisted to a stream nor written to etcd; the ui
// reads it.
type TranscodeProgress struct {
	// File is the MediaFile being transcoded.
	File Ref `json:"file"`

	// Seq is the dispatch being run.
	Seq int64 `json:"seq"`

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
func (TranscodeProgress) Schema() string { return "transcode.Progress.v2" }
