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
// trigger from spec §8.2. The reconciler validates the request, publishes one
// catalog.SearchTask.v1 at high priority, waits for the search worker to land
// its results, turns spec.grab into Download objects, and TTL-deletes the
// object once spec.ttl has elapsed.
//
// Nothing here registers itself. catalogarr's run.go calls
// NewReconciler(...).SetupWithManager(mgr).
package search

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jonboulle/clockwork"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
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
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconciler reconciles Search. It is the sole writer of Search's
// status.phase, status.conditions, status.observedGeneration,
// status.startedAt and status.grabbed; the search worker owns the disjoint
// set status.results, status.indexerOutcomes and status.finishedAt under
// k8s.ManagerCatalogarrWorker (§2's controller/worker manager split).
type Reconciler struct {
	Client   client.Client
	Bus      events.Publisher
	Recorder record.EventRecorder
	Scheme   *runtime.Scheme
	Clock    clockwork.Clock
}

// NewReconciler builds a Search reconciler with the real clock and the project
// scheme. SetupWithManager replaces the scheme with the manager's own.
func NewReconciler(c client.Client, bus events.Publisher, rec record.EventRecorder) *Reconciler {
	return &Reconciler{
		Client:   c,
		Bus:      bus,
		Recorder: rec,
		Scheme:   k8s.MustNewScheme(),
		Clock:    clockwork.NewRealClock(),
	}
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
// reject. Query-mode is a valid spec this phase cannot serve yet, which is a
// status outcome, not a reconcile error.
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
	case s.Spec.Query != nil:
		return r.failQueryMode(ctx, s)
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
}

func newStatusUpdate(s *catalogv1alpha1.Search) *statusUpdate {
	return &statusUpdate{
		phase:      s.Status.Phase,
		startedAt:  s.Status.StartedAt,
		grabbed:    append([]catalogv1alpha1.GrabResult(nil), s.Status.Grabbed...),
		conditions: append([]metav1.Condition(nil), s.Status.Conditions...),
	}
}

// apply writes the update. It always sends every field this manager owns.
func (r *Reconciler) apply(ctx context.Context, s *catalogv1alpha1.Search, u *statusUpdate) error {
	statusAC := SearchStatus().
		WithObservedGeneration(s.Generation).
		WithConditions(k8s.ConditionACs(u.conditions)...)
	if u.phase != "" {
		statusAC = statusAC.WithPhase(u.phase)
	}
	if u.startedAt != nil {
		statusAC = statusAC.WithStartedAt(*u.startedAt)
	}
	if len(u.grabbed) > 0 {
		statusAC = statusAC.WithGrabbed(u.grabbed...)
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

// failQueryMode rejects a free-text Search. SearchSpec's CEL rule makes it a
// legal object, but answering it needs indexarr's release index
// (rpc.indexarr.query), which does not exist before Phase D -- so it is
// reported as a documented scope cut rather than left Pending forever.
func (r *Reconciler) failQueryMode(ctx context.Context, s *catalogv1alpha1.Search) (ctrl.Result, error) {
	if s.Status.Phase == catalogv1alpha1.SearchPhaseFailed {
		return ctrl.Result{RequeueAfter: r.ttlRequeue(s)}, nil
	}
	return r.fail(ctx, s, "NotImplemented",
		"query-mode search requires indexarr's release index (rpc.indexarr.query), not yet available before Phase D")
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
		// The CRD defaults spec.ttl to 1h; a zero here means an object
		// created before the default applied, not "delete immediately".
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
	r.Recorder.Eventf(s, "Normal", reason, format, args...)
}

func (r *Reconciler) eventWarning(s *catalogv1alpha1.Search, reason, format string, args ...any) {
	if r.Recorder == nil {
		return
	}
	r.Recorder.Eventf(s, "Warning", reason, format, args...)
}
