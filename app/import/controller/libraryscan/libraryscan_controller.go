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

package libraryscan

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/import/worker/rescan"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/version"
)

const (
	// pollInterval is how often a Running scan re-reads the worker's
	// checkpoint. It matches the worker's own checkpoint cadence: polling
	// faster cannot produce fresher numbers.
	pollInterval = 3 * time.Second

	// dependencyRetry is how long to wait for a RootFolder that is missing
	// or not yet Ready.
	dependencyRetry = 15 * time.Second

	// queueFullRetry is how long to wait when the work stream is at its
	// limit. Publishing is deduplicated by message ID, so retrying costs
	// nothing when the first publish did get through.
	queueFullRetry = time.Minute

	// noProgressTimeout fails a scan whose worker has stopped reporting:
	// no new checkpoint for this long -- measured from the LAST checkpoint
	// this controller saw, or from startedAt when there has been none, so
	// a three-hour walk that checkpoints every few seconds is never failed
	// for its length, and one that stops is failed half an hour after it
	// stopped rather than half an hour after it started. ConsumerImportScan
	// redelivers a task after at most ten minutes of backoff, and a
	// redelivery resumes and checkpoints again, so half an hour of silence
	// means no worker is coming back. (A task that ended in the DLQ is
	// failed sooner: the DeadLettered fold.)
	noProgressTimeout = 30 * time.Minute

	// defaultTTLSeconds matches the CRD's own default for
	// spec.ttlSecondsAfterFinished, for the case where the field is nil
	// because the object predates the default.
	defaultTTLSeconds = 3600

	// maxUnmatched is catalogv1alpha1.LibraryScanStatus.Unmatched's
	// MaxItems. Status lists are capped because unbounded ones are how
	// operators melt etcd; the newest entries win.
	maxUnmatched = 200
)

// Reason tokens local to LibraryScan, alongside the shared pkg/k8s set.
const (
	ReasonScanning           = "Scanning"
	ReasonQueueFull          = "QueueFull"
	ReasonRootFolderNotReady = "RootFolderNotReady"
	ReasonNoProgress         = "NoProgress"
)

// Reconciler drives one LibraryScan from Pending to Completed or Failed and
// then deletes it. It is the sole writer of LibraryScan.status; the rescan
// worker reports through the clustarr-progress bucket instead.
type Reconciler struct {
	// Client reads the scan and its RootFolder and applies the status.
	Client client.Client

	// Bus publishes the scan task and holds the progress checkpoints.
	Bus events.Bus

	// Clock is the time source, injected so tests are deterministic.
	Clock func() time.Time

	// marks remembers, per Running scan, the last checkpoint revision this
	// controller saw and when it saw it: the clock noProgressTimeout runs
	// from. It has to live here, because the clustarr-progress bucket's
	// TTL (ten minutes) is shorter than the timeout, so the checkpoint
	// itself is gone by the time the timeout matters, and LibraryScan.status
	// has no field for it. It is per process: a controller that restarts
	// counts from startedAt, or from the first checkpoint it then sees,
	// which only ever gives a scan longer, never shorter.
	mu    sync.Mutex
	marks map[types.NamespacedName]progressMark
}

// progressMark is the newest checkpoint of one scan the controller has seen.
type progressMark struct {
	uid types.UID
	rev uint64
	at  time.Time
}

// lastProgress records entry (nil when there is no checkpoint) as seen now
// when it is a revision this controller has not seen for this scan, and
// returns when the scan last made progress: the newest of its startedAt and
// the time its newest checkpoint was first seen.
func (r *Reconciler) lastProgress(scan *catalogv1alpha1.LibraryScan, entry *events.Entry, now time.Time) time.Time {
	key := types.NamespacedName{Namespace: scan.Namespace, Name: scan.Name}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.marks == nil {
		r.marks = map[types.NamespacedName]progressMark{}
	}
	m, ok := r.marks[key]
	if ok && m.uid != scan.UID {
		ok = false
	}
	if entry != nil && (!ok || m.rev != entry.Revision) {
		m, ok = progressMark{uid: scan.UID, rev: entry.Revision, at: now}, true
		r.marks[key] = m
	}
	var last time.Time
	if scan.Status.StartedAt != nil {
		last = scan.Status.StartedAt.Time
	}
	if ok && m.at.After(last) {
		last = m.at
	}
	return last
}

// forget drops a scan's mark once it has settled or gone.
func (r *Reconciler) forget(key types.NamespacedName) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.marks, key)
}

// conditions is the scan's live conditions with the DeadLettered annotation
// folded in (k8s.MarkDeadLettered): the one place every status apply of
// this controller builds its conditions from.
func conditions(scan *catalogv1alpha1.LibraryScan) (out []metav1.Condition, changed bool) {
	out = append([]metav1.Condition(nil), scan.Status.Conditions...)
	return out, k8s.MarkDeadLettered(scan, &out)
}

func (r *Reconciler) now() time.Time {
	if r.Clock != nil {
		return r.Clock()
	}
	return time.Now()
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "libraryscan.Reconcile")
	defer span.End()
	ctx = logging.NewContext(ctx, logging.FromContext(ctx).With("libraryScan", req.String()))

	var scan catalogv1alpha1.LibraryScan
	if err := r.Client.Get(ctx, req.NamespacedName, &scan); err != nil {
		if apierrors.IsNotFound(err) {
			r.forget(req.NamespacedName)
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// No finalizer: the only side effects are one deduplicated publish and
	// one TTL'd KV key, so a deleted scan leaks nothing.
	if k8s.IsDeleting(&scan) {
		return ctrl.Result{}, nil
	}

	switch scan.Status.Phase {
	case "", catalogv1alpha1.ScanPhasePending:
		return r.start(ctx, &scan)
	case catalogv1alpha1.ScanPhaseRunning:
		return r.poll(ctx, &scan)
	default:
		return r.maybeExpire(ctx, &scan)
	}
}

// start resolves the RootFolder and hands the walk to the work queue.
func (r *Reconciler) start(ctx context.Context, scan *catalogv1alpha1.LibraryScan) (ctrl.Result, error) {
	now := r.now()
	conditions, _ := conditions(scan)

	var root catalogv1alpha1.RootFolder
	key := types.NamespacedName{Namespace: scan.Namespace, Name: scan.Spec.RootFolderRef}
	if err := r.Client.Get(ctx, key, &root); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		k8s.MarkReady(scan, &conditions, false, k8s.ReasonDependencyNotReady,
			"rootFolder %q not found", scan.Spec.RootFolderRef)
		return r.pending(ctx, scan, conditions, dependencyRetry)
	}
	if !k8s.IsConditionTrue(root.Status.Conditions, catalogv1alpha1.RootFolderConditionReady) {
		// Walking a root folder that is not accessible would report every
		// file as an error; wait for catalogarr's RootFolder reconciler.
		k8s.MarkReady(scan, &conditions, false, ReasonRootFolderNotReady,
			"rootFolder %q is not Ready", root.Name)
		return r.pending(ctx, scan, conditions, dependencyRetry)
	}

	task := schema.ScanTask{
		LibraryScanRef: schema.Ref{Namespace: scan.Namespace, Name: scan.Name, UID: string(scan.UID)},
		RootFolderRef:  schema.Ref{Namespace: root.Namespace, Name: root.Name},
		Path:           filepath.Join(root.Spec.Path, scan.Spec.Subpath),
		Mode:           string(mode(scan)),
		DryRun:         scan.Spec.DryRun,
	}
	schemaName, data, err := schema.Encode(task)
	if err != nil {
		return ctrl.Result{}, err
	}
	env := &events.Envelope{
		ID:     events.MsgIDForObject(string(scan.UID), scan.Generation, "scan"),
		Type:   "importarr.ScanTask",
		Schema: schemaName,
		Source: "importarr@" + version.String(),
		Key:    scan.Namespace + "/" + scan.Name,
		Time:   now,
		Data:   data,
	}
	if _, err := r.Bus.Publish(ctx, events.WorkScanSubject(root.Name), env); err != nil {
		if errors.Is(err, events.ErrQueueFull) {
			k8s.MarkReady(scan, &conditions, false, ReasonQueueFull, "the scan work queue is full")
			return r.pending(ctx, scan, conditions, queueFullRetry)
		}
		return ctrl.Result{}, err
	}

	startedAt := scan.Status.StartedAt
	if startedAt == nil {
		startedAt = ptr.To(metav1.NewTime(now))
	}
	k8s.MarkReady(scan, &conditions, false, ReasonScanning, "walking %s", task.Path)

	ac := baseStatus(scan).
		WithPhase(catalogv1alpha1.ScanPhaseRunning).
		WithStartedAt(*startedAt).
		WithConditions(k8s.ConditionACs(conditions)...)
	if err := r.apply(ctx, scan, ac); err != nil {
		return ctrl.Result{}, err
	}
	logging.FromContext(ctx).Info("library scan started", "path", task.Path, "mode", task.Mode)
	return ctrl.Result{RequeueAfter: pollInterval}, nil
}

// pending re-asserts the whole status with the Pending phase and the given
// conditions, then asks for another attempt.
//
// Every field this manager owns is sent, not just the conditions: server-side
// apply replaces a manager's ownership set on every apply, so an early return
// that sent conditions alone would release phase, startedAt and every counter
// the happy path had set -- reading as a healthy scan being zeroed by a
// transient missing RootFolder.
func (r *Reconciler) pending(
	ctx context.Context,
	scan *catalogv1alpha1.LibraryScan,
	conditions []metav1.Condition,
	after time.Duration,
) (ctrl.Result, error) {
	ac := baseStatus(scan).
		WithPhase(catalogv1alpha1.ScanPhasePending).
		WithConditions(k8s.ConditionACs(conditions)...)
	if err := r.apply(ctx, scan, ac); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: after}, nil
}

// poll reads the worker's checkpoint and aggregates it into status.
//
// The aggregate never moves a counter backwards. A redelivered task resumes
// its tally from the checkpoint, but when the checkpoint has outlived the
// bucket's TTL the redelivery starts from zero, and copying its first
// checkpoints straight into status would show a scan un-seeing files it has
// already reported. Each counter is therefore the higher of what status
// holds and what the worker reports, and status.unmatched keeps an entry
// until the worker reports the same path again.
//
// A Running scan fails, instead of polling forever, when its task has been
// dead-lettered (the DeadLettered fold: no delivery is coming) or when no
// new checkpoint has arrived for noProgressTimeout.
func (r *Reconciler) poll(ctx context.Context, scan *catalogv1alpha1.LibraryScan) (ctrl.Result, error) {
	now := r.now()
	key := types.NamespacedName{Namespace: scan.Namespace, Name: scan.Name}
	conditions, folded := conditions(scan)
	deadLettered := k8s.IsConditionTrue(conditions, k8s.ConditionDeadLettered)

	entry, err := r.Bus.KV(events.BucketProgress).Get(ctx, rescan.ProgressKey(string(scan.UID)))
	if err != nil {
		if !errors.Is(err, events.ErrKeyNotFound) {
			return ctrl.Result{}, err
		}
		// No checkpoint: none yet, or the bucket's TTL took it. The status
		// is re-asserted whole whenever anything is applied, so the
		// counters an earlier poll set survive.
		last := r.lastProgress(scan, nil, now)
		switch {
		case deadLettered:
			return r.fail(ctx, scan, baseStatus(scan), conditions, k8s.ReasonDeadLettered,
				"the scan task was dead-lettered, so no worker will finish the walk")
		case now.Sub(last) > noProgressTimeout:
			return r.fail(ctx, scan, baseStatus(scan), conditions, ReasonNoProgress,
				"no worker reported progress since %s (%s)", last.UTC().Format(time.RFC3339), noProgressTimeout)
		case folded:
			if err := r.apply(ctx, scan, baseStatus(scan).WithConditions(k8s.ConditionACs(conditions)...)); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{RequeueAfter: pollInterval}, nil
	}

	progress, err := rescan.DecodeProgress(entry.Value)
	if err != nil {
		return ctrl.Result{}, err
	}
	last := r.lastProgress(scan, &entry, now)
	ac := aggregate(scan, progress)

	switch {
	case !progress.Done && deadLettered:
		return r.fail(ctx, scan, ac, conditions, k8s.ReasonDeadLettered,
			"the scan task was dead-lettered, so no worker will finish the walk: %s", progress.Summary())
	case !progress.Done && now.Sub(last) > noProgressTimeout:
		return r.fail(ctx, scan, ac, conditions, ReasonNoProgress,
			"no worker reported progress since %s (%s): %s", last.UTC().Format(time.RFC3339), noProgressTimeout,
			progress.Summary())
	case !progress.Done:
		k8s.MarkReady(scan, &conditions, false, ReasonScanning, "%s", progress.Summary())
		ac = ac.WithPhase(catalogv1alpha1.ScanPhaseRunning)
		if err := r.apply(ctx, scan, ac.WithConditions(k8s.ConditionACs(conditions)...)); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: pollInterval}, nil
	case progress.Error != "":
		return r.fail(ctx, scan, ac, conditions, k8s.ReasonFailed, "%s", progress.Error)
	}
	k8s.MarkReady(scan, &conditions, true, k8s.ReasonSucceeded, "%s", progress.Summary())
	ac = ac.WithPhase(catalogv1alpha1.ScanPhaseCompleted).WithFinishedAt(metav1.NewTime(now))
	if err := r.apply(ctx, scan, ac.WithConditions(k8s.ConditionACs(conditions)...)); err != nil {
		return ctrl.Result{}, err
	}
	r.forget(key)
	return ctrl.Result{RequeueAfter: ttl(scan)}, nil
}

// fail settles a Running scan as Failed: ac (every other field this manager
// owns already on it) with the Failed phase, a finish time and a False Ready
// condition carrying reason and the message.
func (r *Reconciler) fail(
	ctx context.Context, scan *catalogv1alpha1.LibraryScan, ac *catalogac.LibraryScanStatusApplyConfiguration,
	conditions []metav1.Condition, reason, format string, args ...any,
) (ctrl.Result, error) {
	k8s.MarkReady(scan, &conditions, false, reason, format, args...)
	ac = ac.WithPhase(catalogv1alpha1.ScanPhaseFailed).
		WithFinishedAt(metav1.NewTime(r.now())).
		WithConditions(k8s.ConditionACs(conditions)...)
	if err := r.apply(ctx, scan, ac); err != nil {
		return ctrl.Result{}, err
	}
	r.forget(types.NamespacedName{Namespace: scan.Namespace, Name: scan.Name})
	return ctrl.Result{RequeueAfter: ttl(scan)}, nil
}

// aggregate folds a checkpoint into the scan's status, never lowering a
// counter status already reports (see poll): every field this manager owns
// except the phase, finishedAt and the conditions, which the caller sets.
func aggregate(scan *catalogv1alpha1.LibraryScan, p rescan.Progress) *catalogac.LibraryScanStatusApplyConfiguration {
	s := scan.Status
	ac := catalogac.LibraryScanStatus().
		WithFilesSeen(max(s.FilesSeen, p.FilesSeen)).
		WithFilesMatched(max(s.FilesMatched, p.FilesMatched)).
		WithItemsCreated(max(s.ItemsCreated, p.ItemsCreated)).
		WithItemsUpdated(max(s.ItemsUpdated, p.ItemsUpdated)).
		WithFilesSkipped(max(s.FilesSkipped, p.FilesSkipped))
	if s.StartedAt != nil {
		ac = ac.WithStartedAt(*s.StartedAt)
	}
	byPath := make(map[string]rescan.UnmatchedFile, len(s.Unmatched)+len(p.Unmatched))
	for _, u := range s.Unmatched {
		byPath[u.Path] = rescan.UnmatchedFile{Path: u.Path, Reason: u.Reason, Candidates: u.Candidates, SeenAt: u.SeenAt.Time}
	}
	for _, u := range p.Unmatched {
		byPath[u.Path] = u // the worker's own report of a path is the freshest
	}
	merged := make([]rescan.UnmatchedFile, 0, len(byPath))
	for _, u := range byPath {
		merged = append(merged, u)
	}
	if unmatched := unmatchedACs(merged); len(unmatched) > 0 {
		ac = ac.WithUnmatched(unmatched...)
	}
	return ac
}

// maybeExpire deletes a settled scan once its TTL has elapsed. A LibraryScan
// is a request, like a Search: it is observable with kubectl while it matters
// and then it goes away on its own.
func (r *Reconciler) maybeExpire(ctx context.Context, scan *catalogv1alpha1.LibraryScan) (ctrl.Result, error) {
	now := r.now()
	conditions, folded := conditions(scan)
	finished := scan.Status.FinishedAt
	if finished == nil {
		// Settled without a finish time (an older object, or a status
		// written before this controller existed). Stamp one so the TTL
		// has something to count from, re-asserting everything else.
		ac := baseStatus(scan).
			WithFinishedAt(metav1.NewTime(now)).
			WithConditions(k8s.ConditionACs(conditions)...)
		if err := r.apply(ctx, scan, ac); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: ttl(scan)}, nil
	}
	if folded {
		// A dead letter that arrives after the scan settled -- the final
		// delivery reported Failed, then its message went to the DLQ -- is
		// still shown, as is its removal.
		if err := r.apply(ctx, scan, baseStatus(scan).WithConditions(k8s.ConditionACs(conditions)...)); err != nil {
			return ctrl.Result{}, err
		}
	}

	expiry := finished.Add(ttl(scan))
	if now.Before(expiry) {
		return ctrl.Result{RequeueAfter: expiry.Sub(now)}, nil
	}
	if err := r.Client.Delete(ctx, scan); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	logging.FromContext(ctx).Info("library scan expired", "phase", scan.Status.Phase)
	return ctrl.Result{}, nil
}

// apply is the one status write in this package, under k8s.ManagerImportarr.
func (r *Reconciler) apply(
	ctx context.Context,
	scan *catalogv1alpha1.LibraryScan,
	status *catalogac.LibraryScanStatusApplyConfiguration,
) error {
	ac := catalogac.LibraryScan(scan.Name, scan.Namespace).WithStatus(status)
	_, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerImportarr, ac)
	return err
}

// baseStatus re-asserts every status field this manager owns except the
// conditions, from what is currently on the object. Callers layer their
// change on top -- including the conditions, which are deliberately left out
// here because WithConditions appends rather than replaces, so setting them
// in both places produces a duplicate listMapKey the apiserver rejects.
//
// Everything else is re-sent on purpose: server-side apply replaces a
// manager's ownership set on every apply, so a field this manager owns and
// omits is released, reading as "reset to zero" on the object.
func baseStatus(scan *catalogv1alpha1.LibraryScan) *catalogac.LibraryScanStatusApplyConfiguration {
	s := scan.Status
	ac := catalogac.LibraryScanStatus().
		WithFilesSeen(s.FilesSeen).
		WithFilesMatched(s.FilesMatched).
		WithItemsCreated(s.ItemsCreated).
		WithItemsUpdated(s.ItemsUpdated).
		WithFilesSkipped(s.FilesSkipped)
	if s.Phase != "" {
		ac = ac.WithPhase(s.Phase)
	}
	if s.StartedAt != nil {
		ac = ac.WithStartedAt(*s.StartedAt)
	}
	if s.FinishedAt != nil {
		ac = ac.WithFinishedAt(*s.FinishedAt)
	}
	if len(s.Unmatched) > 0 {
		acs := make([]*catalogac.UnmatchedFileApplyConfiguration, 0, len(s.Unmatched))
		for _, u := range s.Unmatched {
			acs = append(acs, catalogac.UnmatchedFile().
				WithPath(u.Path).
				WithReason(u.Reason).
				WithCandidates(u.Candidates...).
				WithSeenAt(u.SeenAt))
		}
		ac = ac.WithUnmatched(acs...)
	}
	return ac
}

// unmatchedACs renders the worker's unmatched files newest first and
// truncates to the CRD's MaxItems. A list the apiserver would reject helps
// nobody, and the newest entries are the ones a user is looking for.
func unmatchedACs(in []rescan.UnmatchedFile) []*catalogac.UnmatchedFileApplyConfiguration {
	sorted := make([]rescan.UnmatchedFile, len(in))
	copy(sorted, in)
	slices.SortStableFunc(sorted, func(a, b rescan.UnmatchedFile) int { return b.SeenAt.Compare(a.SeenAt) })
	if len(sorted) > maxUnmatched {
		sorted = sorted[:maxUnmatched]
	}

	out := make([]*catalogac.UnmatchedFileApplyConfiguration, 0, len(sorted))
	for _, u := range sorted {
		out = append(out, catalogac.UnmatchedFile().
			WithPath(u.Path).
			WithReason(u.Reason).
			WithCandidates(u.Candidates...).
			WithSeenAt(metav1.NewTime(u.SeenAt)))
	}
	return out
}

// mode is spec.mode with the CRD's own default applied, for an object that
// was created before the default existed.
func mode(scan *catalogv1alpha1.LibraryScan) catalogv1alpha1.ScanMode {
	if scan.Spec.Mode == "" {
		return catalogv1alpha1.ScanModeIncremental
	}
	return scan.Spec.Mode
}

// ttl is spec.ttlSecondsAfterFinished as a duration.
func ttl(scan *catalogv1alpha1.LibraryScan) time.Duration {
	seconds := int32(defaultTTLSeconds)
	if scan.Spec.TTLSecondsAfterFinished != nil {
		seconds = *scan.Spec.TTLSecondsAfterFinished
	}
	if seconds < 0 {
		seconds = 0
	}
	return time.Duration(seconds) * time.Second
}

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=libraryscans,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=libraryscans/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders,verbs=get;list;watch

// SetupWithManager registers the LibraryScan controller. There is no watch on
// anything but LibraryScan itself: the scan's own progress lives in a KV
// bucket, not in the cluster, so the loop is driven entirely by RequeueAfter.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Client == nil {
		r.Client = mgr.GetClient()
	}
	if r.Bus == nil {
		return fmt.Errorf("libraryscan: a bus is required")
	}
	// DeadLetteredAnnotationChanged is the DLQ fold's: the projector's
	// annotation bumps no generation, and without it the fold would wait
	// for the next poll -- or, on a settled scan, never run.
	return ctrl.NewControllerManagedBy(mgr).
		Named("libraryscan").
		For(&catalogv1alpha1.LibraryScan{}, builder.WithPredicates(
			k8s.Or(k8s.GenerationChanged(), k8s.DeadLetteredAnnotationChanged()))).
		WithOptions(controller.Options{RecoverPanic: ptr.To(true), ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}
