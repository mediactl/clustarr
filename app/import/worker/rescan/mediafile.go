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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/mediafilespec"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/names"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/release"
)

// FieldManager is the manager the scan applier records a scanned file's
// MediaFile spec under; the one declaration is mediafilespec's. The walk
// itself writes nothing (observe.go).
const FieldManager = mediafilespec.FieldManager

// AnnotationObservedFingerprint is how a post-transcode file's change on
// disk reaches catalog: mediafilespec.AnnotationObservedFingerprint, which
// the manager's scan applier applies from this walk's observation.
const AnnotationObservedFingerprint = mediafilespec.AnnotationObservedFingerprint

// ObservedFingerprint renders [AnnotationObservedFingerprint]'s value for a
// file of size bytes last modified at modTime.
func ObservedFingerprint(size int64, modTime time.Time) string {
	return mediafilespec.ObservedFingerprint(size, modTime)
}

// handleMediaFile attributes one walked media file and reports it to the
// manager's scan applier (observe.go), which decides and records it.
func (w *Worker) handleMediaFile(ctx context.Context, st *scanState, path string, info os.FileInfo, probe *fileProbe) error {
	return w.attributeMediaFile(ctx, st, path, info, probe)
}

// attributeMediaFile attributes one walked media file and reports it.
//
// The walk writes nothing (ADR-0019 §7.6). It proposes: the item the file
// belongs to, created from the file's provider id when none exists
// (schema.ItemCreate), the release identity a new MediaFile freezes
// (spec §8.4), and the MediaFile it read (the basis). The manager's scan
// applier refuses an exclusion, a root folder without a default profile, a
// path recorded against another item and a stale basis, and records the
// rest. A new movie file is probed once (probe, shared with the kept-output
// check), and the probe only corrects the quality its name says
// (quality.AugmentFromMediaInfo), as the file-import worker's does.
//
// A movie file whose name does not parse -- an obfuscated download name --
// is attributed by its item folder ("Title (Year) {tmdb-N}";
// release.Options.FolderFallback), and by that folder alone
// (itemFolderIdentity): its own id counts exactly as a filename's would,
// and without one the file is matched by the folder's title and year
// among existing movies, or left unmatched. The scanner never guesses.
//
// A path that already has a MediaFile is not attributed again. The
// MediaFile IS its attribution -- spec.mediaRef is immutable -- so a rescan
// only reports what it observes (size and mtime); the applier re-asserts
// every frozen field verbatim. Re-running the matcher could only agree, or
// list a file that is already in the catalog as unmatched.
func (w *Worker) attributeMediaFile(ctx context.Context, st *scanState, path string, info os.FileInfo, probe *fileProbe) error {
	now := w.now()
	// Relative to the ROOT FOLDER, which is what
	// catalogv1alpha1.UnmatchedFile.Path documents -- not to the walked
	// path, which is the root folder joined with spec.subpath and would
	// drop that prefix from every recorded entry.
	rel := relPath(st.root.Spec.Path, path)

	// Library rescan attributes movie, series, music, book, audiobook and
	// comic root folders. Any other kind is reported honestly rather than
	// guessed at.
	if fileKindForRoot(st.root.Spec.Kind) == "" {
		st.unmatched(rel, CodeUnsupportedKind, fmt.Sprintf(
			"root folder kind %q is not supported by library rescan yet", st.root.Spec.Kind), nil, now)
		return nil
	}

	existing, handled, err := w.existingForRescan(ctx, st, path, info)
	if err != nil || handled {
		return err
	}
	switch {
	case st.manual != nil:
		return w.assignManually(ctx, st, path, rel, info, existing)
	case existing != nil:
		return w.proposeFile(ctx, st, path, info, existing, existing.Spec.MediaRef, nil, nil, "recorded")
	case st.root.Spec.Kind == catalogv1alpha1.RootFolderKindSeries:
		return w.handleEpisodeFile(ctx, st, path, rel, info)
	case st.root.Spec.Kind != catalogv1alpha1.RootFolderKindMovie:
		return w.handleNonVideoFile(ctx, st, path, rel, info)
	}

	parsed, perr := release.ParsePath(path, release.Options{Kind: commonv1.MediaKindMovie, FolderFallback: true})
	if perr != nil {
		st.unmatched(rel, CodeParseError, fmt.Sprintf("could not parse the filename: %v", perr), nil, now)
		return nil
	}
	if ferr := itemFolderIdentity(parsed, path); ferr != nil {
		st.unmatched(rel, CodeParseError, fmt.Sprintf(
			"the filename does not parse, nor does its folder alone: %v", ferr), nil, now)
		return nil
	}

	result := MatchMovie(parsed, st.movies, w.resolveIMDb(ctx))
	if result.Unmatched {
		st.unmatched(rel, result.Code, result.Reason, result.Candidates, now)
		return nil
	}

	movieName := result.ExistingName
	profile, originalLanguage := st.root.Spec.Defaults.QualityProfileRef, ""
	create := st.creates[movieName]
	if movieName == "" {
		// A movie no Movie has: proposed for creation from its provider id.
		// The applier refuses an excluded id (P37) and a root folder with
		// no default profile (P38); every file of this movie in the walk
		// carries the same proposal, so each is refused or recorded alike.
		movieName = names.Movie(parsed.Title, result.TmdbID)
		create = &schema.ItemCreate{
			Kind: string(commonv1.MediaKindMovie), Name: movieName, TmdbID: result.TmdbID,
			QualityProfileRef: profile, RootFolderRef: st.root.Name,
			Title: parsed.Title, Year: int32(parsed.Year), //nolint:gosec // a release year
		}
		if st.creates == nil {
			st.creates = map[string]*schema.ItemCreate{}
		}
		st.creates[movieName] = create
		st.movies = append(st.movies, MovieCandidate{
			Name: movieName, TmdbID: result.TmdbID, Title: parsed.Title, Year: parsed.Year,
			QualityProfileRef: profile,
		})
		if st.task.DryRun {
			// The dry run's count of the item, once: later files of the
			// same movie count as updates, as they always have.
			st.progress.ItemsCreated++
			st.progress.FilesMatched++
			return nil
		}
	} else if c := st.movie(movieName); c != nil {
		profile, originalLanguage = c.QualityProfileRef, c.OriginalLanguage
	}
	if st.task.DryRun {
		st.predict(false)
		return nil
	}

	ref := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movieName}
	q, err := probedQuality(ctx, probe, rel, parsed.Quality)
	if err != nil {
		return err
	}
	parsed.Quality = q
	fresh := w.freshVideoSpec(ctx, st, path, parsed, profile, originalLanguage)
	evidence := "title"
	if result.TmdbID != 0 {
		evidence = "tmdb:" + strconv.FormatInt(result.TmdbID, 10)
	}
	if err := w.proposeFile(ctx, st, path, info, nil, ref, &fresh, create, evidence); err != nil {
		return err
	}
	logging.FromContext(ctx).Debug("reported a scanned file",
		"movie", movieName, "tmdbID", result.TmdbID, "create", create != nil, "fromFolder", parsed.FromFolder)
	return nil
}

// itemFolderIdentity narrows a parse attributed by its folder
// (parsed.FromFolder) to the ids its immediate parent folder carries
// (ruling R14). release.ParsePath merges the ids of every ancestor folder
// into its result, which is right for a file whose own name parses -- a
// library layout puts the id on the item folder, above a disc or extras
// folder -- but not when the folder is the identity: in
// "Alien Collection {tmdb-8091}/Alien (1979)/<hex>.mkv" the collection's
// id is no movie's, and reading it would attach the file to, or create,
// the wrong movie. The parent folder's own id, tmdb or imdb, counts
// exactly as a filename's would (ruling R15), so MatchMovie may attach by
// it or create from it; without one the file is matched by the folder's
// title and year among existing movies only, which never creates. A parse
// from the file's own name is left as it is.
func itemFolderIdentity(parsed *release.ParsedRelease, path string) error {
	if !parsed.FromFolder {
		return nil
	}
	folder, err := release.Parse(filepath.Base(filepath.Dir(path)), release.Options{Kind: commonv1.MediaKindMovie})
	if err != nil {
		return err
	}
	parsed.IDs = folder.IDs
	return nil
}

// movie is the walk's candidate named name, or nil.
func (st *scanState) movie(name string) *MovieCandidate {
	for i := range st.movies {
		if st.movies[i].Name == name {
			return &st.movies[i]
		}
	}
	return nil
}

// existingForRescan looks the path's MediaFile up and decides whether the
// walk is done with the file. handled is true, and the outcome counted or
// reported, for:
//
//   - a post-transcode file (spec.original false). catalog owns its
//     size, mtime and original flag: an unchanged one is counted in
//     Transcoded, and a changed one is reported, so the applier hands it to
//     catalog through [AnnotationObservedFingerprint] (HandedOver).
//   - on an incremental scan, a file whose size and mtime are unchanged
//     (Unchanged).
func (w *Worker) existingForRescan(
	ctx context.Context, st *scanState, path string, info os.FileInfo,
) (existing *catalogv1alpha1.MediaFile, handled bool, err error) {
	existing, err = w.existingMediaFile(ctx, st.scan.Namespace, path)
	if err != nil || existing == nil {
		return existing, false, err
	}
	if existing.Spec.Original != nil && !*existing.Spec.Original {
		if mediafilespec.SameFingerprint(existing, info) {
			st.progress.Transcoded++
			st.progress.FilesSkipped++
			return existing, true, nil
		}
		if st.task.DryRun {
			st.progress.HandedOver++
		}
		if err := w.proposeFile(ctx, st, path, info, existing, existing.Spec.MediaRef, nil, nil, "recorded"); err != nil {
			return existing, false, err
		}
		logging.FromContext(ctx).Info("a transcoded file changed on disk; reported for catalog",
			"mediaFile", existing.Name, "observed", ObservedFingerprint(info.Size(), info.ModTime()))
		return existing, true, nil
	}
	// The incremental fingerprint is spec.sizeBytes plus spec.modTime
	// directly: a file whose size and mtime are unchanged has nothing
	// new to report.
	if st.incremental() && mediafilespec.SameFingerprint(existing, info) {
		st.progress.Unchanged++
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

// maxMatchedFormats is MediaFileSpec.MatchedFormats' MaxItems.
const maxMatchedFormats = 200

// freshVideoSpec is what a scanned movie or episode file freezes: the
// release identity pkg/release parsed, with an untagged file taking the
// item's original language (Radarr's and Sonarr's AggregateLanguages), and
// the custom-format score of that release against the item's QualityProfile
// (a movie's, or an episode's series') -- the same scoring the
// file-import worker freezes, so a file that was scanned into the library
// and one that was imported compare on the same terms when an upgrade is
// decided. releaseGroup and edition are sent even when empty, as they always
// have been on this path.
//
// A profile that cannot be read or resolved leaves the file unscored --
// formatScore, matchedFormats and profileHash unsent -- and the empty
// profileHash says so; the file is still fully tracked.
func (w *Worker) freshVideoSpec(
	ctx context.Context, st *scanState, path string, parsed *release.ParsedRelease, profileRef, originalLanguage string,
) mediafilespec.Frozen {
	languageName := ""
	if originalLanguage != "" {
		if n, ok := catalogue.LanguageName(originalLanguage); ok {
			languageName = n
		}
	}
	parsed.Languages = parsed.LanguagesWith(languageName, release.GroupLanguagesFrom(ctx))
	f := mediafilespec.Frozen{
		Quality:      &parsed.Quality,
		Revision:     &parsed.Revision,
		ReleaseType:  parsed.ReleaseType,
		ReleaseGroup: new(parsed.Group),
		Edition:      new(parsed.Edition),
		Languages:    parsed.Languages,
	}
	profile := w.profile(ctx, st, profileRef)
	if profile == nil {
		return f
	}
	score, matched := profile.Score(ctx, w.catalogue(), parsed,
		// ReleaseTitle custom formats read the release's name and the
		// file's: Radarr's MovieFile input is the scene name, else the
		// original path, else the relative path. A file the scan finds has
		// no MediaFile yet, so no recorded release; its name is all there
		// is, for both.
		catalogue.ItemContext{
			OriginalLanguageName: languageName, ReleaseType: parsed.ReleaseType,
			ReleaseTitle: filepath.Base(path), Filename: filepath.Base(path),
		})
	f.FormatScore = new(int32(score)) //nolint:gosec // a custom-format score is a small bounded sum
	if len(matched) > maxMatchedFormats {
		matched = matched[:maxMatchedFormats]
	}
	f.MatchedFormats = matched
	f.ProfileHash = profile.Hash
	return f
}

// profile resolves a QualityProfile by name for scoring, once per walk. It
// returns nil -- and the walk scores nothing against it -- when the name is
// empty, the profile does not exist, or it does not resolve.
func (w *Worker) profile(ctx context.Context, st *scanState, name string) *quality.Profile {
	if name == "" {
		return nil
	}
	if p, ok := st.profiles[name]; ok {
		return p
	}
	if st.profiles == nil {
		st.profiles = map[string]*quality.Profile{}
	}
	var qp catalogv1alpha1.QualityProfile
	if err := w.Client.Get(ctx, client.ObjectKey{Name: name}, &qp); err != nil {
		logging.FromContext(ctx).Info("cannot score scanned files against a quality profile; leaving them unscored",
			"qualityProfile", name, "error", err)
		st.profiles[name] = nil
		return nil
	}
	resolved, errs := quality.FromCRD(&qp, w.catalogue())
	if len(errs) > 0 {
		logging.FromContext(ctx).Info("quality profile does not resolve; leaving scanned files unscored",
			"qualityProfile", name, "error", errors.Join(errs...))
		st.profiles[name] = nil
		return nil
	}
	st.profiles[name] = &resolved
	return &resolved
}

// catalogue is the custom-format corpus scoring runs against.
func (w *Worker) catalogue() *catalogue.Catalogue {
	if w.Catalogue != nil {
		return w.Catalogue
	}
	return catalogue.LoadedCatalogue()
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
			// Never a guess: whatever the class, the file is unmatched
			// for now, its reason naming the class (no agent, busy, timed
			// out), and the next scan asks again (ADR-0020 §4).
			return 0, fmt.Errorf("rescan: resolve imdb id %q: %w", imdbID, events.DescribeRequestError(events.RPCMetadataResolve, err))
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
