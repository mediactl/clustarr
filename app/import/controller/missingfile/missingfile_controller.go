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

// Package missingfile asks for the library scan that removes the record of
// a file gone from disk, once the file has been gone long enough.
//
// A MediaFile whose file is gone is removed on two observations (ADR-0019
// §7.6, P46): the MediaFile reconciler's own stat, held for
// mediafilespec.MissingGrace (Ready False, FileMissing), and a library
// scan's lstat of the same path from the import agent. The first is made by
// a pass over the file -- the day's recheck, or a scan that did not find it
// (mediafilespec.ObservedMissing). The second needs a scan to run after the
// grace, and until 2026-11-27 nothing asked for one: a root folder without
// a rescan schedule (every one on talos) kept the record until a person
// scanned the folder, and the item kept counting a file it did not have,
// so nothing searched for it again.
//
// This controller is that ask. For a MediaFile marked FileMissing it waits
// out the grace and a margin, then creates one incremental LibraryScan of
// the file's folder, named by the root folder, the folder and the minute
// the file was marked, so the files of one folder that went together share
// a scan and a retry creates nothing twice. The scan's prune pass makes the
// second observation, and the manager's scan applier removes the record.
// The controller deletes nothing and decides nothing about the file.
//
// It does not ask while more than [MassLimit] files are marked at once: a
// library that lost that many files in one go is more likely a share that
// came back empty than a person tidying up, and the scans would remove
// every record, each item then wanted and downloaded again. Those are left
// to a scan a person asks for, and looked at again every [HoldRecheck].
package missingfile

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/import/mediafilespec"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

const (
	// Margin is how long past mediafilespec.MissingGrace the scan is asked
	// for. It is also the width of the slot a scan is named by, the minute
	// the file was marked: every file of the slot was marked less than a
	// Margin after the first, so when the first's scan runs they are all
	// past the grace, by the agent's clock as well as this one.
	Margin = time.Minute

	// MassLimit is how many MediaFiles may be marked FileMissing at once
	// for a scan still to be asked for.
	MassLimit = 100

	// HoldRecheck is how soon a file held by MassLimit is looked at again.
	HoldRecheck = time.Hour

	// MarkedIndex indexes the MediaFiles marked FileMissing, by "true".
	MarkedIndex = "import.missingfile.marked"

	// labelRootFolder is rootfolderschedule.LabelRootFolder: the label every
	// LibraryScan of a root folder carries.
	labelRootFolder = "catalog.clustarr.io/root-folder"
)

// Reconciler asks for the scan of a missing file's folder.
type Reconciler struct {
	Client client.Client
	// Clock is the time source, injected so tests are deterministic.
	Clock func() time.Time
}

func (r *Reconciler) now() time.Time {
	if r.Clock != nil {
		return r.Clock().UTC()
	}
	return time.Now().UTC()
}

// Reconcile implements reconcile.Reconciler over a MediaFile.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx = logging.Subsystem(ctx, "import.missingfile")
	ctx, span := tracing.Start(ctx, "missingfile.Reconcile")
	defer span.End()

	var mf catalogv1alpha1.MediaFile
	if err := r.Client.Get(ctx, req.NamespacedName, &mf); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	since, marked := mediafilespec.MarkedMissing(&mf)
	if !marked || mf.Spec.Path == "" {
		return ctrl.Result{}, nil
	}
	if wait := since.Add(mediafilespec.MissingGrace + Margin).Sub(r.now()); wait > 0 {
		return ctrl.Result{RequeueAfter: wait}, nil
	}

	var all catalogv1alpha1.MediaFileList
	if err := r.Client.List(ctx, &all, client.InNamespace(mf.Namespace),
		client.MatchingFields{MarkedIndex: "true"}, client.Limit(MassLimit+1)); err != nil {
		err = fmt.Errorf("missingfile: list the media files marked missing: %w", err)
		tracing.RecordError(span, err)
		return ctrl.Result{}, err
	}
	if len(all.Items) > MassLimit {
		logging.FromContext(ctx).Warn("more files are missing at once than a scan is asked for; left to a scan a person asks for",
			"mediaFile", mf.Name, "limit", MassLimit)
		return ctrl.Result{RequeueAfter: HoldRecheck}, nil
	}

	var roots catalogv1alpha1.RootFolderList
	if err := r.Client.List(ctx, &roots, client.InNamespace(mf.Namespace)); err != nil {
		err = fmt.Errorf("missingfile: list root folders: %w", err)
		tracing.RecordError(span, err)
		return ctrl.Result{}, err
	}
	root, sub := Folder(roots.Items, mf.Spec.Path)
	if root == nil {
		// A file under no root folder is under none a scan walks.
		return ctrl.Result{}, nil
	}

	scan := &catalogv1alpha1.LibraryScan{}
	scan.Name = ScanName(root.Name, sub, since)
	scan.Namespace = mf.Namespace
	scan.Labels = map[string]string{labelRootFolder: root.Name}
	scan.Spec = catalogv1alpha1.LibraryScanSpec{
		RootFolderRef: root.Name, Subpath: sub, Mode: catalogv1alpha1.ScanModeIncremental,
	}
	switch err := r.Client.Create(ctx, scan, client.FieldOwner(k8s.ManagerImport.String())); {
	case err == nil:
		logging.FromContext(ctx).Info("a recorded file has been gone past the grace; asked for a scan of its folder",
			"mediaFile", mf.Name, "rootFolder", root.Name, "libraryScan", scan.Name, "missingSince", since)
	case apierrors.IsAlreadyExists(err):
		// The folder's scan for this minute is asked for already.
	default:
		err = fmt.Errorf("missingfile: create library scan %s: %w", scan.Name, err)
		tracing.RecordError(span, err)
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// Folder is the root folder path lies under -- the deepest, when one root
// folder holds another -- and the scan's subpath there: path's folder, or
// path itself for a file directly under the root folder, so the scan never
// widens to the whole library. nil when no root folder holds path.
func Folder(roots []catalogv1alpha1.RootFolder, path string) (root *catalogv1alpha1.RootFolder, sub string) {
	path = filepath.Clean(path)
	for i := range roots {
		rp := filepath.Clean(roots[i].Spec.Path)
		if roots[i].Spec.Path == "" || !strings.HasPrefix(path, rp+string(filepath.Separator)) {
			continue
		}
		if root == nil || len(rp) > len(filepath.Clean(root.Spec.Path)) {
			root = &roots[i]
		}
	}
	if root == nil {
		return nil, ""
	}
	rp := filepath.Clean(root.Spec.Path)
	dir := filepath.Dir(path)
	if dir == rp {
		dir = path
	}
	sub, _ = filepath.Rel(rp, dir)
	return root, sub
}

// ScanName names the scan of root's folder sub for a file marked at since.
func ScanName(root, sub string, since time.Time) string {
	return k8s.ChildName(root, "missing", sub, since.UTC().Truncate(Margin).Format("20060102T1504"))
}

// RegisterIndexes registers [MarkedIndex].
func RegisterIndexes(ctx context.Context, idx client.FieldIndexer) error {
	return idx.IndexField(ctx, &catalogv1alpha1.MediaFile{}, MarkedIndex, markedValues)
}

// markedValues is [MarkedIndex]'s value for o: "true" while its file is
// marked missing.
func markedValues(o client.Object) []string {
	mf, ok := o.(*catalogv1alpha1.MediaFile)
	if !ok {
		return nil
	}
	if _, marked := mediafilespec.MarkedMissing(mf); !marked {
		return nil
	}
	return []string{"true"}
}

// The scan is created as rootfolderschedule's and scanapply's are; a
// MediaFile is a row of the library store, read through the manager's cache.
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=libraryscans,verbs=create

// SetupWithManager registers the controller: it sees a MediaFile only while
// the file is marked missing, so a library whose files are all there costs
// it nothing.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Client == nil {
		r.Client = mgr.GetClient()
	}
	if err := RegisterIndexes(context.Background(), mgr.GetFieldIndexer()); err != nil {
		return fmt.Errorf("missingfile: register indexes: %w", err)
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("missingfile").
		For(&catalogv1alpha1.MediaFile{}, builder.WithPredicates(markedMissing())).
		WithOptions(controller.Options{RecoverPanic: new(true), ReconciliationTimeout: time.Minute}).
		Complete(r)
}

// markedMissing passes a MediaFile that is marked FileMissing, created or
// updated; a delete has nothing left to ask for.
func markedMissing() predicate.Predicate {
	is := func(o client.Object) bool {
		mf, ok := o.(*catalogv1alpha1.MediaFile)
		if !ok {
			return false
		}
		_, marked := mediafilespec.MarkedMissing(mf)
		return marked
	}
	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return is(e.Object) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return is(e.ObjectNew) },
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(e event.GenericEvent) bool { return is(e.Object) },
	}
}

// A signature drift is then a compile error rather than a runtime surprise.
var _ reconcile.Reconciler = (*Reconciler)(nil)
