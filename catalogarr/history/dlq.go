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
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// AnnotationDeadLettered is the metadata annotation [DLQProjector] applies,
// per ruling R1, instead of the DeadLettered status condition design spec §5
// originally asked for. Its value is "<original-subject>@<RFC3339>". It IS
// k8s.AnnotationDeadLettered -- the key every owning controller folds into
// its DeadLettered condition -- restated by reference so the two can never
// drift.
const AnnotationDeadLettered = k8s.AnnotationDeadLettered

// AnnotationDeadLetterSeq is the CLUSTARR_DLQ stream sequence of the dead
// letter [AnnotationDeadLettered] describes: the value an operator gives
// [AnnotationReplay] to replay it. [DLQProjector] applies it beside
// AnnotationDeadLettered, in the same apply, when its DLQ reader can name
// the sequence; without one (the in-memory bus) it is omitted.
const AnnotationDeadLetterSeq = "clustarr.io/dead-letter-seq"

// dlqRetry is how long Handle waits before retrying a failed annotation
// apply. It is short: the apiserver call it retries is a single PATCH, and a
// failure here is far more likely a blip than a permanent condition.
const dlqRetry = 10 * time.Second

// DLQDeps is everything the DLQ projector needs from the process around it.
type DLQDeps struct {
	// Client applies the dead-lettered annotation. Only Patch is ever
	// called, and only on the main resource -- never Status().
	Client client.Client

	// Recorder writes events.k8s.io/v1 Events.
	Recorder k8sevents.EventRecorder

	// DLQ, when set, names the stream sequence of each dead letter, so the
	// annotation and the Event can say what to replay. Optional: nil (the
	// in-memory bus has no stream to read) just leaves the sequence out.
	DLQ DLQReader

	// Now is a seam for tests; nil means time.Now.
	Now func() time.Time
}

func (d DLQDeps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;series;episodes;artists;albums;authors;books;audiobooks;comics;issues;importlists;libraryscans,verbs=patch
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexers,verbs=patch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads,verbs=patch
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodejobs,verbs=patch
// +kubebuilder:rbac:groups=subtitle.clustarr.io,resources=subtitlerequests,verbs=patch
//
// namespaces get is here for one reason, and it is not a read: a dead letter
// whose kind cannot be resolved gets a Warning Event REGARDING its core/v1
// Namespace (Handle's second case), so this package names corev1.Namespace.
// Creating an Event needs no permission on the object it regards, and nothing
// here Gets, Lists or Watches a Namespace -- but cmd/clustarr's
// TestEveryBuiltinKindAControllerTouchesHasAnRBACMarker is deliberately
// syntactic (a type a package names is a type it may one day read), and
// `get` is the narrowest verb that answers it. Added by task G1-5.
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get

// DLQProjector is the clustarr-dlq-projector consumer. Per ruling R1 it
// applies one metadata annotation -- [AnnotationDeadLettered] -- to the CR a
// dead letter concerns, plus [AnnotationDeadLetterSeq] when its DLQ reader
// can name the message's stream sequence, under k8s.ManagerDLQProjector,
// and emits a Warning Event saying how to replay it. It never patches a
// status subresource: every RBAC grant on a CR above is the main resource
// only, with the "patch" verb and nothing else -- no get, no list, no watch,
// because a blind server-side-apply PATCH needs none of them (the
// get/list/watch the replay handler needs are its own, in replay.go).
// dlq_envtest_test.go and replay_envtest_test.go assert both halves of that
// against a real apiserver's managedFields, not just this comment.
type DLQProjector struct {
	Deps DLQDeps
}

// NewDLQProjector returns a DLQProjector over d.
func NewDLQProjector(d DLQDeps) *DLQProjector { return &DLQProjector{Deps: d} }

// Subscription is the clustarr-dlq-projector durable consumer from §5's
// table (AckWait 30s, MaxDeliver 3, BackOff 5s/30s, MaxAckPending 64), read
// from events.Default() rather than restated.
func (p *DLQProjector) Subscription() events.Subscription {
	spec, ok := events.Default().Consumer(events.ConsumerDLQProjector)
	if !ok {
		// Unreachable: ConsumerDLQProjector is in defaultConsumers().
		return events.Subscription{}
	}
	return spec.Subscription()
}

// SetupWithManager registers the subscription as a manager.Runnable. See
// Sink.SetupWithManager's doc comment for why this must be a k8s.EveryReplica
// rather than a manager.RunnableFunc.
func (p *DLQProjector) SetupWithManager(mgr ctrl.Manager, bus events.Bus) error {
	return mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		stop, err := bus.Subscribe(ctx, p.Subscription(), p.Handle)
		if err != nil {
			return fmt.Errorf("catalogarr: subscribe dlq-projector: %w", err)
		}
		defer stop()
		<-ctx.Done()
		return nil
	}))
}

// Handle annotates the CR a dead letter concerns, or -- when the kind cannot
// be resolved unambiguously -- emits a namespace-level Event and does
// nothing else, rather than guess. See target.go's resolvers and this
// package's doc comment for what "cannot be resolved" covers: an unlisted
// schema, a namespace-wide task with no single object (WantedScan), or a
// fully global one with no namespace either (DefinitionsSync).
func (p *DLQProjector) Handle(ctx context.Context, m events.Message) error {
	return p.handle(ctx, m)
}

// sequenceOf names the CLUSTARR_DLQ sequence of the dead letter just
// delivered on dlqSubject, or 0 when there is no reader or it cannot say.
// The delivered message does not carry its own sequence (events.Message has
// no such accessor), so this asks the stream for the newest message on the
// subject -- which ends in the envelope id, so it is this dead letter or a
// copy of the same message dead-lettered again. A failure here costs only
// the replay hint, never the annotation.
func (p *DLQProjector) sequenceOf(ctx context.Context, dlqSubject string) uint64 {
	if p.Deps.DLQ == nil || dlqSubject == "" {
		return 0
	}
	seq, err := p.Deps.DLQ.LastDeadLetterSeq(ctx, dlqSubject)
	if err != nil {
		logging.FromContext(ctx).Warn("dlqprojector: could not name the dead letter's sequence", "subject", dlqSubject, "error", err)
		return 0
	}
	return seq
}

func (p *DLQProjector) handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	ctx = tracing.Extract(ctx, env)
	ctx, span := tracing.Start(ctx, "history.DLQProjector.Handle")
	defer span.End()
	log := logging.FromContext(ctx)

	origSubject := env.Header(events.HeaderDLQSubject)
	reason := env.Header(events.HeaderDLQReason)
	consumer := env.Header(events.HeaderDLQConsumer)
	attempts := env.Header(events.HeaderDLQAttempts)
	log = log.With("schema", env.Schema, "originalSubject", origSubject, "originalConsumer", consumer)

	target := Resolve(env)
	value := origSubject
	if value == "" {
		// Every real dead letter carries Clustarr-DLQ-Subject (see
		// events.DeadLetter); this is only reachable for a hand-built test
		// envelope or a future producer that forgets the header, and the
		// message's own delivery subject -- clustarr.dlq.<service>.<task>.
		// <id> -- is still a meaningful fallback.
		value = m.Subject()
	}
	value += "@" + p.Deps.now().UTC().Format(time.RFC3339)
	note := fmt.Sprintf("dead-lettered by %s after %s attempts: %s", consumer, attempts, reason)
	seq := p.sequenceOf(ctx, m.Subject())

	switch {
	case target.KindKnown():
		applied, err := p.applyAnnotation(ctx, target, value, seq)
		if err != nil {
			log.Error("dlqprojector: apply dead-letter annotation", "error", err)
			return events.Retry(dlqRetry, err)
		}
		if seq != 0 {
			note += fmt.Sprintf("; replay it with: kubectl annotate %s %s -n %s %s=%d",
				strings.ToLower(target.Kind), target.Name, target.Namespace, AnnotationReplay, seq)
		}
		p.Deps.Recorder.Eventf(applied, nil, corev1.EventTypeWarning, "DeadLettered", origSubject, note)

	case target.HasNamespace():
		log.Warn("dlqprojector: could not resolve a specific object kind for this dead letter; " +
			"emitting a namespace-level event instead of guessing")
		// The Namespace object carries ITS OWN name as its namespace. A
		// Namespace is cluster-scoped, and the events.k8s.io recorder files
		// an Event about an object with no namespace under "default" -- so
		// every namespace-level dead letter used to land in default, not in
		// the namespace it was about. With the namespace set, the Event is
		// created in that namespace (`kubectl get events -n <ns>` shows it),
		// which the apiserver accepts because the Event's namespace and its
		// regarding.namespace agree.
		ns := &corev1.Namespace{
			TypeMeta:   metav1.TypeMeta{Kind: "Namespace", APIVersion: "v1"},
			ObjectMeta: metav1.ObjectMeta{Name: target.Namespace, Namespace: target.Namespace},
		}
		if seq != 0 {
			note += fmt.Sprintf("; CLUSTARR_DLQ sequence %d", seq)
		}
		p.Deps.Recorder.Eventf(ns, nil, corev1.EventTypeWarning, "DeadLettered", origSubject,
			note+" (object kind unresolved; see Clustarr-DLQ-Subject on the CLUSTARR_DLQ entry for the original subject)")

	default:
		log.Warn("dlqprojector: could not resolve even a namespace for this dead letter; logging only")
	}
	return nil
}

// applyAnnotation declares metadata.annotations[AnnotationDeadLettered] --
// and, when seq is known, metadata.annotations[AnnotationDeadLetterSeq] --
// under k8s.ManagerDLQProjector, and nothing else. It builds the apply body
// from target.object() -- apiVersion, kind, namespace and name only -- so
// server-side apply's per-leaf ownership tracking (see CLAUDE.md) means
// this call can never claim, and therefore can never release, any other
// field on the object: not another annotation, not a label, and
// structurally not spec or status, since those subtrees never appear in the
// patch body at all. Each apply is this manager's complete declaration, so
// a dead letter whose sequence cannot be named releases (removes) an older
// one's sequence rather than leaving it to describe the wrong message.
//
// pkg/k8s.Apply is not used here: its generic constraint requires the
// generated api/applyconfiguration type for the target Kind, chosen at
// compile time, and this call's Kind is resolved at runtime from whichever
// of ~17 possible kinds a dead letter names. The two client.PatchOptions
// below are exactly the ones PatchStatus/Apply send (see pkg/k8s/patch.go);
// this call is unstructured because the target's Go type is not known here,
// not because it wants different apply semantics.
//
// It builds the request with client.RawPatch(types.ApplyPatchType, ...)
// rather than the client.Apply sentinel: client.Apply is the same patch
// type, but is deprecated in this controller-runtime version in favour of
// client.Client.Apply(), which -- per the paragraph above -- cannot take an
// unstructured object at all, so there is no non-deprecated replacement to
// migrate to here.
func (p *DLQProjector) applyAnnotation(ctx context.Context, t Target, value string, seq uint64) (*unstructured.Unstructured, error) {
	if err := k8s.ManagerDLQProjector.Validate(); err != nil {
		return nil, err
	}
	u := t.object()
	annotations := map[string]string{AnnotationDeadLettered: value}
	if seq != 0 {
		annotations[AnnotationDeadLetterSeq] = strconv.FormatUint(seq, 10)
	}
	u.SetAnnotations(annotations)
	data, err := json.Marshal(u)
	if err != nil {
		return nil, fmt.Errorf("dlqprojector: marshal apply body for %s %s/%s: %w", t.Kind, t.Namespace, t.Name, err)
	}
	if err := p.Deps.Client.Patch(ctx, u, client.RawPatch(types.ApplyPatchType, data),
		client.FieldOwner(k8s.ManagerDLQProjector.String()), client.ForceOwnership,
	); err != nil {
		return nil, fmt.Errorf("dlqprojector: apply %s on %s %s/%s: %w",
			AnnotationDeadLettered, t.Kind, t.Namespace, t.Name, err)
	}
	return u, nil
}
