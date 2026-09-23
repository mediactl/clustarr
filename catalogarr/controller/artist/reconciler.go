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
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/version"
)

// conditionQueueFull mirrors movie.MovieConditionQueueFull's name and
// semantics; artist_types.go declares no ArtistConditionQueueFull constant
// of its own (unlike Artist's Ready/MetadataReady/AlbumsSynced, which it
// does declare), so this is a local, package-scoped equivalent rather than
// an invented API constant -- the same reasoning as series.conditionQueueFull.
const conditionQueueFull = "QueueFull"

// albumByArtistRefIndexKey indexes Album by the Artist that owns it
// (spec.artistRef), so the reconciler can List an Artist's own Albums
// without scanning the whole namespace.
const albumByArtistRefIndexKey = ".spec.artistRef"

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

// Reconciler reconciles an Artist: metadata staleness (publishing a
// MetadataTask when the cache is missing or past its RefreshTTL), path, and
// the release-group-listing RPC fan-out into owned Album objects. It is the
// sole writer of status.path, status.albumCount, status.albumFileCount and
// status.addOptionsApplied; status.metadata belongs to the metadata gateway
// (k8s.ManagerCatalogarrMetadata) and this reconciler never builds an
// ArtistStatusApplyConfiguration that calls WithMetadata.
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

	// OnReconcile is a test-only hook, called at the top of every Reconcile.
	// It is nil-checked so production callers never need to set it.
	OnReconcile func()
}

// SetupWithManager registers the Artist controller: the finalizer/
// metadata-refresh predicate on Artist itself, and Owns(&Album{}) guarded by
// StatusFieldChanged on TrackFileCount so an Album's own file count
// changing re-triggers this reconciler's Rollup, without this reconciler's
// own Create calls (which touch no status at all) looping it.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &catalogv1alpha1.Album{}, albumByArtistRefIndexKey,
		func(o client.Object) []string {
			alb, ok := o.(*catalogv1alpha1.Album)
			if !ok {
				return nil
			}
			return []string{alb.Spec.ArtistRef}
		}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("artist").
		For(&catalogv1alpha1.Artist{}, builder.WithPredicates(artistPredicate())).
		Owns(&catalogv1alpha1.Album{}, builder.WithPredicates(k8s.StatusFieldChanged(func(o client.Object) int32 {
			alb, ok := o.(*catalogv1alpha1.Album)
			if !ok {
				return 0
			}
			return alb.Status.TrackFileCount
		}))).
		WithOptions(controller.Options{RecoverPanic: ptr.To(true), ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}

// artistPredicate wakes this controller on a spec change (GenerationChanged)
// or on the metadata gateway's own write (StatusFieldChanged scoped to
// status.metadata.refreshedAt) -- the same self-loop-avoidance shape as the
// Series predicate.
func artistPredicate() predicate.Predicate {
	return k8s.Or(
		k8s.GenerationChanged(),
		k8s.StatusFieldChanged(func(o client.Object) metav1.Time {
			a, ok := o.(*catalogv1alpha1.Artist)
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
	ctx, span := tracing.Start(ctx, "artist.Reconcile")
	defer span.End()
	if r.OnReconcile != nil {
		r.OnReconcile()
	}
	var a catalogv1alpha1.Artist
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

// reconcileDelete removes the finalizer. Owned Albums are garbage collected
// by the apiserver via their controller reference; there is nothing else
// owned outside Kubernetes at this phase.
func (r *Reconciler) reconcileDelete(ctx context.Context, a *catalogv1alpha1.Artist) (ctrl.Result, error) {
	name, err := k8s.FinalizerFor(a, r.Scheme)
	if err != nil {
		return ctrl.Result{}, reconcile.TerminalError(err)
	}
	if _, err := k8s.RemoveFinalizer(ctx, r.Client, a, name); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// reconcileNormal mirrors the Series reconciler's structure (metadata
// staleness decided and a MetadataTask published when needed, path computed
// once metadata is cached), then adds the release-group-listing RPC
// fan-out as the last step: an RPC failure surfaces on
// AlbumsSynced/Ready but never blocks the rest of this same status patch
// from landing. Artist has no Phase field of its own (unlike Series), so
// there is no phase computation here.
func (r *Reconciler) reconcileNormal(ctx context.Context, a *catalogv1alpha1.Artist) (ctrl.Result, error) {
	now := time.Now().UTC()
	conditions := append([]metav1.Condition(nil), a.Status.Conditions...)

	statusAC := catalogac.ArtistStatus().WithObservedGeneration(a.Generation)
	// Sent on every reconcile once the decision point is reached, not just
	// the first: see the identical rationale on movie.Reconciler's and
	// series.Reconciler's reconcileNormal (SSA releases a field a manager
	// stops sending).
	statusAC = statusAC.WithAddOptionsApplied(true)

	stale := a.Status.Metadata == nil
	if !stale {
		ttl := pkgmetadata.RefreshTTL(commonv1.MediaKindArtist, pkgmetadata.RefreshStateActive, a.Status.Metadata.RefreshedAt.Time)
		stale = now.Sub(a.Status.Metadata.RefreshedAt.Time) >= ttl
	}
	metaReady := !stale

	if stale {
		envKey := a.Namespace + "/" + a.Name
		mediaKey := events.MediaKey(string(commonv1.MediaKindArtist), a.Namespace, a.Name)
		schemaName, data, err := schema.Encode(schema.MetadataTask{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindArtist, Name: a.Name},
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
				if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Artist(a.Name, a.Namespace).WithStatus(statusAC)); err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{RequeueAfter: time.Minute}, nil
			}
			return ctrl.Result{}, pubErr
		}
		k8s.MarkFalse(a, &conditions, conditionQueueFull, "Published", "metadata task published")
		k8s.MarkFalse(a, &conditions, catalogv1alpha1.ArtistConditionMetadataReady, "Refreshing", "metadata refresh requested")
	} else {
		k8s.MarkTrue(a, &conditions, catalogv1alpha1.ArtistConditionMetadataReady, k8s.ReasonReconciled, "metadata is fresh")
	}

	if a.Status.Metadata != nil {
		var rf catalogv1alpha1.RootFolder
		if err := r.Get(ctx, types.NamespacedName{Namespace: a.Namespace, Name: a.Spec.RootFolderRef}, &rf); err != nil {
			if apierrors.IsNotFound(err) {
				k8s.MarkFalse(a, &conditions, k8s.ConditionReady, "RootFolderNotFound", "rootFolder %q not found", a.Spec.RootFolderRef)
				statusAC = reassertKnownStatus(statusAC, a)
				statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)
				if _, perr := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Artist(a.Name, a.Namespace).WithStatus(statusAC)); perr != nil {
					return ctrl.Result{}, perr
				}
				return ctrl.Result{RequeueAfter: time.Minute}, nil
			}
			return ctrl.Result{}, err
		}

		eng := naming.NewEngine(naming.Config{
			Dialect:          naming.Dialect(rf.Spec.Naming.Dialect),
			ColonReplacement: naming.ColonReplacement(rf.Spec.Naming.ColonReplacement),
			Overrides:        rf.Spec.Naming.Overrides,
		})
		nctx := naming.Context{
			Kind:       commonv1.MediaKindArtist,
			ArtistName: a.Status.Metadata.Name,
			ArtistMbID: a.Spec.MusicBrainzID,
		}
		pth, err := Path(rf.Spec.Path, a.Spec.Folder, eng, nctx)
		if err != nil {
			return ctrl.Result{}, err
		}
		statusAC = statusAC.WithPath(pth)
	}

	// Release-group-listing RPC fan-out (last step, per §8.1's Series/Episode
	// ordering): a failure here surfaces on AlbumsSynced/Ready without
	// blocking the patch above.
	albumsSynced, albums, syncErr := r.syncAlbums(ctx, a, now)
	if syncErr != nil {
		k8s.MarkFalse(a, &conditions, catalogv1alpha1.ArtistConditionAlbumsSynced, "RPCError", "album listing RPC failed: %s", syncErr.Error())
	} else {
		k8s.MarkTrue(a, &conditions, catalogv1alpha1.ArtistConditionAlbumsSynced, k8s.ReasonReconciled, "albums synced")
	}

	albumCount, albumFileCount := Rollup(albums)
	statusAC = statusAC.WithAlbumCount(albumCount).WithAlbumFileCount(albumFileCount)

	k8s.MarkReady(a, &conditions, metaReady && albumsSynced, k8s.ReasonReconciled, "metadataReady=%t albumsSynced=%t", metaReady, albumsSynced)
	statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)

	if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Artist(a.Name, a.Namespace).WithStatus(statusAC)); err != nil {
		return ctrl.Result{}, err
	}

	if syncErr != nil {
		return ctrl.Result{RequeueAfter: albumSyncRPCBackoff}, nil
	}
	return ctrl.Result{}, nil
}

// reassertKnownStatus re-adds every field this manager owns besides
// ObservedGeneration/AddOptionsApplied/Conditions to statusAC, sourced from
// a's current (pre-reconcile) status. Used on the two early-return paths
// (QueueFull, RootFolderNotFound): both are transient failures, and without
// this, PatchStatus's apply would omit every field it does not mention,
// releasing (zeroing) a healthy Artist's Path/AlbumCount/AlbumFileCount the
// next time either blip happens. Mirrors series.reassertKnownStatus.
func reassertKnownStatus(statusAC *catalogac.ArtistStatusApplyConfiguration, a *catalogv1alpha1.Artist) *catalogac.ArtistStatusApplyConfiguration {
	if a.Status.Path != "" {
		statusAC = statusAC.WithPath(a.Status.Path)
	}
	statusAC = statusAC.WithAlbumCount(a.Status.AlbumCount)
	statusAC = statusAC.WithAlbumFileCount(a.Status.AlbumFileCount)
	return statusAC
}

// syncAlbums lists a's currently owned Albums, requests its release-group
// list from the metadata gateway (rpc.catalogarr.metadata.lookup's
// lookupAlbums handler, keyed by pkgmetadata.KeyMBArtist), filters it
// through AlbumAccepted, ensures each desired Album exists, and returns the
// full (pre-fan-out) owned Album list for Rollup -- self-correcting in the
// same way series.syncEpisodes is: a newly created Album's own Create event
// passes Owns()'s predicate, so a Rollup based on the pre-fan-out list here
// is completed by the very next reconcile it triggers.
func (r *Reconciler) syncAlbums(ctx context.Context, a *catalogv1alpha1.Artist, now time.Time) (synced bool, existing []catalogv1alpha1.Album, err error) {
	var albumList catalogv1alpha1.AlbumList
	if err := r.List(ctx, &albumList, client.InNamespace(a.Namespace), client.MatchingFields{albumByArtistRefIndexKey: a.Name}); err != nil {
		return false, nil, err
	}
	existingIDs := make(map[string]bool, len(albumList.Items))
	for _, alb := range albumList.Items {
		existingIDs[alb.Spec.ReleaseGroupID] = true
	}

	req := schema.MetadataRequest{
		Kind: commonv1.MediaKindAlbum,
		IDs:  map[string]string{pkgmetadata.KeyMBArtist: a.Spec.MusicBrainzID},
	}
	rpcCtx, cancel := context.WithTimeout(ctx, albumSyncRPCBackoff)
	defer cancel()

	var resp schema.MetadataResponse
	if rpcErr := r.Bus.Request(rpcCtx, events.RPCMetadataLookup, req, &resp); rpcErr != nil {
		return false, albumList.Items, rpcErr
	}
	if resp.Error != "" {
		return false, albumList.Items, fmt.Errorf("metadata gateway: %s", resp.Error)
	}

	fetched := make([]pkgmetadata.Album, 0, len(resp.Results))
	for _, raw := range resp.Results {
		var alb pkgmetadata.Album
		if err := json.Unmarshal(raw, &alb); err != nil {
			return false, albumList.Items, fmt.Errorf("decode album: %w", err)
		}
		fetched = append(fetched, alb)
	}

	desired := DesiredAlbums(a, a.Status.AddOptionsApplied, existingIDs, fetched, now)
	for _, d := range desired {
		if err := r.ensureAlbum(ctx, a, d); err != nil {
			return false, albumList.Items, err
		}
	}
	return true, albumList.Items, nil
}

// ensureAlbum gets or creates the Album named d.Name. spec.monitored is only
// ever set on Create (d.Monitored != nil there per DesiredAlbums' contract);
// an already-existing Album's spec.monitored is never touched, since it
// belongs to the user after creation. Unlike series.ensureEpisode, this
// function never patches status under any field manager -- see this
// package's doc.go for why Album's provider-sourced fields are never
// fanned out.
func (r *Reconciler) ensureAlbum(ctx context.Context, a *catalogv1alpha1.Artist, d DesiredAlbum) error {
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
			return err
		}
		if err := r.Create(ctx, &alb); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
		return nil
	case err != nil:
		return err
	default:
		return nil
	}
}
