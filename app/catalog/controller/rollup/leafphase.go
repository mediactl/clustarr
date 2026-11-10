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

package rollup

import (
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// LeafInputs is what a non-video leaf -- an Album, Book, Audiobook or Issue
// -- reads before it computes its phase (2026-11-10 design D4): the one set
// of facts the four kinds share, so the four kinds' phase helpers are one
// function and one table test.
type LeafInputs struct {
	// Monitored is the item's own flag and its container's (D3: an Album
	// is monitored only while its Artist is; itemread.Monitored).
	Monitored bool
	// MetadataReady is whether the item's metadata document has been
	// fetched; until then the item cannot be searched and reads Pending.
	MetadataReady bool
	// Released is whether the item's release date has passed, or it has
	// none (an undated item counts as out, as the search worker's
	// availability rule has it).
	Released bool
	// HasFile is whether any file backs the item (an album: any track).
	HasFile bool
	// CutoffMet is the file's verdict against the profile's cutoff
	// (rollup.FileState), meaningful only when CutoffEvaluated.
	CutoffMet bool
	// CutoffEvaluated is whether the QualityProfile resolved, so CutoffMet
	// is a verdict at all; a dangling profile reads CutoffUnevaluated,
	// never CutoffUnmet, which would put the item in the upgrade rotation.
	CutoffEvaluated bool
	// TracksMissing is an album's: a track of the selected release has no
	// file (Lidarr's TracksWithoutFiles clause), so the album is below
	// its cutoff whatever its files' quality (D6).
	TracksMissing bool
	// PendingGrab is whether status.pendingGrab is set: a chosen release
	// waiting out a DelayProfile delay.
	PendingGrab bool
}

// LeafPhase computes a non-video leaf's phase and its cutoffReason from in,
// with Movie's precedence (movie.Phase), highest first:
//
//	Downloading        a live grab entry -- the caller's overlay (EntryOverlay), over all of these
//	Unmonitored
//	Pending            metadata not ready
//	Imported           every file on disk and at the cutoff (an album: every track too)
//	Delayed            a pending grab
//	CutoffUnevaluated  a file, no profile to rank it against
//	CutoffUnmet        a file below the cutoff, or an album with a track missing
//	Unavailable        not released yet
//	Wanted
//
// There is no Transcoded arm: an album's track, a book, an audiobook's
// part and an issue are never transcoded (D9). The cutoffReason is one of
// the commonv1.CutoffReason* values: Pending while no file backs the item,
// ProfileUnresolved while a file is unjudged, TracksMissing for the album
// clause, else CutoffMet or CutoffUnmet.
func LeafPhase(in LeafInputs) (commonv1.LeafPhase, string) {
	reason := leafCutoffReason(in)
	switch {
	case !in.Monitored:
		return commonv1.LeafPhaseUnmonitored, reason
	case !in.MetadataReady:
		return commonv1.LeafPhasePending, reason
	case in.HasFile && in.CutoffEvaluated && in.CutoffMet && !in.TracksMissing:
		return commonv1.LeafPhaseImported, reason
	case in.PendingGrab:
		return commonv1.LeafPhaseDelayed, reason
	case in.HasFile && !in.CutoffEvaluated:
		return commonv1.LeafPhaseCutoffUnevaluated, reason
	case in.HasFile:
		return commonv1.LeafPhaseCutoffUnmet, reason
	case !in.Released:
		return commonv1.LeafPhaseUnavailable, reason
	default:
		return commonv1.LeafPhaseWanted, reason
	}
}

func leafCutoffReason(in LeafInputs) string {
	switch {
	case !in.HasFile:
		return commonv1.CutoffReasonPending
	case !in.CutoffEvaluated:
		return commonv1.CutoffReasonProfileUnresolved
	case in.TracksMissing:
		return commonv1.CutoffReasonTracksMissing
	case in.CutoffMet:
		return commonv1.CutoffReasonCutoffMet
	default:
		return commonv1.CutoffReasonCutoffUnmet
	}
}

// LeafCutoffMet is the status.cutoffMet a leaf writes beside LeafPhase: a
// file at the cutoff with, for an album, every track on disk. An item
// without a file or without a verdict never reads cutoffMet.
func LeafCutoffMet(in LeafInputs) bool {
	return in.HasFile && in.CutoffEvaluated && in.CutoffMet && !in.TracksMissing
}
