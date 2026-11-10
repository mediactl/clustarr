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

package audiobook

import (
	"context"
	"errors"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/itempass"
	"github.com/mediactl/clustarr/app/catalog/controller/itemstatus"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/catalog/itemread"
	"github.com/mediactl/clustarr/app/catalog/metadatainputs"
	"github.com/mediactl/clustarr/app/remediation/mfindex"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

const (
	// audiobookByQualityProfileIndexKey indexes Audiobook by the
	// QualityProfile it is ranked against, so a watched QualityProfile can
	// be mapped back to every Audiobook whose cutoffMet depends on it.
	audiobookByQualityProfileIndexKey = ".spec.qualityProfileRef"

	// audiobookByBookRefIndexKey indexes Audiobook by spec.bookRef, the
	// reverse direction from a watched Book back to every Audiobook linking
	// to it -- see bookref.go's mapBookRef.
	audiobookByBookRefIndexKey = ".spec.bookRef"
)

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=audiobooks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=audiobooks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=audiobooks/finalizers,verbs=update
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=books,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=qualityprofiles,verbs=get;list;watch
// The Recorder is a k8s.io/client-go/tools/events.EventRecorder, handed in by
// mgr.GetEventRecorder, and it writes events.k8s.io/v1 -- so events.k8s.io is
// the group to grant and the core group is not. See movie's identical marker
// and reconciler.go comment for the occasion this repo learned it.
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconciler reconciles an Audiobook: metadata staleness (publishing a
// MetadataTask when the cache is missing or past its RefreshTTL), path, the
// spec.bookRef link check, the file/download rollup from its MediaFiles
// (the remediation loop wakes it when one's rollup inputs move, loop spec
// §3.12) and Downloads, and its phase and cutoffReason through
// rollup.LeafPhase (2026-11-10 design D4). It is the sole writer of
// status.phase, status.cutoffReason, status.path, status.hasFile,
// status.fileRefs, status.partCount, status.fileQuality,
// status.fileFormatScore, status.cutoffMet, status.addOptionsApplied and
// status.activeDownloadRef (§3's single-writer rule); status.metadata
// belongs to the metadata gateway (field manager k8s.ManagerCatalogMetadata)
// and this reconciler never builds an AudiobookStatusApplyConfiguration that
// calls WithMetadata. It records a Kubernetes Event on every phase edge and
// on the edge into each transient failure (rollup.Reporter, design D14).
//
// status.activeDownloadRef is this reconciler's alone too (gap-fix ruling
// R-5): it is derived level-style from the Audiobook's own non-terminal
// Downloads (activeDownload), exactly as movie.Reconciler derives a Movie's,
// and the grab path no longer writes it. status.pendingGrab is read-only
// here, for Phase and the wake predicate below, and is never written by
// this reconciler.
type Reconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder k8sevents.EventRecorder
	Bus      events.Publisher

	// OnReconcile is a test-only hook, called at the top of every ReconcileItem.
	// It is nil-checked so production callers never need to set it.
	OnReconcile func()
}

// RegisterIndexes registers the field indexes ReconcileItem's Lists and the
// watches' map functions read. The remediation loop calls it once
// (remediation.RegisterIndexes, loop spec §3.16); a test that drives
// ReconcileItem against a bare cache calls it, and mfindex.Register for the
// MediaFiles, which the loop registers.
func RegisterIndexes(ctx context.Context, idx client.FieldIndexer) error {
	if err := idx.IndexField(ctx, &catalogv1alpha1.Audiobook{}, audiobookByQualityProfileIndexKey,
		func(o client.Object) []string {
			a, ok := o.(*catalogv1alpha1.Audiobook)
			if !ok || a.Spec.QualityProfileRef == "" {
				return nil
			}
			return []string{a.Spec.QualityProfileRef}
		}); err != nil {
		return err
	}
	return idx.IndexField(ctx, &catalogv1alpha1.Audiobook{}, audiobookByBookRefIndexKey,
		func(o client.Object) []string {
			a, ok := o.(*catalogv1alpha1.Audiobook)
			if !ok || a.Spec.BookRef == nil || *a.Spec.BookRef == "" {
				return nil
			}
			return []string{*a.Spec.BookRef}
		})
}

// Watches is the Audiobook's item path on the remediation loop (loop spec
// §3.12; S7, S9, S10): every watch that wakes an Audiobook except its files',
// which the loop's file signature (S1') wakes it for -- an active download's
// phase change, a profile edit, or a Book appearing after its Audiobook.
func (r *Reconciler) Watches() []rollup.Watch {
	return []rollup.Watch{
		{Object: &catalogv1alpha1.Audiobook{}, Map: rollup.Self, Predicates: []predicate.Predicate{audiobookPredicate()}, SkipInitial: r.idleAtStart},
		{Object: &catalogv1alpha1.QualityProfile{}, Map: r.mapQualityProfile, Predicates: []predicate.Predicate{k8s.GenerationChanged()}},
		{Object: &catalogv1alpha1.Book{}, Map: r.mapBookRef, Predicates: []predicate.Predicate{k8s.GenerationChanged()}},
	}
}

var _ rollup.Item = (*Reconciler)(nil)

// idleAtStart is the Audiobook watch's SkipInitial (2026-11-10 design D14,
// the Episode's of 2026-10-28): an Audiobook whose pass at a manager's start
// would change nothing -- unmonitored, or imported, with its metadata fresh
// and nothing in flight (rollup.LeafIdleAtStart) -- is left to its watches.
func (r *Reconciler) idleAtStart(ctx context.Context, o client.Object) bool {
	m, ok := o.(*catalogv1alpha1.Audiobook)
	if !ok {
		return false
	}
	monitored, err := itemread.Monitored(ctx, r.Client, m)
	if err != nil {
		return false
	}
	return rollup.LeafIdleAtStart(IdleInputs(m, monitored, time.Now().UTC()))
}

func (r *Reconciler) reporter() rollup.Reporter { return rollup.Reporter{Recorder: r.Recorder} }

// audiobookPredicate wakes this controller on a spec change
// (GenerationChanged), on the metadata gateway's own write
// (StatusFieldChanged scoped to status.metadata.refreshedAt) or on the grab
// path's status.pendingGrab -- and nothing else, so this controller's own
// Phase/Conditions/Path/file-rollup patch, which touches none of those,
// does not loop it. Mirrors movie.moviePredicate exactly; see its doc
// comment for why the pendingGrab arm is what makes Phase=Delayed reachable
// at all.
func audiobookPredicate() predicate.Predicate {
	return k8s.Or(
		k8s.GenerationChanged(),
		// An annotation-only change bumps no generation.
		k8s.DeadLetteredAnnotationChanged(),
		k8s.StatusFieldChanged(func(o client.Object) metav1.Time {
			a, ok := o.(*catalogv1alpha1.Audiobook)
			if !ok || a.Status.Metadata == nil {
				return metav1.Time{}
			}
			return a.Status.Metadata.RefreshedAt
		}),
		k8s.StatusFieldChanged(func(o client.Object) metav1.Time {
			a, ok := o.(*catalogv1alpha1.Audiobook)
			if !ok || a.Status.PendingGrab == nil {
				return metav1.Time{}
			}
			return a.Status.PendingGrab.GrabAt
		}),
	)
}

// activeDownload is the grab entry status.activeDownloadRef names (ruling
// R-5, ADR-0019 §6.11): the oldest non-terminal entry of the Audiobook's own
// status.downloads -- this pass's, when a stage decided them.
func activeDownload(ctx context.Context, m *catalogv1alpha1.Audiobook) *catalogv1alpha1.DownloadEntry {
	return rollup.ActiveEntry(itempass.Downloads(itempass.From(ctx), m.Status.Downloads), nil)
}

// mapQualityProfile is the reverse direction from an edited QualityProfile
// to every Audiobook ranked against it. QualityProfile is CLUSTER-scoped
// while Audiobook is namespaced, so the List deliberately carries no
// client.InNamespace: one profile is shared by every namespace, and
// enqueueing only one of them would be arbitrary. Mirrors
// movie.mapQualityProfile exactly.
func (r *Reconciler) mapQualityProfile(ctx context.Context, o client.Object) []reconcile.Request {
	qp, ok := o.(*catalogv1alpha1.QualityProfile)
	if !ok {
		return nil
	}
	var audiobooks catalogv1alpha1.AudiobookList
	if err := r.List(ctx, &audiobooks, client.MatchingFields{audiobookByQualityProfileIndexKey: qp.Name}, client.UnsafeDisableDeepCopy); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(audiobooks.Items))
	for _, a := range audiobooks.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: a.Namespace, Name: a.Name}})
	}
	return reqs
}

// ReconcileItem is the Audiobook's item path on the remediation loop (loop
// spec §3.12); it keeps the §8.8 skeleton: get, split on deletion, ensure
// the finalizer WITHOUT an early return (the rest of this reconcile runs
// against the same in-memory object in the same pass), then reconcileNormal.
func (r *Reconciler) ReconcileItem(ctx context.Context, nn types.NamespacedName) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "audiobook.Reconcile")
	defer span.End()
	if r.OnReconcile != nil {
		r.OnReconcile()
	}
	var a catalogv1alpha1.Audiobook
	if err := r.Get(ctx, nn, &a); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if k8s.IsDeleting(&a) {
		return r.reconcileDelete(ctx, &a)
	}
	name, err := k8s.FinalizerFor(&a, r.Scheme)
	if err != nil {
		return ctrl.Result{}, reconcile.TerminalError(err)
	}
	if _, err := k8s.EnsureFinalizer(ctx, r.Client, &a, name); err != nil {
		return ctrl.Result{}, err
	}
	return r.reconcileNormal(ctx, &a)
}

// reconcileDelete removes the finalizer. There is nothing else owned outside
// Kubernetes at this phase, mirroring movie.reconcileDelete's reasoning
// exactly: the mediaKey convention this task defines (namespace/name, no KV
// state of its own) has no clustarr-leases or clustarr-pending entries
// created by this controller to clean up.
func (r *Reconciler) reconcileDelete(ctx context.Context, m *catalogv1alpha1.Audiobook) (ctrl.Result, error) {
	// The grabs go first (ADR-0019 §6.8): while the downloads stage moves
	// every entry to Removing, the pass applies its contribution over the
	// stored status -- the complete declaration, so nothing is released --
	// and the Audiobook's own finalizer waits for FinalizerTransfers.
	if itempass.HasContribution(itempass.From(ctx)) {
		statusAC := reassertKnownStatus(catalogac.AudiobookStatus().WithObservedGeneration(m.Status.ObservedGeneration), m)
		statusAC = statusAC.WithConditions(k8s.ConditionACs(m.Status.Conditions)...)
		if conflicted, err := itemstatus.Apply(ctx, r.Client, m, catalogac.Audiobook(m.Name, m.Namespace).WithStatus(withEntries(ctx, statusAC, m))); err != nil || conflicted {
			return itemstatus.Requeue(conflicted), err
		}
	}
	if controllerutil.ContainsFinalizer(m, catalogv1alpha1.FinalizerTransfers) {
		return ctrl.Result{}, nil
	}
	name, err := k8s.FinalizerFor(m, r.Scheme)
	if err != nil {
		return ctrl.Result{}, reconcile.TerminalError(err)
	}
	// The deleted event goes out before the finalizer comes off, so a failed
	// removal re-announces it under the same envelope id rather than losing
	// it; an object that never held the finalizer never reaches here.
	if controllerutil.ContainsFinalizer(m, name) {
		r.publishItem(ctx, m, events.ActionDeleted, time.Now().UTC())
	}
	if _, err := k8s.RemoveFinalizer(ctx, r.Client, m, name); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// reconcileNormal implements spec §8.1's Want flow for Audiobook: the
// bookRef link check (always run, never blocking), metadata staleness
// decided and a MetadataTask published when needed, path computed from
// whatever metadata is cached, the MediaFile/Download rollup folded in and
// the phase decided (PhaseInputs over rollup.LeafPhase).
func (r *Reconciler) reconcileNormal(ctx context.Context, m *catalogv1alpha1.Audiobook) (ctrl.Result, error) {
	now := time.Now().UTC()
	// The Audiobook's own flag, and its Author's once design W1.3 lands in
	// itemread.Monitored (D3).
	monitored, err := itemread.Monitored(ctx, r.Client, m)
	if err != nil {
		return ctrl.Result{}, err
	}
	// Ready, MetadataReady, QueueFull, BookRefResolved and DeadLettered are
	// the Audiobook's conditions; the two retired on 2026-11-10 (design D4:
	// HasFile and CutoffMet, which status.hasFile and status.cutoffReason
	// say) are dropped from an Audiobook still carrying them, since an apply
	// that copied them from the cache would keep them for good.
	conditions := rollup.DropRetiredLeafConditions(m.Status.Conditions)
	// The DLQ projector's clustarr.io/dead-lettered annotation becomes the
	// DeadLettered condition here, on the one slice every status apply below
	// declares -- early returns included -- so no apply releases it.
	k8s.MarkDeadLettered(m, &conditions)

	// Announced before any apply, because the first apply records
	// observedGeneration and so consumes the edge (rollup.ItemAction).
	if action := rollup.ItemAction(m.Generation, m.Status.ObservedGeneration, m.Status.ObservedGeneration != 0); action != "" {
		r.publishItem(ctx, m, action, now)
	}

	statusAC := catalogac.AudiobookStatus().WithObservedGeneration(m.Generation)
	// spec.addOptions applied once, recorded on every pass (2026-11-10
	// design D11; ApplyAddOptions says how searchForMissing is read).
	statusAC = ApplyAddOptions(statusAC, m)

	// The bookRef check runs unconditionally, before either early-return
	// path below, so its result is folded into `conditions` (and therefore
	// into reassertKnownStatus's WithConditions call) regardless of which
	// return this reconcile takes. It never fails the reconcile -- see
	// resolveBookRef's doc comment.
	bookResolved, err := r.resolveBookRef(ctx, m)
	if err != nil {
		return ctrl.Result{}, err
	}
	switch {
	case m.Spec.BookRef == nil || *m.Spec.BookRef == "":
		k8s.MarkTrue(m, &conditions, catalogv1alpha1.AudiobookConditionBookRefResolved, k8s.ReasonReconciled, "no bookRef set")
	case bookResolved:
		k8s.MarkTrue(m, &conditions, catalogv1alpha1.AudiobookConditionBookRefResolved, k8s.ReasonReconciled, "book %q found", *m.Spec.BookRef)
	default:
		r.reporter().WarnEdge(m, m.Status.Conditions, catalogv1alpha1.AudiobookConditionBookRefResolved, metav1.ConditionFalse, "BookRefNotFound", "book %q not found", *m.Spec.BookRef)
		k8s.MarkFalse(m, &conditions, catalogv1alpha1.AudiobookConditionBookRefResolved, "BookRefNotFound", "book %q not found", *m.Spec.BookRef)
	}

	// A stale document is refreshed and the Audiobook reads Pending
	// meanwhile; a recent one the gateway wrote before it learned a field
	// is refreshed once, at once, and the Audiobook stays ready meanwhile
	// (MetadataStale).
	stale, outdated := MetadataStale(m, now)
	metaReady := !stale

	if stale || outdated {
		// The task carries what the refresh asks for -- the item and its
		// inputs, answered in clustarr-item-metadata (ADR-0019 §7.1) -- under
		// Msg-Id metadata/<uid>/<inputsHash>.
		subject, env, err := metadatainputs.Task(m, metadatainputs.For(m), events.PriorityNormal, now)
		if err != nil {
			return ctrl.Result{}, err
		}
		// Admitted by the dispatch ledger (ruling R23): a refusal publishes
		// nothing, and the item path requeues the pass after the wait.
		seq := metadatainputs.Seq(now)
		if itempass.Admit(itempass.From(ctx), metadatainputs.Durable, seq) {
			_, pubErr := r.Bus.Publish(ctx, subject, env)
			if pubErr != nil {
				if errors.Is(pubErr, events.ErrQueueFull) {
					r.reporter().WarnEdge(m, m.Status.Conditions, catalogv1alpha1.AudiobookConditionQueueFull, metav1.ConditionTrue, "QueueFull", "metadata work queue is full; retrying the refresh in a minute")
					k8s.MarkTrue(m, &conditions, catalogv1alpha1.AudiobookConditionQueueFull, "QueueFull", "metadata work queue is full")
					statusAC = reassertKnownStatus(statusAC, m)
					statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)
					if conflicted, err := itemstatus.Apply(ctx, r.Client, m, catalogac.Audiobook(m.Name, m.Namespace).WithStatus(withEntries(ctx, statusAC, m))); err != nil || conflicted {
						return itemstatus.Requeue(conflicted), err
					}
					return ctrl.Result{RequeueAfter: time.Minute}, nil
				}
				return ctrl.Result{}, pubErr
			}
			itempass.Published(itempass.From(ctx), metadatainputs.Durable, seq)
			k8s.MarkFalse(m, &conditions, catalogv1alpha1.AudiobookConditionQueueFull, "Published", "metadata task published")
		}
		k8s.MarkFalse(m, &conditions, catalogv1alpha1.AudiobookConditionMetadataReady, "Refreshing", "metadata refresh requested")
	} else {
		k8s.MarkFalse(m, &conditions, catalogv1alpha1.AudiobookConditionQueueFull, "Published", "no refresh currently queued")
		k8s.MarkTrue(m, &conditions, catalogv1alpha1.AudiobookConditionMetadataReady, k8s.ReasonReconciled, "metadata is fresh")
	}

	if m.Status.Metadata != nil {
		var rf catalogv1alpha1.RootFolder
		if err := r.Get(ctx, types.NamespacedName{Namespace: m.Namespace, Name: m.Spec.RootFolderRef}, &rf); err != nil {
			if apierrors.IsNotFound(err) {
				r.reporter().WarnEdge(m, m.Status.Conditions, k8s.ConditionReady, metav1.ConditionFalse, "RootFolderNotFound", "rootFolder %q not found", m.Spec.RootFolderRef)
				k8s.MarkFalse(m, &conditions, k8s.ConditionReady, "RootFolderNotFound", "rootFolder %q not found", m.Spec.RootFolderRef)
				statusAC = reassertKnownStatus(statusAC, m)
				statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)
				if conflicted, perr := itemstatus.Apply(ctx, r.Client, m, catalogac.Audiobook(m.Name, m.Namespace).WithStatus(withEntries(ctx, statusAC, m))); perr != nil || conflicted {
					return itemstatus.Requeue(conflicted), perr
				}
				return ctrl.Result{RequeueAfter: time.Minute}, nil
			}
			return ctrl.Result{}, err
		}

		eng := naming.NewEngine(naming.Config{
			Dialect:           naming.Dialect(rf.Spec.Naming.Dialect),
			ColonReplacement:  naming.ColonReplacement(rf.Spec.Naming.ColonReplacement),
			MultiEpisodeStyle: naming.MultiEpisodeStyle(rf.Spec.Naming.MultiEpisodeStyle),
			Overrides:         rf.Spec.Naming.Overrides,
		})
		nctx := namingContext(m.Spec, m.Status.Metadata)

		pth, err := Path(rf.Spec.Path, m.Spec.Folder, eng, nctx)
		if err != nil {
			return ctrl.Result{}, err
		}
		statusAC = statusAC.WithPath(pth)
	}

	var mfList catalogv1alpha1.MediaFileList
	if err := r.List(ctx, &mfList, client.InNamespace(m.Namespace), client.MatchingFields{mfindex.Item: mfindex.ItemKey(commonv1.MediaKindAudiobook, m.Name)}); err != nil {
		return ctrl.Result{}, err
	}

	profile, profileProblem, err := r.resolveProfile(ctx, m)
	if err != nil {
		return ctrl.Result{}, err
	}
	if profileProblem != "" {
		logging.FromContext(ctx).Warn("quality profile unresolved; cutoff not evaluated",
			"audiobook", m.Name, "namespace", m.Namespace,
			"qualityProfileRef", m.Spec.QualityProfileRef, "problem", profileProblem)
	}
	hasFile, fileRefs, partCount, fileQuality, fileFormatScore, fileCutoffMet := FileState(mfList.Items, profile)
	// Announced before the apply that records the new fileRefs, so a failed
	// apply re-announces the same edges under the same envelope ids
	// (rollup.MediaFileEvent) rather than losing them.
	for _, e := range FileTransitions(m.Status.FileRefs, fileRefs, mfList.Items) {
		r.publishFile(ctx, m, e.Action, e.File, e.MediaFile, now)
	}

	dl := activeDownload(ctx, m)
	overlayPhase, active := rollup.LeafOverlay(dl)

	in := PhaseInputs(m, monitored, metaReady, hasFile, fileCutoffMet, profile != nil, now)
	phase, reason := rollup.LeafPhase(in)
	if overlayPhase != "" {
		phase = overlayPhase
	}
	cutoffMet := rollup.LeafCutoffMet(in)
	// On the edge into an unresolved profile, never while it persists: the
	// parse errors are reported authoritatively on the QualityProfile, and
	// the cutoffReason carries the steady state (Movie's rule).
	if reason == commonv1.CutoffReasonProfileUnresolved && m.Status.CutoffReason != reason {
		r.reporter().Warn(m, commonv1.CutoffReasonProfileUnresolved, "cutoff not evaluated: %s", profileProblem)
	}

	statusAC = statusAC.
		WithPhase(phase).
		WithCutoffReason(reason).
		WithHasFile(hasFile).
		WithCutoffMet(cutoffMet).
		WithFileRefs(fileRefs...).
		WithPartCount(partCount).
		WithFileFormatScore(fileFormatScore)
	if fileQuality != nil {
		statusAC = statusAC.WithFileQuality(*fileQuality)
	}
	if active {
		statusAC = statusAC.WithActiveDownloadRef(dl.ID)
	}
	// With no Download still working on this item, WithActiveDownloadRef is
	// deliberately not called: omitting a field this manager owns releases
	// it under SSA, and since R-5 this manager is its only owner, so the
	// release removes it.

	// Ready gates on metaReady alone: a Pending phase is the same fact, and
	// BookRefResolved is an independent sibling, exactly as Movie's
	// Available does not gate Ready either.
	k8s.MarkReady(m, &conditions, metaReady, k8s.ReasonReconciled, "phase=%s", phase)
	statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)

	if conflicted, err := itemstatus.Apply(ctx, r.Client, m, catalogac.Audiobook(m.Name, m.Namespace).WithStatus(withEntries(ctx, statusAC, m))); err != nil || conflicted {
		return itemstatus.Requeue(conflicted), err
	}
	// After the apply, so a phase that never landed is never announced.
	r.reporter().PhaseEdge(m, string(m.Status.Phase), string(phase))

	return ctrl.Result{}, nil
}

// resolveProfile fetches the QualityProfile this Audiobook is ranked
// against and reports, as a non-empty problem string, any reason it could
// not be resolved: no reference set, the reference does not resolve, or the
// profile exists but does not parse against the catalogue. Verbatim
// movie.resolveProfile, adapted to Audiobook's own type -- see its doc
// comment for why only a non-NotFound API error is fatal.
//
// pkg/quality's built-in "audiobook" profile (mediaKind audiobook, cutoff
// MP3, catalogue/data/profiles/audiobook.json) is seeded by the
// qualityprofile controller's Bootstrap the same as every other built-in,
// and pkg/quality/definition.go's nonVideoDefinitions carries a real
// "audiobook" ladder (Unknown Audio < MP3 < M4B < FLAC, design §9) --
// quality.FromCRD resolves an audiobook profile exactly as it resolves a
// video one. Per R3 (docs/superpowers/plans/2026-09-23-phase-g-parity.md),
// automated search/grab for a kind is in scope only where pkg/quality
// already supports it; this reconciler's cutoff evaluation is that support
// for Audiobook, so it is wired here rather than skipped.
func (r *Reconciler) resolveProfile(ctx context.Context, m *catalogv1alpha1.Audiobook) (*quality.Profile, string, error) {
	if m.Spec.QualityProfileRef == "" {
		return nil, "spec.qualityProfileRef is empty", nil
	}
	var qp catalogv1alpha1.QualityProfile
	if err := r.Get(ctx, types.NamespacedName{Name: m.Spec.QualityProfileRef}, &qp); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Sprintf("qualityProfile %q not found", m.Spec.QualityProfileRef), nil
		}
		return nil, "", err
	}
	p, errs := quality.FromCRD(&qp, catalogue.LoadedCatalogue())
	if len(errs) > 0 {
		return nil, fmt.Sprintf("qualityProfile %q does not parse: %s", qp.Name, errors.Join(errs...)), nil
	}
	return &p, "", nil
}

// reassertKnownStatus re-adds every field this manager owns besides
// ObservedGeneration/Conditions to statusAC, sourced from m's current
// (pre-reconcile) status. Used on the two early-return paths (QueueFull,
// RootFolderNotFound): both are transient failures, and without this,
// PatchStatus's apply would omit every field it does not mention, releasing
// (zeroing) a healthy Audiobook's Phase/Path/HasFile/FileRefs/Quality/
// CutoffMet/ActiveDownloadRef the next time either blip happens. Verbatim
// movie.reassertKnownStatus's reasoning, adapted to Audiobook's field set
// (no Available/AvailableAt/FileFormatScore; FileRefs is a list, reasserted
// via WithFileRefs(m.Status.FileRefs...) rather than one optional scalar --
// see filestate.go's doc comment on why that call is safe to make
// unconditionally: an empty slice spreads to zero args and the field stays
// released, exactly the outcome wanted when there is genuinely no file).
func reassertKnownStatus(statusAC *catalogac.AudiobookStatusApplyConfiguration, m *catalogv1alpha1.Audiobook) *catalogac.AudiobookStatusApplyConfiguration {
	statusAC = statusAC.WithAddOptionsApplied(m.Status.AddOptionsApplied)
	if m.Status.Phase != "" {
		statusAC = statusAC.WithPhase(m.Status.Phase)
	}
	if m.Status.CutoffReason != "" {
		statusAC = statusAC.WithCutoffReason(m.Status.CutoffReason)
	}
	if m.Status.Path != "" {
		statusAC = statusAC.WithPath(m.Status.Path)
	}
	statusAC = statusAC.WithHasFile(m.Status.HasFile)
	statusAC = statusAC.WithFileRefs(m.Status.FileRefs...)
	statusAC = statusAC.WithPartCount(m.Status.PartCount)
	if m.Status.FileQuality != nil {
		statusAC = statusAC.WithFileQuality(*m.Status.FileQuality)
	}
	statusAC = statusAC.WithFileFormatScore(m.Status.FileFormatScore)
	statusAC = statusAC.WithCutoffMet(m.Status.CutoffMet)
	if m.Status.ActiveDownloadRef != nil {
		statusAC = statusAC.WithActiveDownloadRef(*m.Status.ActiveDownloadRef)
	}
	return statusAC
}

// withEntries folds the grab entries, the download phase, the intent nonces
// and release N's legacy record into a catalog status apply: this pass's
// decisions when the item stages made them, else the stored values, on
// every apply site -- the complete declaration (ADR-0019 §7.0, Review
// Focus 4). It is called once per apply: the list setters append.
func withEntries(ctx context.Context, ac *catalogac.AudiobookStatusApplyConfiguration, o *catalogv1alpha1.Audiobook) *catalogac.AudiobookStatusApplyConfiguration {
	p := itempass.From(ctx)
	if es := itempass.Downloads(p, o.Status.Downloads); len(es) > 0 {
		ac = ac.WithDownloads(itempass.EntryACs(es)...)
	}
	if ph := itempass.Phase(p, o.Status.DownloadPhase); ph != "" {
		ac = ac.WithDownloadPhase(ph)
	}
	if n := itempass.NoncesAC(itempass.Nonces(p, o.Status.DownloadNonces)); n != nil {
		ac = ac.WithDownloadNonces(n)
	}
	return ac
}
