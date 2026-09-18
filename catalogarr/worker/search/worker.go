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

// Package search is the shared search worker every search trigger lands on:
// the consumer of catalog.SearchTask.v1 on catalogarr-search-high and
// catalogarr-search-normal.
//
// Spec §8.2's first half runs here -- snapshot the item, its profile, its
// current MediaFile and its live Downloads; ask indexarr over
// clustarr.rpc.indexarr.search; run every candidate release through
// pkg/decision; rank what survived. What happens to the ranked list depends on
// why the search ran: an interactive search (SearchTask.SearchRef set) has its
// results written onto the Search object here, because only this worker ever
// holds them; every other reason hands the list to a Sink, because turning
// "best approved release" into a grab is §8.2's second half -- DelayProfile
// resolution, the clustarr-pending CAS and the grab lease -- which lives in
// its own worker.
package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jonboulle/clockwork"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	searchctl "github.com/mediactl/clustarr/catalogarr/controller/search"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// EvaluateFunc is the seam onto pkg/decision.Evaluate. It exists so a test can
// drive Handle's plumbing -- the snapshot, the RPC, the ranking, the two sinks
// -- without also exercising the decision engine's own rule table, which has
// its own tests in pkg/decision.
type EvaluateFunc func(
	ctx context.Context,
	t decision.Target,
	p quality.Profile,
	cat *catalogue.Catalogue,
	rels []commonv1.ReleaseInfo,
	o decision.Options,
) []decision.Decision

// Sink is what the worker hands ranked, non-interactive results to. The
// delay-profile and grab path supplies the real implementation; this package
// ships NopSink so the worker compiles, subscribes and is fully testable
// before that lands.
type Sink interface {
	Deliver(ctx context.Context, task schema.SearchTask, ranked []catalogv1alpha1.ReleaseDecision) error
}

// NopSink drops the ranked list with a warning and acks the task. It must not
// return an error: the automatic grab path being unwired is a documented gap
// in this phase, not a transient failure, and naking the message would spin
// the consumer until MaxDeliver dead-letters a task that was fine.
type NopSink struct{}

// Deliver implements Sink.
func (NopSink) Deliver(ctx context.Context, task schema.SearchTask, ranked []catalogv1alpha1.ReleaseDecision) error {
	logging.FromContext(ctx).Warn("search: no grab sink is wired; discarding ranked results",
		"kind", task.MediaRef.Kind, "reason", task.Reason, "results", len(ranked))
	return nil
}

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=searches,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=searches/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;episodes;series;mediafiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=qualityprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads;downloadclients,verbs=get;list;watch

// Worker consumes catalog.SearchTask.v1.
type Worker struct {
	Client    client.Client
	RPC       SearchRPC
	Catalogue *catalogue.Catalogue
	// Evaluate defaults to decision.Evaluate.
	Evaluate EvaluateFunc
	// Sink defaults to NopSink.
	Sink  Sink
	Clock clockwork.Clock
}

// NewWorker builds a Worker with the production decision engine, no grab sink
// and the real clock.
func NewWorker(c client.Client, rpc SearchRPC, cat *catalogue.Catalogue) *Worker {
	return &Worker{
		Client:    c,
		RPC:       rpc,
		Catalogue: cat,
		Evaluate:  decision.Evaluate,
		Sink:      NopSink{},
		Clock:     clockwork.NewRealClock(),
	}
}

func (w *Worker) now() time.Time {
	if w.Clock == nil {
		return time.Now().UTC()
	}
	return w.Clock.Now().UTC()
}

func (w *Worker) log(ctx context.Context) *slog.Logger { return logging.FromContext(ctx) }

func (w *Worker) evaluate() EvaluateFunc {
	if w.Evaluate != nil {
		return w.Evaluate
	}
	return decision.Evaluate
}

func (w *Worker) sink() Sink {
	if w.Sink != nil {
		return w.Sink
	}
	return NopSink{}
}

// Handle implements events.Handler for catalog.SearchTask.v1.
func (w *Worker) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	ctx = tracing.Extract(ctx, env)
	ctx, span := tracing.Start(ctx, "search.Worker.Handle")
	defer span.End()

	var task schema.SearchTask
	if err := schema.Decode(env.Schema, env.Data, &task); err != nil {
		return events.Discard("undecodable SearchTask", err)
	}

	ns, err := namespaceOf(env, task)
	if err != nil {
		return events.Discard("cannot resolve the task's namespace", err)
	}
	ctx = logging.With(ctx, "kind", string(task.MediaRef.Kind), "item", task.MediaRef.Name,
		"namespace", ns, "reason", string(task.Reason))

	switch task.MediaRef.Kind {
	case commonv1.MediaKindMovie, commonv1.MediaKindEpisode:
	default:
		// §16 scopes catalogarr's non-video kinds to M6. Discarding is right:
		// no number of redeliveries makes an artist searchable today.
		return events.Discard("non-video search is M6 scope",
			fmt.Errorf("kind=%s", task.MediaRef.Kind))
	}

	// An interactive search takes its limit, indexers and categories from the
	// Search object rather than from the task, so that editing the Search
	// before the worker picks the task up does what a user expects.
	var srch *catalogv1alpha1.Search
	if task.SearchRef != nil {
		srch = &catalogv1alpha1.Search{}
		key := client.ObjectKey{Namespace: ns, Name: task.SearchRef.Name}
		if err := w.Client.Get(ctx, key, srch); err != nil {
			if apierrors.IsNotFound(err) {
				// Searches are TTL-deleted; one that is already gone has
				// nowhere to put results.
				return events.Discard("Search object no longer exists", err)
			}
			return fmt.Errorf("get Search %s: %w", key, err)
		}
	}

	snap, err := w.snapshot(ctx, ns, task.MediaRef)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return events.Discard("target no longer exists", err)
		}
		tracing.RecordError(span, err)
		return err
	}

	profile, err := w.resolveProfile(ctx, snap.QualityProfileRef)
	if err != nil {
		return err
	}

	req := w.buildRequest(task, snap, srch)
	resp, err := w.RPC.Search(ctx, req)
	if err != nil {
		tracing.RecordError(span, err)
		return err // already an events.RetryError from busSearchRPC, or a plain error -> backoff nak
	}

	opts, err := w.decisionOptions(ctx, ns, task, profile)
	if err != nil {
		return err
	}

	rels := releaseInfos(resp.Releases, w.now())
	decisions := w.evaluate()(ctx, snap.Target, profile, w.Catalogue, rels, opts)
	recordDecisionMetrics(task.MediaRef.Kind, decisions)
	ranked := RankAndCap(decisions, opts, int(req.Limit))

	w.log(ctx).Info("search: decided",
		"releases", len(rels), "approved", countApproved(decisions), "kept", len(ranked))

	if srch != nil {
		return w.writeSearchStatus(ctx, srch, resp, ranked)
	}
	return w.sink().Deliver(ctx, task, ranked)
}

// namespaceOf resolves the namespace the task's objects live in. The envelope
// key is the documented carrier ("<namespace>/<name>" of the owning custom
// resource, see events.Envelope), and an interactive task's SearchRef repeats
// it in the payload -- so the payload is the fallback when a producer set the
// key to something else, which is worth tolerating because a wrong key would
// otherwise make every one of that producer's tasks undeliverable.
func namespaceOf(env *events.Envelope, task schema.SearchTask) (string, error) {
	if ns, _, ok := strings.Cut(env.Key, "/"); ok && ns != "" {
		return ns, nil
	}
	if task.SearchRef != nil && task.SearchRef.Namespace != "" {
		return task.SearchRef.Namespace, nil
	}
	return "", fmt.Errorf("envelope key %q is not <namespace>/<name> and the task carries no namespaced ref", env.Key)
}

// resolveProfile loads and resolves the QualityProfile the item points at.
// QualityProfile is cluster-scoped, so the lookup carries no namespace.
func (w *Worker) resolveProfile(ctx context.Context, ref string) (quality.Profile, error) {
	if ref == "" {
		return quality.Profile{}, events.Discard("item has no qualityProfileRef",
			errors.New("a search cannot be decided without a profile"))
	}
	var qp catalogv1alpha1.QualityProfile
	if err := w.Client.Get(ctx, client.ObjectKey{Name: ref}, &qp); err != nil {
		if apierrors.IsNotFound(err) {
			// Not a Discard: a profile can be created after the item that
			// references it, and the subscription's backoff is the right
			// place to wait for that.
			return quality.Profile{}, fmt.Errorf("get QualityProfile %s: %w", ref, err)
		}
		return quality.Profile{}, fmt.Errorf("get QualityProfile %s: %w", ref, err)
	}
	profile, ferrs := quality.FromCRD(&qp, w.Catalogue)
	if len(ferrs) > 0 {
		return quality.Profile{}, events.Discard("invalid QualityProfile", errors.Join(ferrs...))
	}
	return profile, nil
}

// buildRequest renders the RPC request, letting an interactive Search override
// the per-kind defaults with its own spec.
func (w *Worker) buildRequest(task schema.SearchTask, snap itemSnapshot, srch *catalogv1alpha1.Search) schema.SearchRequest {
	limit := int32(MaxResults)
	var indexerRefs []schema.Ref
	var categories []int32
	if srch != nil {
		if srch.Spec.Limit > 0 {
			limit = srch.Spec.Limit
		}
		categories = srch.Spec.Categories
		for _, name := range srch.Spec.IndexerRefs {
			indexerRefs = append(indexerRefs, schema.Ref{Namespace: srch.Namespace, Name: name})
		}
	}
	return BuildSearchRequest(task.MediaRef.Kind, snap.IDs, limit, task.UserInvoked, indexerRefs, categories)
}

// decisionOptions assembles pkg/decision's Options. PreferredProtocol is left
// empty on purpose: pkg/decision falls back to the profile's own preferred
// protocol, and there is no second source for it in this path.
func (w *Worker) decisionOptions(ctx context.Context, ns string, task schema.SearchTask, _ quality.Profile) (decision.Options, error) {
	enabled, err := w.enabledProtocols(ctx, ns)
	if err != nil {
		return decision.Options{}, err
	}
	return decision.Options{
		UserInvoked:      task.UserInvoked,
		ProtocolsEnabled: enabled,
	}, nil
}

// enabledProtocols reads the live DownloadClients to decide which protocols a
// grab could actually use. decision.Options.ProtocolsEnabled fails closed (a
// missing key is disabled), which is right once clients exist.
//
// A namespace with NO DownloadClient at all is treated as "both enabled"
// rather than "nothing enabled". Failing closed there would reject every
// release with "protocol disabled" purely because the operator has not
// configured a client yet, which hides the search results that would tell them
// what they are missing; the grab path refuses loudly at the point it actually
// needs a client.
func (w *Worker) enabledProtocols(ctx context.Context, ns string) (map[string]bool, error) {
	var list downloadv1alpha1.DownloadClientList
	if err := w.Client.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("list DownloadClients in %s: %w", ns, err)
	}
	if len(list.Items) == 0 {
		return map[string]bool{
			string(commonv1.ProtocolTorrent): true,
			string(commonv1.ProtocolUsenet):  true,
		}, nil
	}
	enabled := map[string]bool{}
	for i := range list.Items {
		c := &list.Items[i]
		if ptr.Deref(c.Spec.Enabled, true) {
			enabled[string(c.Spec.Protocol)] = true
		}
	}
	return enabled, nil
}

// releaseInfos projects the RPC reply onto what pkg/decision consumes.
//
// PublishedAt is backfilled when the indexer did not report one: ReleaseInfo
// carries it as a non-pointer metav1.Time, which marshals to JSON null when
// zero, and the CRD schema types it as a non-nullable string -- so a release
// with no publish date cannot be persisted at all. FetchedAt (when indexarr
// read the release) is the closest true statement, and it is also what usenet
// age ranking needs; falling back to "now" only happens when the reply carried
// neither.
func releaseInfos(rels []schema.Release, now time.Time) []commonv1.ReleaseInfo {
	out := make([]commonv1.ReleaseInfo, 0, len(rels))
	for _, r := range rels {
		info := r.Info
		if info.PublishedAt.IsZero() {
			if !r.FetchedAt.IsZero() {
				info.PublishedAt = metav1.NewTime(r.FetchedAt)
			} else {
				info.PublishedAt = metav1.NewTime(now)
			}
		}
		out = append(out, info)
	}
	return out
}

func countApproved(ds []decision.Decision) int {
	n := 0
	for _, d := range ds {
		if d.Approved {
			n++
		}
	}
	return n
}

// recordDecisionMetrics counts verdicts by kind and outcome. The reason label
// is the RejectionType, never the rejection message: messages are formatted
// with release-specific values and would blow the series cardinality open.
func recordDecisionMetrics(kind commonv1.MediaKind, ds []decision.Decision) {
	for _, d := range ds {
		if d.Approved {
			metrics.SearchDecisionsTotal.WithLabelValues(string(kind), "approved", "none").Inc()
			continue
		}
		reason := string(commonv1.RejectionPermanent)
		if d.TemporarilyRejected {
			reason = string(commonv1.RejectionTemporary)
		}
		metrics.SearchDecisionsTotal.WithLabelValues(string(kind), "rejected", reason).Inc()
	}
}

// writeSearchStatus records an interactive search's outcome.
//
// It writes under k8s.ManagerCatalogarrWorker and owns a set of fields
// deliberately disjoint from the Search reconciler's (§2's
// controller/worker manager split): finishedAt, indexerOutcomes and results
// here; phase, conditions, observedGeneration, startedAt and grabbed there.
// Server-side apply replaces a manager's whole ownership set on every apply,
// so every field this manager owns is sent on every call -- and none that the
// controller owns ever is, which is what stops the two from releasing each
// other's fields. The controller flips phase to Completed when it sees
// finishedAt land.
func (w *Worker) writeSearchStatus(ctx context.Context, srch *catalogv1alpha1.Search, resp schema.SearchResponse, ranked []catalogv1alpha1.ReleaseDecision) error {
	statusAC := searchctl.SearchStatus().
		WithFinishedAt(metav1.NewTime(w.now())).
		WithIndexerOutcomes(indexerOutcomes(resp.Outcomes)...).
		WithResults(ranked...)

	if _, err := k8s.PatchStatus(ctx, w.Client, k8s.ManagerCatalogarrWorker,
		searchctl.Search(srch.Name, srch.Namespace).WithStatus(statusAC)); err != nil {
		return fmt.Errorf("record search results: %w", err)
	}
	return nil
}

// indexerOutcomes maps the RPC's per-indexer report onto the API type.
func indexerOutcomes(outcomes []schema.SearchOutcome) []catalogv1alpha1.IndexerOutcome {
	out := make([]catalogv1alpha1.IndexerOutcome, 0, len(outcomes))
	for _, o := range outcomes {
		name := o.IndexerRef.Name
		if name == "" {
			name = o.IndexerName
		}
		if name == "" {
			// status.indexerOutcomes is a listType=map keyed by name; an
			// entry with no name would be rejected by the apiserver and
			// would take the whole status write with it.
			continue
		}
		out = append(out, catalogv1alpha1.IndexerOutcome{
			Name:       name,
			State:      outcomeState(o.Status),
			Count:      o.Releases,
			DurationMs: int32(o.ElapsedMillis),
			Error:      o.Error,
		})
	}
	return out
}

func outcomeState(s schema.SearchOutcomeStatus) catalogv1alpha1.IndexerOutcomeState {
	switch s {
	case schema.SearchOutcomeOK:
		return catalogv1alpha1.IndexerOutcomeOK
	case schema.SearchOutcomeTimeout:
		return catalogv1alpha1.IndexerOutcomeTimeout
	case schema.SearchOutcomeError:
		return catalogv1alpha1.IndexerOutcomeError
	default:
		return catalogv1alpha1.IndexerOutcomeSkipped
	}
}

// runnableFunc is a manager.Runnable that never needs leader election, so both
// search consumers run on every replica: the work queue itself is what stops
// two replicas doing the same task.
type runnableFunc func(ctx context.Context) error

// Start implements manager.Runnable.
func (f runnableFunc) Start(ctx context.Context) error { return f(ctx) }

// NeedLeaderElection implements manager.LeaderElectionRunnable.
func (runnableFunc) NeedLeaderElection() bool { return false }

// SetupWithManager registers the Download field indexes the worker reads and
// subscribes both search consumers. Nothing here registers itself: catalogarr's
// run.go calls this once, from setupWorkers.
func (w *Worker) SetupWithManager(mgr ctrl.Manager, bus events.Bus) error {
	if err := RegisterDownloadIndexes(context.Background(), mgr.GetFieldIndexer()); err != nil {
		return fmt.Errorf("register Download indexes: %w", err)
	}
	topo := events.Default()
	for _, name := range []string{events.ConsumerCatalogSearchHigh, events.ConsumerCatalogSearchNorm} {
		spec, ok := topo.Consumer(name)
		if !ok {
			return fmt.Errorf("no consumer spec named %s in the default topology", name)
		}
		sub := spec.Subscription()
		if err := mgr.Add(runnableFunc(func(ctx context.Context) error {
			stop, err := bus.Subscribe(ctx, sub, w.Handle)
			if err != nil {
				return fmt.Errorf("subscribe %s: %w", sub.Durable, err)
			}
			defer stop()
			<-ctx.Done()
			return nil
		})); err != nil {
			return fmt.Errorf("add %s consumer: %w", name, err)
		}
	}
	return nil
}
