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

package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	gvkschema "k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
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
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/version"
)

// AnnotationRefresh is the operator's forced metadata refresh (design
// 2026-09-23-library-page-design, "Metadata refresh"; spec §5's
// MetadataTask.refreshEpoch): `kubectl annotate <kind> <name>
// clustarr.io/refresh-metadata=<epoch>` -- the UI's "Refresh metadata"
// writes the same -- publishes one MetadataTask carrying that epoch, which
// the gateway serves past its cache, and consumes the annotation. The
// value is any positive integer; the requester's Unix time is the
// convention, so two requests never repeat an id.
const AnnotationRefresh = catalogv1alpha1.AnnotationRefreshMetadata

// refreshKinds is every kind the gateway refreshes (target.go's
// newTarget), by its GVK: the kinds with a status.metadata of their own.
// Episode and Issue have none and are refreshed through their parent.
var refreshKinds = map[gvkschema.GroupVersionKind]commonv1.MediaKind{
	catalogv1alpha1.GroupVersion.WithKind("Movie"):     commonv1.MediaKindMovie,
	catalogv1alpha1.GroupVersion.WithKind("Series"):    commonv1.MediaKindSeries,
	catalogv1alpha1.GroupVersion.WithKind("Artist"):    commonv1.MediaKindArtist,
	catalogv1alpha1.GroupVersion.WithKind("Album"):     commonv1.MediaKindAlbum,
	catalogv1alpha1.GroupVersion.WithKind("Author"):    commonv1.MediaKindAuthor,
	catalogv1alpha1.GroupVersion.WithKind("Book"):      commonv1.MediaKindBook,
	catalogv1alpha1.GroupVersion.WithKind("Audiobook"): commonv1.MediaKindAudiobook,
	catalogv1alpha1.GroupVersion.WithKind("Comic"):     commonv1.MediaKindComic,
}

// RefreshDeps is everything the Refresher needs from the process around it.
type RefreshDeps struct {
	// Client reads the annotated object's metadata and patches the
	// annotation away. It never writes spec or status.
	Client client.Client
	// Bus publishes the forced task.
	Bus events.Publisher
	// Recorder writes events.k8s.io/v1 Events on the object.
	Recorder k8sevents.EventRecorder
}

// The refresher's RBAC: the annotated kinds' metadata, and the patch that
// consumes the annotation. Nothing here touches a status subresource.
//
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;series;artists;albums;authors;books;audiobooks;comics,verbs=get;list;watch;patch

// Refresher is the clustarr.io/refresh-metadata handler: a metadata-only
// controller per kind in refreshKinds, woken by the annotation, that
// publishes the forced MetadataTask and consumes the annotation with a
// patch that first tests the value it saw, so a newer request written
// meanwhile is not lost but handled on the requeue.
type Refresher struct{ Deps RefreshDeps }

// NewRefresher builds a Refresher.
func NewRefresher(d RefreshDeps) *Refresher { return &Refresher{Deps: d} }

// SetupWithManager registers one metadata-only controller per kind.
func (r *Refresher) SetupWithManager(mgr ctrl.Manager) error {
	for gvk, kind := range refreshKinds {
		obj := &metav1.PartialObjectMetadata{}
		obj.SetGroupVersionKind(gvk)
		if err := ctrl.NewControllerManagedBy(mgr).
			Named("metadata-refresh-"+strings.ToLower(gvk.Kind)).
			For(obj, builder.WithPredicates(refreshRequested())).
			WithOptions(controller.Options{RecoverPanic: ptr.To(true), ReconciliationTimeout: time.Minute}).
			Complete(&refreshKind{r: r, gvk: gvk, kind: kind}); err != nil {
			return fmt.Errorf("metadata: refresh controller for %s: %w", gvk.Kind, err)
		}
	}
	return nil
}

// refreshRequested wakes a controller only when the annotation appears or
// changes value: a reconcile per request, none for anything else.
func refreshRequested() predicate.Predicate {
	value := func(o client.Object) (string, bool) {
		if o == nil {
			return "", false
		}
		v, ok := o.GetAnnotations()[AnnotationRefresh]
		return v, ok
	}
	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool { _, ok := value(e.Object); return ok },
		UpdateFunc: func(e event.UpdateEvent) bool {
			nv, nok := value(e.ObjectNew)
			if !nok {
				return false
			}
			ov, ook := value(e.ObjectOld)
			return !ook || ov != nv
		},
		DeleteFunc:  func(event.DeleteEvent) bool { return false },
		GenericFunc: func(e event.GenericEvent) bool { _, ok := value(e.Object); return ok },
	}
}

type refreshKind struct {
	r    *Refresher
	gvk  gvkschema.GroupVersionKind
	kind commonv1.MediaKind
}

func (k *refreshKind) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	obj := &metav1.PartialObjectMetadata{}
	obj.SetGroupVersionKind(k.gvk)
	if err := k.r.Deps.Client.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return k.r.Refresh(ctx, obj, k.kind)
}

// Refresh handles one annotated object: publish the forced task, record
// the Event, consume the annotation. A value that is not a positive
// integer is refused and consumed, so it is not retried forever.
func (r *Refresher) Refresh(ctx context.Context, obj *metav1.PartialObjectMetadata, kind commonv1.MediaKind) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "metadata.Refresher.Refresh")
	defer span.End()

	value, ok := obj.GetAnnotations()[AnnotationRefresh]
	if !ok {
		return ctrl.Result{}, nil
	}
	epoch, err := strconv.ParseInt(value, 10, 64)
	if err != nil || epoch <= 0 {
		r.Deps.Recorder.Eventf(obj, nil, corev1.EventTypeWarning, "MetadataRefreshRefused", "Refresh",
			"%s=%q is not a positive integer; a refresh epoch is the requester's Unix time", AnnotationRefresh, value)
		return ctrl.Result{}, r.consume(ctx, obj, value)
	}

	schemaName, data, err := schema.Encode(schema.MetadataTask{
		MediaRef:     commonv1.MediaRef{Kind: kind, Name: obj.Name},
		RefreshEpoch: epoch,
	})
	if err != nil {
		tracing.RecordError(span, err)
		return ctrl.Result{}, err
	}
	subject := events.WorkMetadataSubject(events.PriorityHigh, events.MediaKey(string(kind), obj.Namespace, obj.Name))
	env := &events.Envelope{
		// Not the scheduled task's id: JetStream's duplicate window would
		// otherwise drop the refresh as a repeat of it.
		ID:     events.MsgIDForObject(string(obj.UID), obj.Generation, "metadata:refresh:"+value),
		Type:   "catalog.MetadataTask",
		Schema: schemaName,
		Source: "catalogarr@" + version.String(),
		Key:    obj.Namespace + "/" + obj.Name,
		Time:   time.Now(),
		Data:   data,
	}
	if _, err := r.Deps.Bus.Publish(ctx, subject, env); err != nil {
		if errors.Is(err, events.ErrQueueFull) {
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}
		tracing.RecordError(span, err)
		return ctrl.Result{}, err
	}
	logging.FromContext(ctx).Info("metadata refresh requested",
		"kind", kind, "namespace", obj.Namespace, "name", obj.Name, "epoch", epoch)
	r.Deps.Recorder.Eventf(obj, nil, corev1.EventTypeNormal, "MetadataRefreshRequested", "Refresh",
		"metadata refresh %d requested: task published to %s", epoch, subject)
	return ctrl.Result{}, r.consume(ctx, obj, value)
}

type jsonPatchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value string `json:"value,omitempty"`
}

// consume removes the annotation, provided it still holds the value that
// was handled: a newer request written meanwhile fails the test, the patch
// is refused, and the requeue handles that value instead.
func (r *Refresher) consume(ctx context.Context, obj *metav1.PartialObjectMetadata, value string) error {
	path := "/metadata/annotations/" + strings.ReplaceAll(strings.ReplaceAll(AnnotationRefresh, "~", "~0"), "/", "~1")
	data, err := json.Marshal([]jsonPatchOp{
		{Op: "test", Path: path, Value: value},
		{Op: "remove", Path: path},
	})
	if err != nil {
		return err
	}
	if err := r.Deps.Client.Patch(ctx, obj, client.RawPatch(types.JSONPatchType, data)); err != nil {
		return fmt.Errorf("metadata: consume %s on %s %s/%s: %w", AnnotationRefresh, obj.Kind, obj.Namespace, obj.Name, err)
	}
	return nil
}
