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

package v1alpha1

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
)

// User intent on a MediaFile (loop spec §2.9; ADR-0016). The loop never
// writes these annotations; its MediaFile source wakes on a change to any
// of them. Three are one-shot nonces, recorded in status.handledNonces once
// handled; three are standing values the transcode block records in force.
const (
	// AnnotationSubtitleSearch forces a subtitle search now (a nonce;
	// handledNonces.subtitleSearch). It replaces SubtitleRequest
	// spec.forceSearch.
	AnnotationSubtitleSearch = "subtitle.clustarr.io/search"
	// AnnotationTranscodeRetry clears a Failed or Skipped verdict for the
	// current tag and resets attempts (a nonce; handledNonces.transcodeRetry).
	AnnotationTranscodeRetry = "transcode.clustarr.io/retry"
	// AnnotationTranscodeCancel withdraws any in-flight transcode and
	// records Skipped, reason Cancelled (a nonce; handledNonces.transcodeCancel).
	AnnotationTranscodeCancel = "transcode.clustarr.io/cancel"
	// AnnotationTranscodeSuspend is "true" to hold the file out of
	// admission; anything else is not suspended (transcode.suspended).
	AnnotationTranscodeSuspend = "transcode.clustarr.io/suspend"
	// AnnotationTranscodePriority is a decimal int32 admission orders by
	// (transcode.priority).
	AnnotationTranscodePriority = "transcode.clustarr.io/priority"
	// AnnotationTranscodeHardware is cpu, nvidia, intel, auto or gpu, taking
	// effect at the next dispatch (transcode.hardware).
	AnnotationTranscodeHardware = "transcode.clustarr.io/hardware"
)

// IntentAnnotations returns the six intent annotation keys, for the loop's
// MediaFile predicate.
func IntentAnnotations() []string {
	return []string{
		AnnotationSubtitleSearch, AnnotationTranscodeRetry, AnnotationTranscodeCancel,
		AnnotationTranscodeSuspend, AnnotationTranscodePriority, AnnotationTranscodeHardware,
	}
}

// nonceRE is a one-shot intent nonce: by convention the requester's Unix
// time, "fold-<generation>" from the migration.
var nonceRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// ValidNonce reports whether v is a well-formed one-shot intent nonce.
func ValidNonce(v string) bool { return nonceRE.MatchString(v) }

// HandledNonce is what status.handledNonces records for the annotation
// value v, and what a planner compares the record with: v itself when it is
// a valid nonce (or empty), else "invalid-" and the first 16 hex digits of
// v's SHA-256. An invalid value is recorded so that it is handled once, with
// one Warning Event, and the record must fit the field's 63 bytes and equal
// itself on every later pass, which a clamped copy of a long value would not.
// A nonce is pending when v is non-empty and HandledNonce(v) differs from
// the record.
func HandledNonce(v string) string {
	if v == "" || ValidNonce(v) {
		return v
	}
	sum := sha256.Sum256([]byte(v))
	return "invalid-" + hex.EncodeToString(sum[:8])
}

// HandledNonces records the last one-shot nonce handled for each one-shot
// intent annotation. A backlog file with no transcode block still records
// its transcode nonces here, which is why they are not inside the blocks.
type HandledNonces struct {
	// +optional
	// +kubebuilder:validation:MaxLength=63
	SubtitleSearch string `json:"subtitleSearch,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=63
	TranscodeRetry string `json:"transcodeRetry,omitempty"`
	// +optional
	// +kubebuilder:validation:MaxLength=63
	TranscodeCancel string `json:"transcodeCancel,omitempty"`
}
