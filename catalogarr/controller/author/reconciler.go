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

package author

import (
	"context"
	"encoding/json"
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
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/version"
)

// conditionQueueFull mirrors movie.MovieConditionQueueFull's name and
// semantics; author_types.go declares no AuthorConditionQueueFull constant
// of its own (unlike Author's Ready/MetadataReady/BooksSynced, which it does
// declare), so this is a local, package-scoped equivalent, the same choice
// series/reconciler.go makes for Series.
const conditionQueueFull = "QueueFull"

// bookByAuthorRefIndexKey indexes Book by the Author that owns it
// (spec.authorRef), so this reconciler can List an Author's own Books
// without scanning the whole namespace. Registered here, not in the book
// package: the book package's own mapAuthor watch handler needs the same
// information but does a plain filtered List instead of sharing this index,
// because a second IndexField call for the same (type, field) on one
// manager cache is a hard "indexer conflict" error at startup (see
// episode/reconciler.go's mapQualityProfile comment for the identical
// tradeoff already made once in this codebase).
const bookByAuthorRefIndexKey = ".spec.authorRef"

// bookSyncRPCBackoff is the RequeueAfter used when the book-listing RPC
// fails, mirroring series.episodeSyncRPCBackoff.
const bookSyncRPCBackoff = 30 * time.Second

// bus is the subset of events.Bus this reconciler actually calls: Publish
// for the metadata-staleness task and Request for the book-listing RPC.
// Narrower than the full events.Requester per the same rationale as
// series.bus.
type bus interface {
	events.Publisher
	Request(ctx context.Context, subject string, in, out any) error
}

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=authors,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=authors/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=authors/finalizers,verbs=update
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=books,verbs=get;list;watch;create
// The Recorder is a k8s.io/client-go/tools/events.EventRecorder, handed in by
// mgr.GetEventRecorder, and it writes events.k8s.io/v1 -- so events.k8s.io is
// the group to grant and the core group is not. See series/reconciler.go's
// identical marker and comment for the occasion this repo learned it.
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconciler reconciles an Author: metadata staleness (publishing a
// MetadataTask when the cache is missing or past its RefreshTTL), path, and
// the works-listing RPC fan-out into owned Book objects. It is the sole
// writer of status.path, status.bookCount, status.bookFileCount and
// status.addOptionsApplied; status.metadata belongs to the metadata gateway
// (field manager k8s.ManagerCatalogarrMetadata) and this reconciler never
// builds an AuthorStatusApplyConfiguration that calls WithMetadata.
//
// Unlike Series, this reconciler writes NOTHING onto the Book objects it
// fans out beyond their initial spec-only Create -- see this package's
// doc.go for why that is deliberate, not merely simpler.
type Reconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder k8sevents.EventRecorder
	Bus      bus

	// OnReconcile is a test-only hook, called at the top of every Reconcile.
	// It is nil-checked so production callers never need to set it.
	OnReconcile func()
}

// SetupWithManager registers the Author controller: the finalizer/
// metadata-refresh predicate on Author itself, and Owns(&Book{}) guarded by
// StatusFieldChanged on HasFile so a Book's own HasFile flip (from the Book
// controller's MediaFile watch) re-triggers this reconciler's Rollup,
// without this reconciler's own Create-only writes to the Books it owns
// looping it (Create events always pass Owns' predicate regardless, so the
// guard here only matters for later updates -- and this reconciler makes
// none). Only fanned-out Books carry an Author owner reference; a standalone
// Book (no spec.authorRef) is never watched here, matching book_types.go's
// "unset makes the book standalone" contract.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &catalogv1alpha1.Book{}, bookByAuthorRefIndexKey,
		func(o client.Object) []string {
			bk, ok := o.(*catalogv1alpha1.Book)
			if !ok || bk.Spec.AuthorRef == nil {
				return nil
			}
			return []string{*bk.Spec.AuthorRef}
		}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("author").
		For(&catalogv1alpha1.Author{}, builder.WithPredicates(authorPredicate())).
		Owns(&catalogv1alpha1.Book{}, builder.WithPredicates(k8s.StatusFieldChanged(func(o client.Object) bool {
			bk, ok := o.(*catalogv1alpha1.Book)
			return ok && bk.Status.HasFile
		}))).
		WithOptions(controller.Options{RecoverPanic: ptr.To(true), ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}

// authorPredicate wakes this controller on a spec change (GenerationChanged)
// or on the metadata gateway's own write (StatusFieldChanged scoped to
// status.metadata.refreshedAt) -- the same self-loop-avoidance shape as the
// Movie/Series predicates.
func authorPredicate() predicate.Predicate {
	return k8s.Or(
		k8s.GenerationChanged(),
		// An annotation-only change bumps no generation.
		k8s.DeadLetteredAnnotationChanged(),
		k8s.StatusFieldChanged(func(o client.Object) metav1.Time {
			a, ok := o.(*catalogv1alpha1.Author)
			if !ok || a.Status.Metadata == nil {
				return metav1.Time{}
			}
			return a.Status.Metadata.RefreshedAt
		}),
	)
}

// Reconcile implements the §8.8 skeleton: get, split on deletion, ensure the
// finalizer WITHOUT an early return, then reconcileNormal.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "author.Reconcile")
	defer span.End()
	if r.OnReconcile != nil {
		r.OnReconcile()
	}
	var a catalogv1alpha1.Author
	if err := r.Get(ctx, req.NamespacedName, &a); err != nil {
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

// reconcileDelete removes the finalizer. Owned Books are garbage collected
// by the apiserver via their controller reference; a standalone Book was
// never owned by this Author in the first place, so nothing else needs
// cleanup here.
func (r *Reconciler) reconcileDelete(ctx context.Context, a *catalogv1alpha1.Author) (ctrl.Result, error) {
	name, err := k8s.FinalizerFor(a, r.Scheme)
	if err != nil {
		return ctrl.Result{}, reconcile.TerminalError(err)
	}
	if _, err := k8s.RemoveFinalizer(ctx, r.Client, a, name); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// reconcileNormal mirrors the Series reconciler's structure (addOptions
// recorded on every pass, metadata staleness decided and a MetadataTask
// published when needed, path computed once metadata is cached), then adds
// the works-listing RPC fan-out as the last step: an RPC failure surfaces on
// BooksSynced/Ready but never blocks the rest of this same status patch from
// landing.
func (r *Reconciler) reconcileNormal(ctx context.Context, a *catalogv1alpha1.Author) (ctrl.Result, error) {
	now := time.Now().UTC()
	conditions := append([]metav1.Condition(nil), a.Status.Conditions...)
	// The DLQ projector's clustarr.io/dead-lettered annotation becomes the
	// DeadLettered condition here, on the one slice every status apply below
	// declares -- early returns included -- so no apply releases it.
	k8s.MarkDeadLettered(a, &conditions)

	statusAC := catalogac.AuthorStatus().WithObservedGeneration(a.Generation)
	// Sent on every reconcile once the decision point is reached, not just
	// the first -- see movie.Reconciler's reconcileNormal for why (SSA
	// releases a field a manager stops sending).
	statusAC = statusAC.WithAddOptionsApplied(true)

	stale := a.Status.Metadata == nil
	if !stale {
		ttl := metadata.RefreshTTL(commonv1.MediaKindAuthor, "", a.Status.Metadata.RefreshedAt.Time)
		stale = now.Sub(a.Status.Metadata.RefreshedAt.Time) >= ttl
	}
	metaReady := !stale

	if stale {
		// The envelope key is the <namespace>/<name> routing key every
		// worker parses to recover the namespace; the media key is the
		// subject token -- see movie/series' identical comment.
		envKey := a.Namespace + "/" + a.Name
		mediaKey := events.MediaKey(string(commonv1.MediaKindAuthor), a.Namespace, a.Name)
		schemaName, data, err := schema.Encode(schema.MetadataTask{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindAuthor, Name: a.Name},
		})
		if err != nil {
			return ctrl.Result{}, err
		}
		env := &events.Envelope{
			ID:     events.MsgIDForObject(string(a.UID), a.Generation, "metadata"),
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
				k8s.MarkTrue(a, &conditions, conditionQueueFull, "QueueFull", "metadata work queue is full")
				statusAC = reassertKnownStatus(statusAC, a)
				statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)
				if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Author(a.Name, a.Namespace).WithStatus(statusAC)); err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{RequeueAfter: time.Minute}, nil
			}
			return ctrl.Result{}, pubErr
		}
		k8s.MarkFalse(a, &conditions, conditionQueueFull, "Published", "metadata task published")
		k8s.MarkFalse(a, &conditions, catalogv1alpha1.AuthorConditionMetadataReady, "Refreshing", "metadata refresh requested")
	} else {
		k8s.MarkTrue(a, &conditions, catalogv1alpha1.AuthorConditionMetadataReady, k8s.ReasonReconciled, "metadata is fresh")
	}

	if a.Status.Metadata != nil {
		var rf catalogv1alpha1.RootFolder
		if err := r.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: a.Spec.RootFolderRef}, &rf); err != nil {
			if apierrors.IsNotFound(err) {
				k8s.MarkFalse(a, &conditions, k8s.ConditionReady, "RootFolderNotFound", "rootFolder %q not found", a.Spec.RootFolderRef)
				statusAC = reassertKnownStatus(statusAC, a)
				statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)
				if _, perr := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Author(a.Name, a.Namespace).WithStatus(statusAC)); perr != nil {
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
		nctx := naming.Context{Kind: commonv1.MediaKindAuthor, AuthorName: a.Status.Metadata.Name}
		pth, err := Path(rf.Spec.Path, a.Spec.Folder, eng, nctx)
		if err != nil {
			return ctrl.Result{}, err
		}
		statusAC = statusAC.WithPath(pth)
	}

	// Works-listing RPC fan-out (last step, mirroring series' own ordering):
	// a failure here surfaces on BooksSynced/Ready without blocking the
	// patch above.
	booksSynced, books, syncErr := r.syncBooks(ctx, a, now)
	if syncErr != nil {
		k8s.MarkFalse(a, &conditions, catalogv1alpha1.AuthorConditionBooksSynced, "RPCError", "book listing RPC failed: %s", syncErr.Error())
	} else {
		k8s.MarkTrue(a, &conditions, catalogv1alpha1.AuthorConditionBooksSynced, k8s.ReasonReconciled, "books synced")
	}

	bookCount, bookFileCount := Rollup(books)
	statusAC = statusAC.WithBookCount(bookCount).WithBookFileCount(bookFileCount)

	k8s.MarkReady(a, &conditions, metaReady && booksSynced, k8s.ReasonReconciled, "metadataReady=%t booksSynced=%t", metaReady, booksSynced)
	statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)

	if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Author(a.Name, a.Namespace).WithStatus(statusAC)); err != nil {
		return ctrl.Result{}, err
	}

	if syncErr != nil {
		return ctrl.Result{RequeueAfter: bookSyncRPCBackoff}, nil
	}
	return ctrl.Result{}, nil
}

// reassertKnownStatus re-adds every field this manager owns besides
// ObservedGeneration/AddOptionsApplied/Conditions to statusAC, sourced from
// a's current (pre-reconcile) status. Used on the two early-return paths
// (QueueFull, RootFolderNotFound), for the identical reason
// movie.reassertKnownStatus and series.reassertKnownStatus exist: both are
// transient failures, and without this a healthy Author's
// Path/BookCount/BookFileCount would be released (zeroed) the next time
// either blip happens.
func reassertKnownStatus(statusAC *catalogac.AuthorStatusApplyConfiguration, a *catalogv1alpha1.Author) *catalogac.AuthorStatusApplyConfiguration {
	if a.Status.Path != "" {
		statusAC = statusAC.WithPath(a.Status.Path)
	}
	statusAC = statusAC.WithBookCount(a.Status.BookCount)
	statusAC = statusAC.WithBookFileCount(a.Status.BookFileCount)
	return statusAC
}

// syncBooks lists a's currently owned Books, requests its works list from
// the metadata gateway, ensures each desired Book exists (spec fields only,
// Create-or-leave-alone -- see ensureBook), and returns the full owned Book
// list for Rollup. Self-correcting the same way series.syncEpisodes is: a
// newly created Book's own Create event passes Owns()'s StatusFieldChanged
// predicate (Creates always pass), so a Rollup based on the pre-fan-out list
// here is completed by the very next reconcile it triggers.
func (r *Reconciler) syncBooks(ctx context.Context, a *catalogv1alpha1.Author, now time.Time) (synced bool, existing []catalogv1alpha1.Book, err error) {
	var bookList catalogv1alpha1.BookList
	if err := r.List(ctx, &bookList, client.InNamespace(a.Namespace), client.MatchingFields{bookByAuthorRefIndexKey: a.Name}); err != nil {
		return false, nil, err
	}
	existingNames := make(map[string]bool, len(bookList.Items))
	for _, bk := range bookList.Items {
		existingNames[bk.Name] = true
	}

	req := schema.MetadataRequest{
		Kind: commonv1.MediaKindBook,
		IDs:  map[string]string{metadata.KeyOpenLibraryAuthor: a.Spec.OpenLibraryID},
	}
	rpcCtx, cancel := context.WithTimeout(ctx, bookSyncRPCBackoff)
	defer cancel()

	var resp schema.MetadataResponse
	if rpcErr := r.Bus.Request(rpcCtx, events.RPCMetadataLookup, req, &resp); rpcErr != nil {
		return false, bookList.Items, rpcErr
	}
	if resp.Error != "" {
		return false, bookList.Items, fmt.Errorf("metadata gateway: %s", resp.Error)
	}

	fetched := make([]metadata.Book, 0, len(resp.Results))
	for _, raw := range resp.Results {
		var b metadata.Book
		if err := json.Unmarshal(raw, &b); err != nil {
			return false, bookList.Items, fmt.Errorf("decode book: %w", err)
		}
		fetched = append(fetched, b)
	}

	desired := DesiredBooks(a, a.Status.AddOptionsApplied, existingNames, fetched, now)
	for _, d := range desired {
		if err := r.ensureBook(ctx, a, d); err != nil {
			return false, bookList.Items, err
		}
	}
	return true, bookList.Items, nil
}

// ensureBook gets or creates the Book named d.Name. It NEVER patches an
// already-existing Book's spec or status -- see this package's doc.go for
// why: unlike Series/Episode, Book independently refreshes its own
// status.metadata, so this reconciler has nothing to push after Create.
func (r *Reconciler) ensureBook(ctx context.Context, a *catalogv1alpha1.Author, d DesiredBook) error {
	var bk catalogv1alpha1.Book
	key := types.NamespacedName{Namespace: a.Namespace, Name: d.Name}
	err := r.Get(ctx, key, &bk)
	switch {
	case apierrors.IsNotFound(err):
		bk = catalogv1alpha1.Book{
			ObjectMeta: metav1.ObjectMeta{Name: d.Name, Namespace: a.Namespace},
			Spec: catalogv1alpha1.BookSpec{
				AuthorRef: ptr.To(a.Name),
				WorkID:    d.WorkID,
			},
		}
		if d.Monitored != nil {
			bk.Spec.Monitored = d.Monitored
		}
		if err := k8s.SetControllerReference(a, &bk, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, &bk); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
		return nil
	case err != nil:
		return err
	default:
		return nil
	}
}
