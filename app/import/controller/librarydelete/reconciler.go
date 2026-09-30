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

package librarydelete

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;series;artists;authors;audiobooks;comics,verbs=get;list;watch;patch;delete
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=episodes;albums;books;issues,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=importexclusions,verbs=get;create
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconciler carries out the delete request on one kind.
type Reconciler struct {
	client.Client
	Recorder k8sevents.EventRecorder
	kind     kindSpec
}

// SetupWithManager registers the indexes once and a controller per
// deletable kind.
func SetupWithManager(mgr ctrl.Manager) error {
	if err := RegisterIndexes(context.Background(), mgr.GetFieldIndexer()); err != nil {
		return err
	}
	for _, k := range kinds() {
		r := &Reconciler{Client: mgr.GetClient(), Recorder: mgr.GetEventRecorder("librarydelete"), kind: k}
		if err := ctrl.NewControllerManagedBy(mgr).
			Named("librarydelete-"+string(k.kind)).
			For(k.newObject(), builder.WithPredicates(deleteRequested())).
			Complete(r); err != nil {
			return fmt.Errorf("librarydelete %s: %w", k.kind, err)
		}
	}
	return nil
}

// deleteRequested passes a pending request: at start one not refused yet,
// and on update a new or renewed request -- never importarr's own
// delete-error write, which would retry a refusal forever.
func deleteRequested() predicate.Predicate {
	ann := func(o client.Object, key string) string { return o.GetAnnotations()[key] }
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return ann(e.Object, catalogv1alpha1.AnnotationDelete) != "" && ann(e.Object, catalogv1alpha1.AnnotationDeleteError) == ""
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			o, n := e.ObjectOld, e.ObjectNew
			if ann(n, catalogv1alpha1.AnnotationDelete) == "" {
				return false
			}
			return ann(n, catalogv1alpha1.AnnotationDelete) != ann(o, catalogv1alpha1.AnnotationDelete) ||
				(ann(o, catalogv1alpha1.AnnotationDeleteError) != "" && ann(n, catalogv1alpha1.AnnotationDeleteError) == "")
		},
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// Reconcile deletes an annotated item: exclusion, disk (for "files"),
// MediaFiles, then the item. A refusal writes delete-error and is not
// retried; an API or I/O error writes it and is retried with backoff.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	obj := r.kind.newObject()
	if err := r.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	mode := obj.GetAnnotations()[catalogv1alpha1.AnnotationDelete]
	if mode == "" || !obj.GetDeletionTimestamp().IsZero() {
		return ctrl.Result{}, nil
	}
	log := logging.FromContext(ctx).With("kind", string(r.kind.kind), "item", obj.GetName(), "mode", mode)
	if mode != catalogv1alpha1.DeleteFiles && mode != catalogv1alpha1.DeleteRecords {
		return ctrl.Result{}, r.fail(ctx, obj, fmt.Errorf("%w: %s=%q, want %q or %q", ErrRefused,
			catalogv1alpha1.AnnotationDelete, mode, catalogv1alpha1.DeleteFiles, catalogv1alpha1.DeleteRecords))
	}

	t, err := r.target(ctx, obj)
	if err != nil {
		return ctrl.Result{}, r.fail(ctx, obj, err)
	}
	if mode == catalogv1alpha1.DeleteFiles {
		var all catalogv1alpha1.MediaFileList
		if err := r.List(ctx, &all, client.InNamespace(obj.GetNamespace())); err != nil {
			return ctrl.Result{}, r.fail(ctx, obj, err)
		}
		occupants, err := r.occupants(ctx, obj, t)
		if err != nil {
			return ctrl.Result{}, r.fail(ctx, obj, err)
		}
		if err := Check(t, all.Items, occupants); err != nil {
			return ctrl.Result{}, r.fail(ctx, obj, err)
		}
	}
	if obj.GetAnnotations()[catalogv1alpha1.AnnotationDeleteAddExclusion] == "true" {
		if err := r.exclude(ctx, obj); err != nil {
			return ctrl.Result{}, r.fail(ctx, obj, err)
		}
	}
	if mode == catalogv1alpha1.DeleteFiles {
		if err := RemoveFromDisk(ctx, t); err != nil {
			return ctrl.Result{}, r.fail(ctx, obj, err)
		}
	}
	for i := range t.Files {
		if err := client.IgnoreNotFound(r.Delete(ctx, &t.Files[i])); err != nil {
			return ctrl.Result{}, r.fail(ctx, obj, fmt.Errorf("delete media file %s: %w", t.Files[i].Name, err))
		}
	}
	if err := client.IgnoreNotFound(r.Delete(ctx, obj)); err != nil {
		return ctrl.Result{}, r.fail(ctx, obj, err)
	}
	log.Info("librarydelete: deleted", "files", len(t.Files), "folder", t.Folder)
	r.event(obj, corev1.EventTypeNormal, "Deleted", "deleted with %d media file(s) (%s)", len(t.Files), mode)
	return ctrl.Result{}, nil
}

// target resolves what the delete acts on.
func (r *Reconciler) target(ctx context.Context, obj client.Object) (Target, error) {
	t := Target{Folder: r.kind.path(obj), Keys: map[string]bool{TargetKey(r.kind.kind, obj.GetName()): true}}
	var rf catalogv1alpha1.RootFolder
	switch err := r.Get(ctx, types.NamespacedName{Namespace: obj.GetNamespace(), Name: r.kind.rootFolderRef(obj)}, &rf); {
	case err == nil:
		t.Root = rf.Spec.Path
	case !apierrors.IsNotFound(err):
		return t, err
	}
	if r.kind.newChildren != nil {
		children := r.kind.newChildren()
		if err := r.List(ctx, children, client.InNamespace(obj.GetNamespace()),
			client.MatchingFields{childByParent: obj.GetName()}); err != nil {
			return t, fmt.Errorf("list %s of %s: %w", r.kind.childKind, obj.GetName(), err)
		}
		items, err := metaList(children)
		if err != nil {
			return t, err
		}
		for _, name := range items {
			t.Keys[TargetKey(r.kind.childKind, name)] = true
		}
	}
	seen := map[string]bool{}
	for key := range t.Keys {
		var mfs catalogv1alpha1.MediaFileList
		if err := r.List(ctx, &mfs, client.InNamespace(obj.GetNamespace()), client.MatchingFields{mediaFileByTarget: key}); err != nil {
			return t, fmt.Errorf("list media files of %s: %w", key, err)
		}
		for _, mf := range mfs.Items {
			if !seen[mf.Name] {
				seen[mf.Name] = true
				t.Files = append(t.Files, mf)
			}
		}
	}
	return t, nil
}

// occupants are the folders the item's folder must not hold: every other
// library item's status.path -- in any namespace, since two namespaces'
// RootFolders may share a path -- except the item's own children, and
// every RootFolder's spec.path.
func (r *Reconciler) occupants(ctx context.Context, obj client.Object, t Target) ([]Occupant, error) {
	var out []Occupant
	for _, k := range append(kinds(), bookKind()) {
		list := k.newList()
		if err := r.List(ctx, list); err != nil {
			return nil, fmt.Errorf("list %s: %w", k.kind, err)
		}
		if err := meta.EachListItem(list, func(o runtime.Object) error {
			item := o.(client.Object)
			own := item.GetNamespace() == obj.GetNamespace() && t.Keys[TargetKey(k.kind, item.GetName())]
			if own {
				return nil
			}
			if p := k.path(item); p != "" {
				out = append(out, Occupant{What: fmt.Sprintf("%s %s/%s", k.kind, item.GetNamespace(), item.GetName()), Path: p})
			}
			return nil
		}); err != nil {
			return nil, err
		}
	}
	var roots catalogv1alpha1.RootFolderList
	if err := r.List(ctx, &roots); err != nil {
		return nil, fmt.Errorf("list root folders: %w", err)
	}
	for _, rf := range roots.Items {
		out = append(out, Occupant{What: fmt.Sprintf("root folder %s/%s", rf.Namespace, rf.Name), Path: rf.Spec.Path})
	}
	return out, nil
}

// metaList is the names in a typed list.
func metaList(list client.ObjectList) ([]string, error) {
	raw, err := json.Marshal(list)
	if err != nil {
		return nil, err
	}
	var decoded struct {
		Items []struct {
			Metadata metav1.ObjectMeta `json:"metadata"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(decoded.Items))
	for _, it := range decoded.Items {
		out = append(out, it.Metadata.Name)
	}
	return out, nil
}

// exclude creates the item's ImportExclusion, named after it, once.
func (r *Reconciler) exclude(ctx context.Context, obj client.Object) error {
	spec, ok := r.kind.exclusion(obj)
	if !ok {
		return nil
	}
	ex := &catalogv1alpha1.ImportExclusion{
		ObjectMeta: metav1.ObjectMeta{Namespace: obj.GetNamespace(), Name: obj.GetName()},
		Spec:       spec,
	}
	if err := r.Create(ctx, ex); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create import exclusion: %w", err)
	}
	return nil
}

// fail records err on the item as delete-error plus a Warning Event. A
// refusal returns nil, so it waits for a renewed request; anything else is
// returned for the backoff retry.
func (r *Reconciler) fail(ctx context.Context, obj client.Object, err error) error {
	logging.FromContext(ctx).Warn("librarydelete: not deleted", "kind", string(r.kind.kind), "item", obj.GetName(), "error", err)
	r.event(obj, corev1.EventTypeWarning, "DeleteFailed", "%s", err.Error())
	patch, merr := json.Marshal(map[string]any{"metadata": map[string]any{
		"annotations": map[string]string{catalogv1alpha1.AnnotationDeleteError: err.Error()},
	}})
	if merr == nil {
		target := r.kind.newObject()
		target.SetNamespace(obj.GetNamespace())
		target.SetName(obj.GetName())
		if perr := r.Patch(ctx, target, client.RawPatch(types.MergePatchType, patch), client.FieldOwner(k8s.ManagerImportarr.String())); perr != nil {
			logging.FromContext(ctx).Error("librarydelete: record delete-error", "error", perr)
		}
	}
	if errors.Is(err, ErrRefused) {
		return nil
	}
	return err
}

func (r *Reconciler) event(obj client.Object, eventtype, reason, format string, args ...any) {
	if r.Recorder != nil {
		r.Recorder.Eventf(obj, nil, eventtype, reason, "Delete", format, args...)
	}
}
