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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/folderscan"
	"github.com/mediactl/clustarr/app/catalog/controller/itempass"
	"github.com/mediactl/clustarr/app/catalog/controller/itemstatus"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/app/catalog/metadatainputs"
	"github.com/mediactl/clustarr/app/remediation/childsync"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// conditionQueueFull mirrors movie.MovieConditionQueueFull's name and
// semantics; author_types.go declares no AuthorConditionQueueFull constant
// of its own (unlike Author's Ready/MetadataReady/BooksSynced, which it does
// declare), so this is a local, package-scoped equivalent, the same choice
// series/reconciler.go makes for Series.
const conditionQueueFull = "QueueFull"

// bookByAuthorRefIndexKey indexes Book by the Author that owns it
// (spec.authorRef), so the pass can List an Author's own Books without
// scanning the whole namespace. The book package's own mapAuthor watch
// handler does a plain filtered List instead of sharing this index, because
// a second IndexField call for the same (type, field) on one manager cache
// is a hard "indexer conflict" error at startup.
const bookByAuthorRefIndexKey = ".spec.authorRef"

// BookByAuthorRefIndex is the field index of Books by spec.authorRef, for
// other readers of the cache.
const BookByAuthorRefIndex = bookByAuthorRefIndexKey

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
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=audiobooks,verbs=get;list;watch
// The Recorder is a k8s.io/client-go/tools/events.EventRecorder, handed in by
// mgr.GetEventRecorder, and it writes events.k8s.io/v1 -- so events.k8s.io is
// the group to grant and the core group is not. See series/reconciler.go's
// identical marker and comment for the occasion this repo learned it.
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconciler is the Author's item path on the remediation loop (2026-11-10
// design D2, §5.1): metadata staleness (publishing a MetadataTask, admitted
// by the dispatch ledger, when the cache is missing or past its
// RefreshTTL), path, the works-listing RPC fan-out into owned Book objects
// behind the child-sync memo, the container rollup (rollup.Container) and
// the folder scan a new author's first books ask for. It is the sole
// writer, under catalog, of status.path, status.bookCount,
// status.bookFileCount, status.addOptionsApplied, the ContainerRollup
// fields and the conditions; status.metadata and status.artwork belong to
// the loop's metadata stage (catalog-metadata), status.nfo to its nfo
// stage (catalog-nfo), and this reconciler never builds an
// AuthorStatusApplyConfiguration that calls WithMetadata.
//
// Unlike Series, this reconciler writes NOTHING onto the Book objects it
// fans out beyond their initial spec-only Create -- see this package's
// doc.go for why that is deliberate, not merely simpler.
type Reconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder k8sevents.EventRecorder
	Bus      bus

	// Folders reads an author's folder when its first books ask for a
	// scan of it (folderscan.Pass, smoke finding O8): the remediation
	// loop's /data executor in the manager. Nil asks for no scan.
	Folders folderscan.Folders

	// OnReconcile is a test-only hook, called at the top of every Reconcile.
	// It is nil-checked so production callers never need to set it.
	OnReconcile func()

	// childSync skips a fan-out whose inputs have not moved since the last
	// complete one in this process (design D2's memo).
	childSync childsync.Memo
}

// RegisterIndexes registers the Book-by-spec.authorRef index under its
// existing name. The remediation loop registers it once
// (remediation.RegisterIndexes) since the Author became an item key.
func RegisterIndexes(ctx context.Context, idx client.FieldIndexer) error {
	return idx.IndexField(ctx, &catalogv1alpha1.Book{}, bookByAuthorRefIndexKey,
		func(o client.Object) []string {
			bk, ok := o.(*catalogv1alpha1.Book)
			if !ok || bk.Spec.AuthorRef == nil {
				return nil
			}
			return []string{*bk.Spec.AuthorRef}
		})
}

// Watches is the Author's item path on the remediation loop (loop spec
// §3.12): the Author itself (a spec change, the metadata gateway's write,
// the DLQ annotation), its Books by spec.authorRef when a rollup input moves
// (a file, a phase, a release date, a monitor flag -- never the Book's own
// metadata document, which would loop the author on every book refresh),
// its Audiobooks by spec.authorRef when their metadata moves (the author's
// artist.nfo lists them, design §8.1), and a RootFolder's spec edit (its
// naming, its writeNFO, its defaults) for the authors under it. Only
// fanned-out Books carry spec.authorRef; a standalone Book never wakes an
// Author, matching book_types.go's "unset makes the book standalone"
// contract. The controller named "author" is gone; the recorder keeps the
// name.
func (r *Reconciler) Watches() []rollup.Watch {
	return []rollup.Watch{
		{Object: &catalogv1alpha1.Author{}, Map: rollup.Self, Predicates: []predicate.Predicate{authorPredicate()}},
		{Object: &catalogv1alpha1.Book{}, Map: mapBookToAuthor, Predicates: []predicate.Predicate{bookRollupChanged()}},
		{Object: &catalogv1alpha1.Audiobook{}, Map: mapAudiobookToAuthor, Predicates: []predicate.Predicate{audiobookChanged()}},
		{Object: &catalogv1alpha1.RootFolder{}, Map: r.mapRootFolder, Predicates: []predicate.Predicate{k8s.GenerationChanged()}},
	}
}

var _ rollup.Item = (*Reconciler)(nil)

// mapBookToAuthor maps a Book to the Author its spec.authorRef names.
func mapBookToAuthor(_ context.Context, o client.Object) []reconcile.Request {
	bk, ok := o.(*catalogv1alpha1.Book)
	if !ok || ptr.Deref(bk.Spec.AuthorRef, "") == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: bk.Namespace, Name: *bk.Spec.AuthorRef}}}
}

// mapAudiobookToAuthor maps an Audiobook to the Author its spec.authorRef
// names.
func mapAudiobookToAuthor(_ context.Context, o client.Object) []reconcile.Request {
	ab, ok := o.(*catalogv1alpha1.Audiobook)
	if !ok || ptr.Deref(ab.Spec.AuthorRef, "") == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: ab.Namespace, Name: *ab.Spec.AuthorRef}}}
}

// mapRootFolder enqueues the RootFolder's authors. It lists the namespace's
// authors per event, which stays cheap because RootFolders are few and
// change rarely: at startup it runs once per RootFolder, not per author
// (CLAUDE.md's map-function gotcha).
func (r *Reconciler) mapRootFolder(ctx context.Context, o client.Object) []reconcile.Request {
	var list catalogv1alpha1.AuthorList
	if err := r.List(ctx, &list, client.InNamespace(o.GetNamespace()), client.UnsafeDisableDeepCopy); err != nil {
		logging.FromContext(ctx).Warn("author: list for a RootFolder change failed", "rootFolder", o.GetName(), "error", err)
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		a := &list.Items[i]
		if a.Spec.RootFolderRef == o.GetName() {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: a.Namespace, Name: a.Name}})
		}
	}
	return reqs
}

// bookRollupKey is what of a Book the Author's rollup reads: its monitor
// flag and phase, its file, and its release date (the container's next and
// previous release dates).
type bookRollupKey struct {
	monitored   bool
	phase       commonv1.LeafPhase
	hasFile     bool
	releaseDate int64
}

func bookRollupKeyOf(o client.Object) bookRollupKey {
	bk, ok := o.(*catalogv1alpha1.Book)
	if !ok {
		return bookRollupKey{}
	}
	k := bookRollupKey{monitored: ptr.Deref(bk.Spec.Monitored, true), phase: bk.Status.Phase, hasFile: bk.Status.HasFile}
	if bk.Status.Metadata != nil && bk.Status.Metadata.ReleaseDate != nil {
		k.releaseDate = bk.Status.Metadata.ReleaseDate.Unix()
	}
	return k
}

// bookRollupChanged wakes the Author when an owned Book's rollup inputs
// change, on its creation and its deletion, and not on the Book's other
// status writes (its downloads, its search bookkeeping), which would loop
// it.
func bookRollupChanged() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			if e.ObjectOld == nil || e.ObjectNew == nil {
				return false
			}
			return bookRollupKeyOf(e.ObjectOld) != bookRollupKeyOf(e.ObjectNew)
		},
	}
}

// audiobookChanged wakes the Author when an Audiobook of theirs is created,
// deleted, edited (its authorRef) or refreshed (its title and release date
// are in the author's artist.nfo).
func audiobookChanged() predicate.Predicate {
	return k8s.Or(
		k8s.GenerationChanged(),
		k8s.StatusFieldChanged(func(o client.Object) metav1.Time {
			ab, ok := o.(*catalogv1alpha1.Audiobook)
			if !ok || ab.Status.Metadata == nil {
				return metav1.Time{}
			}
			return ab.Status.Metadata.RefreshedAt
		}),
	)
}

// authorPredicate wakes this item on a spec change (GenerationChanged) or
// on the metadata gateway's own write (StatusFieldChanged scoped to
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

// ReconcileItem is the Author's item path on the remediation loop; it keeps
// the §8.8 skeleton: get, split on deletion, ensure the finalizer WITHOUT
// an early return, then reconcileNormal.
func (r *Reconciler) ReconcileItem(ctx context.Context, nn types.NamespacedName) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "author.Reconcile")
	defer span.End()
	if r.OnReconcile != nil {
		r.OnReconcile()
	}
	var a catalogv1alpha1.Author
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

// reconcileDelete removes the finalizer. Owned Books are garbage collected
// by the apiserver via their controller reference; a standalone Book was
// never owned by this Author in the first place, and an Author owns no
// grabs, so nothing waits on the downloads stage.
func (r *Reconciler) reconcileDelete(ctx context.Context, a *catalogv1alpha1.Author) (ctrl.Result, error) {
	name, err := k8s.FinalizerFor(a, r.Scheme)
	if err != nil {
		return ctrl.Result{}, reconcile.TerminalError(err)
	}
	// The deleted event goes out before the finalizer comes off, so a failed
	// removal re-announces it under the same envelope id rather than losing
	// it; an object that never held the finalizer never reaches here.
	if controllerutil.ContainsFinalizer(a, name) {
		r.publishItem(ctx, a, events.ActionDeleted, time.Now().UTC())
	}
	if _, err := k8s.RemoveFinalizer(ctx, r.Client, a, name); err != nil {
		return ctrl.Result{}, err
	}
	r.childSync.Forget(a.UID)
	return ctrl.Result{}, nil
}

// reconcileNormal mirrors the Series reconciler's structure (metadata
// staleness decided and a MetadataTask published when needed, path computed
// once metadata is cached, addOptionsApplied recorded from the first
// complete fan-out on), then adds
// the works-listing RPC fan-out, the container rollup and the folder scan
// as the last steps: an RPC failure surfaces on BooksSynced/Ready/Phase but
// never blocks the rest of this same status apply from landing.
func (r *Reconciler) reconcileNormal(ctx context.Context, a *catalogv1alpha1.Author) (ctrl.Result, error) {
	now := time.Now().UTC()
	monitored := ptr.Deref(a.Spec.Monitored, true)
	conditions := append([]metav1.Condition(nil), a.Status.Conditions...)
	// The DLQ projector's clustarr.io/dead-lettered annotation becomes the
	// DeadLettered condition here, on the one slice every status apply below
	// declares -- early returns included -- so no apply releases it.
	k8s.MarkDeadLettered(a, &conditions)

	// Announced before any apply, because the first apply records
	// observedGeneration and so consumes the edge (rollup.ItemAction, the
	// Comic's rule). Read off observedGeneration, never addOptionsApplied:
	// that flag stays false through every MetadataPending pass (below), and
	// would announce the Author added on each of them.
	if action := rollup.ItemAction(a.Generation, a.Status.ObservedGeneration, a.Status.ObservedGeneration != 0); action != "" {
		r.publishItem(ctx, a, action, now)
	}

	statusAC := catalogac.AuthorStatus().WithObservedGeneration(a.Generation)
	// The stored value until the fan-out below has run: the two early
	// returns (QueueFull, RootFolderNotFound) send it unchanged, and a pass
	// without metadata -- whose fan-out waits for the document -- flips
	// nothing, since the first fan-out reads it to apply addOptions.monitor
	// (DesiredBooks' `!addOptionsApplied` arm). Until 2026-11-10 every pass
	// sent true, so once the fan-out waited for the metadata the first real
	// fan-out ran with the flag already flipped and every new Author's
	// Books were created under spec.monitorNewItems (all by default)
	// instead of InitialBookMonitored -- every book monitored under none,
	// future, missing or existing. Sent on every apply, because server-side
	// apply releases a field a manager stops sending (movie.Reconciler's
	// note on the same flag).
	statusAC = statusAC.WithAddOptionsApplied(a.Status.AddOptionsApplied)

	stale := a.Status.Metadata == nil
	if !stale {
		ttl := metadata.RefreshTTL(commonv1.MediaKindAuthor, "", a.Status.Metadata.RefreshedAt.Time)
		stale = now.Sub(a.Status.Metadata.RefreshedAt.Time) >= ttl
	}
	metaReady := !stale
	// A recent document the gateway wrote before it learned a field is
	// refreshed once, at once, and the item stays ready meanwhile: every
	// item in an upgraded library would otherwise drop to Pending together
	// (Movie's rule; the 2026-11-10 design D8 gives every kind a
	// schemaVersion).
	outdated := !stale && a.Status.Metadata.SchemaVersion < metadata.SchemaVersion

	if stale || outdated {
		// The task carries what the refresh asks for -- the item and its
		// inputs, answered in clustarr-item-metadata (ADR-0019 §7.1) -- under
		// Msg-Id metadata/<uid>/<inputsHash>.
		subject, env, err := metadatainputs.Task(a, metadatainputs.For(a), events.PriorityNormal, now)
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
					if rollup.Transitioned(a.Status.Conditions, conditionQueueFull, metav1.ConditionTrue, "QueueFull") {
						r.warn(a, "QueueFull", "metadata work queue is full; retrying the refresh in a minute")
					}
					k8s.MarkTrue(a, &conditions, conditionQueueFull, "QueueFull", "metadata work queue is full")
					statusAC = reassertKnownStatus(statusAC, a)
					statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)
					if conflicted, err := itemstatus.Apply(ctx, r.Client, a, catalogac.Author(a.Name, a.Namespace).WithStatus(statusAC)); err != nil || conflicted {
						return itemstatus.Requeue(conflicted), err
					}
					return ctrl.Result{RequeueAfter: time.Minute}, nil
				}
				return ctrl.Result{}, pubErr
			}
			itempass.Published(itempass.From(ctx), metadatainputs.Durable, seq)
			k8s.MarkFalse(a, &conditions, conditionQueueFull, "Published", "metadata task published")
		}
		if outdated {
			k8s.MarkTrue(a, &conditions, catalogv1alpha1.AuthorConditionMetadataReady, "Upgrading",
				"metadata schema %d is older than %d; refreshing once", a.Status.Metadata.SchemaVersion, metadata.SchemaVersion)
		} else {
			k8s.MarkFalse(a, &conditions, catalogv1alpha1.AuthorConditionMetadataReady, "Refreshing", "metadata refresh requested")
		}
	} else {
		k8s.MarkTrue(a, &conditions, catalogv1alpha1.AuthorConditionMetadataReady, k8s.ReasonReconciled, "metadata is fresh")
	}

	// The author's RootFolder and folder, once its metadata is cached: what
	// the folder scan its first books ask for needs (folderscan.Pass).
	var (
		root   *catalogv1alpha1.RootFolder
		folder string
	)
	if a.Status.Metadata != nil {
		var rf catalogv1alpha1.RootFolder
		if err := r.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: a.Spec.RootFolderRef}, &rf); err != nil {
			if apierrors.IsNotFound(err) {
				if rollup.Transitioned(a.Status.Conditions, k8s.ConditionReady, metav1.ConditionFalse, "RootFolderNotFound") {
					r.warn(a, "RootFolderNotFound", "rootFolder %q not found", a.Spec.RootFolderRef)
				}
				k8s.MarkFalse(a, &conditions, k8s.ConditionReady, "RootFolderNotFound", "rootFolder %q not found", a.Spec.RootFolderRef)
				statusAC = reassertKnownStatus(statusAC, a)
				statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)
				if conflicted, perr := itemstatus.Apply(ctx, r.Client, a, catalogac.Author(a.Name, a.Namespace).WithStatus(statusAC)); perr != nil || conflicted {
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
		nctx := naming.Context{Kind: commonv1.MediaKindAuthor, AuthorName: a.Status.Metadata.Name}
		pth, err := Path(rf.Spec.Path, a.Spec.Folder, eng, nctx)
		if err != nil {
			return ctrl.Result{}, err
		}
		statusAC = statusAC.WithPath(pth)
		root, folder = &rf, pth
	}

	// Works-listing RPC fan-out (mirroring series' own ordering), behind the
	// child-sync memo: a failure here surfaces on BooksSynced/Ready without
	// blocking the apply.
	// The fan-out reads the object's PRE-reconcile status.addOptionsApplied
	// (DesiredBooks): the add-time monitor mode until one complete fan-out
	// has applied it.
	fan, syncErr := r.syncBooks(ctx, a, now)
	// Applied once (the Comic's rule): true from the first complete fan-out
	// on, and sent on every apply after that. A first pass whose fan-out
	// failed, or waits for the metadata, leaves it false, so the next one
	// still applies the add-time mode to the Books it has yet to create;
	// a pass the memo skips reads synced because a complete fan-out of this
	// process stands behind it, so the flip it sends is owed, a cache behind
	// the flip's apply or a conflicted apply included (syncKey).
	statusAC = statusAC.WithAddOptionsApplied(a.Status.AddOptionsApplied || fan.synced)
	switch {
	case syncErr != nil:
		reason := events.ClassifyRequestError(syncErr).Reason()
		if rollup.Transitioned(a.Status.Conditions, catalogv1alpha1.AuthorConditionBooksSynced, metav1.ConditionFalse, reason) {
			r.warn(a, "BookSyncFailed", "book listing RPC failed: %s", syncErr.Error())
		}
		k8s.MarkFalse(a, &conditions, catalogv1alpha1.AuthorConditionBooksSynced, reason, "book listing RPC failed: %s", syncErr.Error())
	case !fan.synced:
		// No metadata yet: the fan-out waits for it (syncBooks), and the
		// gateway's write wakes this item.
		k8s.MarkFalse(a, &conditions, catalogv1alpha1.AuthorConditionBooksSynced, "MetadataPending", "books are listed once the author's metadata is cached")
	default:
		k8s.MarkTrue(a, &conditions, catalogv1alpha1.AuthorConditionBooksSynced, k8s.ReasonReconciled, "books synced")
	}
	if fan.created > 0 {
		r.normal(a, "BooksAdded", "created %d Book(s)", fan.created)
	}

	// The rollups are taken over the POST-fan-out Books: what this very
	// reconcile created, not the list read before the RPC (series.Rollup's
	// rule).
	bookCount, bookFileCount := Rollup(fan.books)
	statusAC = statusAC.WithBookCount(bookCount).WithBookFileCount(bookFileCount)
	// The container rollup (2026-11-10 design §4.1, §5.2): Pending until
	// the metadata is fetched and the books listed (the Series' rule),
	// Unmonitored, else Ready; the counts and the release dates from the
	// books' phases.
	roll := rollup.Container(monitored, metaReady && fan.synced, Children(fan.books), now)
	statusAC = withContainerRollup(statusAC, roll)

	// Readarr's RefreshAuthor, then RescanAuthor (smoke finding O8, design
	// D2): a scan attributes a book file only to a Book that exists, so the
	// files of an author a scan (or Add New, or an import list) created
	// before its works were listed waited for the next scan. The pass that
	// first lists them owes the author's folder one scan, recorded in the
	// FolderScanned condition; an author whose status already held its
	// books owes none.
	folderScan := folderscan.Pass(ctx, r.Client, r.Folders, folderscan.Request{
		Item: a, Root: root, Path: folder,
		HadChildren: a.Status.BookCount > 0, HasChildren: len(fan.books) > 0, Children: "books",
		FieldManager: k8s.ManagerCatalog, Now: now,
	}, &conditions)
	if folderScan.Scan != "" {
		r.normal(a, "FolderScanRequested", "LibraryScan %s scans the author's folder for the files its first books awaited", folderScan.Scan)
	}

	k8s.MarkReady(a, &conditions, metaReady && fan.synced, k8s.ReasonReconciled, "metadataReady=%t booksSynced=%t phase=%s", metaReady, fan.synced, roll.Phase)
	statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)

	if conflicted, err := itemstatus.Apply(ctx, r.Client, a, catalogac.Author(a.Name, a.Namespace).WithStatus(statusAC)); err != nil || conflicted {
		return itemstatus.Requeue(conflicted), err
	}
	if a.Status.Phase != "" && a.Status.Phase != roll.Phase {
		r.normal(a, string(roll.Phase), "phase %s -> %s", a.Status.Phase, roll.Phase)
	}

	if syncErr != nil {
		return ctrl.Result{RequeueAfter: soonest(bookSyncRPCBackoff, folderScan.Retry)}, nil
	}
	// nextReleaseDate moves when a book is published, and nothing else
	// wakes this item then, so come back just after it.
	if roll.NextReleaseDate != nil {
		return ctrl.Result{RequeueAfter: soonest(roll.NextReleaseDate.Sub(now)+time.Second, folderScan.Retry)}, nil
	}
	return ctrl.Result{RequeueAfter: folderScan.Retry}, nil
}

// soonest is the earlier of two requeue delays, zero meaning none.
func soonest(a, b time.Duration) time.Duration {
	if a <= 0 {
		return b
	}
	if b <= 0 {
		return a
	}
	return min(a, b)
}

// withContainerRollup renders the ContainerRollup fields, the one place
// they become apply configurations, shared by the happy path and
// reassertKnownStatus so both declare the same leaves.
func withContainerRollup(ac *catalogac.AuthorStatusApplyConfiguration, roll catalogv1alpha1.ContainerRollup) *catalogac.AuthorStatusApplyConfiguration {
	if roll.Phase != "" {
		ac = ac.WithPhase(roll.Phase)
	}
	ac = ac.WithMissingChildCount(roll.MissingChildCount).
		WithDownloadingChildCount(roll.DownloadingChildCount).
		WithCutoffUnmetChildCount(roll.CutoffUnmetChildCount)
	if roll.NextReleaseDate != nil {
		ac = ac.WithNextReleaseDate(*roll.NextReleaseDate)
	}
	if roll.PreviousReleaseDate != nil {
		ac = ac.WithPreviousReleaseDate(*roll.PreviousReleaseDate)
	}
	return ac
}

// reassertKnownStatus re-adds every field this manager owns besides
// ObservedGeneration/AddOptionsApplied/Conditions to statusAC, sourced from
// a's current (pre-reconcile) status. Used on the two early-return paths
// (QueueFull, RootFolderNotFound), for the identical reason
// movie.reassertKnownStatus and series.reassertKnownStatus exist: both are
// transient failures, and without this a healthy Author's
// Path/BookCount/BookFileCount and container rollup would be released
// (zeroed) the next time either blip happens.
func reassertKnownStatus(statusAC *catalogac.AuthorStatusApplyConfiguration, a *catalogv1alpha1.Author) *catalogac.AuthorStatusApplyConfiguration {
	if a.Status.Path != "" {
		statusAC = statusAC.WithPath(a.Status.Path)
	}
	statusAC = statusAC.WithBookCount(a.Status.BookCount)
	statusAC = statusAC.WithBookFileCount(a.Status.BookFileCount)
	return withContainerRollup(statusAC, a.Status.ContainerRollup)
}

// fanOut is what syncBooks reports: whether the fan-out completed, how many
// Books it created, and the Author's Books as they stand AFTER it.
type fanOut struct {
	synced  bool
	created int
	books   []catalogv1alpha1.Book
}

// syncKey is the child-sync memo's key for a: its spec generation and its
// metadata refresh. status.addOptionsApplied is no input, though
// DesiredBooks reads it: the memo records only a complete fan-out, the
// first of which applies the add-time mode and flips the flag in the same
// pass (reconcileNormal sends stored || fan.synced), so the key carries
// no flag (childsync.Key, which carried one until 2026-11-10) whatever the
// pass read -- a pass reading false after that fan-out
// (a Book's own Create wakes the Author before the flip's apply reaches
// the cache; or the apply conflicted) skips the fetch, which would create
// nothing, and sends the flip again (2026-11-10: keyed on the read value,
// TestAuthorItemPath's fan-out fetched twice). An author without metadata
// has no key: its fan-out waits for the document (syncBooks).
func syncKey(a *catalogv1alpha1.Author) (childsync.Key, bool) {
	if a.Status.Metadata == nil {
		return childsync.Key{}, false
	}
	return childsync.KeyAt(a.Generation, a.Status.Metadata.RefreshedAt.Time), true
}

// syncBooks lists a's currently owned Books and, once the metadata is
// cached and the memo says a fan-out input moved since the last complete
// one, requests its works list from the metadata gateway and ensures each
// desired Book exists (spec fields only, Create-or-leave-alone -- see
// ensureBook). It returns the post-fan-out Book list for the rollups:
// every Book the List found plus the ones this pass created. On any
// failure it still returns everything it knows.
//
// An author without metadata lists no books (synced false, no error):
// design D2 keys the fan-out on the metadata refresh, so a pass before it
// has nothing to key on and refetched the list on every wake (the
// creation, the finalizer, each Book's own create: three gateway calls for
// one author on 2026-11-10), and the container reads Pending until the
// metadata is fetched and the books listed (§5.2). The gateway's write
// wakes the item (authorPredicate's refreshedAt arm), so nothing waits on
// a timer.
func (r *Reconciler) syncBooks(ctx context.Context, a *catalogv1alpha1.Author, now time.Time) (fanOut, error) {
	var bookList catalogv1alpha1.BookList
	if err := r.List(ctx, &bookList, client.InNamespace(a.Namespace), client.MatchingFields{bookByAuthorRefIndexKey: a.Name}); err != nil {
		return fanOut{}, err
	}
	out := fanOut{books: bookList.Items}
	existingNames := make(map[string]bool, len(bookList.Items))
	for _, bk := range bookList.Items {
		existingNames[bk.Name] = true
	}

	key, hasMetadata := syncKey(a)
	if !hasMetadata {
		return out, nil
	}
	if !r.childSync.Due(a.UID, key, true) {
		// Nothing a fan-out reads has moved since the last complete one:
		// the Books the List found are what it would leave.
		out.synced = true
		return out, nil
	}

	req := schema.MetadataRequest{
		Kind: commonv1.MediaKindBook,
		IDs:  map[string]string{metadata.KeyOpenLibraryAuthor: a.Spec.OpenLibraryID},
	}
	rpcCtx, cancel := context.WithTimeout(ctx, bookSyncRPCBackoff)
	defer cancel()

	var resp schema.MetadataResponse
	// An idempotent lookup: every class retries on the reconciler's
	// requeue, and the condition names the class (ADR-0020 §4).
	if rpcErr := r.Bus.Request(rpcCtx, events.RPCMetadataLookup, req, &resp); rpcErr != nil {
		return out, events.DescribeRequestError(events.RPCMetadataLookup, rpcErr)
	}
	if resp.Error != "" {
		return out, fmt.Errorf("metadata gateway: %s", resp.Error)
	}

	fetched := make([]metadata.Book, 0, len(resp.Results))
	for _, raw := range resp.Results {
		var b metadata.Book
		if err := json.Unmarshal(raw, &b); err != nil {
			return out, fmt.Errorf("decode book: %w", err)
		}
		fetched = append(fetched, b)
	}

	desired := DesiredBooks(a, a.Status.AddOptionsApplied, existingNames, fetched, now)
	for _, d := range desired {
		bk, created, err := r.ensureBook(ctx, a, d)
		if err != nil {
			return out, err
		}
		if created {
			out.books = append(out.books, bk)
			out.created++
		}
	}
	out.synced = true
	r.childSync.Synced(a.UID, key, true)
	return out, nil
}

// ensureBook gets or creates the Book named d.Name, returning it and
// whether it created it. It NEVER patches an already-existing Book's spec
// or status -- see this package's doc.go for why: unlike Series/Episode,
// Book independently refreshes its own status.metadata, so this reconciler
// has nothing to push after Create.
func (r *Reconciler) ensureBook(ctx context.Context, a *catalogv1alpha1.Author, d DesiredBook) (catalogv1alpha1.Book, bool, error) {
	var bk catalogv1alpha1.Book
	key := types.NamespacedName{Namespace: a.Namespace, Name: d.Name}
	err := r.Get(ctx, key, &bk)
	switch {
	case apierrors.IsNotFound(err):
		bk = catalogv1alpha1.Book{
			ObjectMeta: metav1.ObjectMeta{Name: d.Name, Namespace: a.Namespace},
			Spec: catalogv1alpha1.BookSpec{
				AuthorRef: new(a.Name),
				WorkID:    d.WorkID,
			},
		}
		if d.Monitored != nil {
			bk.Spec.Monitored = d.Monitored
		}
		if err := k8s.SetControllerReference(a, &bk, r.Scheme); err != nil {
			return bk, false, err
		}
		if err := r.Create(ctx, &bk); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return bk, false, nil
			}
			return bk, false, err
		}
		return bk, true, nil
	case err != nil:
		return bk, false, err
	default:
		return bk, false, nil
	}
}
