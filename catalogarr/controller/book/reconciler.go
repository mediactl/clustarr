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

package book

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
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
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
	// mediaFileByBookIndexKey indexes MediaFile by the Book it backs,
	// filtered to spec.mediaRef.kind=book, mirroring
	// movie.mediaFileByMovieIndexKey.
	mediaFileByBookIndexKey = ".spec.mediaRef.book"

	// bookByActiveDownloadIndexKey indexes Book by its
	// status.activeDownloadRef, the reverse direction from a watched
	// Download back to the Book holding the reference.
	bookByActiveDownloadIndexKey = ".status.activeDownloadRef"

	// bookByQualityProfileIndexKey indexes Book by its OWN direct
	// spec.qualityProfileRef override only -- not the profile it effectively
	// resolves to via an owning Author (BookSpec.QualityProfileRef's own doc
	// comment: "overrides the Author's QualityProfile"). A Book that
	// inherits its profile from its Author is not reachable through this
	// index; mapQualityProfile's own doc comment records this as a known,
	// deliberate scope limit rather than a silent gap.
	bookByQualityProfileIndexKey = ".spec.qualityProfileRef"
)

// conditionQueueFull mirrors movie.MovieConditionQueueFull's name and
// semantics; book_types.go declares no BookConditionQueueFull constant of
// its own, so this is a local, package-scoped equivalent.
const conditionQueueFull = "QueueFull"

// metadataRPCTimeout bounds how long a single reconcile waits on the
// metadata-staleness publish before giving up -- there is no RPC call in
// this reconciler (unlike author's), but this constant mirrors
// author.bookSyncRPCBackoff's name/shape for the requeue used after a
// context resolution problem.
const rootFolderRequeueAfter = time.Minute

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=books,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=books/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=books/finalizers,verbs=update
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=authors,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=qualityprofiles,verbs=get;list;watch
// The Recorder is a k8s.io/client-go/tools/events.EventRecorder, handed in by
// mgr.GetEventRecorder, and it writes events.k8s.io/v1 -- so events.k8s.io is
// the group to grant and the core group is not. See series/reconciler.go's
// identical marker and comment for the occasion this repo learned it.
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconciler reconciles a Book: metadata staleness (publishing a
// MetadataTask when the cache is missing or past its RefreshTTL), path
// (resolved through an owning Author when spec.authorRef is set, or directly
// from the Book's own spec.rootFolderRef when standalone), and the file/
// download rollup from a watched MediaFile and Download. It is the sole
// writer of status.phase, status.path, status.hasFile, status.fileRef,
// status.fileFormat, status.cutoffMet and status.activeDownloadRef;
// status.metadata belongs to the metadata gateway (field manager
// k8s.ManagerCatalogarrMetadata) and this reconciler never builds a
// BookStatusApplyConfiguration that calls WithMetadata.
//
// Books has no Create/Delete verb in this reconciler's own RBAC marker: a
// fanned-out Book is created by the author package's Reconciler, a
// standalone Book by a user or import list -- this reconciler only ever
// Gets and patches status on a Book that already exists.
type Reconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder k8sevents.EventRecorder
	Bus      events.Publisher

	// OnReconcile is a test-only hook, called at the top of every Reconcile.
	// It is nil-checked so production callers never need to set it.
	OnReconcile func()
}

// SetupWithManager registers the Book controller: the finalizer/metadata-
// refresh/pendingGrab predicate on Book itself, the MediaFile/Download
// watches mirroring Movie/Episode, and a watch on Author (mapAuthor) so an
// owning Author's own metadata refresh (which changes the AuthorName this
// reconciler's Path depends on) re-triggers every Book it owns without
// waiting for an unrelated poll. A standalone Book has no owning Author, so
// mapAuthor simply never enqueues one.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &catalogv1alpha1.MediaFile{}, mediaFileByBookIndexKey,
		func(o client.Object) []string {
			mf, ok := o.(*catalogv1alpha1.MediaFile)
			if !ok || mf.Spec.MediaRef.Kind != commonv1.MediaKindBook {
				return nil
			}
			return []string{mf.Spec.MediaRef.Name}
		}); err != nil {
		return err
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &catalogv1alpha1.Book{}, bookByActiveDownloadIndexKey,
		func(o client.Object) []string {
			bk, ok := o.(*catalogv1alpha1.Book)
			if !ok || bk.Status.ActiveDownloadRef == nil {
				return nil
			}
			return []string{*bk.Status.ActiveDownloadRef}
		}); err != nil {
		return err
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &catalogv1alpha1.Book{}, bookByQualityProfileIndexKey,
		func(o client.Object) []string {
			bk, ok := o.(*catalogv1alpha1.Book)
			if !ok || bk.Spec.QualityProfileRef == nil || *bk.Spec.QualityProfileRef == "" {
				return nil
			}
			return []string{*bk.Spec.QualityProfileRef}
		}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("book").
		For(&catalogv1alpha1.Book{}, builder.WithPredicates(bookPredicate())).
		Watches(&catalogv1alpha1.MediaFile{}, handler.EnqueueRequestsFromMapFunc(r.mapMediaFile), builder.WithPredicates(k8s.GenerationChanged())).
		Watches(&downloadv1alpha1.Download{}, handler.EnqueueRequestsFromMapFunc(r.mapDownload), builder.WithPredicates(downloadPredicate())).
		Watches(&catalogv1alpha1.Author{}, handler.EnqueueRequestsFromMapFunc(r.mapAuthor), builder.WithPredicates(authorMetadataPredicate())).
		Watches(&catalogv1alpha1.QualityProfile{}, handler.EnqueueRequestsFromMapFunc(r.mapQualityProfile), builder.WithPredicates(k8s.GenerationChanged())).
		WithOptions(controller.Options{RecoverPanic: ptr.To(true), ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}

// bookPredicate wakes this controller on a spec change (GenerationChanged),
// on the metadata gateway's own write (StatusFieldChanged scoped to
// status.metadata.refreshedAt) or on the grab worker's status.pendingGrab --
// mirroring movie.moviePredicate exactly.
func bookPredicate() predicate.Predicate {
	return k8s.Or(
		k8s.GenerationChanged(),
		k8s.StatusFieldChanged(func(o client.Object) metav1.Time {
			bk, ok := o.(*catalogv1alpha1.Book)
			if !ok || bk.Status.Metadata == nil {
				return metav1.Time{}
			}
			return bk.Status.Metadata.RefreshedAt
		}),
		k8s.StatusFieldChanged(func(o client.Object) metav1.Time {
			bk, ok := o.(*catalogv1alpha1.Book)
			if !ok || bk.Status.PendingGrab == nil {
				return metav1.Time{}
			}
			return bk.Status.PendingGrab.GrabAt
		}),
	)
}

// downloadPredicate mirrors movie.downloadPredicate exactly.
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
	)
}

// authorMetadataPredicate wakes the Author watch only when the referenced
// Author's own status.metadata.refreshedAt changes -- an Author's spec edit
// (RootFolderRef, Folder, ...) already reaches its owned Books through a
// different route (an operator editing an Author's root folder is expected
// to reconcile eventually via the normal poll; this watch exists
// specifically so a freshly-resolved AuthorName reaches Path promptly,
// mirroring episode.mapQualityProfile's "wake on the thing this
// reconciler's own computation actually reads" scoping).
func authorMetadataPredicate() predicate.Predicate {
	return k8s.StatusFieldChanged(func(o client.Object) metav1.Time {
		a, ok := o.(*catalogv1alpha1.Author)
		if !ok || a.Status.Metadata == nil {
			return metav1.Time{}
		}
		return a.Status.Metadata.RefreshedAt
	})
}

func (r *Reconciler) mapMediaFile(_ context.Context, o client.Object) []reconcile.Request {
	mf, ok := o.(*catalogv1alpha1.MediaFile)
	if !ok || mf.Spec.MediaRef.Kind != commonv1.MediaKindBook {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: mf.Namespace, Name: mf.Spec.MediaRef.Name}}}
}

func (r *Reconciler) mapDownload(ctx context.Context, o client.Object) []reconcile.Request {
	dl, ok := o.(*downloadv1alpha1.Download)
	if !ok {
		return nil
	}
	var books catalogv1alpha1.BookList
	if err := r.List(ctx, &books, client.InNamespace(dl.Namespace), client.MatchingFields{bookByActiveDownloadIndexKey: dl.Name}); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(books.Items))
	for _, bk := range books.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: bk.Namespace, Name: bk.Name}})
	}
	return reqs
}

// mapAuthor is a plain filtered List, NOT an indexed lookup: the author
// package's own Reconciler already registers an IndexField for
// (Book, spec.authorRef) (bookByAuthorRefIndexKey) to list its own owned
// Books, and a second IndexField call for the same (type, field) on one
// manager cache is a hard "indexer conflict" error at startup -- the exact
// tradeoff episode.mapQualityProfile documents and makes for the identical
// reason. Author edits are a cold path (operators edit an Author by hand,
// not a controller), so the O(books in namespace) scan here costs nothing
// that matters.
func (r *Reconciler) mapAuthor(ctx context.Context, o client.Object) []reconcile.Request {
	a, ok := o.(*catalogv1alpha1.Author)
	if !ok {
		return nil
	}
	var books catalogv1alpha1.BookList
	if err := r.List(ctx, &books, client.InNamespace(a.Namespace)); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for _, bk := range books.Items {
		if bk.Spec.AuthorRef == nil || *bk.Spec.AuthorRef != a.Name {
			continue
		}
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: bk.Namespace, Name: bk.Name}})
	}
	return reqs
}

// mapQualityProfile is the reverse direction from an edited QualityProfile
// to every Book directly ranked against it (bookByQualityProfileIndexKey's
// own doc comment records the scope limit: a Book that inherits its profile
// from an owning Author, rather than overriding it directly, is not woken
// by this watch -- it picks the change up on its next reconcile for any
// other reason, such as its own periodic requeue or its Author's own
// metadata refresh). QualityProfile is CLUSTER-scoped while Book is
// namespaced, so this List deliberately carries no client.InNamespace,
// mirroring movie.mapQualityProfile exactly.
func (r *Reconciler) mapQualityProfile(ctx context.Context, o client.Object) []reconcile.Request {
	qp, ok := o.(*catalogv1alpha1.QualityProfile)
	if !ok {
		return nil
	}
	var books catalogv1alpha1.BookList
	if err := r.List(ctx, &books, client.MatchingFields{bookByQualityProfileIndexKey: qp.Name}); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(books.Items))
	for _, bk := range books.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: bk.Namespace, Name: bk.Name}})
	}
	return reqs
}

// Reconcile implements the §8.8 skeleton: get, split on deletion, ensure the
// finalizer WITHOUT an early return, then reconcileNormal.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "book.Reconcile")
	defer span.End()
	if r.OnReconcile != nil {
		r.OnReconcile()
	}
	var bk catalogv1alpha1.Book
	if err := r.Get(ctx, req.NamespacedName, &bk); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if k8s.IsDeleting(&bk) {
		return r.reconcileDelete(ctx, &bk)
	}
	name, err := k8s.FinalizerFor(&bk, r.Scheme)
	if err != nil {
		return ctrl.Result{}, reconcile.TerminalError(err)
	}
	if _, err := k8s.EnsureFinalizer(ctx, r.Client, &bk, name); err != nil {
		return ctrl.Result{}, err
	}
	return r.reconcileNormal(ctx, &bk)
}

func (r *Reconciler) reconcileDelete(ctx context.Context, bk *catalogv1alpha1.Book) (ctrl.Result, error) {
	name, err := k8s.FinalizerFor(bk, r.Scheme)
	if err != nil {
		return ctrl.Result{}, reconcile.TerminalError(err)
	}
	if _, err := k8s.RemoveFinalizer(ctx, r.Client, bk, name); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// reconcileNormal mirrors Movie's structure: metadata staleness decided and
// a MetadataTask published when needed, then root-folder/author context
// resolved and path computed, then the MediaFile/Download rollup folded in.
//
// Unlike Movie, path/phase/file-rollup computation does NOT wait for
// status.metadata to be populated first: BuildFolder(MediaKindBook, ...)
// renders only "{Author Name}" (path.go's own doc comment -- there is no
// book-title segment in a Book's folder, "no separate book folder"), so
// nothing about this Book's own path needs its own metadata, only its
// resolved author name and root folder. Gating on metaReady the way Movie
// gates on its own Title/Year would only delay a Book's phase/file rollup
// for no benefit BookPhase's own enum needs (it has no Pending bucket to
// hold that wait in, unlike MoviePhase).
func (r *Reconciler) reconcileNormal(ctx context.Context, bk *catalogv1alpha1.Book) (ctrl.Result, error) {
	now := time.Now().UTC()
	monitored := ptr.Deref(bk.Spec.Monitored, true)
	conditions := append([]metav1.Condition(nil), bk.Status.Conditions...)

	statusAC := catalogac.BookStatus().WithObservedGeneration(bk.Generation)

	stale := bk.Status.Metadata == nil
	if !stale {
		ttl := metadata.RefreshTTL(commonv1.MediaKindBook, "", bk.Status.Metadata.RefreshedAt.Time)
		stale = now.Sub(bk.Status.Metadata.RefreshedAt.Time) >= ttl
	}
	metaReady := !stale

	if stale {
		envKey := bk.Namespace + "/" + bk.Name
		mediaKey := events.MediaKey(string(commonv1.MediaKindBook), bk.Namespace, bk.Name)
		schemaName, data, err := schema.Encode(schema.MetadataTask{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: bk.Name},
		})
		if err != nil {
			return ctrl.Result{}, err
		}
		env := &events.Envelope{
			ID:     events.MsgIDForObject(string(bk.UID), bk.Generation, "metadata"),
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
				k8s.MarkTrue(bk, &conditions, conditionQueueFull, "QueueFull", "metadata work queue is full")
				statusAC = reassertKnownStatus(statusAC, bk)
				statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)
				if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Book(bk.Name, bk.Namespace).WithStatus(statusAC)); err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{RequeueAfter: time.Minute}, nil
			}
			return ctrl.Result{}, pubErr
		}
		k8s.MarkFalse(bk, &conditions, conditionQueueFull, "Published", "metadata task published")
		k8s.MarkFalse(bk, &conditions, catalogv1alpha1.BookConditionMetadataReady, "Refreshing", "metadata refresh requested")
	} else {
		k8s.MarkTrue(bk, &conditions, catalogv1alpha1.BookConditionMetadataReady, k8s.ReasonReconciled, "metadata is fresh")
	}

	author, rf, problem, err := r.resolveContext(ctx, bk)
	if err != nil {
		return ctrl.Result{}, err
	}
	if problem != "" {
		k8s.MarkFalse(bk, &conditions, k8s.ConditionReady, "Unresolved", "%s", problem)
		statusAC = reassertKnownStatus(statusAC, bk)
		statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)
		if _, perr := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Book(bk.Name, bk.Namespace).WithStatus(statusAC)); perr != nil {
			return ctrl.Result{}, perr
		}
		return ctrl.Result{RequeueAfter: rootFolderRequeueAfter}, nil
	}

	authorName := ""
	if author != nil && author.Status.Metadata != nil {
		authorName = author.Status.Metadata.Name
	}
	eng := naming.NewEngine(naming.Config{
		Dialect:           naming.Dialect(rf.Spec.Naming.Dialect),
		ColonReplacement:  naming.ColonReplacement(rf.Spec.Naming.ColonReplacement),
		MultiEpisodeStyle: naming.MultiEpisodeStyle(rf.Spec.Naming.MultiEpisodeStyle),
		Overrides:         rf.Spec.Naming.Overrides,
	})
	pth, err := Path(rf.Spec.Path, authorName, eng)
	if err != nil {
		return ctrl.Result{}, err
	}
	statusAC = statusAC.WithPath(pth)

	var mfList catalogv1alpha1.MediaFileList
	if err := r.List(ctx, &mfList, client.InNamespace(bk.Namespace), client.MatchingFields{mediaFileByBookIndexKey: bk.Name}); err != nil {
		return ctrl.Result{}, err
	}
	mf := rollup.PickMediaFile(mfList.Items)

	profile, profileProblem, err := r.resolveProfile(ctx, bk, author)
	if err != nil {
		return ctrl.Result{}, err
	}
	if profileProblem != "" {
		logging.FromContext(ctx).Warn("quality profile unresolved; cutoff not evaluated",
			"book", bk.Name, "namespace", bk.Namespace, "problem", profileProblem)
	}
	hasFile, fileRef, fileFormat, cutoffMet := FileState(mf, profile)

	var dl *downloadv1alpha1.Download
	if bk.Status.ActiveDownloadRef != nil {
		var d downloadv1alpha1.Download
		if err := r.Get(ctx, types.NamespacedName{Namespace: bk.Namespace, Name: *bk.Status.ActiveDownloadRef}, &d); err != nil {
			if !apierrors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
		} else {
			dl = &d
		}
	}
	overlayPhase, active := DownloadOverlay(dl)

	phase := Phase(monitored, hasFile, cutoffMet, bk.Status.PendingGrab != nil)
	if overlayPhase != "" {
		phase = overlayPhase
	}

	statusAC = statusAC.
		WithPhase(phase).
		WithHasFile(hasFile).
		WithCutoffMet(cutoffMet)
	if fileRef != nil {
		statusAC = statusAC.WithFileRef(*fileRef)
	}
	if fileFormat != "" {
		statusAC = statusAC.WithFileFormat(fileFormat)
	}
	if active && bk.Status.ActiveDownloadRef != nil {
		statusAC = statusAC.WithActiveDownloadRef(*bk.Status.ActiveDownloadRef)
	}
	// When !active and a ref was set, WithActiveDownloadRef is deliberately
	// not called -- see movie.Reconciler's identical rationale: omitting a
	// field this manager owns releases it under SSA, which is how the ref is
	// cleared on a terminal Download phase.

	if hasFile {
		k8s.MarkTrue(bk, &conditions, catalogv1alpha1.BookConditionHasFile, "HasFile", "backed by MediaFile %s", ptr.Deref(fileRef, ""))
	} else {
		k8s.MarkFalse(bk, &conditions, catalogv1alpha1.BookConditionHasFile, k8s.ReasonPending, "no MediaFile backs this book")
	}
	switch {
	case !hasFile:
		k8s.MarkFalse(bk, &conditions, catalogv1alpha1.BookConditionCutoffMet, k8s.ReasonPending, "no file to rank against the profile cutoff")
	case profile == nil:
		// The cutoff was NOT evaluated -- see movie.Reconciler's identical
		// branch for why this gets a reason of its own rather than reading
		// as a genuine CutoffUnmet.
		k8s.MarkFalse(bk, &conditions, catalogv1alpha1.BookConditionCutoffMet, "ProfileUnresolved", "cutoff not evaluated: %s", profileProblem)
	case cutoffMet:
		k8s.MarkTrue(bk, &conditions, catalogv1alpha1.BookConditionCutoffMet, "CutoffMet", "file meets the profile cutoff")
	default:
		k8s.MarkFalse(bk, &conditions, catalogv1alpha1.BookConditionCutoffMet, "CutoffUnmet", "file does not meet the profile cutoff")
	}

	k8s.MarkReady(bk, &conditions, metaReady, k8s.ReasonReconciled, "phase=%s", phase)
	statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)

	if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Book(bk.Name, bk.Namespace).WithStatus(statusAC)); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// resolveContext resolves the RootFolder a Book stores under, and the
// Author it belongs to when spec.authorRef is set. This is the one place
// this reconciler genuinely branches on which of book_types.go:159-161's two
// shapes bk is:
//
//   - Fanned out (spec.authorRef set): the owning Author is fetched; its own
//     spec.rootFolderRef is the default root folder, overridden by
//     bk.Spec.RootFolderRef when the user set one (BookSpec.RootFolderRef's
//     own doc comment: "overrides the Author's RootFolder").
//   - Standalone (spec.authorRef nil): author is nil, and
//     bk.Spec.RootFolderRef is required -- there is no Author to inherit
//     from, and BookSpec's own schema leaves it optional (a *string) only
//     because it is ALSO the override field for the fanned-out case, so this
//     function is what turns "unset" into a reported problem for a
//     standalone Book rather than a nil-pointer panic three lines later.
//
// Only a non-NotFound API error is fatal; every other outcome degrades to
// (author, nil, problem, nil) so a missing reference cannot wedge the
// reconcile, mirroring movie.resolveProfile's identical contract.
func (r *Reconciler) resolveContext(ctx context.Context, bk *catalogv1alpha1.Book) (author *catalogv1alpha1.Author, rootFolder *catalogv1alpha1.RootFolder, problem string, err error) {
	rootFolderRef := ""
	if bk.Spec.AuthorRef != nil {
		var a catalogv1alpha1.Author
		if getErr := r.Get(ctx, types.NamespacedName{Namespace: bk.Namespace, Name: *bk.Spec.AuthorRef}, &a); getErr != nil {
			if apierrors.IsNotFound(getErr) {
				return nil, nil, fmt.Sprintf("author %q not found", *bk.Spec.AuthorRef), nil
			}
			return nil, nil, "", getErr
		}
		author = &a
		rootFolderRef = a.Spec.RootFolderRef
	}
	if bk.Spec.RootFolderRef != nil && *bk.Spec.RootFolderRef != "" {
		rootFolderRef = *bk.Spec.RootFolderRef
	}
	if rootFolderRef == "" {
		return author, nil, "spec.rootFolderRef is required for a standalone book", nil
	}

	var rf catalogv1alpha1.RootFolder
	if getErr := r.Get(ctx, types.NamespacedName{Namespace: bk.Namespace, Name: rootFolderRef}, &rf); getErr != nil {
		if apierrors.IsNotFound(getErr) {
			return author, nil, fmt.Sprintf("rootFolder %q not found", rootFolderRef), nil
		}
		return author, nil, "", getErr
	}
	return author, &rf, "", nil
}

// resolveProfile fetches the QualityProfile bk is ranked against:
// bk.Spec.QualityProfileRef when set (BookSpec.QualityProfileRef's own doc
// comment: "overrides the Author's QualityProfile"), otherwise
// author.Spec.QualityProfileRef when bk has an owning Author, otherwise
// unresolved -- a standalone Book with no override has nothing to inherit
// from, exactly the same shape resolveContext's RootFolderRef inheritance
// takes. author is the value resolveContext already resolved for this same
// reconcile, threaded through rather than re-fetched.
//
// Mirrors movie.resolveProfile/audiobook.resolveProfile's contract: only a
// non-NotFound API error is fatal, every other outcome degrades to
// (nil, problem, nil) so an unresolved or malformed profile cannot wedge
// the reconcile -- cutoff is simply not evaluated (see the CutoffMet
// condition's ProfileUnresolved branch above), never guessed.
func (r *Reconciler) resolveProfile(ctx context.Context, bk *catalogv1alpha1.Book, author *catalogv1alpha1.Author) (*quality.Profile, string, error) {
	ref := ""
	switch {
	case bk.Spec.QualityProfileRef != nil && *bk.Spec.QualityProfileRef != "":
		ref = *bk.Spec.QualityProfileRef
	case author != nil:
		ref = author.Spec.QualityProfileRef
	}
	if ref == "" {
		return nil, "no qualityProfileRef resolved (standalone book with none set)", nil
	}
	var qp catalogv1alpha1.QualityProfile
	if err := r.Get(ctx, types.NamespacedName{Name: ref}, &qp); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Sprintf("qualityProfile %q not found", ref), nil
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
// ObservedGeneration/Conditions to statusAC, sourced from bk's current
// (pre-reconcile) status. Used on the two early-return paths (QueueFull,
// resolveContext's problem path), for the identical reason
// movie.reassertKnownStatus exists: both are potentially transient (a full
// queue, a briefly-missing RootFolder or Author), and without this a
// healthy Book's Phase/Path/HasFile/FileRef/FileFormat/CutoffMet/
// ActiveDownloadRef would be released (zeroed) the next time either blip
// happens.
func reassertKnownStatus(statusAC *catalogac.BookStatusApplyConfiguration, bk *catalogv1alpha1.Book) *catalogac.BookStatusApplyConfiguration {
	if bk.Status.Phase != "" {
		statusAC = statusAC.WithPhase(bk.Status.Phase)
	}
	if bk.Status.Path != "" {
		statusAC = statusAC.WithPath(bk.Status.Path)
	}
	statusAC = statusAC.WithHasFile(bk.Status.HasFile)
	if bk.Status.FileRef != nil {
		statusAC = statusAC.WithFileRef(*bk.Status.FileRef)
	}
	if bk.Status.FileFormat != "" {
		statusAC = statusAC.WithFileFormat(bk.Status.FileFormat)
	}
	statusAC = statusAC.WithCutoffMet(bk.Status.CutoffMet)
	if bk.Status.ActiveDownloadRef != nil {
		statusAC = statusAC.WithActiveDownloadRef(*bk.Status.ActiveDownloadRef)
	}
	return statusAC
}
