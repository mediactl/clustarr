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

package grab

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// pendingReadRetry is how long Handle waits before re-reading a pending entry
// the bucket failed to serve. It is shorter than the consumer's own first
// backoff step because the failure is a broker hiccup, not a busy downstream.
const pendingReadRetry = 10 * time.Second

// grabRetry is how long Handle waits after a failed grab. It is long enough
// for a transient apiserver or indexer blip to clear and short enough that the
// consumer's MaxDeliver of 5 still spans a useful window.
const grabRetry = 30 * time.Second

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;episodes;series,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies/status;episodes/status,verbs=get;patch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads,verbs=get;list;watch;create;patch
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexers,verbs=get;list;watch

// Handler is the catalogarr-grab consumer: the scheduled half of §8.2. A
// GrabTask published by Decide with WithScheduleAt arrives here once the
// delay has elapsed, and this is where the pending candidate -- which may have
// been replaced several times since -- is finally turned into a Download.
type Handler struct {
	Deps Deps

	// Topology is the bus topology this process installed --
	// k8s.Options.BusTopology(), the value run.go hands k8s.EnsureTopology
	// -- and Subscription looks the catalogarr-grab consumer up in it, as
	// catalogarr/worker/search does for its two. Nil means
	// events.Default(), which is only correct while BusTopology's
	// single-node collapse leaves consumers untouched; a caller that has
	// the process's topology should always set it, so the consumer a
	// replica subscribes to is the one it created.
	Topology *events.Topology
}

// NewHandler returns a Handler over d.
func NewHandler(d Deps) *Handler { return &Handler{Deps: d} }

// Subscription is the catalogarr-grab durable consumer from §5's table:
// AckWait 60s, MaxDeliver 5, BackOff 10s/1m/5m, MaxAckPending 16. It is read
// from the topology rather than restated so the tuning lives in exactly one
// place.
func (h *Handler) Subscription() events.Subscription {
	topo := events.Default()
	if h.Topology != nil {
		topo = *h.Topology
	}
	spec, ok := topo.Consumer(events.ConsumerCatalogGrab)
	if !ok {
		// A topology without the catalogarr-grab consumer. A zero
		// Subscription fails Subscription.Validate loudly at Subscribe
		// time rather than silently consuming nothing.
		return events.Subscription{}
	}
	return spec.Subscription()
}

// SetupWithManager registers the subscription as a manager.Runnable so its
// lifetime is the manager's: it subscribes when the manager starts and drains
// when the manager stops, rather than leaking a context.Background()
// subscription that outlives a graceful shutdown.
//
// This, with Topology set, is the ONLY registration catalogarr/run.go's
// setupQueueWorkers needs for the grab worker:
//
//	h := grab.NewHandler(grab.Deps{Client: mgr.GetClient(), Reader: mgr.GetAPIReader(), Bus: bus})
//	h.Topology = &topo // o.BusTopology()
//	if err := h.SetupWithManager(mgr, bus); err != nil {
//		return fmt.Errorf("catalogarr: subscribe grab: %w", err)
//	}
//
// It does NOT need leader election: every replica runs the queue workers and
// competes for the same durable consumer (§3's topology).
//
// That sentence was FALSE as written until Task C12a's review. The runnable
// was a manager.RunnableFunc, which is a bare func type with no
// NeedLeaderElection method, so controller-runtime's runnables.Add falls
// through its type switch to `default: r.LeaderElection.Add(...)` and puts it
// behind the lease anyway. With --leader-elect on, exactly one replica ran the
// grab consumer: latent at replicas 1 and a silent throughput ceiling at any
// scale-out. A worker-only Deployment was unaffected -- with leader election
// disabled controller-runtime treats the process as elected and starts the
// runnable regardless -- so the damage was confined to the combined-role
// Deployment that does elect. k8s.EveryReplica is a type WITH the method,
// which is what makes the claim above true everywhere rather than by
// accident.
func (h *Handler) SetupWithManager(mgr ctrl.Manager, bus events.Bus) error {
	return mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		stop, err := bus.Subscribe(ctx, h.Subscription(), h.Handle)
		if err != nil {
			return fmt.Errorf("catalogarr: subscribe grab: %w", err)
		}
		defer stop()
		<-ctx.Done()
		return nil
	}))
}

// Handle turns one delivered GrabTask into a Download.
//
// The pending entry, not the task, is the source of truth for WHAT to grab.
// The task carries only the media key, because between scheduling and delivery
// the candidate may have been replaced by a better release; re-reading the
// bucket is what makes "pending CAS keep-best updates a delayed grab" (§8.7)
// work.
//
// A missing pending entry is an acknowledgement, not an error. It is the
// normal shape of at-least-once delivery: an earlier delivery already grabbed
// and deleted the entry, or the seven-day bucket TTL expired it. Naking would
// turn a routine redelivery into MaxDeliver attempts and then a dead letter.
func (h *Handler) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	// Extract before Start, so this span continues the trace of the search or
	// RSS match that scheduled the grab rather than beginning a new one.
	ctx = tracing.Extract(ctx, env)
	ctx, span := tracing.Start(ctx, "grab.Handler.Handle")
	defer span.End()

	var task schema.GrabTask
	if err := schema.Decode(env.Schema, env.Data, &task); err != nil {
		return events.Discard("grab: malformed GrabTask", err)
	}
	ns, _, ok := strings.Cut(env.Key, "/")
	if !ok || ns == "" {
		return events.Discard("grab: envelope key is not <namespace>/<name>", fmt.Errorf("key=%q", env.Key))
	}

	log := logging.FromContext(ctx).With("kind", string(task.MediaRef.Kind), "namespace", ns)
	// MediaKeyFor(.., task.Keys), matching exactly what Decide used when it
	// wrote the entry: a pack's key is scoped to its episodes.
	mediaKey := MediaKeyFor(ns, task.MediaRef, task.Keys)
	kv := h.Deps.Bus.KV(events.BucketPending)
	pendingKey := events.PendingKey(mediaKey)

	entry, err := kv.Get(ctx, pendingKey)
	switch {
	case errors.Is(err, events.ErrKeyNotFound):
		log.Debug("grab: no pending candidate; an earlier delivery already grabbed it")
		return nil
	case err != nil:
		return events.Retry(pendingReadRetry, fmt.Errorf("grab: read pending candidate: %w", err))
	}

	var pv pendingValue
	if err := json.Unmarshal(entry.Value, &pv); err != nil {
		return events.Discard("grab: corrupt pending candidate", err)
	}
	grabbedBy := pv.GrabbedBy
	if grabbedBy == "" {
		// Entries written before pendingValue carried GrabbedBy, and any
		// caller that left it unset. Search is the overwhelmingly common
		// origin of a delayed grab, and the field is provenance only.
		grabbedBy = downloadv1alpha1.GrabSourceSearch
	}

	if err := performGrab(ctx, h.Deps, ns, pv.Target, pv.Keys, pv.Release, grabbedBy); err != nil {
		if errors.Is(err, ErrDuplicateGrab) {
			// Ack and stop, per §8.2. performGrab has already cleared
			// status.pendingGrab on every status target (clearPendingGrab);
			// deleting the KV entry here only stops a later redelivery
			// re-running the same losing grab -- it does not touch the
			// object, which is why the object-side clear has to happen
			// there and not here.
			log.Info("grab: already grabbed elsewhere; acknowledging", "error", err)
			if delErr := kv.Delete(ctx, pendingKey); delErr != nil {
				log.Warn("grab: deleting the consumed pending candidate failed", "error", delErr)
			}
			return nil
		}
		var discard *events.DiscardError
		if errors.As(err, &discard) {
			// A discarded grab will never be retried, so its candidate must
			// not stay the incumbent either: casKeepBest would keep
			// comparing every later release against it, and schedule it
			// again whenever nothing better arrived. performGrab has
			// already cleared status.pendingGrab where it can.
			if delErr := kv.Delete(ctx, pendingKey); delErr != nil {
				log.Warn("grab: deleting a discarded pending candidate failed", "error", delErr)
			}
			return err
		}
		return events.Retry(grabRetry, err)
	}

	// Deleted only after a successful grab: if the delete fails the next
	// redelivery re-runs performGrab, which is idempotent -- it re-enters the
	// lease its own Download name holds, finds its own Download and resumes
	// after the apply rather than creating a second one.
	if err := kv.Delete(ctx, pendingKey); err != nil {
		log.Warn("grab: deleting the consumed pending candidate failed", "error", err)
	}
	return nil
}
