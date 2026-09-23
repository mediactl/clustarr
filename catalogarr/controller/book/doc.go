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

// Package book implements the Book controller: phase, file state and
// download overlay as pure functions, and a thin Reconciler around them,
// covering BOTH shapes book_types.go:159-161 allows -- a Book fanned out
// from an Author (spec.authorRef set, created by the author package's
// Reconciler) and a standalone Book (spec.authorRef nil, created directly by
// a user/import list, never owned by anything). Both reconcile through this
// same Reconciler and the same reconcileNormal: nothing here branches on
// "am I owned" except resolveContext's root-folder/author-name resolution,
// which is the only place the two shapes genuinely differ (see
// resolveContext's own doc comment).
//
// This reconciler is the sole writer of status.phase, status.path,
// status.hasFile, status.fileRef, status.fileFormat, status.cutoffMet and
// status.activeDownloadRef; status.metadata belongs to the metadata gateway
// (field manager k8s.ManagerCatalogarrMetadata) and this reconciler never
// builds a BookStatusApplyConfiguration that calls WithMetadata. The
// author package's fan-out never writes anything onto a Book beyond its
// initial spec-only Create -- see author/doc.go for why -- so unlike
// Episode/Issue, this reconciler never needs to leave a provider-sourced
// field alone to avoid clobbering a sibling field manager; every field it
// owns is genuinely this reconciler's alone, under the single
// k8s.ManagerCatalogarr, the same shape Movie uses (not the Series/Episode
// or Comic/Issue two-manager split).
//
// A book quality ladder DOES exist (corrected after an earlier draft of
// this package believed Ruling R3 meant otherwise): pkg/quality/
// definition.go's nonVideoDefinitions["book"] (PDF < MOBI < EPUB < AZW3) and
// the built-in "ebook" profile (pkg/quality/catalogue/data/profiles/
// ebook.json, cutoff MOBI), seeded by the qualityprofile controller's
// Bootstrap runnable exactly like the 13 video profiles. This reconciler
// resolves BookSpec.QualityProfileRef (inheriting AuthorSpec.
// QualityProfileRef when unset, mirroring RootFolderRef's own inheritance
// in resolveContext) and ranks the backing MediaFile's spec.quality against
// it via rollup.FileState -- the same call Movie/Episode/Audiobook make --
// exactly as G2-3's Audiobook controller does (catalogarr/controller/
// audiobook/reconciler.go's resolveProfile, filestate.go). BookStatus has
// no FileQuality/FileFormatScore leaf to store the resolved Quality in
// (unlike Movie/Episode/Audiobook), only FileFormat (a string); filestate.go
// explains how that string is derived. CutoffMet is a genuine ranking
// verdict again, not a hasFile stand-in, so
// catalogv1alpha1.BookPhaseCutoffUnmet is reachable exactly as designed.
package book
