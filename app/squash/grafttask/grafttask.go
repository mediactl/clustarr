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

// Package grafttask is the contract between squasharr's AudioGraft
// controller and the graft run of squasharr-worker (anime dual-audio spec
// §7.2): the Task a graft Job's pod is given as --graft-task, and the
// Result it leaves in its termination message. It links no FFmpeg, so the
// controller (cmd/clustarr) and the worker share it.
package grafttask

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// Task is one graft: every path a logical /data path.
type Task struct {
	// Graft names the AudioGraft (<namespace>/<name>), for logs.
	Graft string `json:"graft"`
	// Target is the library file the dub is muxed into, and
	// TargetProbeHash its probe hash when the graft was planned: a file
	// that changed since is not swapped.
	Target          string `json:"target"`
	TargetProbeHash string `json:"targetProbeHash"`
	// Root is the target's RootFolder path: the target and the donor must
	// lie under it.
	Root string `json:"root"`
	// Donor is the donor as importarr placed it.
	Donor string `json:"donor"`
	// Language is the track to graft, Anchor the language both files carry
	// (BCP-47); Default makes the grafted track the default.
	Language string `json:"language"`
	Anchor   string `json:"anchor"`
	Default  bool   `json:"default,omitempty"`
	// RecycleBin receives the target's original name before the swap; empty
	// is none.
	RecycleBin string `json:"recycleBin,omitempty"`
}

// Validate reports a Task the worker cannot run.
func (t Task) Validate() error {
	switch {
	case t.Target == "" || t.Donor == "" || t.Root == "":
		return fmt.Errorf("grafttask: target, donor and root are required")
	case t.TargetProbeHash == "":
		return fmt.Errorf("grafttask: targetProbeHash is required")
	case t.Language == "" || t.Anchor == "":
		return fmt.Errorf("grafttask: language and anchor are required")
	}
	return nil
}

// Phases of a Result.
const (
	PhaseSucceeded = "Succeeded"
	PhaseFailed    = "Failed"
)

// Reasons of a Result: why it failed, or Grafted/Present.
const (
	ReasonGrafted            = "Grafted"
	ReasonPresent            = "Present" // the target already carries the language
	ReasonInvalidTask        = "InvalidTask"
	ReasonUnsupported        = "UnsupportedContainer"
	ReasonTargetChanged      = "TargetChanged"
	ReasonTargetLacksAnchor  = "TargetLacksAnchor"
	ReasonDonorLacksLanguage = "DonorLacksLanguage"
	ReasonAlignmentRejected  = "AlignmentRejected"
	ReasonVerifyFailed       = "VerifyFailed"
	ReasonMuxFailed          = "MuxFailed"
	ReasonError              = "Error"
)

// Segment is one run of the alignment, in milliseconds.
type Segment struct {
	DonorStartMillis  int64 `json:"donorStartMillis"`
	TargetStartMillis int64 `json:"targetStartMillis"`
	LengthMillis      int64 `json:"lengthMillis"`
}

// Result is what a graft run reports.
type Result struct {
	Phase           string    `json:"phase"`
	Reason          string    `json:"reason"`
	Message         string    `json:"message,omitempty"`
	RateName        string    `json:"rateName,omitempty"`
	RateMicros      int64     `json:"rateMicros,omitempty"`
	RateMarginMilli int32     `json:"rateMarginMilli,omitempty"`
	CoveragePercent int32     `json:"coveragePercent,omitempty"`
	Segments        []Segment `json:"segments,omitempty"`
	ResidualMillis  int32     `json:"residualMillis,omitempty"`
	Within80Percent int32     `json:"within80Percent,omitempty"`
	GraftTag        string    `json:"graftTag,omitempty"`
	// DonorAudio is the donor reduced to its audio, as the run left it.
	DonorAudio      string `json:"donorAudio,omitempty"`
	OutputSizeBytes int64  `json:"outputSizeBytes,omitempty"`
}

// MaxMessage bounds Result.Message: the termination message the Result
// travels in is 4096 bytes, and the AudioGraft's status.message 2048.
const MaxMessage = 1024

// Failed is a failed Result.
func Failed(reason string, format string, args ...any) Result {
	return Result{Phase: PhaseFailed, Reason: reason, Message: Clamp(fmt.Sprintf(format, args...))}
}

// Clamp cuts s to MaxMessage bytes on a rune boundary.
func Clamp(s string) string {
	if len(s) <= MaxMessage {
		return s
	}
	s = s[:MaxMessage]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// Encode is the Result as the termination message carries it.
func (r Result) Encode() []byte {
	r.Message = Clamp(r.Message)
	b, _ := json.Marshal(r)
	return b
}

// Decode reads a termination message.
func Decode(b []byte) (Result, error) {
	var r Result
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("grafttask: decode result: %w", err)
	}
	if r.Phase != PhaseSucceeded && r.Phase != PhaseFailed {
		return r, fmt.Errorf("grafttask: result phase %q", r.Phase)
	}
	return r, nil
}
