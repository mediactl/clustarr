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

// Result is a transcode's output as the worker answers it in
// clustarr-transcodes (Answer.Result). It moved here from the API when the
// per-file transcode object was removed (loop spec §7.4), keeping its JSON
// names; the output's probe is never carried, since status.mediaInfo holds
// it after the re-probe (loop spec §4.12).
type Result struct {
	// OutputPath is where the output was written.
	OutputPath string `json:"outputPath,omitempty"`
	// OutputSizeBytes is the output file size.
	OutputSizeBytes int64 `json:"outputSizeBytes,omitempty"`
	// OutputToSourcePercent is the output size as a percentage of the
	// source size, so an output 45% the size of its source is 45.
	OutputToSourcePercent int32 `json:"outputToSourcePercent,omitempty"`
	// VMAFCentis is the measured VMAF score in hundredths, so 95.42 is 9542.
	VMAFCentis *int32 `json:"vmafCentis,omitempty"`
	// KeptVideo is set when the planned encode came out over
	// policy.maxOutputToSourcePercent and the worker copied the source's
	// video into the MP4 layout instead (standard.KeepVideo): the output is
	// that remux, tagged CLUSTARR_VIDEO=keep, and status.transcode.plan
	// records it so the planner reads the file as final.
	KeptVideo *KeptVideo `json:"keptVideo,omitempty"`
}

// KeptVideo is the remux that ran in place of an encode no smaller than its
// source.
type KeptVideo struct {
	// PlanHash is the remux's standard.Result.Hash.
	PlanHash string `json:"planHash"`
	// EncodePercent is the refused encode's output as a percentage of the
	// source.
	EncodePercent int32 `json:"encodePercent"`
}
