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

package rescan

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/release"
)

// FieldManager is the server-side-apply field manager every write in this
// package uses, on the Movie it attributes a file to and on the MediaFile
// itself. It is k8s.ManagerImportarrWorker, NOT k8s.ManagerImportarr, and
// the difference is not cosmetic.
//
// This worker runs on the same replicas as importarr's controllers --
// run.go adds it with mgr.Add(k8s.EveryReplica(...)), inside the same
// manager process -- and those controllers write under ManagerImportarr.
// Server-side apply replaces a manager's whole ownership set on every apply
// rather than merging it, so two writers sharing ONE manager name on one
// object silently release each other's fields. Today they never meet on one
// object, so the name was inert; M3's fileimport consumer writes
// Download.status.import AND MediaFileSpec from these same replicas, which
// is exactly the shape that forced catalogarr-worker to split into
// catalogarr-metadata and catalogarr-grab after both directions of that
// release were reproduced against a real apiserver. Splitting before the
// collision costs one word; splitting after it costs a debugging session.
//
// It is exported so the tests that assert the split -- catalogarr's
// mandatory two-writer gate above all, which has to stand in for this
// worker rather than run it -- name the manager production actually uses
// instead of restating a constant that can drift away from it. It had
// drifted: production wrote as ManagerImportarr while the gate, and
// k8s.ManagerImportarrWorker's own doc comment, described ManagerImportarr-
// Worker.
const FieldManager = k8s.ManagerImportarrWorker

// handleMediaFile attributes one walked media file and records the result.
//
// The write it performs is deliberately narrow. importarr creates the
// MediaFile and owns MediaFileSpec -- what it observed on disk plus the
// release identity frozen at import (spec §8.4) -- and catalogarr owns all of
// MediaFileStatus. Nothing here calls PatchStatus, and nothing here probes:
// there is nowhere on MediaFileSpec for a probe result to go, and probing is
// catalogarr's job (spec §8.5).
//
// A path that already has a MediaFile is not attributed again. The
// MediaFile IS its attribution -- spec.mediaRef is immutable -- so a rescan
// only refreshes what it observes (size and mtime) and re-asserts every
// frozen field verbatim; re-running the matcher could only agree, or list a
// file that is already in the catalog as unmatched.
func (w *Worker) handleMediaFile(ctx context.Context, st *scanState, path string, info os.FileInfo) error {
	now := w.now()
	// Relative to the ROOT FOLDER, which is what
	// catalogv1alpha1.UnmatchedFile.Path documents -- not to the walked
	// path, which is the root folder joined with spec.subpath and would
	// drop that prefix from every recorded entry.
	rel := relPath(st.root.Spec.Path, path)

	// Library rescan attributes movie, music, book, audiobook and comic
	// root folders. A file under a series root is reported honestly rather
	// than guessed at: episode attribution is not built.
	if fileKindForRoot(st.root.Spec.Kind) == "" {
		st.unmatched(rel, CodeUnsupportedKind, fmt.Sprintf(
			"root folder kind %q is not supported by library rescan yet", st.root.Spec.Kind), nil, now)
		return nil
	}

	existing, skip, err := w.existingForRescan(ctx, st, path, info)
	if err != nil || skip {
		return err
	}
	switch {
	case st.manual != nil:
		return w.assignManually(ctx, st, path, rel, info, existing)
	case existing != nil:
		if !st.task.DryRun {
			if err := w.applyObserved(ctx, st.scan.Namespace, existing, existing.Spec.MediaRef, path, info, frozenFields{}); err != nil {
				return err
			}
		}
		st.progress.ItemsUpdated++
		st.progress.FilesMatched++
		return nil
	case st.root.Spec.Kind != catalogv1alpha1.RootFolderKindMovie:
		return w.handleNonVideoFile(ctx, st, path, rel, info)
	}

	parsed, perr := release.ParsePath(path, release.Options{Kind: commonv1.MediaKindMovie})
	if perr != nil {
		st.unmatched(rel, CodeParseError, fmt.Sprintf("could not parse the filename: %v", perr), nil, now)
		return nil
	}

	result := MatchMovie(parsed, st.movies, w.resolveIMDb(ctx))
	if result.Unmatched {
		st.unmatched(rel, result.Code, result.Reason, result.Candidates, now)
		return nil
	}

	movieName := result.ExistingName
	created := movieName == ""
	if created {
		profile := st.root.Spec.Defaults.QualityProfileRef
		if profile == "" {
			// Movie.spec.qualityProfileRef is required, so a movie cannot
			// be created without one. Saying so beats applying an object
			// the apiserver will reject on every file.
			st.unmatched(rel, CodeNoQualityProfile, fmt.Sprintf(
				"root folder %q sets no default qualityProfileRef, which a new movie requires",
				st.root.Name), nil, now)
			return nil
		}
		movieName = k8s.ChildName(parsed.Title, "movie", strconv.FormatInt(result.TmdbID, 10))
		if !st.task.DryRun {
			if err := w.applyMovie(ctx, st, movieName, result.TmdbID, profile); err != nil {
				return err
			}
		}
		// Record it locally too, so a second file for the same movie
		// counts as an update rather than another creation.
		st.movies = append(st.movies, MovieCandidate{
			Name: movieName, TmdbID: result.TmdbID, Title: parsed.Title, Year: parsed.Year,
		})
		st.progress.ItemsCreated++
	} else {
		st.progress.ItemsUpdated++
	}

	if !st.task.DryRun {
		ref := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movieName}
		if err := w.applyObserved(ctx, st.scan.Namespace, nil, ref, path, info, freshMovieSpec(parsed)); err != nil {
			return err
		}
	}
	st.progress.FilesMatched++
	logging.FromContext(ctx).Debug("attributed a scanned file",
		"movie", movieName, "tmdbID", result.TmdbID, "created", created)
	return nil
}

// existingForRescan looks the path's MediaFile up and decides whether the
// walk leaves it alone entirely. skip is true (and counted) for a file
// catalogarr has taken over post-transcode and, on an incremental scan, for
// an unchanged fingerprint.
func (w *Worker) existingForRescan(
	ctx context.Context, st *scanState, path string, info os.FileInfo,
) (existing *catalogv1alpha1.MediaFile, skip bool, err error) {
	existing, err = w.existingMediaFile(ctx, st.scan.Namespace, path)
	if err != nil || existing == nil {
		return existing, false, err
	}
	// catalogarr takes spec.sizeBytes, spec.modTime and spec.original
	// over once it incorporates a transcode swap (spec §8.5). k8s.Apply
	// forces ownership, so re-applying those fields here would silently
	// reclaim them and break the split; leave the file alone entirely.
	if existing.Spec.Original != nil && !*existing.Spec.Original {
		st.progress.FilesSkipped++
		return existing, true, nil
	}
	// The incremental fingerprint is spec.sizeBytes plus spec.modTime
	// directly: a file whose size and mtime are unchanged has nothing
	// new to record.
	if st.incremental() && sameFingerprint(existing, info) {
		st.progress.FilesSkipped++
		return existing, true, nil
	}
	return existing, false, nil
}

// existingMediaFile looks a walked path up through the spec.path field index.
// It returns nil when no MediaFile records the path yet.
func (w *Worker) existingMediaFile(ctx context.Context, namespace, path string) (*catalogv1alpha1.MediaFile, error) {
	var list catalogv1alpha1.MediaFileList
	if err := w.Client.List(ctx, &list,
		client.InNamespace(namespace),
		client.MatchingFields{MediaFilePathIndexKey: path},
	); err != nil {
		return nil, fmt.Errorf("rescan: look up media file by path: %w", err)
	}
	if len(list.Items) == 0 {
		return nil, nil
	}
	return &list.Items[0], nil
}

// sameFingerprint compares the stored size and mtime with what is on disk.
// Both sides are truncated to whole seconds because metav1.Time serialises as
// RFC 3339, so a stored mtime has already lost its sub-second part and an
// exact comparison would never hold.
func sameFingerprint(mf *catalogv1alpha1.MediaFile, info os.FileInfo) bool {
	if mf.Spec.SizeBytes != info.Size() {
		return false
	}
	stored := mf.Spec.ModTime.Time.UTC().Truncate(time.Second)
	onDisk := info.ModTime().UTC().Truncate(time.Second)
	return !stored.IsZero() && stored.Equal(onDisk)
}

// applyMovie creates (or re-asserts) the Movie a scanned file was attributed
// to, under [FieldManager]. addMethod is "scan", which the CRD's own
// enum carries precisely so a scanned discovery is distinguishable from
// something a user added by hand, and searchForMovie is false: a file that is
// already on disk must not immediately trigger a grab for itself.
func (w *Worker) applyMovie(ctx context.Context, st *scanState, name string, tmdbID int64, profile string) error {
	ac := catalogac.Movie(name, st.scan.Namespace).WithSpec(
		catalogac.MovieSpec().
			WithTmdbID(tmdbID).
			WithQualityProfileRef(profile).
			WithRootFolderRef(st.root.Name).
			WithAddOptions(catalogac.MovieAddOptions().
				WithSearchForMovie(false).
				WithAddMethod(catalogv1alpha1.MovieAddMethodScan)),
	)
	if _, err := k8s.Apply(ctx, w.Client, FieldManager, ac); err != nil {
		return fmt.Errorf("rescan: apply movie %s: %w", name, err)
	}
	return nil
}

// frozenFields is what a first sighting of a file freezes into
// MediaFileSpec beyond the observed path, size and mtime (spec §8.4). A nil
// or empty field is not sent, so this manager never claims a field it has
// nothing to say about.
type frozenFields struct {
	quality      *commonv1.Quality
	revision     *commonv1.Revision
	releaseType  commonv1.ReleaseType
	releaseGroup *string
	edition      *string
	languages    []string
	importedFrom *catalogac.ImportSourceApplyConfiguration
}

// freshMovieSpec is what a scanned movie file freezes: the release identity
// pkg/release parsed. releaseGroup and edition are sent even when empty, as
// they always have been on this path.
//
// formatScore, matchedFormats and profileHash are deliberately left at their
// zero values. They are importarr's fields, but scoring them needs the item's
// QualityProfile and pkg/quality/catalogue's custom-format evaluation, which
// this path does not integrate yet. An unscored file is still fully tracked;
// the score only affects later upgrade decisions.
func freshMovieSpec(parsed *release.ParsedRelease) frozenFields {
	return frozenFields{
		quality:      &parsed.Quality,
		revision:     &parsed.Revision,
		releaseType:  parsed.ReleaseType,
		releaseGroup: ptr.To(parsed.Group),
		edition:      ptr.To(parsed.Edition),
		languages:    parsed.Languages,
	}
}

// applyObserved applies a MediaFile's spec under [FieldManager].
//
// For a file with no MediaFile yet it sends ref, the observed path, size and
// mtime, and fresh. For an existing one it sends the observed fields plus
// every field this manager owns re-asserted verbatim from existing.Spec --
// fresh is ignored. That is the complete-declaration rule applied to spec:
// an apply that omitted formatScore, matchedFormats, profileHash,
// importedFrom or original would RELEASE them, and fileimport sets all five
// under this same manager, so a plain re-scan of an imported file used to
// wipe its import score and provenance. Re-parsing instead of re-asserting
// would be worse: the importer renames files, and a renamed file can parse
// to a different quality than the release it was frozen from.
func (w *Worker) applyObserved(
	ctx context.Context, namespace string, existing *catalogv1alpha1.MediaFile,
	ref commonv1.MediaRef, path string, info os.FileInfo, fresh frozenFields,
) error {
	name := k8s.ChildName(ref.Name, "mediafile", path)
	if existing != nil {
		name = existing.Name
		ref = existing.Spec.MediaRef
		fresh = reassertFrozen(&existing.Spec)
	}
	spec := catalogac.MediaFileSpec().
		WithMediaRef(ref).
		WithPath(path).
		WithSizeBytes(info.Size()).
		WithModTime(metav1.NewTime(info.ModTime()))
	if fresh.quality != nil {
		spec = spec.WithQuality(*fresh.quality)
	}
	if fresh.revision != nil {
		spec = spec.WithRevision(*fresh.revision)
	}
	if fresh.releaseType != "" {
		spec = spec.WithReleaseType(fresh.releaseType)
	}
	if fresh.releaseGroup != nil {
		spec = spec.WithReleaseGroup(*fresh.releaseGroup)
	}
	if fresh.edition != nil {
		spec = spec.WithEdition(*fresh.edition)
	}
	if len(fresh.languages) > 0 {
		spec = spec.WithLanguages(fresh.languages...)
	}
	if fresh.importedFrom != nil {
		spec = spec.WithImportedFrom(fresh.importedFrom)
	}
	if existing != nil {
		s := &existing.Spec
		if s.FormatScore != 0 {
			spec = spec.WithFormatScore(s.FormatScore)
		}
		if len(s.MatchedFormats) > 0 {
			spec = spec.WithMatchedFormats(s.MatchedFormats...)
		}
		if s.ProfileHash != "" {
			spec = spec.WithProfileHash(s.ProfileHash)
		}
		if s.Original != nil {
			spec = spec.WithOriginal(*s.Original)
		}
	}

	if _, err := k8s.Apply(ctx, w.Client, FieldManager,
		catalogac.MediaFile(name, namespace).WithSpec(spec)); err != nil {
		return fmt.Errorf("rescan: apply media file %s: %w", name, err)
	}
	return nil
}

// reassertFrozen reads the frozen fields back off an existing spec, sending
// only what is set.
func reassertFrozen(s *catalogv1alpha1.MediaFileSpec) frozenFields {
	var f frozenFields
	if s.Quality != (commonv1.Quality{}) {
		f.quality = &s.Quality
	}
	if s.Revision != (commonv1.Revision{}) {
		f.revision = &s.Revision
	}
	f.releaseType = s.ReleaseType
	if s.ReleaseGroup != "" {
		f.releaseGroup = &s.ReleaseGroup
	}
	if s.Edition != "" {
		f.edition = &s.Edition
	}
	f.languages = s.Languages
	if src := s.ImportedFrom; src != nil {
		ac := catalogac.ImportSource()
		if src.DownloadRef != "" {
			ac = ac.WithDownloadRef(src.DownloadRef)
		}
		if src.ReleaseTitle != "" {
			ac = ac.WithReleaseTitle(src.ReleaseTitle)
		}
		if src.IndexerName != "" {
			ac = ac.WithIndexerName(src.IndexerName)
		}
		if src.Protocol != "" {
			ac = ac.WithProtocol(src.Protocol)
		}
		if !src.ImportedAt.IsZero() {
			ac = ac.WithImportedAt(src.ImportedAt)
		}
		if src.Manual {
			ac = ac.WithManual(true)
		}
		f.importedFrom = ac
	}
	return f
}

// resolveIMDb asks the metadata gateway to turn an IMDb id into a TMDB one.
// A gateway that is down, slow or does not know the id yields an error, which
// MatchMovie turns into an unmatched file rather than a looser guess.
func (w *Worker) resolveIMDb(ctx context.Context) ResolveIMDb {
	return func(imdbID string) (int64, error) {
		if w.Bus == nil {
			return 0, fmt.Errorf("rescan: no bus to resolve imdb id %q with", imdbID)
		}
		rpcCtx, cancel := context.WithTimeout(ctx, w.metadataTimeout())
		defer cancel()

		req := schema.MetadataRequest{
			Kind: commonv1.MediaKindMovie,
			IDs:  map[string]string{"imdb": imdbID},
		}
		var resp schema.MetadataResponse
		if err := w.Bus.Request(rpcCtx, events.RPCMetadataResolve, req, &resp); err != nil {
			return 0, fmt.Errorf("rescan: resolve imdb id %q: %w", imdbID, err)
		}
		if resp.Error != "" {
			return 0, fmt.Errorf("rescan: resolve imdb id %q: %s", imdbID, resp.Error)
		}
		raw := resp.IDs["tmdb"]
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			return 0, fmt.Errorf("rescan: gateway returned no usable tmdb id for %q (got %q)", imdbID, raw)
		}
		return id, nil
	}
}
