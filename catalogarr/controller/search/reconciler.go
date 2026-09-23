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

// Package search owns the Search custom resource: the interactive search
// trigger from spec §8.2. For a mediaRef-mode Search the reconciler
// validates the request, publishes one catalog.SearchTask.v1 at high
// priority, and waits for the search worker to land its results. For a
// query-mode Search (spec.query, free text against indexarr's local release
// index) there is no worker to wait for: the reconciler answers it directly
// over clustarr.rpc.indexarr.query. Either way it then turns spec.grab into
// Download objects and TTL-deletes the object once spec.ttl has elapsed.
//
// Nothing here registers itself. catalogarr's run.go calls
// NewReconciler(...).SetupWithManager(mgr).
package search

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jonboulle/clockwork"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8sevents "k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/version"
)

// SearchRunningTimeout bounds how long a Search may sit in Running before the
// reconciler declares it failed. It is this controller's own safety margin,
// not a protocol deadline: the RPC answers within the worker's 45s deadline
// (spec §5) and the worker then parses, scores and evaluates up to
// schema.MaxSearchReleases (500) releases before keeping at most spec.limit of
// them, so five minutes is several times the worst realistic case while still
// being short enough that a human watching `kubectl get searches` sees a
// verdict.
//
// It is the backstop for a task that never settles at all -- a worker pod
// killed mid-flight, a redelivery exhausting MaxDeliver. A failure the worker
// can see coming is reported directly: it writes status.finishedAt with an
// explanatory status.indexerOutcomes entry, which trips the completion branch
// below on the next watch event instead of waiting this out.
const SearchRunningTimeout = 5 * time.Minute

// queueFullRequeue is how long to wait before republishing when the catalogarr
// work stream is applying back-pressure.
const queueFullRequeue = time.Minute

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=searches,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=searches/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=episodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=series,verbs=get;list;watch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads,verbs=get;list;watch;create;update;patch
// The Recorder is a k8s.io/client-go/tools/events.EventRecorder, handed in by
// mgr.GetEventRecorder, and it writes events.k8s.io/v1 -- so events.k8s.io is
// the group to grant and the core group is not. The marker and the recorder
// type move together or not at all: a mismatch is denied only on a real
// cluster, and no suite can see it, because envtest does not enforce RBAC.
// catalogarr's setupControllers records the occasion this repo learned it.
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconciler reconciles Search. It is the sole writer of Search's
// status.phase, status.conditions, status.observedGeneration,
// status.startedAt and status.grabbed.
//
// status.results, status.indexerOutcomes and status.finishedAt split by
// mode. For a mediaRef-mode Search the search worker owns them, under
// k8s.ManagerCatalogarrWorker (§2's controller/worker manager split); this
// reconciler never touches them for that object. For a query-mode Search
// (spec.query) there is no worker, so THIS reconciler is their sole writer
// instead, under its own k8s.ManagerCatalogarr -- see runQuery, and
// newStatusUpdate/apply, which gate the three on s.Spec.Query != nil so
// each mode's writer only ever declares what it owns.
type Reconciler struct {
	Client   client.Client
	Bus      events.Publisher
	Recorder k8sevents.EventRecorder
	Scheme   *runtime.Scheme
	Clock    clockwork.Clock

	// Query answers a query-mode Search (spec.query) against indexarr's
	// local release index, clustarr.rpc.indexarr.query. Nil is a valid,
	// tested state -- runQuery reports NotConfigured rather than
	// dereferencing it -- because NewReconciler can only wire this
	// opportunistically; see its doc comment.
	Query QueryRPC
}

// NewReconciler builds a Search reconciler with the real clock and the project
// scheme. SetupWithManager replaces the scheme with the manager's own.
//
// Query is wired from bus when bus also satisfies events.Requester, which
// the events.Bus every real caller passes always does; this constructor's
// parameter stays events.Publisher -- unchanged from before query-mode
// existed -- so a fake that only ever implemented Publish (this package's
// own test fixture included) keeps compiling instead of having to grow the
// rest of events.Bus's surface just to satisfy a wider signature.
func NewReconciler(c client.Client, bus events.Publisher, rec k8sevents.EventRecorder) *Reconciler {
	r := &Reconciler{
		Client:   c,
		Bus:      bus,
		Recorder: rec,
		Scheme:   k8s.MustNewScheme(),
		Clock:    clockwork.NewRealClock(),
	}
	if requester, ok := bus.(events.Requester); ok {
		r.Query = NewBusQueryRPC(requester)
	}
	return r
}

// SetupWithManager registers the Search controller.
//
// There is deliberately no finalizer. A Search owns nothing that needs
// cleaning up: the Downloads handleGrabs creates are owned by the catalog item
// they target, not by the Search, precisely so that deleting a Search -- which
// happens automatically, on a one-hour TTL -- never cascade-deletes the grabs
// the user made from it. Do not "fix" this by adding one.
//
// reconcile.TerminalError is likewise absent: SearchSpec's only cross-field
// invariant (exactly one of query/mediaRef) is a CEL rule enforced at
// admission, so Reconcile never observes a spec it would have to terminally
// reject. A query-mode Search that outruns its Query RPC (indexarr
// unreachable, the release index itself erroring) is likewise a status
// outcome -- Failed with a reason, or a requeue -- never a reconcile error
// that would panic-recover or spin the workqueue.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	r.Scheme = mgr.GetScheme()
	return ctrl.NewControllerManagedBy(mgr).
		Named("search").
		For(&catalogv1alpha1.Search{}).
		WithOptions(controller.Options{RecoverPanic: ptr.To(true), ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}

func (r *Reconciler) now() time.Time {
	if r.Clock == nil {
		return time.Now().UTC()
	}
	return r.Clock.Now().UTC()
}

// Reconcile implements reconcile.Reconciler.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "search.Reconciler.Reconcile")
	defer span.End()

	s := &catalogv1alpha1.Search{}
	if err := r.Client.Get(ctx, req.NamespacedName, s); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !s.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	// The TTL check comes first so that every terminal state expires, including
	// the query-mode rejection below, which would otherwise return before ever
	// reaching it and leave the object behind forever. ttlDeadline returns the
	// zero time for anything that has not finished, so this cannot delete a
	// search in flight.
	switch {
	case r.ttlExpired(s):
		return r.deleteExpired(ctx, s)
	case s.Status.Phase == "" && s.Spec.Query != nil:
		return r.runQuery(ctx, s)
	case s.Status.Phase == "":
		return r.startSearch(ctx, s)
	case s.Status.FinishedAt != nil && s.Status.Phase != catalogv1alpha1.SearchPhaseCompleted:
		// Deliberately NOT gated on Running. The consumer's BackOff ladder
		// runs 30s/2m/10m/1h, so a redelivered task routinely lands its
		// results AFTER SearchRunningTimeout has already flipped this object
		// to Failed. Requiring Running there stranded the user in the worst
		// possible state: results visible in status.results, spec.grab
		// accepted by the apiserver, and nothing whatsoever happening --
		// because handleGrabs only runs on a Completed search. Late results
		// are still results.
		return r.completeSearch(ctx, s)
	case s.Status.Phase == catalogv1alpha1.SearchPhaseRunning && r.stuck(s):
		return r.failStuck(ctx, s)
	case s.Status.Phase == catalogv1alpha1.SearchPhaseCompleted && r.grabsPending(s):
		return r.handleGrabs(ctx, s)
	}
	return ctrl.Result{RequeueAfter: r.ttlRequeue(s)}, nil
}

// statusUpdate is a complete declaration of everything this reconciler's field
// manager owns on a Search. Every write path starts from the object's live
// status and mutates it, because server-side apply RELEASES any field this
// manager sent before and omits now -- an early return that patched only
// conditions would silently reset phase, startedAt and grabbed to zero.
type statusUpdate struct {
	phase      catalogv1alpha1.SearchPhase
	startedAt  *metav1.Time
	grabbed    []catalogv1alpha1.GrabResult
	conditions []metav1.Condition

	// finishedAt, indexerOutcomes and results are set only for a query-mode
	// Search (spec.query != nil). A mediaRef-mode Search never populates
	// them here: those three stay k8s.ManagerCatalogarrWorker's alone, per
	// the Reconciler doc comment, and apply() only ever declares them when
	// s.Spec.Query != nil -- see both doc comments before changing either.
	finishedAt      *metav1.Time
	indexerOutcomes []catalogv1alpha1.IndexerOutcome
	results         []catalogv1alpha1.ReleaseDecision
}

func newStatusUpdate(s *catalogv1alpha1.Search) *statusUpdate {
	u := &statusUpdate{
		phase:      s.Status.Phase,
		startedAt:  s.Status.StartedAt,
		grabbed:    append([]catalogv1alpha1.GrabResult(nil), s.Status.Grabbed...),
		conditions: append([]metav1.Condition(nil), s.Status.Conditions...),
	}
	if s.Spec.Query != nil {
		// Query mode has no worker: THIS manager is the sole writer of
		// these three for this object, so -- exactly like grabbed and
		// conditions above -- every apply must re-declare them from the
		// live object or release them out from under itself. A
		// mediaRef-mode Search (the else of this branch) never carries
		// them forward, which is what keeps this reconciler from ever
		// declaring them on an object the worker owns.
		u.finishedAt = s.Status.FinishedAt
		u.indexerOutcomes = append([]catalogv1alpha1.IndexerOutcome(nil), s.Status.IndexerOutcomes...)
		u.results = append([]catalogv1alpha1.ReleaseDecision(nil), s.Status.Results...)
	}
	return u
}

// apply writes the update. It always sends every field this manager owns.
//
// status.grabbed is declared unconditionally, empty list included, so this
// function is uniformly "declare everything I own" -- the rule this codebase
// keeps getting bitten by. It is NOT what protects the recorded grabs:
// status.grabbed is listType=map, and server-side apply tracks an associative
// list per entry, so declaring it empty removes this manager's entries exactly
// as omitting it would (see SearchStatusApplyConfiguration's doc comment).
// What protects them is newStatusUpdate copying the live status.grabbed into
// every update, so the contents are re-declared rather than merely re-claimed.
//
// phase and startedAt stay conditional because they are scalars whose zero
// value is genuinely "nothing to say yet": a Search that has not started has
// no phase to declare, and once either is set newStatusUpdate carries it
// forward on every subsequent apply.
func (r *Reconciler) apply(ctx context.Context, s *catalogv1alpha1.Search, u *statusUpdate) error {
	statusAC := SearchStatus().
		WithObservedGeneration(s.Generation).
		WithConditions(k8s.ConditionACs(u.conditions)...).
		WithGrabbed(u.grabbed...)
	if u.phase != "" {
		statusAC = statusAC.WithPhase(u.phase)
	}
	if u.startedAt != nil {
		statusAC = statusAC.WithStartedAt(*u.startedAt)
	}
	if s.Spec.Query != nil {
		// See newStatusUpdate: a query-mode Search has no worker, so this
		// manager declares these three itself, every apply, the same way
		// it already declares grabbed unconditionally above. Guarded on
		// s.Spec.Query so a mediaRef-mode object -- where these fields
		// belong to k8s.ManagerCatalogarrWorker -- never has this manager
		// say anything about them at all.
		statusAC = statusAC.WithIndexerOutcomes(u.indexerOutcomes...).WithResults(u.results...)
		if u.finishedAt != nil {
			statusAC = statusAC.WithFinishedAt(*u.finishedAt)
		}
	}
	if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr,
		Search(s.Name, s.Namespace).WithStatus(statusAC)); err != nil {
		return err
	}
	return nil
}

// startSearch publishes the one SearchTask an interactive Search produces, at
// high priority: spec §8.2's "high = interactive".
func (r *Reconciler) startSearch(ctx context.Context, s *catalogv1alpha1.Search) (ctrl.Result, error) {
	if s.Spec.MediaRef == nil {
		// Unreachable through the apiserver (SearchSpec's CEL rule requires
		// exactly one of query/mediaRef) but cheap to state rather than
		// dereference nil.
		return r.fail(ctx, s, "InvalidSpec", "neither spec.query nor spec.mediaRef is set")
	}

	now := r.now()
	mediaKey := events.MediaKey(string(s.Spec.MediaRef.Kind), s.Namespace, s.Spec.MediaRef.Name)
	schemaName, data, err := schema.Encode(schema.SearchTask{
		MediaRef:    *s.Spec.MediaRef,
		Keys:        s.Spec.MediaRef.Keys,
		Reason:      schema.SearchReasonInteractive,
		SearchRef:   &schema.Ref{Namespace: s.Namespace, Name: s.Name, UID: string(s.UID)},
		UserInvoked: true,
	})
	if err != nil {
		return ctrl.Result{}, err
	}
	env := &events.Envelope{
		ID:     events.MsgIDForObject(string(s.UID), s.Generation, "search"),
		Type:   "catalog.SearchTask",
		Schema: schemaName,
		Source: "catalogarr@" + version.String(),
		// The envelope key is the OWNING object -- this Search -- per
		// events.Envelope's contract; the worker resolves the namespace from
		// it. The mediaKey identifies the item and belongs in the subject,
		// where the grab lease and the pending bucket derive from the same
		// token.
		Key:  s.Namespace + "/" + s.Name,
		Time: now,
		Data: data,
	}
	tracing.Inject(ctx, env)

	u := newStatusUpdate(s)
	if _, err := r.Bus.Publish(ctx, events.WorkSearchSubject(events.PriorityHigh, mediaKey), env); err != nil {
		if errors.Is(err, events.ErrQueueFull) {
			// Back-pressure, not failure: leave the phase alone so the next
			// reconcile still takes the startSearch branch, and say why on
			// Ready. Every other owned field is re-sent by apply().
			k8s.MarkFalse(s, &u.conditions, k8s.ConditionReady, "QueueFull", "the catalogarr search queue is full")
			if perr := r.apply(ctx, s, u); perr != nil {
				return ctrl.Result{}, perr
			}
			return ctrl.Result{RequeueAfter: queueFullRequeue}, nil
		}
		return ctrl.Result{}, fmt.Errorf("publish search task: %w", err)
	}

	startedAt := metav1.NewTime(now)
	u.phase = catalogv1alpha1.SearchPhaseRunning
	u.startedAt = &startedAt
	k8s.MarkFalse(s, &u.conditions, catalogv1alpha1.SearchConditionCompleted, "Searching", "search task published")
	k8s.MarkFalse(s, &u.conditions, catalogv1alpha1.SearchConditionFailed, "Searching", "search task published")
	k8s.MarkFalse(s, &u.conditions, k8s.ConditionReady, "Searching", "waiting for indexer results")
	if err := r.apply(ctx, s, u); err != nil {
		return ctrl.Result{}, err
	}
	r.event(s, "SearchStarted", "published an interactive search for %s/%s", s.Spec.MediaRef.Kind, s.Spec.MediaRef.Name)
	logging.FromContext(ctx).Info("search: task published", "mediaKey", mediaKey, "search", s.Name)
	return ctrl.Result{RequeueAfter: SearchRunningTimeout}, nil
}

// completeSearch flips a Running Search to Completed once the worker's
// status.finishedAt has landed. The phase belongs to this reconciler and the
// results belong to the worker, so "the worker is done" is observed rather
// than signalled.
func (r *Reconciler) completeSearch(ctx context.Context, s *catalogv1alpha1.Search) (ctrl.Result, error) {
	// A search that returned nothing because the worker could not run it at
	// all reports why through an indexerOutcomes entry. Surfacing that on the
	// condition is the difference between "0 results" and "your movie has no
	// qualityProfileRef".
	if msg, failed := workerFailure(s); failed {
		return r.fail(ctx, s, "SearchFailed", msg)
	}

	u := newStatusUpdate(s)
	u.phase = catalogv1alpha1.SearchPhaseCompleted
	k8s.MarkTrue(s, &u.conditions, catalogv1alpha1.SearchConditionCompleted, k8s.ReasonReconciled,
		"%d releases kept from %d indexers", len(s.Status.Results), len(s.Status.IndexerOutcomes))
	k8s.MarkFalse(s, &u.conditions, catalogv1alpha1.SearchConditionFailed, k8s.ReasonReconciled, "search completed")
	k8s.MarkTrue(s, &u.conditions, k8s.ConditionReady, k8s.ReasonReconciled, "status.results reflects spec")
	if err := r.apply(ctx, s, u); err != nil {
		return ctrl.Result{}, err
	}
	r.event(s, "SearchCompleted", "kept %d releases", len(s.Status.Results))
	return ctrl.Result{RequeueAfter: r.ttlRequeue(s)}, nil
}

// workerFailure reports the search worker's own explanation when it finished
// without being able to search at all.
//
// The worker writes status.finishedAt, status.indexerOutcomes and
// status.results and nothing else -- phase and conditions are this
// reconciler's -- so a failure it can see coming arrives as an outcome entry
// under its reserved name with no results behind it. Reading it here is what
// turns that into a phase and a message.
func workerFailure(s *catalogv1alpha1.Search) (string, bool) {
	if len(s.Status.Results) > 0 {
		return "", false
	}
	for _, o := range s.Status.IndexerOutcomes {
		if o.Name == WorkerOutcomeName && o.State == catalogv1alpha1.IndexerOutcomeError {
			return o.Error, true
		}
	}
	return "", false
}

// noQueryResponderRequeue is how long to wait before asking indexarr's
// release index again when nothing answers clustarr.rpc.indexarr.query --
// most often indexarr rolling, or not up yet. It mirrors
// catalogarr/worker/search's own noRespondersRetryAfter for the same subject
// family, without reusing pkg/events.Retry: that helper drives a queue
// consumer's nak/redelivery schedule, and this is a plain controller-runtime
// Reconcile, which already gets a retry from a returned RequeueAfter.
const noQueryResponderRequeue = 15 * time.Second

// QueryOutcomeName is the status.indexerOutcomes entry a query-mode Search
// reports its one line under. There is no per-indexer fan-out to report --
// query mode is a single read against indexarr's already-merged local
// index, not a live search of any indexer -- but
// status.indexerOutcomes[0].count is kubectl's own "Results" printer column
// (api/catalog/v1alpha1/search_types.go's +kubebuilder:printcolumn), so
// leaving the list empty would print a perfectly successful query-mode
// search as blank. Deliberately not a valid DNS-1123 subdomain, so it can
// never collide with a real Indexer's name in this listType=map keyed by
// name -- the same trick WorkerOutcomeName uses in applyconfiguration.go,
// and the same "_local-index" spelling indexarr/query's own metrics use for
// the same reason (indexarr/query/service.go's localIndexLabel).
const QueryOutcomeName = "_local-index"

// runQuery answers a free-text Search (spec.query) directly against
// indexarr's local release index over clustarr.rpc.indexarr.query, rather
// than through the async SearchTask pipeline startSearch uses for a
// mediaRef Search.
//
// There is no MediaRef to run decision.Evaluate/Rank against -- query mode
// is a raw listing, not a ranked, profile-checked one -- so there is
// nothing left for a worker to do that this reconciler cannot do inline,
// which is why it is the sole writer of
// status.results/indexerOutcomes/finishedAt for this object: see
// newStatusUpdate and apply, which gate those three fields on
// s.Spec.Query != nil for exactly this reason. SearchSpec's CEL rule
// (exactly one of query/mediaRef) means the two field managers this
// produces -- this one, and k8s.ManagerCatalogarrWorker on a mediaRef
// object -- never apply to the same object.
//
// The caller gates this on status.phase == "", the same gate startSearch
// uses, so a later reconcile (a status update this apply itself causes, a
// resync) falls through to the phase-based branches below instead of
// re-querying and re-stamping finishedAt on every pass.
func (r *Reconciler) runQuery(ctx context.Context, s *catalogv1alpha1.Search) (ctrl.Result, error) {
	if r.Query == nil {
		return r.fail(ctx, s, "NotConfigured",
			"indexarr's release index (rpc.indexarr.query) is not wired into this reconciler")
	}

	req := schema.QueryRequest{Text: *s.Spec.Query, Limit: s.Spec.Limit}
	if filters := queryFilters(s); len(filters) > 0 {
		req.Filters = filters
	}

	resp, err := r.Query.Query(ctx, req)
	if err != nil {
		if errors.Is(err, events.ErrNoResponders) {
			// Transient -- indexarr is not up yet -- not a verdict on the
			// query. Leave phase alone so the next reconcile still takes
			// the runQuery branch, the same shape startSearch uses for
			// ErrQueueFull.
			u := newStatusUpdate(s)
			k8s.MarkFalse(s, &u.conditions, k8s.ConditionReady, "IndexarrUnavailable",
				"indexarr's release index is not reachable yet")
			if perr := r.apply(ctx, s, u); perr != nil {
				return ctrl.Result{}, perr
			}
			return ctrl.Result{RequeueAfter: noQueryResponderRequeue}, nil
		}
		return ctrl.Result{}, fmt.Errorf("query RPC: %w", err)
	}
	if resp.Error != "" {
		return r.fail(ctx, s, "QueryFailed", resp.Error)
	}

	results := make([]catalogv1alpha1.ReleaseDecision, 0, len(resp.Releases))
	for _, rel := range resp.Releases {
		// No decision.Evaluate runs here: that needs a MediaRef's quality
		// profile, availability window and blocklist, none of which a
		// free-text query has. Every hit comes back unapproved and
		// unranked -- the user grabs straight from the listing via
		// spec.grab, per §8.2's "Search-CR grabs".
		results = append(results, catalogv1alpha1.ReleaseDecision{ReleaseInfo: rel.Info})
	}

	now := metav1.NewTime(r.now())
	u := newStatusUpdate(s)
	u.phase = catalogv1alpha1.SearchPhaseCompleted
	if u.startedAt == nil {
		u.startedAt = &now
	}
	u.finishedAt = &now
	u.results = results
	u.indexerOutcomes = []catalogv1alpha1.IndexerOutcome{{
		Name:  QueryOutcomeName,
		State: catalogv1alpha1.IndexerOutcomeOK,
		Count: int32(len(results)),
	}}
	k8s.MarkTrue(s, &u.conditions, catalogv1alpha1.SearchConditionCompleted, k8s.ReasonReconciled,
		"%d releases matched the local index", len(results))
	k8s.MarkFalse(s, &u.conditions, catalogv1alpha1.SearchConditionFailed, k8s.ReasonReconciled, "search completed")
	k8s.MarkTrue(s, &u.conditions, k8s.ConditionReady, k8s.ReasonReconciled, "status.results reflects spec")
	if err := r.apply(ctx, s, u); err != nil {
		return ctrl.Result{}, err
	}
	r.event(s, "SearchCompleted", "matched %d releases from the local index", len(results))
	return ctrl.Result{RequeueAfter: r.ttlRequeue(s)}, nil
}

// queryFilters translates the SearchSpec fields indexarr/query's filter
// vocabulary understands -- category and indexer, see
// indexarr/query/filters.go's filterKeys -- into a QueryRequest.Filters map.
// spec.kinds has no counterpart there (query's filters are category,
// indexer, protocol and since) and is left unfiltered rather than guessed
// at; spec.protocol and spec.since have no SearchSpec field to read from.
func queryFilters(s *catalogv1alpha1.Search) map[string]string {
	filters := map[string]string{}
	if len(s.Spec.Categories) > 0 {
		cats := make([]string, len(s.Spec.Categories))
		for i, c := range s.Spec.Categories {
			cats[i] = strconv.Itoa(int(c))
		}
		filters["category"] = strings.Join(cats, ",")
	}
	if len(s.Spec.IndexerRefs) > 0 {
		filters["indexer"] = strings.Join(s.Spec.IndexerRefs, ",")
	}
	return filters
}

// failStuck self-heals a Search whose worker never answered.
func (r *Reconciler) failStuck(ctx context.Context, s *catalogv1alpha1.Search) (ctrl.Result, error) {
	return r.fail(ctx, s, "Timeout",
		fmt.Sprintf("no results after %s; the search task was not completed", SearchRunningTimeout))
}

func (r *Reconciler) fail(ctx context.Context, s *catalogv1alpha1.Search, reason, message string) (ctrl.Result, error) {
	u := newStatusUpdate(s)
	u.phase = catalogv1alpha1.SearchPhaseFailed
	if u.startedAt == nil {
		// A failed Search still needs a TTL anchor; see ttlDeadline.
		startedAt := metav1.NewTime(r.now())
		u.startedAt = &startedAt
	}
	k8s.MarkTrue(s, &u.conditions, catalogv1alpha1.SearchConditionFailed, reason, "%s", message)
	k8s.MarkFalse(s, &u.conditions, catalogv1alpha1.SearchConditionCompleted, reason, "%s", message)
	k8s.MarkFalse(s, &u.conditions, k8s.ConditionReady, reason, "%s", message)
	if err := r.apply(ctx, s, u); err != nil {
		return ctrl.Result{}, err
	}
	r.eventWarning(s, reason, "%s", message)
	return ctrl.Result{RequeueAfter: r.ttlRequeue(s)}, nil
}

// grabsPending reports whether spec.grab names a GUID status.grabbed has not
// successfully handled yet. It is only consulted for a Completed search:
// spec.grab names GUIDs the user read off status.results, which do not exist
// before the worker has written them, so acting earlier could only record
// "guid is not in status.results" for every entry. An entry that previously errored is retried, which
// is what makes flipping spec.override to true work without editing spec.grab.
func (r *Reconciler) grabsPending(s *catalogv1alpha1.Search) bool {
	if len(s.Spec.Grab) == 0 {
		return false
	}
	done := make(map[string]catalogv1alpha1.GrabResult, len(s.Status.Grabbed))
	for _, g := range s.Status.Grabbed {
		done[g.GUID] = g
	}
	for _, guid := range s.Spec.Grab {
		if g, ok := done[guid]; !ok || g.Error != "" {
			return true
		}
	}
	return false
}

// handleGrabs turns spec.grab into Download objects -- spec §8.2's "Search-CR
// grabs: user sets spec.grab=[guid]; controller creates Downloads (Override
// required for Permanent rejections), fills status.grabbed".
//
// Re-running it is a no-op rather than a conflict: k8s.Apply is a server-side
// apply, the Download names are deterministic (k8s.ChildName, the spec's
// <target>-<sha1(guid)[:10]>), and every DownloadSpec field this path sets is
// covered by a `self == oldSelf` CEL rule, so re-applying the same values
// passes and applying different ones would be rejected loudly.
func (r *Reconciler) handleGrabs(ctx context.Context, s *catalogv1alpha1.Search) (ctrl.Result, error) {
	log := logging.FromContext(ctx)
	grabbed := make(map[string]catalogv1alpha1.GrabResult, len(s.Status.Grabbed))
	for _, g := range s.Status.Grabbed {
		grabbed[g.GUID] = g
	}

	if s.Spec.MediaRef == nil {
		// A query-mode Search's results have no catalog item behind them:
		// resolveTarget switches on spec.mediaRef.Kind and the loop below
		// dereferences *s.Spec.MediaRef for WithTarget and
		// k8s.ChildName(s.Spec.MediaRef.Name, guid), both nil for every
		// query-mode object (SearchSpec's CEL rule makes query and
		// mediaRef mutually exclusive). Report the gap per guid, like
		// every other grab rejection here, instead of reaching either.
		for _, guid := range s.Spec.Grab {
			if existing, done := grabbed[guid]; done && existing.Error == "" {
				continue
			}
			grabbed[guid] = catalogv1alpha1.GrabResult{
				GUID:  guid,
				Error: "query-mode search has no spec.mediaRef; nothing to attach the Download to",
			}
		}
	} else {
		owner, qualityProfileRef, err := r.resolveTarget(ctx, s)
		if err != nil {
			return ctrl.Result{}, err
		}

		for _, guid := range s.Spec.Grab {
			if existing, done := grabbed[guid]; done && existing.Error == "" {
				continue
			}
			got := resolveGrab(guid, s.Status.Results, s.Spec.Override)
			if !got.Allowed {
				grabbed[guid] = catalogv1alpha1.GrabResult{GUID: guid, Error: got.Error}
				continue
			}

			name := k8s.ChildName(s.Spec.MediaRef.Name, guid)
			// Manual is set because a Search-CR grab IS an operator-forced grab:
			// the user read status.results and picked this release by hand. Its
			// own doc comment -- "the importer then skips the monitored and
			// minimum-availability checks it would otherwise apply" -- describes
			// exactly what has to happen for a hand-picked grab of an unmonitored
			// or not-yet-released item to survive import. Without it the download
			// completes in full and is thrown away at the import gate, which is a
			// silent waste of the user's bandwidth and of a seeding slot.
			specAC := downloadac.DownloadSpec().
				WithProtocol(got.Release.Protocol).
				WithSource(toDownloadSourceAC(BuildDownloadSource(got.Release))).
				WithRelease(got.Release).
				WithTarget(*s.Spec.MediaRef).
				WithGrabbedBy(downloadv1alpha1.GrabSourceInteractive).
				WithManual(true)
			if qualityProfileRef != "" {
				specAC = specAC.WithQualityProfileRef(qualityProfileRef)
			}
			dl := downloadac.Download(name, s.Namespace).WithSpec(specAC)
			if owner != nil {
				ownerAC, oerr := k8s.OwnerReferenceAC(owner, r.Scheme)
				if oerr != nil {
					return ctrl.Result{}, oerr
				}
				dl = dl.WithOwnerReferences(ownerAC)
			}

			if _, err := k8s.Apply(ctx, r.Client, k8s.ManagerCatalogarr, dl); err != nil {
				log.Error("search: grab failed", "guid", guid, "download", name, "err", err)
				grabbed[guid] = catalogv1alpha1.GrabResult{GUID: guid, Error: err.Error()}
				r.eventWarning(s, "GrabFailed", "could not create Download %s: %v", name, err)
				continue
			}
			grabbed[guid] = catalogv1alpha1.GrabResult{GUID: guid, DownloadRef: name}
			r.event(s, "Grabbed", "created Download %s", name)
		}
	}

	u := newStatusUpdate(s)
	u.grabbed = make([]catalogv1alpha1.GrabResult, 0, len(s.Spec.Grab))
	for _, guid := range s.Spec.Grab {
		u.grabbed = append(u.grabbed, grabbed[guid])
	}
	if err := r.apply(ctx, s, u); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.ttlRequeue(s)}, nil
}

// resolveTarget fetches the catalog item a grab is for, so the Download can
// carry its ownerReference and its QualityProfile -- both listed in spec
// §8.2's Download field set. A missing item is not fatal: the Download is
// still created, just without an owner, and the grab path reports the item
// separately. That keeps a stale MediaRef from silently swallowing a grab the
// user explicitly asked for.
func (r *Reconciler) resolveTarget(ctx context.Context, s *catalogv1alpha1.Search) (client.Object, string, error) {
	key := client.ObjectKey{Namespace: s.Namespace, Name: s.Spec.MediaRef.Name}
	switch s.Spec.MediaRef.Kind {
	case commonv1.MediaKindMovie:
		m := &catalogv1alpha1.Movie{}
		if err := r.Client.Get(ctx, key, m); err != nil {
			return nil, "", client.IgnoreNotFound(err)
		}
		return m, m.Spec.QualityProfileRef, nil
	case commonv1.MediaKindEpisode:
		e := &catalogv1alpha1.Episode{}
		if err := r.Client.Get(ctx, key, e); err != nil {
			return nil, "", client.IgnoreNotFound(err)
		}
		series := &catalogv1alpha1.Series{}
		if err := r.Client.Get(ctx, client.ObjectKey{Namespace: s.Namespace, Name: e.Spec.SeriesRef}, series); err != nil {
			if apierrors.IsNotFound(err) {
				return e, "", nil
			}
			return nil, "", err
		}
		return e, series.Spec.QualityProfileRef, nil
	default:
		return nil, "", nil
	}
}

// ttlDeadline is when this Search may be deleted, or the zero time when it is
// not eligible yet.
//
// A completed search anchors on status.finishedAt, which the worker writes. A
// failed one has no finishedAt -- the worker never ran, or never answered --
// so it anchors on status.startedAt, which fail() guarantees is set. Anything
// still running has no deadline: a Search is deleted once it has an answer,
// never mid-flight.
func (r *Reconciler) ttlDeadline(s *catalogv1alpha1.Search) time.Time {
	ttl := s.Spec.TTL.Duration
	if ttl <= 0 {
		// The CRD defaults spec.ttl to 1h, but only when the field is
		// absent: a Search created by a Go client (the UI's "search now"
		// included) always sends "0s", so a zero here is the default
		// unapplied, not "delete immediately".
		ttl = time.Hour
	}
	switch {
	case s.Status.FinishedAt != nil:
		return s.Status.FinishedAt.Add(ttl)
	case s.Status.Phase == catalogv1alpha1.SearchPhaseFailed && s.Status.StartedAt != nil:
		return s.Status.StartedAt.Add(ttl)
	default:
		return time.Time{}
	}
}

func (r *Reconciler) ttlExpired(s *catalogv1alpha1.Search) bool {
	deadline := r.ttlDeadline(s)
	return !deadline.IsZero() && !r.now().Before(deadline)
}

// ttlRequeue is how long to wait before looking at this Search again: the time
// left on its TTL, or the running timeout while it is still in flight.
func (r *Reconciler) ttlRequeue(s *catalogv1alpha1.Search) time.Duration {
	deadline := r.ttlDeadline(s)
	if deadline.IsZero() {
		return SearchRunningTimeout
	}
	if left := deadline.Sub(r.now()); left > 0 {
		return left
	}
	return time.Second
}

func (r *Reconciler) deleteExpired(ctx context.Context, s *catalogv1alpha1.Search) (ctrl.Result, error) {
	logging.FromContext(ctx).Info("search: ttl elapsed, deleting", "search", s.Name, "ttl", s.Spec.TTL.Duration)
	return ctrl.Result{}, client.IgnoreNotFound(r.Client.Delete(ctx, s))
}

// stuck reports whether a Running Search has exceeded SearchRunningTimeout.
func (r *Reconciler) stuck(s *catalogv1alpha1.Search) bool {
	if s.Status.StartedAt == nil {
		return false
	}
	return r.now().Sub(s.Status.StartedAt.Time) > SearchRunningTimeout
}

func (r *Reconciler) event(s *catalogv1alpha1.Search, reason, format string, args ...any) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(s, nil, "Normal", reason, "Reconcile", format, args...)
}

func (r *Reconciler) eventWarning(s *catalogv1alpha1.Search, reason, format string, args ...any) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(s, nil, "Warning", reason, "Reconcile", format, args...)
}
