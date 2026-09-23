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

// Package movie implements the Movie controller: availability, naming and
// phase as pure functions (Availability, Path, Phase, and the rollup.go
// re-exports FileState/DownloadOverlay), and a thin Reconciler around them
// per spec §8.1 and §8.8.
//
// Movie is the sole writer of status.phase, status.hasFile, status.fileRef,
// status.fileQuality, status.fileFormatScore, status.cutoffMet and
// status.activeDownloadRef (§3's single-writer rule; for the ref, gap-fix
// ruling R-5, derived from the Movie's own non-terminal Downloads);
// status.metadata belongs to the metadata gateway (Task C5, field manager
// k8s.ManagerCatalogarrMetadata) and this package never writes it. It folds
// the DLQ projector's clustarr.io/dead-lettered annotation into a
// DeadLettered condition, emits Kubernetes Events on phase and failure
// edges, and publishes the catalog item and media-file domain events the
// history sink records (report.go).
package movie
