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

// Package audiobook implements the Audiobook controller: the Audiobook's
// view of the shared leaf phase (PhaseInputs over rollup.LeafPhase,
// 2026-11-10 design D4), naming, the MediaFile/Download rollup and the
// start-time idle rule (Path, FileState, PartRefs, IdleInputs) as pure
// functions, and a thin Reconciler around them, following
// app/catalog/controller/movie -- task G2-3's assigned precedent for a leaf
// kind with no parent fan-out (spec §8.1, amendment-1 §A1.5).
//
// Audiobook differs from Movie in three ways this package encodes directly:
//
//   - it is keyed by spec.asin + spec.region against Audnexus rather than a
//     single provider id, but that threading is entirely the metadata
//     gateway's concern (app/catalog/metadata/target.go's externalIDs) -- this
//     package only ever names the item by kind and reads whatever
//     status.metadata the gateway already wrote.
//   - its phase is the four non-video leaves' LeafPhase, with Movie's
//     precedence: Pending until the metadata is fresh, Unavailable before
//     the publication date, CutoffUnevaluated for files no profile judged,
//     and never Transcoded, since an audiobook's parts are never transcoded
//     (design D9). See PhaseInputs for how each input is read.
//   - status.fileRefs is a list (≤200 audio parts, spec §4.2), not a single
//     fileRef: an audiobook release commonly ships as N tracks importing as N
//     MediaFiles, in play order by each part's spec.partIndex (PartRefs,
//     parts.go; design D11), counted in status.partCount. See FileState's
//     doc comment for how the single status.fileQuality, fileFormatScore and
//     cutoffMet verdict is derived from them.
//
// Audiobook is the sole writer of status.phase, status.cutoffReason,
// status.path, status.hasFile, status.fileRefs, status.partCount,
// status.fileQuality, status.fileFormatScore, status.cutoffMet,
// status.addOptionsApplied and status.activeDownloadRef (§3's single-writer
// rule, and the ownership decision recorded in
// .superpowers/sdd/2026-09-23-phases-efg/g2-controllers-brief.md, settled by
// G2-1); status.metadata belongs to the metadata gateway (field manager
// k8s.ManagerCatalogMetadata) and this package never writes it.
// status.pendingGrab is read only, for phase and wake purposes -- it is
// written by the grab path, not this controller, mirroring Movie exactly (see
// reconciler.go's field-manager note). The HasFile and CutoffMet conditions
// were retired on 2026-11-10 (design D4): status.hasFile and
// status.cutoffReason say what they restated, and Ready, MetadataReady,
// QueueFull, BookRefResolved and DeadLettered stay.
package audiobook
