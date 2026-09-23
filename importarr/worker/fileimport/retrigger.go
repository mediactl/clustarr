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

package fileimport

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/version"
)

// Retrigger re-publishes a Blocked Download's ImportTask when a user sets or
// changes one of the import annotations, which is what makes
// "catalog.clustarr.io/import-target directs a blocked import at a specific
// item" true: grabarr publishes the ImportTask once, on completion, and the
// Worker acks a Blocked outcome, so without this nothing would ever look at
// the annotation a user adds afterwards.
//
// It watches Downloads with a predicate that passes a create (so an
// annotation set while importarr was down is still acted on after a
// restart) and an update only when either annotation's value changed; the
// reconcile then publishes only for a Download whose status.import is
// Blocked. The Envelope ID carries a hash of both annotation values, so the
// broker's duplicate window absorbs a replica or a resync publishing the
// same instruction twice, while a changed instruction is a new message. A
// re-run that blocks again writes the same status.import, which is no
// annotation change and so cannot loop.
//
// Nothing registers it. The wiring task adds it to importarr's controller
// setup with
//
//	if err := (&fileimport.Retrigger{Client: mgr.GetClient(), Bus: bus}).SetupWithManager(mgr); err != nil {
//	        return fmt.Errorf("importarr: fileimport retrigger: %w", err)
//	}
type Retrigger struct {
	Client client.Client
	Bus    events.Publisher
	Clock  func() time.Time
}

// SetupWithManager registers the Retrigger controller.
func (r *Retrigger) SetupWithManager(mgr ctrl.Manager) error {
	if r.Client == nil {
		r.Client = mgr.GetClient()
	}
	if r.Bus == nil {
		return fmt.Errorf("fileimport: retrigger needs a bus")
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("fileimport-retrigger").
		For(&downloadv1alpha1.Download{}, builder.WithPredicates(ImportAnnotationsChanged())).
		WithOptions(controller.Options{RecoverPanic: ptr.To(true)}).
		Complete(r)
}

// ImportAnnotationsChanged passes a create carrying either import
// annotation, and an update that changed either one's value. Deletes and
// generic events never trigger a re-import.
func ImportAnnotationsChanged() predicate.Funcs {
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			a := e.Object.GetAnnotations()
			_, t := a[AnnotationImportTarget]
			_, o := a[AnnotationImportOverride]
			return t || o
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			if e.ObjectOld == nil || e.ObjectNew == nil {
				return false
			}
			oldA, newA := e.ObjectOld.GetAnnotations(), e.ObjectNew.GetAnnotations()
			return oldA[AnnotationImportTarget] != newA[AnnotationImportTarget] ||
				oldA[AnnotationImportOverride] != newA[AnnotationImportOverride]
		},
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// Reconcile publishes a fresh ImportTask for a Blocked Download that carries
// an import annotation.
func (r *Retrigger) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "fileimport.Retrigger.Reconcile")
	defer span.End()

	var dl downloadv1alpha1.Download
	if err := r.Client.Get(ctx, req.NamespacedName, &dl); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	if dl.Status.Import == nil || dl.Status.Import.State != downloadv1alpha1.ImportPhaseBlocked {
		return ctrl.Result{}, nil
	}
	target, hasTarget := dl.Annotations[AnnotationImportTarget]
	override, hasOverride := dl.Annotations[AnnotationImportOverride]
	if !hasTarget && !hasOverride {
		return ctrl.Result{}, nil
	}

	task := schema.ImportTask{
		DownloadRef: schema.Ref{Namespace: dl.Namespace, Name: dl.Name, UID: string(dl.UID)},
	}
	schemaName, data, err := schema.Encode(task)
	if err != nil {
		return ctrl.Result{}, reconcile.TerminalError(fmt.Errorf("fileimport: encode import task: %w", err))
	}
	env := &events.Envelope{
		ID:     RetriggerMessageID(dl.Namespace, dl.Name, string(dl.UID), target, override),
		Type:   "catalog.ImportTask",
		Schema: schemaName,
		Source: "importarr-fileimport-retrigger@" + version.String(),
		Key:    dl.Namespace + "/" + dl.Name,
		Time:   r.now(),
		Data:   data,
	}
	tracing.Inject(ctx, env)
	subject := events.WorkFileImportSubject(string(dl.UID))
	if _, err := r.Bus.Publish(ctx, subject, env); err != nil {
		return ctrl.Result{}, fmt.Errorf("fileimport: publish %s: %w", subject, err)
	}
	logging.FromContext(ctx).Info("fileimport: re-queued a blocked import after its import annotations changed",
		"download", req.String())
	return ctrl.Result{}, nil
}

// RetriggerMessageID is the Envelope ID of a re-queued import: distinct from
// grabarr's "<ns>/<name>:<uid>:import" and from any other annotation pair's.
func RetriggerMessageID(namespace, name, uid, target, override string) string {
	return namespace + "/" + name + ":" + uid + ":import:" + k8s.HashSuffix(target, override)
}

func (r *Retrigger) now() time.Time {
	if r.Clock != nil {
		return r.Clock()
	}
	return time.Now()
}

var _ reconcile.Reconciler = (*Retrigger)(nil)
