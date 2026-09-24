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

package history

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
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// The replay handler watches every kind the DLQ projector can annotate
// (metadata only), and patches exactly the annotations it consumes. The
// patch verb is already granted to the projector above; the watch needs get,
// list and watch.
//
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;series;episodes;artists;albums;authors;books;audiobooks;comics;issues;importlists;libraryscans,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexers,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodejobs,verbs=get;list;watch;patch
// +kubebuilder:rbac:groups=subtitle.clustarr.io,resources=subtitlerequests,verbs=get;list;watch;patch

// AnnotationReplay is the operator's replay request, from design spec §5:
// `kubectl annotate <kind> <name> clustarr.io/replay=<dlq-seq>` republishes
// the dead letter stored at that CLUSTARR_DLQ sequence to its original
// subject, with a fresh Nats-Msg-Id. The DLQ projector records the
// sequence to use in [AnnotationDeadLetterSeq] when it can resolve one.
const AnnotationReplay = "clustarr.io/replay"

// ReplayKinds is every kind a dead letter can be replayed from: exactly the
// kinds the DLQ projector annotates (target.go's resolvers, and
// pkg/k8s.AnnotationDeadLettered's list), because a replay is only accepted
// from the object its dead letter resolves to.
var ReplayKinds = []schema.GroupVersionKind{
	catalogv1alpha1.GroupVersion.WithKind("Movie"),
	catalogv1alpha1.GroupVersion.WithKind("Series"),
	catalogv1alpha1.GroupVersion.WithKind("Episode"),
	catalogv1alpha1.GroupVersion.WithKind("Artist"),
	catalogv1alpha1.GroupVersion.WithKind("Album"),
	catalogv1alpha1.GroupVersion.WithKind("Author"),
	catalogv1alpha1.GroupVersion.WithKind("Book"),
	catalogv1alpha1.GroupVersion.WithKind("Audiobook"),
	catalogv1alpha1.GroupVersion.WithKind("Comic"),
	catalogv1alpha1.GroupVersion.WithKind("Issue"),
	catalogv1alpha1.GroupVersion.WithKind("ImportList"),
	catalogv1alpha1.GroupVersion.WithKind("LibraryScan"),
	indexv1alpha1.GroupVersion.WithKind("Indexer"),
	downloadv1alpha1.GroupVersion.WithKind("Download"),
	transcodev1alpha1.GroupVersion.WithKind("TranscodeJob"),
	subtitlev1alpha1.GroupVersion.WithKind("SubtitleRequest"),
}

// ReplayDeps is everything the replay handler needs from the process
// around it.
type ReplayDeps struct {
	// Client reads the annotated object's metadata and patches away the
	// annotations a replay consumes. It never writes spec or status.
	Client client.Client

	// Bus publishes the replayed message.
	Bus events.Publisher

	// DLQ reads the dead letter back by sequence; see DLQReaderFor.
	DLQ DLQReader

	// Recorder writes events.k8s.io/v1 Events on the object.
	Recorder k8sevents.EventRecorder
}

// Replayer is the clustarr.io/replay handler. It is a metadata-only
// controller per kind in [ReplayKinds], woken by the annotation, and it:
//
//  1. reads the dead letter at the annotated sequence off CLUSTARR_DLQ;
//  2. refuses it unless it resolves (target.go's Resolve) to this very
//     object -- a typo must not replay someone else's message;
//  3. republishes it to its original subject (Clustarr-DLQ-Subject) with
//     the Clustarr-DLQ-* headers stripped and a fresh envelope id,
//     "replay:<seq>:<uid>", so JetStream's duplicate window does not drop
//     it as the original, yet a retry of this same replay is dropped as a
//     duplicate of itself;
//  4. removes the replay annotation, and -- when the replayed sequence is
//     the one the projector recorded -- the dead-lettered marker too, since
//     the message is being retried; a new failure brings a new marker.
//
// A request it cannot honour (not a number, no such sequence, another
// object's message, no original subject) is answered with a Warning Event
// and the annotation removed: it would never succeed on a retry. Only a
// failure to read the DLQ or to publish is retried.
type Replayer struct {
	Deps ReplayDeps
}

// NewReplayer returns a Replayer over d.
func NewReplayer(d ReplayDeps) *Replayer { return &Replayer{Deps: d} }

// SetupWithManager registers one metadata-only controller per kind in
// [ReplayKinds]. It is leader-only, like every controller: a replay must
// happen once, not once per replica.
func (p *Replayer) SetupWithManager(mgr ctrl.Manager) error {
	for _, gvk := range ReplayKinds {
		obj := &metav1.PartialObjectMetadata{}
		obj.SetGroupVersionKind(gvk)
		if err := ctrl.NewControllerManagedBy(mgr).
			Named("replay-"+strings.ToLower(gvk.Kind)).
			For(obj, builder.WithPredicates(replayRequested())).
			WithOptions(controller.Options{RecoverPanic: ptr.To(true), ReconciliationTimeout: time.Minute}).
			Complete(&replayKind{p: p, gvk: gvk}); err != nil {
			return fmt.Errorf("history: replay controller for %s: %w", gvk.Kind, err)
		}
	}
	return nil
}

// replayRequested passes an object carrying the replay annotation, and only
// when the annotation is new or its value changed -- a status write on an
// annotated object is not a second request.
func replayRequested() predicate.Predicate {
	value := func(o client.Object) (string, bool) {
		if o == nil {
			return "", false
		}
		v, ok := o.GetAnnotations()[AnnotationReplay]
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

type replayKind struct {
	p   *Replayer
	gvk schema.GroupVersionKind
}

func (k *replayKind) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	obj := &metav1.PartialObjectMetadata{}
	obj.SetGroupVersionKind(k.gvk)
	if err := k.p.Deps.Client.Get(ctx, req.NamespacedName, obj); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	return k.p.Replay(ctx, obj)
}

// Replay handles the replay annotation on obj, a metadata-only object whose
// GroupVersionKind is set. It is what each per-kind controller calls, and is
// exported so a test can drive it without a manager.
func (p *Replayer) Replay(ctx context.Context, obj *metav1.PartialObjectMetadata) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "history.Replayer.Replay")
	defer span.End()
	value, ok := obj.GetAnnotations()[AnnotationReplay]
	if !ok {
		return ctrl.Result{}, nil
	}
	log := logging.FromContext(ctx).With("kind", obj.Kind, "namespace", obj.Namespace, "name", obj.Name, "replay", value)

	seq, err := strconv.ParseUint(strings.TrimSpace(value), 10, 64)
	if err != nil || seq == 0 {
		return p.refuse(ctx, obj, value, "%s=%q is not a CLUSTARR_DLQ sequence number", AnnotationReplay, value)
	}
	dlqSubject, env, err := p.Deps.DLQ.GetDeadLetter(ctx, seq)
	if errors.Is(err, ErrDeadLetterNotFound) {
		return p.refuse(ctx, obj, value, "CLUSTARR_DLQ holds no message at sequence %d (it keeps 30 days)", seq)
	}
	if err != nil {
		return ctrl.Result{}, err
	}

	gv := obj.GroupVersionKind().GroupVersion().String()
	target := Resolve(env)
	if !target.KindKnown() || target.Kind != obj.Kind || target.APIVersion != gv ||
		target.Namespace != obj.Namespace || target.Name != obj.Name {
		return p.refuse(ctx, obj, value, "dead letter %d (%s) concerns %s %s/%s, not this object",
			seq, dlqSubject, target.Kind, target.Namespace, target.Name)
	}
	original := env.Header(events.HeaderDLQSubject)
	if original == "" {
		return p.refuse(ctx, obj, value, "dead letter %d carries no %s header, so there is no subject to replay it to",
			seq, events.HeaderDLQSubject)
	}

	out := replayEnvelope(env, seq, obj.GetUID())
	if _, err := p.Deps.Bus.Publish(ctx, original, out); err != nil {
		if errors.Is(err, events.ErrQueueFull) {
			log.Warn("replay: the original subject's work queue is full; retrying in a minute")
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}
		return ctrl.Result{}, fmt.Errorf("history: replay dead letter %d to %s: %w", seq, original, err)
	}
	log.Info("replayed a dead letter", "seq", seq, "subject", original, "id", out.ID)
	p.event(obj, corev1.EventTypeNormal, "Replayed", "replayed dead letter %d to %s as %s", seq, original, out.ID)

	clearMarker := obj.GetAnnotations()[AnnotationDeadLetterSeq] == strconv.FormatUint(seq, 10)
	return ctrl.Result{}, p.consume(ctx, obj, value, clearMarker)
}

// refuse answers a replay request that can never succeed: a Warning Event
// saying why, and the annotation removed so it is not retried forever.
func (p *Replayer) refuse(ctx context.Context, obj *metav1.PartialObjectMetadata, value, format string, args ...any) (ctrl.Result, error) {
	p.event(obj, corev1.EventTypeWarning, "ReplayRefused", format, args...)
	return ctrl.Result{}, p.consume(ctx, obj, value, false)
}

// replayEnvelope is the dead letter as it is republished: its payload and
// original headers, less the Clustarr-DLQ-* ones (a fresh failure adds its
// own), under the id "replay:<seq>:<uid>".
func replayEnvelope(env *events.Envelope, seq uint64, uid types.UID) *events.Envelope {
	out := env.Clone()
	for _, h := range []string{
		events.HeaderDLQReason, events.HeaderDLQAttempts, events.HeaderDLQConsumer,
		events.HeaderDLQSubject, events.HeaderDLQMsgID,
	} {
		delete(out.Headers, h)
	}
	out.ID = "replay:" + strconv.FormatUint(seq, 10) + ":" + string(uid)
	return out
}

// jsonPatchOp is one RFC 6902 operation.
type jsonPatchOp struct {
	Op   string `json:"op"`
	Path string `json:"path"`
	// Value is nil for a remove; a test carries its string even when empty,
	// which is why this is not a plain omitempty string.
	Value any `json:"value,omitempty"`
}

// consume removes the replay annotation -- guarded by a JSON-patch test on
// its value, so an operator who re-annotated in between keeps their new
// request -- and, with clearMarker, the projector's two annotations, guarded
// the same way. A failed guard fails the whole patch and the reconcile
// retries against fresh values; the publish before it is idempotent under
// its envelope id, so that retry replays nothing twice.
func (p *Replayer) consume(ctx context.Context, obj *metav1.PartialObjectMetadata, value string, clearMarker bool) error {
	ops := []jsonPatchOp{
		{Op: "test", Path: annotationPath(AnnotationReplay), Value: value},
		{Op: "remove", Path: annotationPath(AnnotationReplay)},
	}
	if clearMarker {
		for _, key := range []string{AnnotationDeadLetterSeq, AnnotationDeadLettered} {
			if v, ok := obj.GetAnnotations()[key]; ok {
				ops = append(ops,
					jsonPatchOp{Op: "test", Path: annotationPath(key), Value: v},
					jsonPatchOp{Op: "remove", Path: annotationPath(key)})
			}
		}
	}
	data, err := json.Marshal(ops)
	if err != nil {
		return err
	}
	if err := p.Deps.Client.Patch(ctx, obj, client.RawPatch(types.JSONPatchType, data)); err != nil {
		return client.IgnoreNotFound(fmt.Errorf("history: consume %s on %s %s/%s: %w",
			AnnotationReplay, obj.Kind, obj.Namespace, obj.Name, err))
	}
	return nil
}

// annotationPath is the RFC 6901 pointer to one annotation key: "~" and "/"
// in the key are escaped as "~0" and "~1".
func annotationPath(key string) string {
	return "/metadata/annotations/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(key)
}

func (p *Replayer) event(obj *metav1.PartialObjectMetadata, eventType, reason, format string, args ...any) {
	if p.Deps.Recorder != nil {
		p.Deps.Recorder.Eventf(obj, nil, eventType, reason, "Replay", format, args...)
	}
}
