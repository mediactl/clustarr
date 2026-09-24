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

// Package author implements the Author controller: metadata staleness
// (publishing a MetadataTask when the cache is missing or past its
// RefreshTTL), path, and the works-listing RPC fan-out into owned Book
// objects per spec §4.2's Author/Book pair, gated by
// AuthorSpec.MetadataProfile. It is the sole writer of status.path,
// status.bookCount, status.bookFileCount and status.addOptionsApplied;
// status.metadata belongs to the metadata gateway (task G2-1, field manager
// k8s.ManagerCatalogarrMetadata) and this reconciler never builds an
// AuthorStatusApplyConfiguration that calls WithMetadata.
//
// Unlike Series->Episode, this reconciler writes NOTHING onto the Book
// objects it fans out beyond their initial Create -- not even under a
// distinct field manager. It creates each Book with spec fields only
// (spec.authorRef, spec.workID, and spec.monitored set once, at creation)
// and never touches it again. This is a deliberate, settled difference from
// Series/Episode and Comic/Issue, not an oversight:
//
//   - Episode and Issue carry no status.metadata of their own -- neither
//     kind has a MetadataReady condition, and pkg/pipeline/project.go's
//     describeItem treats both as "always metadata-synced" because each is
//     only created once its parent's own metadata sync has already run. Their
//     provider-sourced fields (title, overview, ...) have nowhere else to
//     come from, so the parent's fan-out writes them directly: Series under
//     k8s.ManagerCatalogarrSeries, Comic under k8s.ManagerCatalogarrFanout.
//   - Book, like Album, DOES carry its own status.metadata and its own
//     MetadataReady condition (book_types.go), and independently refreshes
//     it the same way Movie/Series/Author do: app/catalog/metadata/target.go's
//     externalIDs maps *catalogv1alpha1.Book to
//     {pkgmetadata.KeyOpenLibraryWork: spec.workID}, and
//     Registry.Lookup(kind=book) already serves that single-work fetch. A
//     freshly-created Book with no status at all publishes its own
//     MetadataTask on its very next reconcile (see the book package), the
//     same self-starting behaviour Movie/Series/Artist/Author already have.
//     There is therefore nothing on a Book this reconciler needs to push --
//     pushing it anyway would only create a second writer to keep disjoint
//     from Book's own reconciler for no gain, repeating the exact clobbering
//     hazard the Series/Episode split exists to avoid (see
//     k8s.ManagerCatalogarrFanout's doc comment, and this package's own
//     reconciler.go for where that manager is deliberately never referenced).
//
// A parallel data note belongs here too: AuthorSpec.MetadataProfile
// (BookMetadataProfile) is applied in full against pkg/metadata.Book's real
// field set (fanout.go's MatchesProfile), but
// pkg/metadata/clients/openlibrary.Client.Books -- the author-works-list call
// this fan-out is built on -- maps each work record's title, overview,
// subjects, authors and first-publication date, and fetches no editions (one
// more request per work; openlibrary.Client.Book fetches them for a single
// work). So SkipPartsAndSets now sees each work's subjects, while
// AllowedLanguages and MinPages, which read editions, see none and pass
// under MatchesProfile's absent-never-excludes rule. SkipMissingDate is
// Readarr's rule, `!SkipMissingDate || ReleaseDate.HasValue`
// (MetadataProfileService.FilterBooks), acting on the first-publication date
// Books carries; SkipMissingISBN stays a documented no-op, because it reads
// editions (MatchesProfile's doc comment says why). MinPopularity has no
// home at all yet: no field on pkg/metadata.Book, pkg/metadata.Author or the
// CRD's BookMetadata carries a popularity score anywhere in this pipeline,
// so it is a documented no-op (never disqualifies a work) rather than a
// guess at a source.
package author
