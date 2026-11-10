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

// LeafPhase is the coarse lifecycle state of a non-video leaf item -- an
// Album, Book, Audiobook or Issue -- one vocabulary for the four, with
// Movie's precedence (docs/superpowers/specs/2026-11-10-non-video-libraries-design.md
// D4): Downloading, Unmonitored, Pending, Imported, Delayed,
// CutoffUnevaluated, CutoffUnmet, Unavailable, Wanted. Until 2026-11-10 each
// kind had its own six-value enum (Wanted, Delayed, Downloading, Imported,
// CutoffUnmet, Unmonitored) and Issue Mylar's state names.
//
// +kubebuilder:validation:Enum=Pending;Unavailable;Wanted;Delayed;Downloading;Imported;CutoffUnmet;CutoffUnevaluated;Unmonitored
type LeafPhase string

// Leaf phases.
const (
	// LeafPhasePending: no metadata yet, so the item cannot be searched.
	LeafPhasePending LeafPhase = "Pending"
	// LeafPhaseUnavailable: monitored, no file, not released yet.
	LeafPhaseUnavailable LeafPhase = "Unavailable"
	// LeafPhaseWanted: monitored, released, no file, nothing in flight.
	LeafPhaseWanted LeafPhase = "Wanted"
	// LeafPhaseDelayed: a chosen release waiting out a DelayProfile delay.
	LeafPhaseDelayed LeafPhase = "Delayed"
	// LeafPhaseDownloading: a grab entry is live for the item.
	LeafPhaseDownloading LeafPhase = "Downloading"
	// LeafPhaseImported: every file is on disk and meets the cutoff.
	LeafPhaseImported LeafPhase = "Imported"
	// LeafPhaseCutoffUnmet: a file is on disk below the profile's cutoff, or
	// an album has a track without a file (cutoffReason TracksMissing).
	LeafPhaseCutoffUnmet LeafPhase = "CutoffUnmet"
	// LeafPhaseCutoffUnevaluated: a file is on disk and the profile cannot
	// be resolved, so the file is unjudged.
	LeafPhaseCutoffUnevaluated LeafPhase = "CutoffUnevaluated"
	// LeafPhaseUnmonitored: the item, or its container, is not monitored.
	LeafPhaseUnmonitored LeafPhase = "Unmonitored"
)

// The cutoffReason values a non-video leaf writes beside its phase (D4);
// video items carry Episode's richer set in the same field.
const (
	CutoffReasonPending           = "Pending"
	CutoffReasonProfileUnresolved = "ProfileUnresolved"
	CutoffReasonCutoffMet         = "CutoffMet"
	CutoffReasonCutoffUnmet       = "CutoffUnmet"
	// CutoffReasonTracksMissing is an album's: a track of the selected
	// release has no file (Lidarr's TracksWithoutFiles clause).
	CutoffReasonTracksMissing = "TracksMissing"
)
