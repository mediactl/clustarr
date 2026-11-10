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

package artist

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
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/naming/catalogctx"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// conditionQueueFull mirrors movie.MovieConditionQueueFull's name and
// semantics; artist_types.go declares no ArtistConditionQueueFull constant
// of its own (unlike Artist's Ready/MetadataReady/AlbumsSynced, which it
// does declare), so this is a local, package-scoped equivalent rather than
// an invented API constant -- the same reasoning as series.conditionQueueFull.
const conditionQueueFull = "QueueFull"

// albumByArtistRefIndexKey indexes Album by the Artist that owns it
// (spec.artistRef), so the pass can List an Artist's own Albums without
// scanning the whole namespace.
const albumByArtistRefIndexKey = ".spec.artistRef"

// AlbumByArtistRefIndex is the field index of Albums by spec.artistRef, for
// other readers of the cache (the nfo stage).
const AlbumByArtistRefIndex = albumByArtistRefIndexKey

// albumSyncRPCBackoff is the RequeueAfter used when the album-listing RPC
// fails, mirroring series.episodeSyncRPCBackoff.
const albumSyncRPCBackoff = 30 * time.Second

// bus is the subset of events.Bus this reconciler actually calls: Publish
// for the metadata-staleness task and Request for the album-listing RPC.
// Mirrors series.bus's own narrowing rationale.
type bus interface {
	events.Publisher
	Request(ctx context.Context, subject string, in, out any) error
}

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=artists,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=artists/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=artists/finalizers,verbs=update
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=albums,verbs=get;list;watch;create
// The Recorder is a k8s.io/client-go/tools/events.EventRecorder, handed in by
// mgr.GetEventRecorder, and it writes events.k8s.io/v1 -- so events.k8s.io is
// the group to grant and the core group is not; see series/reconciler.go's
// identical marker for the occasion this repo learned it.
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconciler is the Artist's item path on the remediation loop (2026-11-10
// design D2, §5.1): metadata staleness (publishing a MetadataTask, admitted
// by the dispatch ledger, when the cache is missing or past its
// RefreshTTL), path, the release-group-listing RPC fan-out into owned
// Album objects behind the child-sync memo, the container rollup
// (rollup.Container) and the folder scan a new artist's first albums ask
// for. It is the sole writer, under catalog, of status.path,
// status.albumCount, status.albumFileCount, status.addOptionsApplied, the
// ContainerRollup fields and the conditions; status.metadata and
// status.artwork belong to the loop's metadata stage (catalog-metadata),
// status.nfo to its nfo stage (catalog-nfo), and this reconciler never
// builds an ArtistStatusApplyConfiguration that calls WithMetadata.
//
// Unlike series.Reconciler, this reconciler writes NOTHING onto the Album
// objects it creates -- no second field manager, no per-item provider
// fields seeded at create time. See this package's doc.go for why (G2-1's
// settled ownership decision, documented on buildAlbumMetadataAC).
type Reconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder k8sevents.EventRecorder
	Bus      bus

	// Folders reads an artist's folder when its first albums ask for a
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

// RegisterIndexes registers the Album-by-spec.artistRef index under its
// existing name. The remediation loop registers it once
// (remediation.RegisterIndexes) since the Artist became an item key.
func RegisterIndexes(ctx context.Context, idx client.FieldIndexer) error {
	return idx.IndexField(ctx, &catalogv1alpha1.Album{}, albumByArtistRefIndexKey,
		func(o client.Object) []string {
			alb, ok := o.(*catalogv1alpha1.Album)
			if !ok {
				return nil
			}
			return []string{alb.Spec.ArtistRef}
		})
}

// Watches is the Artist's item path on the remediation loop (loop spec
// §3.12): the Artist itself (a spec change, the metadata gateway's write,
// the DLQ annotation), its Albums by spec.artistRef when a rollup input
// moves (a file, a phase, a release date, a monitor flag -- never the
// Album's own metadata document, which would loop the artist on every
// album refresh), and a RootFolder's spec edit (its naming, its writeNFO,
// its defaults) for the artists under it, as an Episode wakes its Series.
// The controller named "artist" is gone; the recorder keeps the name.
func (r *Reconciler) Watches() []rollup.Watch {
	return []rollup.Watch{
		{Object: &catalogv1alpha1.Artist{}, Map: rollup.Self, Predicates: []predicate.Predicate{artistPredicate()}},
		{Object: &catalogv1alpha1.Album{}, Map: mapAlbumToArtist, Predicates: []predicate.Predicate{albumRollupChanged()}},
		{Object: &catalogv1alpha1.RootFolder{}, Map: r.mapRootFolder, Predicates: []predicate.Predicate{k8s.GenerationChanged()}},
	}
}

var _ rollup.Item = (*Reconciler)(nil)

// mapAlbumToArtist maps an Album to the Artist its spec.artistRef names.
func mapAlbumToArtist(_ context.Context, o client.Object) []reconcile.Request {
	alb, ok := o.(*catalogv1alpha1.Album)
	if !ok || alb.Spec.ArtistRef == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: alb.Namespace, Name: alb.Spec.ArtistRef}}}
}

// mapRootFolder enqueues the RootFolder's artists. It lists the namespace's
// artists per event, which stays cheap because RootFolders are few and
// change rarely: at startup it runs once per RootFolder, not per artist
// (CLAUDE.md's map-function gotcha).
func (r *Reconciler) mapRootFolder(ctx context.Context, o client.Object) []reconcile.Request {
	var list catalogv1alpha1.ArtistList
	if err := r.List(ctx, &list, client.InNamespace(o.GetNamespace()), client.UnsafeDisableDeepCopy); err != nil {
		logging.FromContext(ctx).Warn("artist: list for a RootFolder change failed", "rootFolder", o.GetName(), "error", err)
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

// albumRollupKey is what of an Album the Artist's rollup reads: its
// monitor flag and phase, its file, and its release date (the container's
// next and previous release dates).
type albumRollupKey struct {
	monitored      bool
	phase          commonv1.LeafPhase
	hasFile        bool
	trackFileCount int32
	releaseDate    int64
}

func albumRollupKeyOf(o client.Object) albumRollupKey {
	alb, ok := o.(*catalogv1alpha1.Album)
	if !ok {
		return albumRollupKey{}
	}
	k := albumRollupKey{
		monitored: ptr.Deref(alb.Spec.Monitored, true), phase: alb.Status.Phase,
		hasFile: alb.Status.HasFile, trackFileCount: alb.Status.TrackFileCount,
	}
	if alb.Status.Metadata != nil && alb.Status.Metadata.ReleaseDate != nil {
		k.releaseDate = alb.Status.Metadata.ReleaseDate.Unix()
	}
	return k
}

// albumRollupChanged wakes the Artist when an owned Album's rollup inputs
// change, on its creation and its deletion, and not on the Album's other
// status writes (its tracks, its downloads), which would loop it.
func albumRollupChanged() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			if e.ObjectOld == nil || e.ObjectNew == nil {
				return false
			}
			return albumRollupKeyOf(e.ObjectOld) != albumRollupKeyOf(e.ObjectNew)
		},
	}
}

// artistPredicate wakes this item on a spec change (GenerationChanged) or
// on the metadata gateway's own write (StatusFieldChanged scoped to
// status.metadata.refreshedAt) -- the same self-loop-avoidance shape as the
// Series predicate.
func artistPredicate() predicate.Predicate {
	return k8s.Or(
		k8s.GenerationChanged(),
		// An annotation-only change bumps no generation.
		k8s.DeadLetteredAnnotationChanged(),
		k8s.StatusFieldChanged(func(o client.Object) metav1.Time {
			a, ok := o.(*catalogv1alpha1.Artist)
			if !ok || a.Status.Metadata == nil {
				return metav1.Time{}
			}
			return a.Status.Metadata.RefreshedAt
		}),
	)
}

// ReconcileItem is the Artist's item path on the remediation loop; it keeps
// the §8.8 skeleton: get, split on deletion, ensure the finalizer WITHOUT
// an early return, then reconcileNormal.
func (r *Reconciler) ReconcileItem(ctx context.Context, nn types.NamespacedName) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "artist.Reconcile")
	defer span.End()
	if r.OnReconcile != nil {
		r.OnReconcile()
	}
	var a catalogv1alpha1.Artist
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

// reconcileDelete removes the finalizer. Owned Albums are garbage collected
// by the apiserver via their controller reference; an Artist owns no grabs,
// so nothing waits on the downloads stage.
func (r *Reconciler) reconcileDelete(ctx context.Context, a *catalogv1alpha1.Artist) (ctrl.Result, error) {
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
// the release-group-listing RPC fan-out, the container rollup and the
// folder scan as the last steps: an RPC failure surfaces on
// AlbumsSynced/Ready/Phase but never blocks the rest of this same status
// apply from landing.
func (r *Reconciler) reconcileNormal(ctx context.Context, a *catalogv1alpha1.Artist) (ctrl.Result, error) {
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
	// would announce the Artist added on each of them.
	if action := rollup.ItemAction(a.Generation, a.Status.ObservedGeneration, a.Status.ObservedGeneration != 0); action != "" {
		r.publishItem(ctx, a, action, now)
	}

	statusAC := catalogac.ArtistStatus().WithObservedGeneration(a.Generation)
	// The stored value until the fan-out below has run: the two early
	// returns (QueueFull, RootFolderNotFound) send it unchanged, and a pass
	// without metadata -- whose fan-out waits for the document -- flips
	// nothing, since the first fan-out reads it to apply addOptions.monitor
	// (DesiredAlbums' `!addOptionsApplied` arm). Until 2026-11-10 every
	// pass sent true, so once the fan-out waited for the metadata the first
	// real fan-out ran with the flag already flipped and every new Artist's
	// Albums were created under spec.monitorNewItems (all by default)
	// instead of InitialAlbumMonitored -- every album monitored under none,
	// future, missing, existing, latest or first. Sent on every apply,
	// because server-side apply releases a field a manager stops sending
	// (movie.Reconciler's note on the same flag).
	statusAC = statusAC.WithAddOptionsApplied(a.Status.AddOptionsApplied)

	stale := a.Status.Metadata == nil
	if !stale {
		ttl := pkgmetadata.RefreshTTL(commonv1.MediaKindArtist, pkgmetadata.RefreshStateActive, a.Status.Metadata.RefreshedAt.Time)
		stale = now.Sub(a.Status.Metadata.RefreshedAt.Time) >= ttl
	}
	metaReady := !stale
	// A recent document the gateway wrote before it learned a field is
	// refreshed once, at once, and the item stays ready meanwhile: every
	// item in an upgraded library would otherwise drop to Pending together
	// (Movie's rule; the 2026-11-10 design D8 gives every kind a
	// schemaVersion).
	outdated := !stale && a.Status.Metadata.SchemaVersion < pkgmetadata.SchemaVersion

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
					if conflicted, err := itemstatus.Apply(ctx, r.Client, a, catalogac.Artist(a.Name, a.Namespace).WithStatus(statusAC)); err != nil || conflicted {
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
			k8s.MarkTrue(a, &conditions, catalogv1alpha1.ArtistConditionMetadataReady, "Upgrading",
				"metadata schema %d is older than %d; refreshing once", a.Status.Metadata.SchemaVersion, pkgmetadata.SchemaVersion)
		} else {
			k8s.MarkFalse(a, &conditions, catalogv1alpha1.ArtistConditionMetadataReady, "Refreshing", "metadata refresh requested")
		}
	} else {
		k8s.MarkTrue(a, &conditions, catalogv1alpha1.ArtistConditionMetadataReady, k8s.ReasonReconciled, "metadata is fresh")
	}

	// The artist's RootFolder and folder, once its metadata is cached: what
	// the folder scan its first albums ask for needs (folderscan.Pass).
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
				if conflicted, perr := itemstatus.Apply(ctx, r.Client, a, catalogac.Artist(a.Name, a.Namespace).WithStatus(statusAC)); perr != nil || conflicted {
					return itemstatus.Requeue(conflicted), perr
				}
				return ctrl.Result{RequeueAfter: time.Minute}, nil
			}
			return ctrl.Result{}, err
		}

		nctx := naming.Context{
			Kind:       commonv1.MediaKindArtist,
			ArtistName: a.Status.Metadata.Name,
			ArtistMbID: a.Spec.MusicBrainzID,
		}
		pth, err := Path(&rf, a.Spec.Folder, nctx)
		if err != nil {
			// The folder rule cannot render the name it knows -- one of only
			// dots, or nothing once sanitized (catalogctx.ItemFolder): the
			// steady state is kept and the problem reported on Ready, as a
			// missing RootFolder is, never an error that would back the
			// artist off for good. The next metadata refresh or spec edit
			// renders it again; the fixed requeue stands in for the wake a
			// RootFolder's naming edit does not send.
			if rollup.Transitioned(a.Status.Conditions, k8s.ConditionReady, metav1.ConditionFalse, catalogctx.ReasonFolderUnrenderable) {
				r.warn(a, catalogctx.ReasonFolderUnrenderable, "no folder under rootFolder %q: %v", a.Spec.RootFolderRef, err)
			}
			k8s.MarkFalse(a, &conditions, k8s.ConditionReady, catalogctx.ReasonFolderUnrenderable, "no folder under rootFolder %q: %v", a.Spec.RootFolderRef, err)
			statusAC = reassertKnownStatus(statusAC, a)
			statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)
			if conflicted, perr := itemstatus.Apply(ctx, r.Client, a, catalogac.Artist(a.Name, a.Namespace).WithStatus(statusAC)); perr != nil || conflicted {
				return itemstatus.Requeue(conflicted), perr
			}
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}
		statusAC = statusAC.WithPath(pth)
		root, folder = &rf, pth
	}

	// Release-group-listing RPC fan-out (per §8.1's Series/Episode
	// ordering), behind the child-sync memo: a failure here surfaces on
	// AlbumsSynced/Ready without blocking the apply.
	// The fan-out reads the object's PRE-reconcile status.addOptionsApplied
	// (DesiredAlbums): the add-time monitor mode until one complete fan-out
	// has applied it.
	fan, syncErr := r.syncAlbums(ctx, a, now)
	// Applied once (the Comic's rule): true from the first complete fan-out
	// on, and sent on every apply after that. A first pass whose fan-out
	// failed, or waits for the metadata, leaves it false, so the next one
	// still applies the add-time mode to the Albums it has yet to create;
	// a pass the memo skips reads synced because a complete fan-out of this
	// process stands behind it, so the flip it sends is owed, a cache behind
	// the flip's apply or a conflicted apply included (syncKey).
	statusAC = statusAC.WithAddOptionsApplied(a.Status.AddOptionsApplied || fan.synced)
	switch {
	case syncErr != nil:
		reason := events.ClassifyRequestError(syncErr).Reason()
		if rollup.Transitioned(a.Status.Conditions, catalogv1alpha1.ArtistConditionAlbumsSynced, metav1.ConditionFalse, reason) {
			r.warn(a, "AlbumSyncFailed", "album listing RPC failed: %s", syncErr.Error())
		}
		k8s.MarkFalse(a, &conditions, catalogv1alpha1.ArtistConditionAlbumsSynced, reason, "album listing RPC failed: %s", syncErr.Error())
	case !fan.synced:
		// No metadata yet: the fan-out waits for it (syncAlbums), and the
		// gateway's write wakes this item.
		k8s.MarkFalse(a, &conditions, catalogv1alpha1.ArtistConditionAlbumsSynced, "MetadataPending", "albums are listed once the artist's metadata is cached")
	default:
		k8s.MarkTrue(a, &conditions, catalogv1alpha1.ArtistConditionAlbumsSynced, k8s.ReasonReconciled, "albums synced")
	}
	if fan.created > 0 {
		r.normal(a, "AlbumsAdded", "created %d Album(s)", fan.created)
	}

	// The rollups are taken over the POST-fan-out Albums: what this very
	// reconcile created, not the list read before the RPC (series.Rollup's
	// rule).
	albumCount, albumFileCount := Rollup(fan.albums)
	statusAC = statusAC.WithAlbumCount(albumCount).WithAlbumFileCount(albumFileCount)
	// The container rollup (2026-11-10 design §4.1, §5.2): Pending until
	// the metadata is fetched and the albums listed (the Series' rule),
	// Unmonitored, else Ready; the counts and the release dates from the
	// albums' phases.
	roll := rollup.Container(monitored, metaReady && fan.synced, Children(fan.albums), now)
	statusAC = withContainerRollup(statusAC, roll)

	// Lidarr's RefreshArtist, then RescanArtist (smoke finding O8, design
	// D2): a scan attributes a track file only to an Album that exists, so
	// the files of an artist a scan (or Add New, or an import list) created
	// before its albums were listed waited for the next scan. The pass that
	// first lists them owes the artist's folder one scan, recorded in the
	// FolderScanned condition; an artist whose status already held its
	// albums owes none.
	folderScan := folderscan.Pass(ctx, r.Client, r.Folders, folderscan.Request{
		Item: a, Root: root, Path: folder,
		HadChildren: a.Status.AlbumCount > 0, HasChildren: len(fan.albums) > 0, Children: "albums",
		FieldManager: k8s.ManagerCatalog, Now: now,
	}, &conditions)
	if folderScan.Scan != "" {
		r.normal(a, "FolderScanRequested", "LibraryScan %s scans the artist's folder for the files its first albums awaited", folderScan.Scan)
	}

	k8s.MarkReady(a, &conditions, metaReady && fan.synced, k8s.ReasonReconciled, "metadataReady=%t albumsSynced=%t phase=%s", metaReady, fan.synced, roll.Phase)
	statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)

	if conflicted, err := itemstatus.Apply(ctx, r.Client, a, catalogac.Artist(a.Name, a.Namespace).WithStatus(statusAC)); err != nil || conflicted {
		return itemstatus.Requeue(conflicted), err
	}
	if a.Status.Phase != "" && a.Status.Phase != roll.Phase {
		r.normal(a, string(roll.Phase), "phase %s -> %s", a.Status.Phase, roll.Phase)
	}

	if syncErr != nil {
		return ctrl.Result{RequeueAfter: soonest(albumSyncRPCBackoff, folderScan.Retry)}, nil
	}
	// nextReleaseDate moves when an album is released, and nothing else
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
func withContainerRollup(ac *catalogac.ArtistStatusApplyConfiguration, roll catalogv1alpha1.ContainerRollup) *catalogac.ArtistStatusApplyConfiguration {
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
// (QueueFull, RootFolderNotFound): both are transient failures, and without
// this, the apply would omit every field it does not mention, releasing
// (zeroing) a healthy Artist's Path/AlbumCount/AlbumFileCount and its
// container rollup the next time either blip happens. Mirrors
// series.reassertKnownStatus.
func reassertKnownStatus(statusAC *catalogac.ArtistStatusApplyConfiguration, a *catalogv1alpha1.Artist) *catalogac.ArtistStatusApplyConfiguration {
	if a.Status.Path != "" {
		statusAC = statusAC.WithPath(a.Status.Path)
	}
	statusAC = statusAC.WithAlbumCount(a.Status.AlbumCount)
	statusAC = statusAC.WithAlbumFileCount(a.Status.AlbumFileCount)
	return withContainerRollup(statusAC, a.Status.ContainerRollup)
}

// fanOut is what syncAlbums reports: whether the fan-out completed, how
// many Albums it created, and the Artist's Albums as they stand AFTER it.
type fanOut struct {
	synced  bool
	created int
	albums  []catalogv1alpha1.Album
}

// syncKey is the child-sync memo's key for a: its spec generation and its
// metadata refresh. status.addOptionsApplied is no input, though
// DesiredAlbums reads it: the memo records only a complete fan-out, the
// first of which applies the add-time mode and flips the flag in the same
// pass (reconcileNormal sends stored || fan.synced), so the key carries
// no flag (childsync.Key, which carried one until 2026-11-10) whatever the
// pass read -- a pass reading false after that fan-out
// (an Album's own Create wakes the Artist before the flip's apply reaches
// the cache; or the apply conflicted) skips the fetch, which would create
// nothing, and sends the flip again (2026-11-10: keyed on the read value,
// TestArtistItemPath's fan-out fetched twice). An artist without metadata
// has no key: its fan-out waits for the document (syncAlbums).
func syncKey(a *catalogv1alpha1.Artist) (childsync.Key, bool) {
	if a.Status.Metadata == nil {
		return childsync.Key{}, false
	}
	return childsync.KeyAt(a.Generation, a.Status.Metadata.RefreshedAt.Time), true
}

// syncAlbums lists a's currently owned Albums and, once the metadata is
// cached and the memo says a fan-out input moved since the last complete
// one, requests its release-group list from the metadata gateway
// (rpc.catalog.metadata.lookup's lookupAlbums handler, keyed by
// pkgmetadata.KeyMBArtist), filters it through AlbumAccepted and ensures
// each desired Album exists. It returns the post-fan-out Album list for the
// rollups: every Album the List found plus the ones this pass created. On
// any failure it still returns everything it knows.
//
// An artist without metadata lists no albums (synced false, no error):
// design D2 keys the fan-out on the metadata refresh, so a pass before it
// has nothing to key on and refetched the list on every wake (the
// creation, the finalizer, each Album's own create: three gateway calls for
// one artist on 2026-11-10); the container reads Pending until the metadata
// is fetched and the albums listed (§5.2); and the first fan-out's monitor
// rule (InitialAlbumMonitored under future) reads the run status from the
// metadata at the one Create that sets spec.monitored. The gateway's write
// wakes the item (artistPredicate's refreshedAt arm), so nothing waits on
// a timer.
func (r *Reconciler) syncAlbums(ctx context.Context, a *catalogv1alpha1.Artist, now time.Time) (fanOut, error) {
	var albumList catalogv1alpha1.AlbumList
	if err := r.List(ctx, &albumList, client.InNamespace(a.Namespace), client.MatchingFields{albumByArtistRefIndexKey: a.Name}); err != nil {
		return fanOut{}, err
	}
	out := fanOut{albums: albumList.Items}
	existingIDs := make(map[string]bool, len(albumList.Items))
	for _, alb := range albumList.Items {
		existingIDs[alb.Spec.ReleaseGroupID] = true
	}

	key, hasMetadata := syncKey(a)
	if !hasMetadata {
		return out, nil
	}
	if !r.childSync.Due(a.UID, key, true) {
		// Nothing a fan-out reads has moved since the last complete one:
		// the Albums the List found are what it would leave.
		out.synced = true
		return out, nil
	}

	req := schema.MetadataRequest{
		Kind: commonv1.MediaKindAlbum,
		IDs:  map[string]string{pkgmetadata.KeyMBArtist: a.Spec.MusicBrainzID},
	}
	rpcCtx, cancel := context.WithTimeout(ctx, albumSyncRPCBackoff)
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

	fetched := make([]pkgmetadata.Album, 0, len(resp.Results))
	for _, raw := range resp.Results {
		var alb pkgmetadata.Album
		if err := json.Unmarshal(raw, &alb); err != nil {
			return out, fmt.Errorf("decode album: %w", err)
		}
		fetched = append(fetched, alb)
	}

	desired := DesiredAlbums(a, a.Status.AddOptionsApplied, existingIDs, fetched, now)
	for _, d := range desired {
		alb, created, err := r.ensureAlbum(ctx, a, d)
		if err != nil {
			return out, err
		}
		if created {
			out.albums = append(out.albums, alb)
			out.created++
		}
	}
	out.synced = true
	r.childSync.Synced(a.UID, key, true)
	return out, nil
}

// ensureAlbum gets or creates the Album named d.Name, returning it and
// whether it created it. spec.monitored is only ever set on Create
// (d.Monitored != nil there per DesiredAlbums' contract); an
// already-existing Album's spec.monitored is never touched, since it
// belongs to the user after creation. Unlike series.ensureEpisode, this
// function never patches status under any field manager -- see this
// package's doc.go for why Album's provider-sourced fields are never
// fanned out.
func (r *Reconciler) ensureAlbum(ctx context.Context, a *catalogv1alpha1.Artist, d DesiredAlbum) (catalogv1alpha1.Album, bool, error) {
	var alb catalogv1alpha1.Album
	key := types.NamespacedName{Namespace: a.Namespace, Name: d.Name}
	err := r.Get(ctx, key, &alb)
	switch {
	case apierrors.IsNotFound(err):
		alb = catalogv1alpha1.Album{
			ObjectMeta: metav1.ObjectMeta{Name: d.Name, Namespace: a.Namespace},
			Spec: catalogv1alpha1.AlbumSpec{
				ArtistRef:      a.Name,
				ReleaseGroupID: d.ReleaseGroupID,
			},
		}
		if d.Monitored != nil {
			alb.Spec.Monitored = d.Monitored
		}
		if err := k8s.SetControllerReference(a, &alb, r.Scheme); err != nil {
			return alb, false, err
		}
		if err := r.Create(ctx, &alb); err != nil {
			if apierrors.IsAlreadyExists(err) {
				return alb, false, nil
			}
			return alb, false, err
		}
		return alb, true, nil
	case err != nil:
		return alb, false, err
	default:
		return alb, false, nil
	}
}
