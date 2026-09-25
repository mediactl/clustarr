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

// Package rename is importarr's streaming rename pass (probe-driven naming
// spec §5): it moves a library file to the canonical path catalogarr
// proposes in MediaFile.status.naming, when the file's RootFolder sets
// spec.naming.renameFiles.
//
// The move and the spec apply are app/import/worker/rescan's RenameFile,
// which the LibraryScan rename pass shares; this package only decides when
// to call it. catalogarr owns all of MediaFileStatus, so nothing here
// writes status: after the move, catalogarr's own watch re-probes the file
// and reports it current.
//
// Nothing here reads status.mediaInfo, and nothing may: the controller
// role's cache strips it from every MediaFile (importarr's ManagerOptions)
// to keep the library's probe results -- about 16k of them on the owner's
// library -- out of this Deployment's memory. The probe's verdict reaches
// the rename as status.naming.quality, which RenameFile reads uncached.
package rename

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/import/worker/rescan"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Event reasons. A refusal RenameFile reports is recorded under its
// rescan.Rename* reason.
const (
	// ReasonRenamed: the file was moved to its canonical path.
	ReasonRenamed = "Renamed"
	// ReasonScanInProgress: the rename is held while a LibraryScan walks
	// the file's folder (spec D6).
	ReasonScanInProgress = "ScanInProgress"
)

// recheckAfter is how long a held file waits before it is tried again: a
// file that changed on disk, which the rescan has usually re-observed by
// then, or one under a scan still in progress.
const recheckAfter = time.Minute

// Reconciler renames library files to the paths catalogarr proposes.
type Reconciler struct {
	// Client reads MediaFiles and RootFolders through the cache, and
	// applies the renamed MediaFile's spec.
	Client client.Client

	// APIReader is the manager's uncached reader. RenameFile re-reads the
	// MediaFile through it immediately before it moves the file, and the
	// file's item is looked up through it: only a renameable file under a
	// renameFiles RootFolder ever needs one, so caching every Movie,
	// Episode and Series in this Deployment would cost far more.
	APIReader client.Reader

	// Recorder reports each rename, and each refusal, on the MediaFile.
	Recorder events.EventRecorder
}

// Reconcile renames one MediaFile's file when it is renameable
// ([Renameable]) and the RootFolder its item is stored under sets
// spec.naming.renameFiles.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "rename.Reconcile")
	defer span.End()
	ctx = logging.NewContext(ctx, logging.FromContext(ctx).With("mediaFile", req.String()))

	var mf catalogv1alpha1.MediaFile
	if err := r.Client.Get(ctx, req.NamespacedName, &mf); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if k8s.IsDeleting(&mf) || !Renameable(&mf) {
		return ctrl.Result{}, nil
	}
	roots, enabled, err := r.renameEnabled(ctx, &mf)
	if err != nil || !enabled {
		return ctrl.Result{}, err
	}
	scan, err := r.openScanOver(ctx, &mf, roots)
	if err != nil {
		return ctrl.Result{}, err
	}
	if scan != "" {
		// A scan walking the folder could see the vacated path mid-move and
		// re-attribute it, re-applying the frozen fields of a MediaFile the
		// rename is rewriting (spec D6). Wait for it to finish.
		r.event(&mf, corev1.EventTypeNormal, ReasonScanInProgress,
			"not renamed yet: LibraryScan %s is walking the file's folder", scan)
		return ctrl.Result{RequeueAfter: recheckAfter}, nil
	}

	out, err := rescan.RenameFile(ctx, r.Client, r.APIReader, &mf, false)
	if err != nil {
		return ctrl.Result{}, err
	}
	log := logging.FromContext(ctx)
	switch {
	case out.Moved:
		r.event(&mf, corev1.EventTypeNormal, ReasonRenamed, "renamed %s to %s", out.From, out.To)
		log.Info("renamed a library file to its canonical path", "from", out.From, "to", out.To)
	case out.Reason == rescan.RenameCollision:
		r.event(&mf, corev1.EventTypeWarning, out.Reason, "not renamed to %s: something already exists there", out.To)
	case out.Reason == rescan.RenameChanged:
		r.event(&mf, corev1.EventTypeWarning, out.Reason,
			"not renamed to %s: the file changed since it was recorded; retrying once it is re-observed", out.To)
		return ctrl.Result{RequeueAfter: recheckAfter}, nil
	case out.Reason == rescan.RenameHeld:
		r.event(&mf, corev1.EventTypeNormal, out.Reason, "not renamed to %s: %s", out.To, heldBecause(&mf))
	}
	return ctrl.Result{}, nil
}

// heldBecause says why RenameFile held a rename, for the Event.
func heldBecause(mf *catalogv1alpha1.MediaFile) string {
	if n := mf.Status.Naming; n != nil && n.Reason != "" {
		return "catalogarr holds it (" + string(n.Reason) + ")"
	}
	return "the proposed path is in another folder, and only files are renamed"
}

func (r *Reconciler) event(mf *catalogv1alpha1.MediaFile, eventType, reason, note string, args ...any) {
	if r.Recorder != nil {
		r.Recorder.Eventf(mf, nil, eventType, reason, "Rename", note, args...)
	}
}

// renameEnabled reports whether the RootFolder mf's item is stored under
// sets spec.naming.renameFiles, and returns the namespace's RootFolders.
// They are listed first, from the cache: with the switch off everywhere --
// the default -- the item is never looked up at all.
func (r *Reconciler) renameEnabled(ctx context.Context, mf *catalogv1alpha1.MediaFile) ([]catalogv1alpha1.RootFolder, bool, error) {
	var roots catalogv1alpha1.RootFolderList
	if err := r.Client.List(ctx, &roots, client.InNamespace(mf.Namespace)); err != nil {
		return nil, false, err
	}
	on := func(rf catalogv1alpha1.RootFolder) bool { return rf.Spec.Naming.RenameFilesOrDefault() }
	if !slices.ContainsFunc(roots.Items, on) {
		return roots.Items, false, nil
	}
	ref, err := r.rootFolderRef(ctx, mf)
	if err != nil || ref == "" {
		return roots.Items, false, err
	}
	i := slices.IndexFunc(roots.Items, func(rf catalogv1alpha1.RootFolder) bool { return rf.Name == ref })
	return roots.Items, i >= 0 && on(roots.Items[i]), nil
}

// openScanOver names a LibraryScan still in progress -- not Completed or
// Failed -- whose walk covers the folder mf's file is in, or returns "".
// A scan's walk is its RootFolder's path joined with spec.subpath; one
// narrowed to a single file in the folder counts too. LibraryScans are
// read from the cache the LibraryScan controller already keeps.
func (r *Reconciler) openScanOver(
	ctx context.Context, mf *catalogv1alpha1.MediaFile, roots []catalogv1alpha1.RootFolder,
) (string, error) {
	var scans catalogv1alpha1.LibraryScanList
	if err := r.Client.List(ctx, &scans, client.InNamespace(mf.Namespace)); err != nil {
		return "", err
	}
	dir := filepath.Dir(filepath.Clean(mf.Spec.Path))
	for i := range scans.Items {
		scan := &scans.Items[i]
		if phase := scan.Status.Phase; phase == catalogv1alpha1.ScanPhaseCompleted || phase == catalogv1alpha1.ScanPhaseFailed ||
			k8s.IsDeleting(scan) {
			continue
		}
		j := slices.IndexFunc(roots, func(rf catalogv1alpha1.RootFolder) bool { return rf.Name == scan.Spec.RootFolderRef })
		if j < 0 {
			continue
		}
		walk := filepath.Clean(filepath.Join(roots[j].Spec.Path, scan.Spec.Subpath))
		if dir == walk || strings.HasPrefix(dir, walk+string(filepath.Separator)) || filepath.Dir(walk) == dir {
			return scan.Name, nil
		}
	}
	return "", nil
}

// rootFolderRef is the RootFolder mf's item is stored under: a Movie's own,
// or an Episode's Series'. It is "" for an item that does not exist and for
// any other kind -- catalogarr names only movies and episodes.
func (r *Reconciler) rootFolderRef(ctx context.Context, mf *catalogv1alpha1.MediaFile) (string, error) {
	key := func(name string) types.NamespacedName {
		return types.NamespacedName{Namespace: mf.Namespace, Name: name}
	}
	ref := mf.Spec.MediaRef
	switch ref.Kind {
	case commonv1.MediaKindMovie:
		var m catalogv1alpha1.Movie
		if err := r.APIReader.Get(ctx, key(ref.Name), &m); err != nil {
			return "", client.IgnoreNotFound(err)
		}
		return m.Spec.RootFolderRef, nil
	case commonv1.MediaKindEpisode:
		var ep catalogv1alpha1.Episode
		if err := r.APIReader.Get(ctx, key(ref.Name), &ep); err != nil {
			return "", client.IgnoreNotFound(err)
		}
		var s catalogv1alpha1.Series
		if err := r.APIReader.Get(ctx, key(ep.Spec.SeriesRef), &s); err != nil {
			return "", client.IgnoreNotFound(err)
		}
		return s.Spec.RootFolderRef, nil
	}
	return "", nil
}

// Renameable reports whether mf is one this controller acts on: catalogarr
// says its path is not the canonical one (NamingCurrent False), and the file
// is present and probed (Ready and Probed True). catalogarr holds a file it
// cannot name yet with NamingCurrent Unknown, so a held file never passes;
// RenameFile refuses one too.
func Renameable(mf *catalogv1alpha1.MediaFile) bool {
	c := mf.Status.Conditions
	return meta.IsStatusConditionFalse(c, catalogv1alpha1.ConditionNamingCurrent) &&
		meta.IsStatusConditionTrue(c, catalogv1alpha1.MediaFileConditionReady) &&
		meta.IsStatusConditionTrue(c, catalogv1alpha1.MediaFileConditionProbed)
}

// Predicate admits a MediaFile that is [Renameable] when it is first seen,
// and on an update only when its conditions or its proposed path changed:
// catalogarr rewrites status on every reconcile, and the rename's own spec
// apply changes neither, so neither loops the controller.
func Predicate() predicate.Predicate {
	renameable := func(o client.Object) bool {
		mf, ok := o.(*catalogv1alpha1.MediaFile)
		return ok && Renameable(mf)
	}
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { return renameable(e.Object) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			old, ok := e.ObjectOld.(*catalogv1alpha1.MediaFile)
			if !ok || !renameable(e.ObjectNew) {
				return false
			}
			nw := e.ObjectNew.(*catalogv1alpha1.MediaFile)
			return !equality.Semantic.DeepEqual(old.Status.Conditions, nw.Status.Conditions) ||
				expectedPath(old) != expectedPath(nw)
		},
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// expectedPath is mf's status.naming.expectedPath, or "".
func expectedPath(mf *catalogv1alpha1.MediaFile) string {
	if mf.Status.Naming == nil {
		return ""
	}
	return mf.Status.Naming.ExpectedPath
}

// The spec apply is server-side apply, which is the patch verb. The
// Recorder is an events.k8s.io/v1 EventRecorder (mgr.GetEventRecorder); see
// app/import/controller/rootfolderschedule for why that group and not core.
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;series;episodes;rootfolders;libraryscans,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// SetupWithManager registers the rename controller. It needs the library
// mounted where MediaFile spec.path says the files are.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Client == nil {
		r.Client = mgr.GetClient()
	}
	if r.APIReader == nil {
		r.APIReader = mgr.GetAPIReader()
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("rename").
		For(&catalogv1alpha1.MediaFile{}, builder.WithPredicates(Predicate())).
		WithOptions(controller.Options{RecoverPanic: ptr.To(true), ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}

// A signature drift is then a compile error rather than a runtime surprise.
var _ reconcile.Reconciler = (*Reconciler)(nil)
