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

// handleMediaFile attributes one walked media file and records the result.
//
// The write it performs is deliberately narrow. importarr creates the
// MediaFile and owns MediaFileSpec -- what it observed on disk plus the
// release identity frozen at import (spec §8.4) -- and catalogarr owns all of
// MediaFileStatus. Nothing here calls PatchStatus, and nothing here probes:
// there is nowhere on MediaFileSpec for a probe result to go, and probing is
// catalogarr's job (spec §8.5).
func (w *Worker) handleMediaFile(ctx context.Context, st *scanState, path string, info os.FileInfo) error {
	now := w.now()
	rel := relPath(st.task.Path, path)

	// Library rescan understands movie root folders. A file under any other
	// kind is reported honestly rather than guessed at: extending this to
	// series, music and books is M6 work.
	if st.root.Spec.Kind != catalogv1alpha1.RootFolderKindMovie {
		st.unmatched(rel, CodeUnsupportedKind, fmt.Sprintf(
			"root folder kind %q is not supported by library rescan yet", st.root.Spec.Kind), nil, now)
		return nil
	}

	existing, err := w.existingMediaFile(ctx, st.scan.Namespace, path)
	if err != nil {
		return err
	}
	if existing != nil {
		// catalogarr takes spec.sizeBytes, spec.modTime and spec.original
		// over once it incorporates a transcode swap (spec §8.5). k8s.Apply
		// forces ownership, so re-applying those fields here would silently
		// reclaim them and break the split; leave the file alone entirely.
		if existing.Spec.Original != nil && !*existing.Spec.Original {
			st.progress.FilesSkipped++
			return nil
		}
		// The incremental fingerprint is spec.sizeBytes plus spec.modTime
		// directly: a file whose size and mtime are unchanged has nothing
		// new to record.
		if st.incremental() && sameFingerprint(existing, info) {
			st.progress.FilesSkipped++
			return nil
		}
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
		if err := w.applyMediaFile(ctx, st, movieName, path, info, parsed); err != nil {
			return err
		}
	}
	st.progress.FilesMatched++
	logging.FromContext(ctx).Debug("attributed a scanned file",
		"movie", movieName, "tmdbID", result.TmdbID, "created", created)
	return nil
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
// to, under k8s.ManagerImportarr. addMethod is "scan", which the CRD's own
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
	if _, err := k8s.Apply(ctx, w.Client, k8s.ManagerImportarr, ac); err != nil {
		return fmt.Errorf("rescan: apply movie %s: %w", name, err)
	}
	return nil
}

// applyMediaFile records the file itself. Every field set here is a
// MediaFileSpec field: the observed path, size and mtime, and the release
// identity pkg/release parsed and spec §8.4 freezes at import.
//
// formatScore, matchedFormats and profileHash are deliberately left at their
// zero values. They are importarr's fields, but scoring them needs the item's
// QualityProfile and pkg/quality/catalogue's custom-format evaluation, which
// this path does not integrate yet. An unscored file is still fully tracked;
// the score only affects later upgrade decisions.
func (w *Worker) applyMediaFile(
	ctx context.Context,
	st *scanState,
	movieName, path string,
	info os.FileInfo,
	parsed *release.ParsedRelease,
) error {
	name := k8s.ChildName(movieName, "mediafile", path)
	spec := catalogac.MediaFileSpec().
		WithMediaRef(commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movieName}).
		WithPath(path).
		WithSizeBytes(info.Size()).
		WithModTime(metav1.NewTime(info.ModTime())).
		WithQuality(parsed.Quality).
		WithRevision(parsed.Revision).
		WithReleaseType(parsed.ReleaseType).
		WithReleaseGroup(parsed.Group).
		WithEdition(parsed.Edition)
	if len(parsed.Languages) > 0 {
		spec = spec.WithLanguages(parsed.Languages...)
	}

	if _, err := k8s.Apply(ctx, w.Client, k8s.ManagerImportarr,
		catalogac.MediaFile(name, st.scan.Namespace).WithSpec(spec)); err != nil {
		return fmt.Errorf("rescan: apply media file %s: %w", name, err)
	}
	return nil
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
