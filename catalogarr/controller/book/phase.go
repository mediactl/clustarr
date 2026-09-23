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

package book

import (
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// Phase computes the coarse lifecycle state of a Book. Unlike MoviePhase,
// BookPhase (book_types.go) has no Pending or Unavailable bucket -- it is
// Wanted;Delayed;Downloading;Imported;CutoffUnmet;Unmonitored, the exact
// shape design §4.2 also gives Album's own Phase enum -- so this function
// takes no metadataReady or available parameter: a Book with no cached
// metadata yet, or one no availability concept applies to at all (books have
// no release-window gate the way Movie's MinimumAvailability does), simply
// reads Wanted until a file appears, the same steady state it would occupy
// anyway.
//
// pendingGrab reports whether status.pendingGrab is set, mirroring Movie's
// identical parameter and rank: above CutoffUnmet and Wanted (a scheduled
// upgrade IS the story), below Imported (a pending grab is an internal
// timer, not something the user can see or cancel, unlike a Download --
// DownloadOverlay's Downloading is allowed to override Imported for exactly
// that reason).
//
// cutoffMet is filestate.go's FileState output, a genuine ranking verdict
// against the resolved QualityProfile (pkg/quality/definition.go's book
// ladder) -- see this package's doc.go for the correction from an earlier
// draft that wrongly treated it as a hasFile stand-in. BookPhaseCutoffUnmet
// is reachable: a file below the profile's cutoff (e.g. a PDF against the
// built-in ebook profile's MOBI cutoff) lands here.
func Phase(monitored, hasFile, cutoffMet, pendingGrab bool) catalogv1alpha1.BookPhase {
	switch {
	case !monitored:
		return catalogv1alpha1.BookPhaseUnmonitored
	case hasFile && cutoffMet:
		return catalogv1alpha1.BookPhaseImported
	case pendingGrab:
		return catalogv1alpha1.BookPhaseDelayed
	case hasFile:
		return catalogv1alpha1.BookPhaseCutoffUnmet
	default:
		return catalogv1alpha1.BookPhaseWanted
	}
}
