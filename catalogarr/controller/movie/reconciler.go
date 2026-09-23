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

package movie

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/controller/rollup"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/version"
)

const (
	// mediaFileByMovieIndexKey indexes MediaFile by the Movie it backs,
	// filtered to spec.mediaRef.kind=movie so an Episode's own MediaFile
	// (same name is not possible across kinds today, but this future-proofs
	// the index against that) never matches a Movie's List.
	mediaFileByMovieIndexKey = ".spec.mediaRef.movie"

	// downloadByMovieIndexKey indexes Download by the Movie its
	// spec.target names (kind movie only). It is how the reconciler finds
	// the Downloads it derives status.activeDownloadRef from. spec.target is
	// immutable, so the index never has to follow an edit.
	downloadByMovieIndexKey = ".spec.target.movie"

	// movieByQualityProfileIndexKey indexes Movie by the QualityProfile it
	// is ranked against, so a watched QualityProfile can be mapped back to
	// every Movie whose cutoffMet depends on it.
	movieByQualityProfileIndexKey = ".spec.qualityProfileRef"
)

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies/finalizers,verbs=update
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=qualityprofiles,verbs=get;list;watch
// The Recorder is a k8s.io/client-go/tools/events.EventRecorder, handed in by
// mgr.GetEventRecorder, and it writes events.k8s.io/v1 -- so events.k8s.io is
// the group to grant and the core group is not. The marker and the recorder
// type move together or not at all: a mismatch is denied only on a real
// cluster, and no suite can see it, because envtest does not enforce RBAC.
// catalogarr's setupControllers records the occasion this repo learned it.
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconciler reconciles a Movie: metadata staleness (publishing a
// MetadataTask when the cache is missing or past its RefreshTTL),
// availability and path, and the file/download rollup from a watched
// MediaFile and Download. It is the sole writer of status.phase,
// status.hasFile, status.fileRef, status.fileQuality,
// status.fileFormatScore, status.cutoffMet and status.activeDownloadRef
// (§3's single-writer rule); status.metadata belongs to the metadata
// gateway (Task C5, field manager k8s.ManagerCatalogarrMetadata) and this
// reconciler never builds a MovieStatusApplyConfiguration that calls
// WithMetadata.
//
// status.activeDownloadRef has had exactly one writer since gap-fix ruling
// R-5: this reconciler, under k8s.ManagerCatalogarr, deriving it level-style
// from the Movie's own non-terminal Downloads (rollup.ActiveDownload) on
// every reconcile. The grab worker used to write it too, under
// k8s.ManagerCatalogarrGrab; PatchStatus forces ownership, so the field
// migrated to whichever wrote last and the clear-by-omission below only
// worked while this reconciler happened to hold it. The grab worker no
// longer writes it, and the Download watch below is what makes a new
// Download reach the ref without one.
//
// It also reports what it observes: a Kubernetes Event on each phase edge
// and on the edge into each transient failure (Recorder), and the catalog
// domain events -- the item added, updated and deleted, and its file
// imported, replaced and deleted -- on the EVENTS stream (Bus), which the
// history sink turns into the item's history.
type Reconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder k8sevents.EventRecorder
	Bus      events.Publisher

	// OnReconcile is a test-only hook, called at the top of every Reconcile.
	// It is nil-checked so production callers never need to set it.
	OnReconcile func()
}

// RegisterIndexes registers every field index Reconcile's List calls and
// the watches' map functions read, on idx. SetupWithManager calls it; a test
// that drives Reconcile against a bare manager cache calls it too, so the
// index names and extractors live in exactly one place.
func RegisterIndexes(ctx context.Context, idx client.FieldIndexer) error {
	if err := idx.IndexField(ctx, &catalogv1alpha1.MediaFile{}, mediaFileByMovieIndexKey,
		func(o client.Object) []string {
			mf, ok := o.(*catalogv1alpha1.MediaFile)
			if !ok || mf.Spec.MediaRef.Kind != commonv1.MediaKindMovie {
				return nil
			}
			return []string{mf.Spec.MediaRef.Name}
		}); err != nil {
		return err
	}
	if err := idx.IndexField(ctx, &downloadv1alpha1.Download{}, downloadByMovieIndexKey,
		func(o client.Object) []string {
			dl, ok := o.(*downloadv1alpha1.Download)
			if !ok || dl.Spec.Target.Kind != commonv1.MediaKindMovie {
				return nil
			}
			return []string{dl.Spec.Target.Name}
		}); err != nil {
		return err
	}
	return idx.IndexField(ctx, &catalogv1alpha1.Movie{}, movieByQualityProfileIndexKey,
		func(o client.Object) []string {
			m, ok := o.(*catalogv1alpha1.Movie)
			if !ok || m.Spec.QualityProfileRef == "" {
				return nil
			}
			return []string{m.Spec.QualityProfileRef}
		})
}

// SetupWithManager registers the Movie controller: the finalizer/
// metadata-refresh predicate on Movie itself, and the MediaFile/Download
// watches added in review so an imported file or an active download's
// phase change reaches this reconciler without waiting for a poll.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := RegisterIndexes(context.Background(), mgr.GetFieldIndexer()); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("movie").
		For(&catalogv1alpha1.Movie{}, builder.WithPredicates(moviePredicate())).
		Watches(&catalogv1alpha1.MediaFile{}, handler.EnqueueRequestsFromMapFunc(r.mapMediaFile), builder.WithPredicates(k8s.GenerationChanged())).
		Watches(&downloadv1alpha1.Download{}, handler.EnqueueRequestsFromMapFunc(r.mapDownload), builder.WithPredicates(downloadPredicate())).
		Watches(&catalogv1alpha1.QualityProfile{}, handler.EnqueueRequestsFromMapFunc(r.mapQualityProfile), builder.WithPredicates(k8s.GenerationChanged())).
		WithOptions(controller.Options{RecoverPanic: ptr.To(true), ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}

// moviePredicate wakes this controller on a spec change (GenerationChanged),
// on the metadata gateway's own write (StatusFieldChanged scoped to
// status.metadata.refreshedAt) or on the grab worker's status.pendingGrab --
// and nothing else, so this controller's own Phase/Conditions/Available/Path/
// file-rollup patch, which touches none of those, does not loop it. This is
// the same self-loop-avoidance shape §10 and Step 8's envtest require.
//
// The DLQ projector's clustarr.io/dead-lettered annotation is the fourth
// arm: it changes neither generation nor status, so without
// k8s.DeadLetteredAnnotationChanged the DeadLettered condition would wait
// for some unrelated event to be folded in, and an operator removing the
// annotation would not clear it either.
//
// The pendingGrab arm is what makes Phase=Delayed reachable at all. The grab
// worker writes status.pendingGrab under k8s.ManagerCatalogarrGrab, which
// bumps no generation and touches no metadata, so without this arm the write
// would not even schedule a reconcile: the movie would sit at Wanted for the
// whole delay window and only move when a Download appeared. The extracted
// key is grabAt, which changes whenever the pending grab is set, rescheduled
// or cleared -- and metav1.Time is comparable, which StatusFieldChanged
// requires.
func moviePredicate() predicate.Predicate {
	return k8s.Or(
		k8s.GenerationChanged(),
		k8s.StatusFieldChanged(func(o client.Object) metav1.Time {
			mv, ok := o.(*catalogv1alpha1.Movie)
			if !ok || mv.Status.Metadata == nil {
				return metav1.Time{}
			}
			return mv.Status.Metadata.RefreshedAt
		}),
		k8s.StatusFieldChanged(func(o client.Object) metav1.Time {
			mv, ok := o.(*catalogv1alpha1.Movie)
			if !ok || mv.Status.PendingGrab == nil {
				return metav1.Time{}
			}
			return mv.Status.PendingGrab.GrabAt
		}),
		k8s.DeadLetteredAnnotationChanged(),
	)
}

// downloadPredicate wakes the Download watch on a spec change (Create
// always passes regardless), on a status.phase transition, and on the
// deletion timestamp appearing. GenerationChanged alone would be wrong here,
// unlike the MediaFile watch above: MediaFileSpec's Quality/FormatScore are
// spec fields, so a create or edit bumps generation, but
// DownloadStatus.Phase is entirely status-driven -- grabarr sets it through
// k8s.PatchStatus, which never touches spec/generation -- so a
// GenerationChanged-only predicate would never fire on the one transition
// this watch exists to observe. The deletion arm is there because a
// Download being torn down stops counting as the Movie's active download
// (rollup.DownloadNonTerminal) the moment it is marked, not when its
// finalizers finally let it go.
func downloadPredicate() predicate.Predicate {
	return k8s.Or(
		k8s.GenerationChanged(),
		k8s.StatusFieldChanged(func(o client.Object) downloadv1alpha1.DownloadPhase {
			dl, ok := o.(*downloadv1alpha1.Download)
			if !ok {
				return ""
			}
			return dl.Status.Phase
		}),
		k8s.StatusFieldChanged(k8s.IsDeleting),
	)
}

// mapMediaFile needs no List/index -- a MediaFile already carries its
// target's identity directly in spec.mediaRef, so this is the cheap
// direction.
func (r *Reconciler) mapMediaFile(_ context.Context, o client.Object) []reconcile.Request {
	mf, ok := o.(*catalogv1alpha1.MediaFile)
	if !ok || mf.Spec.MediaRef.Kind != commonv1.MediaKindMovie {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: mf.Namespace, Name: mf.Spec.MediaRef.Name}}}
}

// mapDownload needs no List either: a Download names its target in
// spec.target, so a Download that appears -- before anything has set the
// ref, which is the whole point of deriving the ref from the Download --
// reaches its Movie directly.
func (r *Reconciler) mapDownload(_ context.Context, o client.Object) []reconcile.Request {
	dl, ok := o.(*downloadv1alpha1.Download)
	if !ok || dl.Spec.Target.Kind != commonv1.MediaKindMovie {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: dl.Namespace, Name: dl.Spec.Target.Name}}}
}

// mapQualityProfile is the reverse direction from an edited QualityProfile
// to every Movie ranked against it. Without this watch an operator raising
// or lowering a profile's cutoff changed nothing observable: cutoffMet (and
// with it the spec-mandated CutoffMet condition, the Imported/CutoffUnmet
// phase and the item's place in the search rotation) was only recomputed
// when some unrelated event happened to wake the Movie.
//
// QualityProfile is CLUSTER-scoped while Movie is namespaced, so the List
// deliberately carries no client.InNamespace: one profile is shared by
// every namespace, and enqueueing only one of them would be arbitrary.
func (r *Reconciler) mapQualityProfile(ctx context.Context, o client.Object) []reconcile.Request {
	qp, ok := o.(*catalogv1alpha1.QualityProfile)
	if !ok {
		return nil
	}
	var movies catalogv1alpha1.MovieList
	if err := r.List(ctx, &movies, client.MatchingFields{movieByQualityProfileIndexKey: qp.Name}); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(movies.Items))
	for _, m := range movies.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: m.Namespace, Name: m.Name}})
	}
	return reqs
}

// Reconcile implements the §8.8 skeleton: get, split on deletion, ensure the
// finalizer WITHOUT an early return (the rest of this reconcile runs against
// the same in-memory object in the same pass), then reconcileNormal.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "movie.Reconcile")
	defer span.End()
	if r.OnReconcile != nil {
		r.OnReconcile()
	}
	var m catalogv1alpha1.Movie
	if err := r.Get(ctx, req.NamespacedName, &m); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if k8s.IsDeleting(&m) {
		return r.reconcileDelete(ctx, &m)
	}
	name, err := k8s.FinalizerFor(&m, r.Scheme)
	if err != nil {
		return ctrl.Result{}, reconcile.TerminalError(err)
	}
	if _, err := k8s.EnsureFinalizer(ctx, r.Client, &m, name); err != nil {
		return ctrl.Result{}, err
	}
	return r.reconcileNormal(ctx, &m)
}

// reconcileDelete announces the deletion and removes the finalizer. There
// is nothing else owned outside Kubernetes at this phase: the mediaKey
// convention this task defines (namespace/name, no KV state of its own) has
// no clustarr-leases or clustarr-pending entries created by this controller
// to clean up, and adding speculative KV cleanup here would be untestable
// against a real bus without over-scoping this task.
//
// The deleted ItemEvent goes out before the finalizer comes off, so a
// failed removal re-announces with the same envelope id rather than losing
// the event; a Movie that never held the finalizer never reaches here.
func (r *Reconciler) reconcileDelete(ctx context.Context, m *catalogv1alpha1.Movie) (ctrl.Result, error) {
	name, err := k8s.FinalizerFor(m, r.Scheme)
	if err != nil {
		return ctrl.Result{}, reconcile.TerminalError(err)
	}
	if controllerutil.ContainsFinalizer(m, name) {
		r.publishItem(ctx, m, events.ActionDeleted, time.Now().UTC())
	}
	if _, err := k8s.RemoveFinalizer(ctx, r.Client, m, name); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// reconcileNormal implements spec §8.1's Want flow for Movie: addOptions
// recorded once, metadata staleness decided and a MetadataTask published
// when needed, availability/path/phase computed from whatever metadata is
// cached, and the MediaFile/Download rollup folded in.
func (r *Reconciler) reconcileNormal(ctx context.Context, m *catalogv1alpha1.Movie) (ctrl.Result, error) {
	now := time.Now().UTC()
	monitored := ptr.Deref(m.Spec.Monitored, true)
	conditions := append([]metav1.Condition(nil), m.Status.Conditions...)
	// Folded first, on the one slice every apply below declares -- the two
	// early returns included -- so the DLQ projector's annotation reaches
	// status.conditions on whichever path this reconcile takes, and no
	// partial apply can release the condition while the annotation stands.
	k8s.MarkDeadLettered(m, &conditions)

	// Announced before any apply, because the first apply records
	// addOptionsApplied (and observedGeneration) and so consumes the edge.
	if action := rollup.ItemAction(m.Generation, m.Status.ObservedGeneration, m.Status.AddOptionsApplied); action != "" {
		r.publishItem(ctx, m, action, now)
	}

	statusAC := catalogac.MovieStatus().WithObservedGeneration(m.Generation)
	// Collection fan-out (AddOptions.Monitor's only real effect) has no
	// owning task in this wave; there is nothing to actually apply yet, so
	// this only records that the decision point was reached. It is sent on
	// every reconcile, not just the first, deliberately: server-side apply
	// releases (clears) a field a manager stops sending -- proven by
	// pkg/k8s/patch_envtest_test.go's TestPatchStatusReleasesItsOwnFieldsOnly
	// -- so omitting WithAddOptionsApplied once it is already true would
	// flip it back to false on this manager's very next apply, not leave it
	// unchanged. Idempotence here means "no new decision is made", which a
	// constant true already expresses; it does not mean "stop asserting the
	// field".
	statusAC = statusAC.WithAddOptionsApplied(true)

	stale := m.Status.Metadata == nil
	if !stale {
		state := movieRefreshState(m.Status.Metadata, now)
		ttl := metadata.RefreshTTL(commonv1.MediaKindMovie, state, m.Status.Metadata.RefreshedAt.Time)
		stale = now.Sub(m.Status.Metadata.RefreshedAt.Time) >= ttl
	}
	metaReady := !stale

	if stale {
		// The envelope key is the <namespace>/<name> routing key every
		// worker parses to recover the namespace; the media key is the
		// subject token. They are not interchangeable -- the media key
		// is tokenised for the wire and has no slash to cut on.
		envKey := m.Namespace + "/" + m.Name
		mediaKey := events.MediaKey(string(commonv1.MediaKindMovie), m.Namespace, m.Name)
		schemaName, data, err := schema.Encode(schema.MetadataTask{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: m.Name},
		})
		if err != nil {
			return ctrl.Result{}, err
		}
		env := &events.Envelope{
			ID:     events.MsgIDForObject(string(m.UID), m.Generation, "metadata"),
			Type:   "catalog.MetadataTask",
			Schema: schemaName,
			Source: "catalogarr@" + version.String(),
			Key:    envKey,
			Time:   now,
			Data:   data,
		}
		_, pubErr := r.Bus.Publish(ctx, events.WorkMetadataSubject(events.PriorityNormal, mediaKey), env)
		if pubErr != nil {
			if errors.Is(pubErr, events.ErrQueueFull) {
				if rollup.Transitioned(m.Status.Conditions, catalogv1alpha1.MovieConditionQueueFull, metav1.ConditionTrue, "QueueFull") {
					r.warn(m, "QueueFull", "metadata work queue is full; retrying the refresh in a minute")
				}
				k8s.MarkTrue(m, &conditions, catalogv1alpha1.MovieConditionQueueFull, "QueueFull", "metadata work queue is full")
				statusAC = reassertKnownStatus(statusAC, m)
				statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)
				if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Movie(m.Name, m.Namespace).WithStatus(statusAC)); err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{RequeueAfter: time.Minute}, nil
			}
			return ctrl.Result{}, pubErr
		}
		k8s.MarkFalse(m, &conditions, catalogv1alpha1.MovieConditionQueueFull, "Published", "metadata task published")
		k8s.MarkFalse(m, &conditions, catalogv1alpha1.MovieConditionMetadataReady, "Refreshing", "metadata refresh requested")
	} else {
		k8s.MarkTrue(m, &conditions, catalogv1alpha1.MovieConditionMetadataReady, k8s.ReasonReconciled, "metadata is fresh")
	}

	var available bool
	var availableAt time.Time
	if m.Status.Metadata != nil {
		var rf catalogv1alpha1.RootFolder
		if err := r.Get(ctx, types.NamespacedName{Namespace: m.Namespace, Name: m.Spec.RootFolderRef}, &rf); err != nil {
			if apierrors.IsNotFound(err) {
				if rollup.Transitioned(m.Status.Conditions, k8s.ConditionReady, metav1.ConditionFalse, "RootFolderNotFound") {
					r.warn(m, "RootFolderNotFound", "rootFolder %q not found", m.Spec.RootFolderRef)
				}
				k8s.MarkFalse(m, &conditions, k8s.ConditionReady, "RootFolderNotFound", "rootFolder %q not found", m.Spec.RootFolderRef)
				statusAC = reassertKnownStatus(statusAC, m)
				statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)
				if _, perr := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Movie(m.Name, m.Namespace).WithStatus(statusAC)); perr != nil {
					return ctrl.Result{}, perr
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
		nctx := naming.Context{
			Kind:   commonv1.MediaKindMovie,
			Title:  m.Status.Metadata.Title,
			Year:   int(m.Status.Metadata.Year),
			TmdbID: strconv.FormatInt(m.Spec.TmdbID, 10),
			ImdbID: m.Status.Metadata.ExternalIDs["imdb"],
		}

		available, availableAt = Availability(m.Spec.MinimumAvailability, m.Status.Metadata, m.Spec.AvailabilityDelayDays, now)
		pth, err := Path(rf.Spec.Path, m.Spec.Folder, eng, nctx)
		if err != nil {
			return ctrl.Result{}, err
		}
		statusAC = statusAC.WithAvailable(available).WithPath(pth)
		if !availableAt.IsZero() {
			statusAC = statusAC.WithAvailableAt(metav1.NewTime(availableAt))
		}
		k8s.MarkTrue(m, &conditions, catalogv1alpha1.MovieConditionAvailable, k8s.ReasonReconciled, "available=%t", available)
		if !available {
			k8s.MarkFalse(m, &conditions, catalogv1alpha1.MovieConditionAvailable, k8s.ReasonPending, "not yet available")
		}
	}

	var mfList catalogv1alpha1.MediaFileList
	if err := r.List(ctx, &mfList, client.InNamespace(m.Namespace), client.MatchingFields{mediaFileByMovieIndexKey: m.Name}); err != nil {
		return ctrl.Result{}, err
	}
	mf := rollup.PickMediaFile(mfList.Items)

	profile, profileProblem, err := r.resolveProfile(ctx, m)
	if err != nil {
		return ctrl.Result{}, err
	}
	if profileProblem != "" {
		logging.FromContext(ctx).Warn("quality profile unresolved; cutoff not evaluated",
			"movie", m.Name, "namespace", m.Namespace,
			"qualityProfileRef", m.Spec.QualityProfileRef, "problem", profileProblem)
	}
	hasFile, fileRef, fileQuality, fileFormatScore, cutoffMet := FileState(mf, profile)
	if action, file := rollup.FileTransition(m.Status.FileRef, mf); action != "" {
		r.publishFile(ctx, m, action, file, mf, now)
	}

	dl, err := r.activeDownload(ctx, m)
	if err != nil {
		return ctrl.Result{}, err
	}
	// The overlay only decides the phase. Whether the ref is set is
	// rollup.DownloadNonTerminal's call, made inside activeDownload, and the
	// two differ on purpose for Completed and Seeding: see its doc comment.
	overlayPhase, _ := DownloadOverlay(dl)

	phase := Phase(monitored, metaReady, available, hasFile, cutoffMet, profile != nil, m.Status.PendingGrab != nil)
	if overlayPhase != "" {
		phase = overlayPhase
	}

	statusAC = statusAC.
		WithPhase(phase).
		WithHasFile(hasFile).
		WithFileFormatScore(fileFormatScore).
		WithCutoffMet(cutoffMet)
	if fileRef != nil {
		statusAC = statusAC.WithFileRef(*fileRef)
	}
	if fileQuality != nil {
		statusAC = statusAC.WithFileQuality(*fileQuality)
	}
	if dl != nil {
		statusAC = statusAC.WithActiveDownloadRef(dl.Name)
	}
	// With no non-terminal Download, WithActiveDownloadRef is deliberately
	// not called: omitting a field this manager owns releases it under SSA
	// (pkg/k8s.PatchStatus's doc; proven by
	// pkg/k8s/patch_envtest_test.go's TestPatchStatusReleasesItsOwnFieldsOnly),
	// and since R-5 this manager is the field's only owner, so the release
	// removes it.

	// HasFile and CutoffMet are two of the six conditions spec §4.2 lists
	// for Movie. Until task C13 their only writer anywhere was the MediaFile
	// controller's rollupToOwner, which applied them (and the file fields
	// above) under this reconciler's own k8s.ManagerCatalogarr and therefore
	// released path/available/availableAt/addOptionsApplied/
	// observedGeneration/activeDownloadRef on every apply. That rollup is
	// gone; the conditions move here, to the object's sole status writer,
	// alongside the file fields they describe. They are strictly more
	// complete here: the rollup only ran when a MediaFile existed, so it
	// could raise HasFile but never lower it, and it had no way to say
	// "no file yet" at all.
	if hasFile {
		k8s.MarkTrue(m, &conditions, catalogv1alpha1.MovieConditionHasFile, "HasFile", "backed by MediaFile %s", ptr.Deref(fileRef, ""))
	} else {
		k8s.MarkFalse(m, &conditions, catalogv1alpha1.MovieConditionHasFile, k8s.ReasonPending, "no MediaFile backs this movie")
	}
	switch {
	case !hasFile:
		k8s.MarkFalse(m, &conditions, catalogv1alpha1.MovieConditionCutoffMet, k8s.ReasonPending, "no file to rank against the profile cutoff")
	case profile == nil:
		if rollup.Transitioned(m.Status.Conditions, catalogv1alpha1.MovieConditionCutoffMet, metav1.ConditionFalse, "ProfileUnresolved") {
			r.warn(m, "ProfileUnresolved", "cutoff not evaluated: %s", profileProblem)
		}
		// The cutoff was NOT evaluated. Reporting the ordinary CutoffUnmet
		// here would be a lie with consequences: it reads as "this file is
		// below your cutoff, an upgrade is wanted", so a malformed or
		// missing profile silently parks the item in the search rotation
		// looking like a legitimate upgrade candidate. A distinct reason is
		// the right carrier rather than an Event, because this is a steady
		// state that persists on every reconcile until an operator fixes
		// the profile -- an Event would re-fire per item per reconcile --
		// and because the parse errors themselves are already reported
		// authoritatively on the QualityProfile object by the
		// qualityprofile controller. The item's job is only to stop
		// claiming a verdict it never reached.
		k8s.MarkFalse(m, &conditions, catalogv1alpha1.MovieConditionCutoffMet, "ProfileUnresolved", "cutoff not evaluated: %s", profileProblem)
	case cutoffMet:
		k8s.MarkTrue(m, &conditions, catalogv1alpha1.MovieConditionCutoffMet, "CutoffMet", "file meets the profile cutoff")
	default:
		k8s.MarkFalse(m, &conditions, catalogv1alpha1.MovieConditionCutoffMet, "CutoffUnmet", "file does not meet the profile cutoff")
	}

	k8s.MarkReady(m, &conditions, metaReady && phase != catalogv1alpha1.MoviePhasePending, k8s.ReasonReconciled, "phase=%s", phase)
	statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)

	if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Movie(m.Name, m.Namespace).WithStatus(statusAC)); err != nil {
		return ctrl.Result{}, err
	}
	// After the apply, so a phase that never landed is never announced. The
	// first phase a Movie ever gets is not an edge worth an Event: the
	// ItemEvent above already said it was added.
	if m.Status.Phase != "" && m.Status.Phase != phase {
		r.normal(m, string(phase), "phase %s -> %s", m.Status.Phase, phase)
	}

	result := ctrl.Result{}
	if m.Status.Metadata != nil && !available && !availableAt.IsZero() {
		d := availableAt.Sub(now)
		if d < 0 {
			d = 0
		}
		result.RequeueAfter = d
	}
	return result, nil
}

// resolveProfile fetches the QualityProfile this Movie is ranked against
// and reports, as a non-empty problem string, any reason it could not be
// resolved: no reference set, the reference does not resolve, or the
// profile exists but does not parse against the catalogue. Before task C13
// all three collapsed into a bare nil profile and a cutoffMet=false that
// was indistinguishable from a genuine "this file is below the cutoff" --
// see the CutoffMet condition's own comment for why that matters now.
//
// Only a non-NotFound API error is fatal; every other outcome degrades to
// (nil, problem, nil) so one bad profile cannot wedge the reconcile.
func (r *Reconciler) resolveProfile(ctx context.Context, m *catalogv1alpha1.Movie) (*quality.Profile, string, error) {
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
// ObservedGeneration/AddOptionsApplied/Conditions to statusAC, sourced from
// m's current (pre-reconcile) status. Used on the two early-return paths
// (QueueFull, RootFolderNotFound): both are transient failures -- a metadata
// publish hitting a full queue, or a RootFolder lookup that briefly 404s --
// and without this, PatchStatus's apply would omit every field it does not
// mention, releasing (zeroing) a healthy Movie's Phase/Available/
// AvailableAt/Path/HasFile/FileRef/FileQuality/FileFormatScore/CutoffMet/
// ActiveDownloadRef the next time either blip happens. This is the same
// apply-release mechanism as AddOptionsApplied's own fix above and the
// Series/Episode field-manager split, a third form of it in this
// reconciler: a healthy object sitting at Imported must not be reset to
// zero by a transient failure that never reaches the code recomputing those
// fields.
func reassertKnownStatus(statusAC *catalogac.MovieStatusApplyConfiguration, m *catalogv1alpha1.Movie) *catalogac.MovieStatusApplyConfiguration {
	if m.Status.Phase != "" {
		statusAC = statusAC.WithPhase(m.Status.Phase)
	}
	statusAC = statusAC.WithAvailable(m.Status.Available)
	if m.Status.AvailableAt != nil {
		statusAC = statusAC.WithAvailableAt(*m.Status.AvailableAt)
	}
	if m.Status.Path != "" {
		statusAC = statusAC.WithPath(m.Status.Path)
	}
	statusAC = statusAC.WithHasFile(m.Status.HasFile)
	if m.Status.FileRef != nil {
		statusAC = statusAC.WithFileRef(*m.Status.FileRef)
	}
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

// movieRefreshState derives the metadata.RefreshTTL state bucket from a
// Movie's own cached metadata, per pkg/metadata/refresh.go's bucket names
// (not docs/research/quality.md's unrelated 90-day Sonarr "Recent"
// episode-monitor window).
func movieRefreshState(meta *catalogv1alpha1.MovieMetadata, now time.Time) string {
	switch meta.Status {
	case catalogv1alpha1.MovieReleaseStatusTBA, catalogv1alpha1.MovieReleaseStatusAnnounced:
		return metadata.RefreshStateAnnounced
	case catalogv1alpha1.MovieReleaseStatusInCinemas:
		return metadata.RefreshStateInCinemas
	default: // released
		latest := latestReleaseDate(meta)
		if !latest.IsZero() && now.Sub(latest) < ReleasedRecentWindow {
			return metadata.RefreshStateReleasedRecent
		}
		return metadata.RefreshStateReleasedOld
	}
}

func latestReleaseDate(meta *catalogv1alpha1.MovieMetadata) time.Time {
	var latest time.Time
	if meta.DigitalRelease != nil && meta.DigitalRelease.After(latest) {
		latest = meta.DigitalRelease.Time
	}
	if meta.PhysicalRelease != nil && meta.PhysicalRelease.After(latest) {
		latest = meta.PhysicalRelease.Time
	}
	return latest
}
