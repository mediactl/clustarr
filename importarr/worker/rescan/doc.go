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

// Package rescan is importarr's library-rescan work consumer: the
// clustarr.work.importarr.scan.* handler that walks a RootFolder, classifies
// what it finds, attributes each media file to a catalog item, and records it
// as a MediaFile -- creating the Movie for a movie file that carries a
// provider id (amendment §A1.3, §A1.5, §A1.6; §16 M1, M6).
//
// The scanner never guesses. A file that cannot be attributed with
// confidence is recorded as an [UnmatchedFile] with a reason, never turned
// into a speculative catalog item -- [MatchMovie], [MatchAlbum],
// [MatchBook], [MatchAudiobook] and [MatchIssue] are the pure functions that
// decision lives in, so the "must not match" cases are testable without a
// cluster.
//
// Classification is fsops', as the root folder's kind
// (fileimport.ClassifierFor), and happens before matching. A part, a file
// in a video extras folder beneath the root folder, a file whose name marks
// it a sample, and a non-media file are not considered, and each is counted
// by what it is (Progress.Parts, Extras, Samples, NotMedia) -- not as a
// skipped media file, which filesSkipped once conflated them with. A video
// file only the size floor ([Worker.SampleMaxBytes]) flags is not passed
// over: a size is a guess, so it is recorded as unmatched with
// [CodeSuspectedSample] and its size, unless a MediaFile already records it
// or a person is assigning it. An entry the walk cannot read is recorded as
// unmatched with [CodeUnreadable], and the walk carries on.
//
// [Progress] documents where every walked file lands; its Summary is the
// breakdown LibraryScan.status has no counters for.
//
// # Redelivery
//
// A walk checkpoints its tally, with the last file it has counted
// (Progress.Resume), to the clustarr-progress bucket. A redelivered task
// reads that checkpoint back and resumes after that file, so a redelivery
// neither counts a file twice nor drives the scan's counters backwards; a
// redelivery of a walk that already wrote its final tally is a no-op; and a
// task for a scan the controller has already settled is discarded.
//
// A path that already has a MediaFile is never attributed again: the
// MediaFile is its attribution (spec.mediaRef is immutable), so a rescan
// refreshes only the observed size and mtime and re-asserts every frozen
// field verbatim. See applyObserved for why omitting one would release it.
//
// # Root folder kinds
//
//   - movie: [MatchMovie]; a file carrying a tmdb or imdb id may create its
//     Movie.
//   - music, book, audiobook, comic: attributed to an EXISTING Album, Book,
//     Audiobook or Issue only (nonvideo.go explains why nothing on disk can
//     honestly yield the provider id creating one would need). Files are
//     classified as their own kind -- no video size floor, no video
//     extras folders -- and freeze only the quality the file determines
//     exactly (fileimport.FrozenFileQuality: a probe for music, the
//     extension otherwise).
//   - series: attributed to an EXISTING Series and its Episodes only
//     (series.go): the series by its folder -- status.path, a TheTVDB id in
//     the folder name, spec.folder, or its title and year -- and the
//     episode by the numbering the file's name carries
//     (fileimport.MatchEpisodes, which the import worker uses too). A file
//     holding several episodes is one MediaFile naming them all in
//     spec.mediaRef.keys.
//
// # Manual assignment
//
// This is the contract the UI's unmatched page (task G3-3) builds its
// "assign" action on. It needs no API addition and no new UI grant: the UI
// already may create LibraryScans (ui/actions.Grants).
//
// To assign one unmatched file to an item, create a LibraryScan in the
// unmatched entry's namespace:
//
//	apiVersion: catalog.clustarr.io/v1alpha1
//	kind: LibraryScan
//	metadata:
//	  generateName: assign-
//	  annotations:
//	    catalog.clustarr.io/import-target: album/ok-computer   # <kind>/<name>[/<key>]
//	spec:
//	  rootFolderRef: <the root folder of the scan that listed the file>
//	  subpath: <the UnmatchedFile.path, verbatim -- it is relative to the root>
//	  mode: full
//
// The annotation is the Download annotation's grammar, parsed by the same
// function (fileimport.ParseImportTarget). The target names the item that
// holds the file: movie/<m>, album/<a>, book/<b>, audiobook/<ab>, for an
// episode either episode/<e> or series/<s>/<e>, and for a comic either
// issue/<i> or comic/<c>/<i>. Its kind must fit the root folder
// (fileimport.FileRefFitsRoot), and the item must exist and be stored under
// that root folder.
//
// subpath names the FILE, not its directory. The walk of a file path visits
// exactly that file, so a comic series folder holding many issues, or an
// unsorted folder holding many albums, never has its neighbours swept into
// one item. (LibraryScanSpec.Subpath's doc comment names both shapes: a
// directory, whose tree is walked, or a single file, the only one visited.)
// A directory subpath assigns every unattributed media file beneath it,
// which is right for a movie, album or audiobook folder.
//
// The walk then records every file it visits against the target without
// matching -- a person made the attribution, which is the one way the
// never-guess rule admits an unmatchable file -- with
// spec.importedFrom.manual=true. That includes a suspected_sample file that
// is the only video the subpath holds: the size floor does not overrule a
// person. A suspected sample BESIDE real media in an assigned folder is the
// release's promo clip, and is left behind as unmatched with the remedy (a
// subpath naming the file itself). It does not include a part, an
// extras-folder file or a file whose name marks it a sample, which a
// directory subpath would otherwise sweep into the item. It does not bypass the MediaFile rules: a
// path already recorded against a different item is reported as unmatched
// (recorded_elsewhere) and never re-pointed, a malformed or unusable
// annotation, a missing item, an item of another root, and a subpath outside
// the root all end the scan Done with the reason (the LibraryScan controller
// reports it as Failed), and dry-run is honoured. catalog.clustarr.io/
// import-override has no meaning on a LibraryScan and is ignored.
//
// Reading results back: the new scan's status reports the file as matched.
// The ORIGINAL scan's status.unmatched still lists it until that scan's TTL
// removes it or a later scan supersedes it, so the page should treat an
// entry as resolved once a MediaFile with spec.path == <root
// spec.path>/<entry path> exists, rather than wait for the list to change.
//
// # The status split
//
// This worker writes MediaFileSpec, under the manager importarr's file-import
// worker also uses (importarr-worker): the observed path, size and mtime,
// plus the quality, revision, release type, release group, edition and
// languages frozen at import (spec §8.4). It never writes
// MediaFileStatus, which catalogarr owns in full and populates by probing
// (spec §8.5), and it never re-applies a MediaFile whose spec.original is
// already false -- catalogarr has taken spec.sizeBytes, spec.modTime and
// spec.original over after a transcode swap, and k8s.Apply's force-ownership
// would silently reclaim them. When such a file's bytes have changed on disk
// it tells catalogarr instead, with one annotation
// ([AnnotationObservedFingerprint]) under k8s.ManagerImportarr, which owns
// nothing else on a MediaFile. And every apply to an existing MediaFile
// carries the resourceVersion the walk read, so a takeover landing between
// the read and the write is refused rather than reverted (handleMediaFile).
// Every spec write is made under [FieldManager], which is
// k8s.ManagerImportarrWorker -- see that constant's doc comment for why this
// worker does not share importarr's controller manager name even though it
// runs in the same process.
//
// A scanned movie file is scored against its movie's QualityProfile, as an
// imported one is, so the two compare on the same terms: the formatScore,
// matchedFormats and profileHash it freezes are the file-import worker's
// (freshMovieSpec). A music file's quality is read from the file by a probe
// (fileimport.FrozenFileQuality).
//
// The worker also never writes LibraryScan.status: the LibraryScan
// controller is its single writer. Progress instead flows through a
// [Progress] checkpoint in the clustarr-progress bucket, written roughly
// every [checkpointInterval] and polled by the controller, with in-progress
// acks roughly every [heartbeatInterval] so a multi-minute walk outlives
// ConsumerImportScan's 60s AckWait.
//
// # Registration
//
// Nothing here registers itself. Task C12 wires it into importarr's
// setupWorkers with exactly:
//
//	if err := rescan.IndexMediaFileByPath(ctx, mgr); err != nil {
//	        return fmt.Errorf("importarr: index mediafile path: %w", err)
//	}
//	spec, ok := o.BusTopology().Consumer(events.ConsumerImportScan)
//	if !ok {
//	        return fmt.Errorf("importarr: consumer %s missing from topology", events.ConsumerImportScan)
//	}
//	worker := rescan.NewWorker(mgr.GetClient(), bus)
//
// k8s.EveryReplica, not manager.RunnableFunc: the latter has no
// NeedLeaderElection method, so controller-runtime puts it behind the leader
// lease on any service that elects.
//
//	if err := mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
//	        stop, err := bus.Subscribe(ctx, spec.Subscription(), worker.Handle)
//	        if err != nil {
//	                return fmt.Errorf("importarr: subscribe %s: %w", events.ConsumerImportScan, err)
//	        }
//	        <-ctx.Done()
//	        stop()
//	        return nil
//	})); err != nil {
//	        return err
//	}
//
// IndexMediaFileByPath must run before the manager starts: it registers the
// spec.path field index the incremental fingerprint check reads.
//
// Non-video attribution needs no new registration, but the walk now lists
// Artists, Albums, Authors, Books, Audiobooks, Comics and Issues, and reads
// QualityProfiles, through the manager's cache, so importarr's role needs
// get/list/watch on them: the package-level +kubebuilder:rbac marker in
// worker.go says so, and takes effect at the next `make manifests` plus
// chart sync.
package rescan
