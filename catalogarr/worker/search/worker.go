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
	"unicode/utf8"

	"github.com/jonboulle/clockwork"
	"go.opentelemetry.io/otel/trace"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	searchctl "github.com/mediactl/clustarr/catalogarr/controller/search"
	"github.com/mediactl/clustarr/catalogarr/worker/grab"
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

// cacheWarmRetry is how long to wait before looking again for an object a task
// depends on that the worker's informer cache has not seen yet.
//
// The producer writes the object through the apiserver and then publishes; the
// worker reads it back through a watch. Nothing orders those two, so a task can
// legitimately arrive microseconds before the object does. Discarding on the
// first miss would dead-letter a perfectly good search because a cache was a
// beat behind -- and leave its Search stuck in Running until the controller's
// timeout. One short retry closes the race; an object that is really gone is
// discarded on the next delivery.
const cacheWarmRetry = 2 * time.Second

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

// Sink is what the worker hands ranked, non-interactive results to.
// catalogarr/worker/grab.Sink is the production implementation and this is its
// method set verbatim.
//
// The namespace is a parameter because neither schema.SearchTask nor
// commonv1.MediaRef carries one, while every object a grab touches is
// namespaced. This worker has already resolved it from the envelope (see
// namespaceOf), so passing it costs nothing here and is unguessable on the
// other side. target carries Keys, so a pack grab reaches every episode it
// covers.
type Sink interface {
	Deliver(ctx context.Context, ns string, target commonv1.MediaRef, ranked []catalogv1alpha1.ReleaseDecision) error
}

// NopSink drops the ranked list with a warning and acks the task. It must not
// return an error: an unwired grab sink is a wiring gap, not a transient
// failure, and naking would spin the consumer until MaxDeliver dead-letters a
// task that was fine.
type NopSink struct{}

// Deliver implements Sink.
func (NopSink) Deliver(ctx context.Context, ns string, target commonv1.MediaRef, ranked []catalogv1alpha1.ReleaseDecision) error {
	logging.FromContext(ctx).Warn("search: no grab sink is wired; discarding ranked results",
		"namespace", ns, "kind", target.Kind, "item", target.Name, "results", len(ranked))
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
	// Publisher fans a WantedScan out into one SearchTask per eligible item.
	// SetupWithManager defaults it to the bus it is handed; a Worker driven
	// directly in a test sets it itself. A nil Publisher makes the WantedScan
	// branch fail loudly rather than sweep nothing in silence.
	Publisher events.Publisher
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

// Payload schemas the search consumers can hand this worker. They are computed
// rather than spelled out so a rename in pkg/events/schema cannot drift from
// the dispatch below.
var (
	schemaSearchTask = schema.SearchTask{}.Schema()
	schemaWantedScan = schema.WantedScan{}.Schema()
)

// Handle implements events.Handler for both payloads the search consumers
// carry.
//
// catalogarr-search-normal filters three subjects, not one: search.normal.>,
// search.low.> AND wantedscan.> (events.FilterCatalogWanted, pinned in
// pkg/events/topology.go). So this handler receives catalog.WantedScan.v1 as
// well as catalog.SearchTask.v1 and must dispatch on the schema header rather
// than assume: decoding everything as a SearchTask dead-lettered every
// twelve-hourly sweep on first delivery, silently.
func (w *Worker) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	ctx = tracing.Extract(ctx, env)
	ctx, span := tracing.Start(ctx, "search.Worker.Handle")
	defer span.End()

	switch env.Schema {
	case schemaSearchTask:
		return w.handleSearchTask(ctx, span, m)
	case schemaWantedScan:
		return w.handleWantedScan(ctx, span, m)
	default:
		return events.Discard("unknown payload schema on a search consumer",
			fmt.Errorf("schema=%q subject=%q", env.Schema, m.Subject()))
	}
}

// handleSearchTask runs spec §8.2's first half for one catalog item.
func (w *Worker) handleSearchTask(ctx context.Context, span trace.Span, m events.Message) error {
	env := m.Envelope()

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

	// The Search is fetched BEFORE anything that can fail terminally, so a
	// terminal failure has somewhere to report itself. Leaving the object in
	// Running until the reconciler's five-minute timeout and then telling the
	// user "Timeout: no results" is actively misleading when the real cause is
	// a missing qualityProfileRef.
	//
	// An interactive search also takes its keep-limit, indexers and categories
	// from the Search object rather than from the task, so editing the Search
	// before the worker picks the task up does what a user expects.
	var srch *catalogv1alpha1.Search
	if task.SearchRef != nil {
		srch = &catalogv1alpha1.Search{}
		key := client.ObjectKey{Namespace: ns, Name: task.SearchRef.Name}
		if err := w.Client.Get(ctx, key, srch); err != nil {
			if apierrors.IsNotFound(err) {
				// Searches are TTL-deleted; one that is really gone has
				// nowhere to put results. See missingObject for why the
				// first miss is retried instead.
				return missingObject(m, "Search object no longer exists", err)
			}
			return fmt.Errorf("get Search %s: %w", key, err)
		}
	}

	switch task.MediaRef.Kind {
	case commonv1.MediaKindMovie, commonv1.MediaKindEpisode:
	default:
		// §16 scopes catalogarr's non-video kinds to M6. Discarding is right:
		// no number of redeliveries makes an artist searchable today.
		return w.terminal(ctx, srch, "non-video search is M6 scope",
			fmt.Errorf("kind=%s", task.MediaRef.Kind))
	}

	snap, err := w.snapshot(ctx, ns, task.MediaRef)
	if err != nil {
		if apierrors.IsNotFound(err) {
			if m.Attempt() > 1 {
				return w.terminal(ctx, srch, "target no longer exists", err)
			}
			return missingObject(m, "target no longer exists", err)
		}
		tracing.RecordError(span, err)
		return err
	}

	profile, err := w.resolveProfile(ctx, snap.QualityProfileRef)
	if err != nil {
		var de *events.DiscardError
		if errors.As(err, &de) {
			return w.terminal(ctx, srch, de.Reason, err)
		}
		return err
	}

	req := w.buildRequest(ns, task, snap, srch)
	resp, err := w.RPC.Search(ctx, req)
	if err != nil {
		tracing.RecordError(span, err)
		return err // already an events.RetryError from busSearchRPC, or a plain error -> backoff nak
	}

	// The attempt is recorded the moment the indexers answered -- before the
	// decision, and before the sink. Two orderings matter here:
	//
	// Before the sink, because grab.RecordSearchAttempt and the grab sink
	// write the SAME owned set under the same field manager
	// (k8s.ManagerCatalogarrGrab: activeDownloadRef, pendingGrab,
	// lastSearchedAt, searchAttempts) through a read-modify-declare cycle
	// against the informer cache. If this ran after Deliver, it could read a
	// cache that had not yet caught up with the pendingGrab the sink just
	// wrote and release it. Running first means the sink's write is the later
	// one, and the one ordering this function controls is the safe one.
	//
	// After the RPC, because a search that never reached an indexer is not an
	// attempt: stamping it would let the backoff ladder grow while nothing was
	// actually being searched for.
	w.recordAttempt(ctx, ns, grabTarget(task))

	opts, err := w.decisionOptions(ctx, ns, task)
	if err != nil {
		return err
	}

	// Which indexers answered with an id query is known only now. A release
	// from one of them was matched to the item's id server-side; one from a
	// text-fallback indexer was matched by keyword and has to prove its own
	// identity (pkg/decision's identity check).
	snap.Target.Identity.IDQueryIndexers = idQueryIndexers(resp.Outcomes)

	rels := releaseInfos(resp.Releases)
	decisions := w.evaluate()(ctx, snap.Target, profile, w.Catalogue, rels, opts)
	recordDecisionMetrics(task.MediaRef.Kind, decisions)
	ranked := RankAndCap(decisions, opts, keepLimit(srch))

	w.log(ctx).Info("search: decided",
		"releases", len(rels), "approved", countApproved(decisions), "kept", len(ranked),
		"truncated", resp.Truncated)

	if srch != nil {
		return w.writeResults(ctx, srch, resp.Outcomes, ranked)
	}
	return w.sink().Deliver(ctx, ns, grabTarget(task), ranked)
}

// recordAttempt stamps status.lastSearchedAt and status.searchAttempts on the
// item that was just searched for, which is what gives spec §6.1's
// "per-item >=6h gap and Attempts backoff 6h*2^n capped 7d" something to
// count. Without it wantedcron.Backoff stays flat at MinimumGap forever and
// every twelve-hourly sweep re-searches every still-wanted item.
//
// The write goes through catalogarr/worker/grab, which owns that field set
// under k8s.ManagerCatalogarrGrab and re-declares all four fields on every
// apply. Reimplementing the cycle here would release the grab path's
// activeDownloadRef and pendingGrab, which is the failure that split
// catalogarr-worker into per-consumer managers in the first place.
//
// Failing to record is deliberately NOT fatal to the task. The search itself
// succeeded; results are about to be written or a grab dispatched, and
// returning an error here would nak the message and re-run the whole RPC --
// paying for a fresh federated search, and possibly a second grab, to fix a
// timestamp. A missed stamp costs at most one extra sweep of one item.
//
// grab.ErrUnsupportedKind is unreachable from this call site: it is returned
// for a Series ref carrying no keys, and handleSearchTask discards every kind
// but movie and episode before the RPC is ever issued. It is handled by the
// same non-fatal path anyway rather than asserted away, because "unreachable
// today" is exactly the reasoning this package has had to walk back twice.
func (w *Worker) recordAttempt(ctx context.Context, ns string, ref commonv1.MediaRef) {
	if err := grab.RecordSearchAttempt(ctx, w.Client, ns, ref, w.now()); err != nil {
		w.log(ctx).Warn("search: could not record the search attempt; the per-item backoff will not advance",
			"kind", ref.Kind, "item", ref.Name, "err", err)
	}
}

// grabTarget folds SearchTask.Keys onto the MediaRef the Sink receives. The
// two carry the same thing -- which episodes a pack release covers -- and the
// grab path reads MediaRef.Keys, so a task that set only the payload-level
// field would lose its pack membership at the boundary.
func grabTarget(task schema.SearchTask) commonv1.MediaRef {
	target := task.MediaRef
	if len(target.Keys) == 0 {
		target.Keys = task.Keys
	}
	return target
}

// keepLimit is how many ranked results to KEEP, which is a different number
// from how many releases to FETCH.
//
// Search.spec.limit is documented as "caps how many results are kept"
// (api/catalog/v1alpha1/search_types.go) and the CRD caps it at 200, while
// spec §5 pins the federated search reply at schema.MaxSearchReleases (500).
// Sending spec.limit to indexarr as the RPC limit would fetch only that many
// releases and then rank them, so `spec.limit: 10` would surface the INDEXER's
// arbitrary top ten rather than the best ten by the decision engine's ranking
// -- which is the entire point of ranking. The two limits are independent.
func keepLimit(srch *catalogv1alpha1.Search) int {
	if srch != nil && srch.Spec.Limit > 0 {
		return int(srch.Spec.Limit)
	}
	return MaxResults
}

// terminal reports a failure no redelivery can fix. When the task came from a
// Search CR it first writes the worker-owned half of that object's status --
// finishedAt plus one explanatory indexerOutcomes entry, and no results -- so
// the reconciler's completion trigger fires immediately and the user sees the
// real cause instead of waiting five minutes for "Timeout: no results".
//
// Nothing here touches a controller-owned field: finishedAt, indexerOutcomes
// and results belong to this manager, phase and conditions to the reconciler.
// That is why the worker can report a terminal failure at all without breaking
// the single-writer rule.
func (w *Worker) terminal(ctx context.Context, srch *catalogv1alpha1.Search, reason string, cause error) error {
	discard := events.Discard(reason, cause)
	if srch == nil {
		return discard
	}
	outcome := catalogv1alpha1.IndexerOutcome{
		Name:  searchctl.WorkerOutcomeName,
		State: catalogv1alpha1.IndexerOutcomeError,
		Error: truncateOutcomeError(reason + ": " + cause.Error()),
	}
	if err := w.writeFailure(ctx, srch, outcome); err != nil {
		// Saying so failed; let the delivery retry. The underlying condition
		// is terminal, but reporting it is not, and silently dropping the
		// report puts the object back in the five-minute-timeout hole this
		// function exists to close.
		return err
	}
	return discard
}

// missingObject settles a NotFound on an object this task depends on: retry
// once to ride out an informer that has not caught up, then dead-letter.
func missingObject(m events.Message, reason string, err error) error {
	if m.Attempt() <= 1 {
		return events.Retry(cacheWarmRetry, err)
	}
	return events.Discard(reason, err)
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
// the per-kind indexer and category defaults with its own spec.
//
// The RPC limit is always schema.MaxSearchReleases -- spec §5's cap on the
// federated reply -- and deliberately NOT Search.spec.limit; see keepLimit for
// why conflating "fetch" with "keep" narrows recall to the indexer's own
// ordering.
func (w *Worker) buildRequest(ns string, task schema.SearchTask, snap itemSnapshot, srch *catalogv1alpha1.Search) schema.SearchRequest {
	var indexerRefs []schema.Ref
	var categories []int32
	if srch != nil {
		categories = srch.Spec.Categories
		for _, name := range srch.Spec.IndexerRefs {
			indexerRefs = append(indexerRefs, schema.Ref{Namespace: srch.Namespace, Name: name})
		}
	}
	return BuildSearchRequest(ns, task.MediaRef.Kind, snap.IDs, schema.MaxSearchReleases,
		task.UserInvoked, indexerRefs, categories)
}

// decisionOptions assembles pkg/decision's Options. PreferredProtocol is left
// empty on purpose: pkg/decision falls back to the profile's own preferred
// protocol, and there is no second source for it in this path.
func (w *Worker) decisionOptions(ctx context.Context, ns string, task schema.SearchTask) (decision.Options, error) {
	enabled, err := w.enabledProtocols(ctx, ns, task.UserInvoked)
	if err != nil {
		return decision.Options{}, err
	}
	return decision.Options{
		UserInvoked:      task.UserInvoked,
		ProtocolsEnabled: enabled,
	}, nil
}

// enabledProtocols reads the live DownloadClients to decide which protocols a
// grab could actually use. decision.Options.ProtocolsEnabled fails closed: a
// missing key is disabled.
//
// A namespace with NO DownloadClient at all opens both protocols, but only for
// a user-invoked search. The asymmetry is the point:
//
//   - An interactive search's only output is a list a human reads. Rejecting
//     every release with "protocol disabled" because the operator has not
//     created a DownloadClient yet hides the very results that would tell them
//     what is missing, and nothing is grabbed without a second, deliberate
//     action.
//   - An automatic search's ranked list goes straight to a Sink that grabs.
//     Opening up there approves releases for a protocol nothing can download,
//     and the failure resurfaces much later as a grab with no assignable
//     client. Fail closed, as the contract says.
func (w *Worker) enabledProtocols(ctx context.Context, ns string, userInvoked bool) (map[string]bool, error) {
	var list downloadv1alpha1.DownloadClientList
	if err := w.Client.List(ctx, &list, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("list DownloadClients in %s: %w", ns, err)
	}
	if len(list.Items) == 0 && userInvoked {
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
// PublishedAt is passed through exactly as the indexer reported it, including
// absence. It used to be backfilled here, because a non-pointer metav1.Time
// marshalled to null and the CRD typed it non-nullable, so a release with no
// publish date could not be persisted at all. It is a *metav1.Time now, so
// absence round-trips, and backfilling would be a lie with consequences:
// pkg/decision ranks usenet releases by publish age, so substituting FetchedAt
// or now makes a dateless release sort as brand new.
func releaseInfos(rels []schema.Release) []commonv1.ReleaseInfo {
	out := make([]commonv1.ReleaseInfo, 0, len(rels))
	for _, r := range rels {
		info := r.Info
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

// writeResults records a completed interactive search.
func (w *Worker) writeResults(
	ctx context.Context,
	srch *catalogv1alpha1.Search,
	outcomes []schema.SearchOutcome,
	ranked []catalogv1alpha1.ReleaseDecision,
) error {
	return w.applySearchStatus(ctx, srch, capOutcomes(mapOutcomes(outcomes)), ranked)
}

// writeFailure records a terminal failure on the Search without destroying
// what an earlier delivery of the same task may already have written.
//
// That preservation is the point. JetStream is at-least-once: a delivery that
// succeeded and then lost its ack is redelivered, and if the second run fails
// terminally -- the target was deleted in between, its qualityProfileRef was
// cleared -- it must not take a good answer down with it. So the existing
// results are re-declared verbatim and the failure is added as one more
// outcome entry, which reads as "these are what we found; the latest attempt
// failed for this reason".
//
// srch comes from the informer cache and may be a beat stale, which is
// tolerable here precisely because every field being re-declared is this
// manager's own: the worst case is re-asserting a value this manager itself
// just wrote.
func (w *Worker) writeFailure(ctx context.Context, srch *catalogv1alpha1.Search, failure catalogv1alpha1.IndexerOutcome) error {
	// The fresh failure goes first so it wins capOutcomes' first-wins dedupe
	// against a stale entry from an earlier failed delivery.
	outcomes := append([]catalogv1alpha1.IndexerOutcome{failure}, srch.Status.IndexerOutcomes...)
	return w.applySearchStatus(ctx, srch, capOutcomes(outcomes), srch.Status.Results)
}

// applySearchStatus is the one place the search worker writes a Search.
//
// It writes under k8s.ManagerCatalogarrWorker and owns a set of fields
// deliberately disjoint from the Search reconciler's (§2's controller/worker
// manager split): finishedAt, indexerOutcomes and results here; phase,
// conditions, observedGeneration, startedAt and grabbed there.
//
// All three owned fields are declared on EVERY call, including when they are
// empty -- WithResults() with nothing to append still sends `[]`. Server-side
// apply replaces a manager's whole ownership set rather than merging it, so a
// field this manager owns and omits is released, which reads as "reset to
// zero" on the object. An apply configuration whose list fields were plain
// slices could not express the difference, because `omitempty` drops a
// zero-length slice: see SearchStatusApplyConfiguration's doc comment.
//
// Nothing here ever touches a controller-owned field, which is what lets the
// worker report a terminal failure at all without breaking the single-writer
// rule.
func (w *Worker) applySearchStatus(
	ctx context.Context,
	srch *catalogv1alpha1.Search,
	outcomes []catalogv1alpha1.IndexerOutcome,
	ranked []catalogv1alpha1.ReleaseDecision,
) error {
	statusAC := searchctl.SearchStatus().
		WithFinishedAt(metav1.NewTime(w.now())).
		WithIndexerOutcomes(outcomes...).
		WithResults(ranked...)

	if _, err := k8s.PatchStatus(ctx, w.Client, k8s.ManagerCatalogarrWorker,
		searchctl.Search(srch.Name, srch.Namespace).WithStatus(statusAC)); err != nil {
		return fmt.Errorf("record search results: %w", err)
	}
	return nil
}

// MaxIndexerOutcomes mirrors status.indexerOutcomes' own
// +kubebuilder:validation:MaxItems=100 (api/catalog/v1alpha1/search_types.go).
const MaxIndexerOutcomes = 100

// maxOutcomeErrorBytes bounds one outcome's error message. status is not a log
// sink, and a hundred multi-kilobyte indexer errors would make the object
// itself a problem.
const maxOutcomeErrorBytes = 512

// mapOutcomes projects the RPC's per-indexer report onto the API type. It
// drops nameless entries: status.indexerOutcomes is listType=map keyed by
// name, and an entry with no key makes the apiserver reject the whole status
// apply.
func mapOutcomes(outcomes []schema.SearchOutcome) []catalogv1alpha1.IndexerOutcome {
	out := make([]catalogv1alpha1.IndexerOutcome, 0, len(outcomes))
	for _, o := range outcomes {
		name := o.IndexerRef.Name
		if name == "" {
			name = o.IndexerName
		}
		if name == "" {
			continue
		}
		out = append(out, catalogv1alpha1.IndexerOutcome{
			Name:       name,
			State:      outcomeState(o.Status),
			Count:      o.Releases,
			DurationMs: int32(o.ElapsedMillis),
			Error:      truncateOutcomeError(o.Error),
		})
	}
	return out
}

// capOutcomes deduplicates by name and truncates to MaxIndexerOutcomes. It is
// the single gate every write to status.indexerOutcomes passes through.
//
// Both guards protect the same thing: the field is listType=map keyed by name
// with MaxItems=100, so a repeated name or a 101st entry makes the apiserver
// reject the WHOLE status apply -- taking status.results with it and leaving
// the Search in Running until the reconciler's five-minute timeout. A search
// that really did fan out to more than a hundred indexers is better reported
// truncated than not at all.
//
// First entry wins on a duplicate. For a successful search that is the RPC's
// own order, so the winner is the indexer whose releases are actually in the
// reply; for a failure report it is why writeFailure puts the fresh
// worker-failure entry at the head, ahead of any stale one.
func capOutcomes(outcomes []catalogv1alpha1.IndexerOutcome) []catalogv1alpha1.IndexerOutcome {
	out := make([]catalogv1alpha1.IndexerOutcome, 0, min(len(outcomes), MaxIndexerOutcomes))
	seen := make(map[string]struct{}, len(outcomes))
	for _, o := range outcomes {
		if o.Name == "" {
			continue
		}
		if _, dup := seen[o.Name]; dup {
			continue
		}
		seen[o.Name] = struct{}{}
		out = append(out, o)
		if len(out) == MaxIndexerOutcomes {
			break
		}
	}
	return out
}

// truncateOutcomeError bounds one error message to maxOutcomeErrorBytes,
// cutting on a rune boundary so the result stays valid UTF-8 (the apiserver
// rejects a string that is not).
func truncateOutcomeError(msg string) string {
	if len(msg) <= maxOutcomeErrorBytes {
		return msg
	}
	cut := maxOutcomeErrorBytes
	for cut > 0 && !utf8.RuneStart(msg[cut]) {
		cut--
	}
	return msg[:cut] + "..."
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

// SetupWithManager subscribes both search consumers. Nothing here registers
// itself: catalogarr's run.go calls this once, from setupWorkers.
//
// It does NOT call [RegisterDownloadIndexes]. It used to, and that made the
// three Download indexes a side effect of this worker being enabled -- while
// catalogarr/worker/rssmatcher reads the same three and degrades to "not
// blocklisted, empty queue" with a warning when they are missing. A role that
// ran the RSS matcher without the search worker would therefore grab
// blocklisted releases, silently. Task C12a moved the registration to
// catalogarr's registerWorkerIndexes, which runs once for every worker role,
// and added a startup assertion that the indexes really reached the cache.
// The caller must have made that call before this one.
func (w *Worker) SetupWithManager(mgr ctrl.Manager, bus events.Bus) error {
	if w.Publisher == nil {
		// The WantedScan fan-out publishes back onto the same bus it consumes
		// from. Defaulting here rather than in NewWorker keeps NewWorker's
		// signature free of a bus it has no other use for, and still leaves a
		// caller free to inject a different publisher.
		w.Publisher = bus
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
