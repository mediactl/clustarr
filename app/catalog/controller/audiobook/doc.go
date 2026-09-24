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

// Package audiobook implements the Audiobook controller: phase, naming and
// the MediaFile/Download rollup as pure functions (Phase, Path,
// FileState, DownloadOverlay), and a thin Reconciler around them, following
// catalogarr/controller/movie -- task G2-3's assigned precedent for a leaf
// kind with no parent fan-out (spec §8.1, amendment-1 §A1.5).
//
// Audiobook differs from Movie in three ways this package encodes directly:
//
//   - it is keyed by spec.asin + spec.region against Audnexus rather than a
//     single provider id, but that threading is entirely the metadata
//     gateway's concern (catalogarr/metadata/target.go's externalIDs) -- this
//     package only ever names the item by kind and reads whatever
//     status.metadata the gateway already wrote.
//   - AudiobookPhase has no Pending or Unavailable value (unlike MoviePhase):
//     there is no minimumAvailability concept for an Audible release, and the
//     type was not given a distinct "metadata not fetched yet" phase. See
//     Phase's doc comment for how this package handles that gap without
//     inventing an enum value the CRD does not accept.
//   - status.fileRefs is a list (≤200 audio parts, spec §4.2), not a single
//     fileRef: an audiobook release commonly ships as N tracks importing as N
//     MediaFiles. See FileState's doc comment for how the list is ordered and
//     how the single status.quality/cutoffMet verdict is derived from it.
//
// Audiobook is the sole writer of status.phase, status.path, status.hasFile,
// status.fileRefs, status.quality, status.cutoffMet and
// status.activeDownloadRef (§3's single-writer rule, and the ownership
// decision recorded in .superpowers/sdd/2026-09-23-phases-efg/
// g2-controllers-brief.md, settled by G2-1); status.metadata belongs to the
// metadata gateway (field manager k8s.ManagerCatalogarrMetadata) and this
// package never writes it. status.pendingGrab is read only, for phase and
// wake purposes -- it is written by the grab path, not this controller,
// mirroring Movie exactly (see reconciler.go's field-manager note).
package audiobook
