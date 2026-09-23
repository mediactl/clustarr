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

// Package album implements the Album controller: phase, path, track-listing
// selection and the file/download rollup, plus a thin Reconciler around
// them. Album objects are created and owned by the Artist controller
// (catalogarr/controller/artist sets spec.artistRef/releaseGroupID and, at
// creation or per spec.monitorNewItems, spec.monitored) -- but unlike
// Episode, Album fetches its OWN metadata: it is a first-class metadata
// target (catalogarr/metadata/target.go's newTarget/externalIDs both cover
// MediaKindAlbum, keyed by spec.releaseGroupID) with its own
// ConditionMetadataReady, exactly like Movie, Series and Artist. So this
// package's reconciler follows movie.Reconciler's shape for the metadata
// staleness half (publish a MetadataTask, mark MetadataReady) layered under
// series.Reconciler's shape for the "resolve a parent for path/quality
// inheritance" half (here, the owning Artist instead of a RootFolder
// directly -- Album carries no rootFolderRef of its own).
//
// This reconciler is the sole writer of status.conditions, status.tracks,
// status.phase, status.path, status.trackFileCount, status.quality,
// status.formatScore, status.cutoffMet and (conditionally)
// status.activeDownloadRef, under k8s.ManagerCatalogarr.
// status.metadata (including status.metadata.selectedReleaseID) belongs
// SOLELY to the metadata gateway (k8s.ManagerCatalogarrMetadata) -- see
// buildAlbumMetadataAC's doc comment in catalogarr/metadata/patch.go for
// why the Artist fan-out that creates this object never seeds it, and why
// this reconciler must not either. status.pendingGrab,
// status.lastSearchedAt and status.searchAttempts belong to the grab path
// (k8s.ManagerCatalogarrGrab); catalogarr/worker/search does not dispatch
// non-video kinds yet, so nothing writes those three fields on an Album
// today, but this reconciler still only READS status.pendingGrab (for
// Phase) and never writes it, mirroring episode.Reconciler's identical
// read-only treatment -- the split is a field-manager rule, not a
// consequence of any gap in non-video support.
//
// status.quality/status.formatScore/status.cutoffMet ARE genuinely
// evaluated, not stubbed: pkg/quality.FromCRD resolves a QualityProfile
// generically regardless of media kind, and pkg/quality/definition.go's
// nonVideoDefinitions carries a real music ladder (Trash..WAV, spec §9),
// seeded into QualityProfile objects by the Bootstrap runnable
// (pkg/quality/catalogue/data/profiles/music-lossless.json,
// music-standard.json). resolveProfile/FileState/rollup.PickMediaFile below
// wire this exactly as movie.Reconciler and audiobook.Reconciler do --
// catalogarr/controller/audiobook is the sibling precedent this package's
// reconciler.go follows for that half.
//
// Track-listing gap: status.tracks is computed by SelectRelease/BuildTracks
// (tracks.go) from the SAME pkg/metadata.Album entity the gateway fetches
// for status.metadata (fetched independently here, via a direct RPC call
// through rpc.catalogarr.metadata.lookup, kind=album, keyed by
// KeyMBReleaseGroup -- the existing single-release-group path
// Registry.Lookup already serves, not a new RPC verb). This is safe against
// the same-manager-co-ownership trap buildAlbumMetadataAC's comment warns
// about because Tracks is a disjoint field from status.metadata, owned
// solely by this reconciler either way. But pkg/metadata/clients/
// musicbrainz's mapAlbum does not currently populate Album.Releases from
// EITHER of ArtistProvider's Albums() or Album() calls (a Phase B client
// gap, out of this task's directories), so in production today
// SelectRelease always sees an empty release list and status.tracks is
// always empty. The mechanism is correct and tested against fixture data;
// it activates once that gap is closed. Flagged in this task's report.
package album
