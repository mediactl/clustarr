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

// Package fileimport is importarr's completed-download import worker: the
// clustarr.work.importarr.fileimport.<download> handler that turns a
// finished Download into one or more MediaFiles under a RootFolder
// (amendment §A1.6; spec §8.4, adjusted for the amendment's relocation of
// the importer out of catalogarr -- see [FieldManager] and this package's
// own status-ownership note below).
//
// # The status split
//
// This worker creates the MediaFile and is the sole writer of
// MediaFileSpec: the observed destination path, size and mtime, plus the
// quality, revision, format score, matched formats and release type frozen
// at import (spec §8.4, CLAUDE.md's invariant). It never writes any part of
// MediaFileStatus, which catalogarr owns in full and populates by probing
// (spec §8.5). The one probe this worker makes is for a field it does own:
// a music file's frozen quality, which is its codec and bitrate
// (FrozenFileQuality, mediainfo.ProbeAudio) -- the result goes into
// spec.quality and nowhere else.
//
// It is also, uniquely, a cross-group status writer: it applies
// Download.status.import under k8s.ManagerImportarr, the one field manager
// on a download.clustarr.io object that importarr, not grabarr, owns. See
// k8s.ManagerImportarr's doc comment for why that write settled here rather
// than under catalogarr (design spec §8.4's original assignment, superseded
// by amendment-1) and why it uses the bare controller-manager name rather
// than [FieldManager] (this package never shares a manager name with
// another importarr writer on the SAME object type, so the collision
// [FieldManager] guards against on MediaFile does not apply to Download).
//
// The scanner-never-guesses rule (amendment §A1.5) applies here too, in its
// file-import shape: a file this worker cannot confidently attribute to the
// Download's target, or cannot parse, is never turned into a speculative
// MediaFile. It is recorded in Download.status.import.rejections with a
// reason instead -- the file-import analogue of LibraryScan.status.unmatched,
// since a Download has no LibraryScan to report through. That includes a
// video file only the sample size floor flags ([Worker.SampleMaxBytes]):
// a size is a suspicion, not a verdict, so the file is a rejection naming
// its size and the threshold, and a manual import takes it. A part, an
// extras-folder file or a file whose name marks it a sample is the
// release's own packaging and is passed over without a rejection
// (Worker.admit says why).
//
// # Scope
//
// The import target is the Download's spec.target, or what its
// [AnnotationImportTarget] annotation redirects it to (see "Manual import").
// Supported: a movie; an episode, or a series whose keys name the episodes
// of a pack (episode_import.go); and the four non-video items that hold
// files -- an album, a book, an audiobook, an issue (spec.target comic/<c>
// with exactly one key, or the annotation comic/<c>/<issue>). An artist,
// author, or comic without an issue is Blocked with a message naming the
// annotation that fixes it -- its files belong to one of its children, and
// choosing which is a guess.
//
// An episode file is attributed by the numbering its name carries
// ([MatchEpisodes]: season and episode, absolute number, or air date) to
// episodes of the target's series, as Sonarr's import maps a release's
// files; a file that names no episode the series has is a rejection. A file
// covering several episodes is one MediaFile whose spec.mediaRef names the
// first and lists all of them in keys ([EpisodeFileRef]).
//
// A non-video import (nonvideo.go) differs from a movie import in three
// honest ways. Its files are classified by their own kind ([ClassifierFor]):
// classified as video, a .flac would not be media, a 1 MiB ebook would be
// under the video sample floor, and a book in a folder named "Extras" would
// be a video extra. Its quality is frozen only where the file determines it
// exactly ([FrozenFileQuality]): a music file by a probe of its codec and
// bitrate, anything else by its extension; a file whose quality is still
// undeterminable is imported only by a manual import. And it is never
// scored: the custom-format corpus is TRaSH video data, so formatScore,
// matchedFormats and profileHash stay unset. A book's or issue's single file
// is replaced by an upgrade exactly as a movie's is. An album's or
// audiobook's files are replaced as a set: a manual import that brings the
// whole of a release supersedes the item's earlier files (which old track a
// new one replaces is not knowable without probing both, so it is all or
// nothing), and one with any file rejected keeps them and says so. A lone
// file imported to an album whose release has one track names that track
// (MediaRef.Track).
//
// No import renames a file over one already at its destination without
// linking the old one into the recycle bin first (placeFile).
//
// An item that holds one file -- a movie, a book, an issue, an episode --
// gets one file from a download however many the download carries for it
// (order.go): every file is admitted first, the candidates are ranked by
// the profile's quality order, then revision, then size, and imported best
// first, and a later file whose item is already filled is a rejection, as in
// Radarr's ImportApprovedMovie and Sonarr's ImportApprovedEpisodes. An
// album's tracks and an audiobook's parts are all imported.
//
// # Manual import
//
// Design spec §8.4's two Download annotations:
//
//   - [AnnotationImportTarget] "<kind>/<name>[/<key>]" directs the import at
//     one item instead of spec.target (which is immutable, and the
//     Download's owner).
//   - [AnnotationImportOverride] "true" has DownloadSpec.Manual's effect;
//     [ParseImportOverride] says exactly what that is.
//
// Both are parsed strictly; a malformed one blocks the import with the
// parse error on status.import rather than importing to spec.target.
// Grabarr publishes a Download's ImportTask once, so an annotation set on a
// Download that is already Blocked is acted on by [Retrigger], which re-
// queues it. The same import-target grammar on a LibraryScan is how a
// rescan-unmatched file is assigned by hand: importarr/worker/rescan's
// package doc, "Manual assignment".
//
// # Registration
//
// Nothing here registers itself. Task D2-8 wires it into importarr-worker's
// setup with exactly:
//
//	if err := fileimport.IndexMediaFileByTarget(ctx, mgr); err != nil {
//	        return fmt.Errorf("importarr: index mediafile target: %w", err)
//	}
//	spec, ok := o.BusTopology().Consumer(events.ConsumerImportFile)
//	if !ok {
//	        return fmt.Errorf("importarr: consumer %s missing from topology", events.ConsumerImportFile)
//	}
//	worker := fileimport.NewWorker(mgr.GetClient(), bus)
//
//	if err := mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
//	        stop, err := bus.Subscribe(ctx, spec.Subscription(), worker.Handle)
//	        if err != nil {
//	                return fmt.Errorf("importarr: subscribe %s: %w", events.ConsumerImportFile, err)
//	        }
//	        <-ctx.Done()
//	        stop()
//	        return nil
//	})); err != nil {
//	        return err
//	}
//
// k8s.EveryReplica, not manager.RunnableFunc, for the same reason
// importarr/worker/rescan uses it: manager.RunnableFunc has no
// NeedLeaderElection method, so controller-runtime would put this behind the
// leader lease on any service that elects, and a completed-download import
// must not wait for leadership.
//
// IndexMediaFileByTarget must run before the manager starts: it registers
// the field index this worker uses to find a target's existing MediaFile.
//
// [Retrigger] is a separate controller, registered the same way as any
// other (its doc comment has the call). It is not a work consumer and runs
// under ordinary leader election.
package fileimport
