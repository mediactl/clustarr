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
	if !fileimport.FileRefFitsRoot(ref, root.Spec.Kind) || ref.Kind == commonv1.MediaKindEpisode {
		return nil, refuse("%s %q cannot be assigned files under root folder %q, which is a %s root",
			fileimport.AnnotationImportTarget, raw, root.Name, root.Spec.Kind)
	}

	itemRoot, err := w.itemRootFolder(ctx, scan.Namespace, t)
	if err != nil {
		return nil, err
	}
	if itemRoot != root.Name {
		return nil, refuse("%s %q names an item stored under root folder %q, not %q",
			fileimport.AnnotationImportTarget, raw, itemRoot, root.Name)
	}
	return &manualAssign{target: t, ref: ref}, nil
}

// itemRootFolder reads the target and whatever parent names its root folder.
// A NotFound anywhere refuses the scan; any other read error is transient.
func (w *Worker) itemRootFolder(ctx context.Context, ns string, t fileimport.ImportTarget) (string, error) {
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
	case commonv1.MediaKindMovie:
		var m catalogv1alpha1.Movie
		if err := get("movie", ref.Name, &m); err != nil {
			return "", err
		}
		return m.Spec.RootFolderRef, nil
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
	fresh := freshNonVideoSpec(ref.Kind, path, rel)
	if ref.Kind == commonv1.MediaKindMovie {
		// A movie file's release identity still comes from its name when
		// the name parses; an unparseable one is recorded without it.
		fresh = frozenFields{}
		if parsed, err := release.ParsePath(path, release.Options{Kind: commonv1.MediaKindMovie}); err == nil {
			fresh = freshMovieSpec(parsed)
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
