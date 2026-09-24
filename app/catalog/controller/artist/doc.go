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

// Package artist implements the Artist controller: metadata staleness
// (publishing a MetadataTask when the cache is missing or past its
// RefreshTTL), path, and a thin Reconciler that fans an Artist out into
// owned Album objects per spec §4.2, mirroring catalogarr/controller/series
// exactly (read that package first -- this one follows it, not the other
// way around).
//
// Artist is the sole writer of status.path, status.albumCount,
// status.albumFileCount and status.addOptionsApplied; status.metadata
// belongs to the metadata gateway (k8s.ManagerCatalogarrMetadata) and this
// reconciler never builds an ArtistStatusApplyConfiguration that calls
// WithMetadata.
//
// Unlike Series->Episode, this reconciler writes NOTHING onto the Album
// objects it creates -- no k8s.ManagerCatalogarrFanout apply, no per-item
// provider fields seeded at create time. That split is settled by G2-1
// (commit f665aa9) and documented on buildAlbumMetadataAC
// (catalogarr/metadata/patch.go): Artist's own release-group discovery call
// (ArtistProvider.Albums(mbArtistID), reached here via the
// rpc.catalogarr.metadata.lookup RPC's lookupAlbums handler) and the
// gateway's later per-item fetch (ArtistProvider.Album(mbReleaseGroupID))
// both bottom out in the same mapAlbum() function in
// pkg/metadata/clients/musicbrainz -- so a fan-out write of, say,
// Album.Status.Metadata.Title and the gateway's own write of the identical
// field would derive identical values from identical data, which is
// exactly the "two managers can co-own a field while their values happen to
// match" trap CLAUDE.md warns makes a release invisible (a test that does
// not deliberately drop the co-owner reports a false pass). The clean way
// to avoid that trap is to not create it: ensureAlbum below creates an
// Album by setting spec fields only (spec.artistRef, spec.releaseGroupID,
// spec.monitored once at creation, mirroring ArtistAddOptions/
// spec.monitorNewItems) and never touches status at all -- not even under
// k8s.ManagerCatalogarrFanout, which this task deliberately leaves unused
// (that manager is reserved for Comic->Issue, the one non-video pair whose
// child has no independent status.metadata of its own to race with a
// fan-out write; see k8s.ManagerCatalogarrFanout's own doc comment). The
// newly created Album then flows through the exact same
// MetadataTask -> Handler -> ManagerCatalogarrMetadata pipeline as any
// Movie, Series or Artist, driven by the Album reconciler's own staleness
// check (catalogarr/controller/album), not by this package.
package artist
