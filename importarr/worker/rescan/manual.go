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
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/importarr/worker/fileimport"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/release"
)

// manualAssign is a LibraryScan's import-target annotation, validated
// against the cluster: the one item every unattributed file the scan walks
// is assigned to. See this package's doc, "Manual assignment".
type manualAssign struct {
	target fileimport.ImportTarget
	ref    commonv1.MediaRef

	// video is the scoring context of a movie or episode target -- the
	// QualityProfile and original language of the Movie, or of the
	// Episode's Series; nil for any other kind.
	video *MovieCandidate
}

// errScanRefused marks a LibraryScan the worker will not walk as asked: a
// malformed or unusable import-target, or a path outside its root folder.
// It is the scan's outcome, reported as Failed with this message, not a
// transient error to redeliver.
var errScanRefused = errors.New("scan refused")

func refuse(format string, args ...any) error {
	return fmt.Errorf("%w: %s", errScanRefused, fmt.Sprintf(format, args...))
}

// resolveManualAssign validates the scan's import-target annotation: the
// grammar (fileimport.ParseImportTarget, the same parser the Download
// annotation uses), that the item it names holds files of this root
// folder's kind, that it exists, and that it is stored under this root
// folder. A keyed comic target must name one of that comic's own issues.
// Returns nil when the scan carries no annotation.
func (w *Worker) resolveManualAssign(
	ctx context.Context, scan *catalogv1alpha1.LibraryScan, root *catalogv1alpha1.RootFolder,
) (*manualAssign, error) {
	raw, ok := scan.Annotations[fileimport.AnnotationImportTarget]
	if !ok {
		return nil, nil
	}
	t, err := fileimport.ParseImportTarget(raw)
	if err != nil {
		return nil, refuse("invalid annotation: %v", err)
	}
	ref := t.FileRef()
	if !fileimport.FileRefFitsRoot(ref, root.Spec.Kind) {
		return nil, refuse("%s %q cannot be assigned files under root folder %q, which is a %s root",
			fileimport.AnnotationImportTarget, raw, root.Name, root.Spec.Kind)
	}

	itemRoot, video, err := w.itemRootFolder(ctx, scan.Namespace, t)
	if err != nil {
		return nil, err
	}
	if itemRoot != root.Name {
		return nil, refuse("%s %q names an item stored under root folder %q, not %q",
			fileimport.AnnotationImportTarget, raw, itemRoot, root.Name)
	}
	return &manualAssign{target: t, ref: ref, video: video}, nil
}

// itemRootFolder reads the target and whatever parent names its root folder,
// and, for a movie or an episode, the scoring context a file assigned to it
// freezes. A NotFound anywhere refuses the scan; any other read error is
// transient.
func (w *Worker) itemRootFolder(ctx context.Context, ns string, t fileimport.ImportTarget) (string, *MovieCandidate, error) {
	if ref := t.FileRef(); ref.Kind == commonv1.MediaKindEpisode {
		return w.episodeRootFolder(ctx, ns, t, ref.Name)
	}
	if ref := t.FileRef(); ref.Kind == commonv1.MediaKindMovie {
		var m catalogv1alpha1.Movie
		if err := w.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, &m); err != nil {
			if apierrors.IsNotFound(err) {
				return "", nil, refuse("%s %q names movie %q, which does not exist",
					fileimport.AnnotationImportTarget, t.String(), ref.Name)
			}
			return "", nil, fmt.Errorf("rescan: get movie %s/%s: %w", ns, ref.Name, err)
		}
		c := candidateFor(&m)
		return m.Spec.RootFolderRef, &c, nil
	}
	root, err := w.nonMovieRootFolder(ctx, ns, t)
	return root, nil, err
}

// episodeRootFolder is itemRootFolder for an episode target ("episode/<e>",
// or "series/<s>/<e>", whose episode must be one of that series'): the
// Episode's Series names the root folder and the scoring context.
func (w *Worker) episodeRootFolder(ctx context.Context, ns string, t fileimport.ImportTarget, episode string) (string, *MovieCandidate, error) {
	get := func(kind, name string, obj client.Object) error {
		if err := w.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, obj); err != nil {
			if apierrors.IsNotFound(err) {
				return refuse("%s %q names %s %q, which does not exist",
					fileimport.AnnotationImportTarget, t.String(), kind, name)
			}
			return fmt.Errorf("rescan: get %s %s/%s: %w", kind, ns, name, err)
		}
		return nil
	}
	var ep catalogv1alpha1.Episode
	if err := get("episode", episode, &ep); err != nil {
		return "", nil, err
	}
	if t.Kind == commonv1.MediaKindSeries && ep.Spec.SeriesRef != t.Name {
		return "", nil, refuse("%s %q: episode %s belongs to series %q, not %q",
			fileimport.AnnotationImportTarget, t.String(), ep.Name, ep.Spec.SeriesRef, t.Name)
	}
	var s catalogv1alpha1.Series
	if err := get("series", ep.Spec.SeriesRef, &s); err != nil {
		return "", nil, err
	}
	c := &MovieCandidate{Name: s.Name, QualityProfileRef: s.Spec.QualityProfileRef}
	if s.Status.Metadata != nil {
		c.OriginalLanguage = s.Status.Metadata.OriginalLanguage
	}
	return s.Spec.RootFolderRef, c, nil
}

// nonMovieRootFolder is itemRootFolder for every kind but a movie.
func (w *Worker) nonMovieRootFolder(ctx context.Context, ns string, t fileimport.ImportTarget) (string, error) {
	get := func(kind, name string, obj client.Object) error {
		if err := w.Client.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, obj); err != nil {
			if apierrors.IsNotFound(err) {
				return refuse("%s %q names %s %q, which does not exist",
					fileimport.AnnotationImportTarget, t.String(), kind, name)
			}
			return fmt.Errorf("rescan: get %s %s/%s: %w", kind, ns, name, err)
		}
		return nil
	}
	ref := t.FileRef()
	switch ref.Kind {
	case commonv1.MediaKindAlbum:
		var al catalogv1alpha1.Album
		if err := get("album", ref.Name, &al); err != nil {
			return "", err
		}
		var ar catalogv1alpha1.Artist
		if err := get("artist", al.Spec.ArtistRef, &ar); err != nil {
			return "", err
		}
		return ar.Spec.RootFolderRef, nil
	case commonv1.MediaKindBook:
		var b catalogv1alpha1.Book
		if err := get("book", ref.Name, &b); err != nil {
			return "", err
		}
		if r := ptr.Deref(b.Spec.RootFolderRef, ""); r != "" {
			return r, nil
		}
		if a := ptr.Deref(b.Spec.AuthorRef, ""); a != "" {
			var au catalogv1alpha1.Author
			if err := get("author", a, &au); err != nil {
				return "", err
			}
			return au.Spec.RootFolderRef, nil
		}
		return "", nil
	case commonv1.MediaKindAudiobook:
		var ab catalogv1alpha1.Audiobook
		if err := get("audiobook", ref.Name, &ab); err != nil {
			return "", err
		}
		return ab.Spec.RootFolderRef, nil
	case commonv1.MediaKindIssue:
		var is catalogv1alpha1.Issue
		if err := get("issue", ref.Name, &is); err != nil {
			return "", err
		}
		if t.Kind == commonv1.MediaKindComic && is.Spec.ComicRef != t.Name {
			return "", refuse("%s %q: issue %s belongs to comic %q, not %q",
				fileimport.AnnotationImportTarget, t.String(), is.Name, is.Spec.ComicRef, t.Name)
		}
		var co catalogv1alpha1.Comic
		if err := get("comic", is.Spec.ComicRef, &co); err != nil {
			return "", err
		}
		return co.Spec.RootFolderRef, nil
	}
	return "", refuse("%s %q: %s items hold no files library rescan can record",
		fileimport.AnnotationImportTarget, t.String(), ref.Kind)
}

// assignManually records one walked file against the scan's import-target.
// It bypasses matching -- a person made this attribution, which is the only
// way the never-guess rule lets an unmatchable file into the catalog -- but
// not the MediaFile invariants: a path already recorded against another item
// is reported, never re-pointed, and a new MediaFile records
// importedFrom.manual.
func (w *Worker) assignManually(
	ctx context.Context, st *scanState, path, rel string, info os.FileInfo, existing *catalogv1alpha1.MediaFile,
) error {
	ref := st.manual.ref
	fresh := w.freshNonVideoSpec(ctx, ref.Kind, path, rel)
	if ref.Kind == commonv1.MediaKindMovie || ref.Kind == commonv1.MediaKindEpisode {
		// A movie or episode file's release identity still comes from its
		// name when the name parses, scored like any scanned file; an
		// unparseable one is recorded without it. The person named the
		// episode, so the name's numbering does not override them.
		fresh = frozenFields{}
		if parsed, err := release.ParsePath(path, release.Options{Kind: ref.Kind}); err == nil {
			var profile, language string
			if v := st.manual.video; v != nil {
				profile, language = v.QualityProfileRef, v.OriginalLanguage
			}
			fresh = w.freshVideoSpec(ctx, st, path, parsed, profile, language)
		}
	}
	fresh.importedFrom = catalogac.ImportSource().
		WithManual(true).
		WithImportedAt(metav1.NewTime(w.now()))
	return w.recordAttribution(ctx, st, path, rel, info, existing, ref, fresh)
}

// withinRoot reports whether path is root itself or lies beneath it.
func withinRoot(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}

// refuseScan ends a scan the worker will not walk: a Done checkpoint carrying
// the reason, which the LibraryScan controller reports as Failed. The message
// is then finished, not redelivered, because nothing about it changes on a
// retry.
func (w *Worker) refuseScan(ctx context.Context, st *scanState, cause error) error {
	st.progress.Done = true
	st.progress.Error = strings.TrimPrefix(cause.Error(), errScanRefused.Error()+": ")
	if err := w.checkpoint(ctx, st, true); err != nil {
		return events.Retry(checkpointInterval, err)
	}
	return nil
}
