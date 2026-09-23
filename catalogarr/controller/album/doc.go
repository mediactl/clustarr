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
// status.formatScore, status.cutoffMet, (conditionally)
// status.activeDownloadRef and status.metadata.selectedReleaseID, under
// k8s.ManagerCatalogarr. Every other leaf of status.metadata belongs to the
// metadata gateway (k8s.ManagerCatalogarrMetadata) -- see
// buildAlbumMetadataAC's doc comment in catalogarr/metadata/patch.go for
// why the Artist fan-out that creates this object never seeds it.
// selectedReleaseID is the one exception because only this reconciler can
// decide it (selectedReleaseAC in reconciler.go); server-side apply tracks
// ownership per leaf, so sharing the struct releases nothing of the
// gateway's. status.pendingGrab, status.lastSearchedAt and
// status.searchAttempts belong to the grab path
// (k8s.ManagerCatalogarrGrab); this reconciler only READS
// status.pendingGrab (for Phase) and never writes it, mirroring
// episode.Reconciler's identical read-only treatment.
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
// Track listing: status.tracks comes from the pkg/metadata.Album the
// gateway also fetches for status.metadata -- fetched independently here,
// via a direct RPC call through rpc.catalogarr.metadata.lookup, kind=album,
// keyed by KeyMBReleaseGroup, which browses the group's releases with their
// media and recordings (pkg/metadata/clients/musicbrainz's Album). One
// release is selected (SelectRelease, tracks.go: a pin, then Lidarr's
// keep-the-monitored-release, most-files, most-tracks rule over the
// releases the Artist's metadata profile accepts) and flattened into
// status.tracks (BuildTracks). A MediaFile addressing one track --
// spec.mediaRef {kind: album, name: <album>, track: <recording MBID>} --
// sets that track's fileRef (FilesByRecording, filestate.go), and
// status.trackFileCount counts them.
package album
