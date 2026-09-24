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

package album

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
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/version"
)

// conditionQueueFull mirrors series.conditionQueueFull's name and semantics;
// album_types.go declares no AlbumConditionQueueFull constant of its own
// (unlike Album's Ready/MetadataReady/Invalid, which it does declare), so
// this is a local, package-scoped equivalent rather than an invented API
// constant.
const conditionQueueFull = "QueueFull"

// conditionTracksSynced is likewise a local, non-API condition type: album's
// own status.tracks has no dedicated CRD condition of its own (Invalid
// covers only the >200-tracks case), so this reconciler reports the
// track-listing RPC's own success/failure the same way series reports
// conditionQueueFull -- a real signal, just not one album_types.go names.
const conditionTracksSynced = "TracksSynced"

const (
	// mediaFileByAlbumIndexKey indexes MediaFile by the Album it backs,
	// filtered to spec.mediaRef.kind=album -- the same shape as
	// episode.mediaFileByEpisodeIndexKey. It returns whole-album and
	// per-track (spec.mediaRef.track) files alike; filestate.go's
	// FileState and FilesByRecording each take the view they need.
	mediaFileByAlbumIndexKey = ".spec.mediaRef.album"

	// downloadByAlbumIndexKey indexes Download by the Album its spec.target names
	// (kind album only). It is how the reconciler finds the Downloads it
	// derives status.activeDownloadRef from (gap-fix ruling R-5), the same
	// shape as movie.downloadByMovieIndexKey. spec.target is immutable, so
	// the index never has to follow an edit.
	downloadByAlbumIndexKey = ".spec.target.album"

	// albumByQualityProfileIndexKey indexes Album by its OWN
	// spec.qualityProfileRef override (nil/empty excluded); mapQualityProfile
	// reaches the Albums inheriting a profile through their Artist instead.
	albumByQualityProfileIndexKey = ".spec.qualityProfileRef"
)

// metadataSyncRPCBackoff is the RequeueAfter used when the track-listing RPC
// fails, mirroring series.episodeSyncRPCBackoff.
const metadataSyncRPCBackoff = 30 * time.Second

// bus is the subset of events.Bus this reconciler actually calls: Publish
// for the metadata-staleness task and Request for the track-listing RPC.
// Mirrors series.bus/artist.bus's own narrowing rationale.
type bus interface {
	events.Publisher
	Request(ctx context.Context, subject string, in, out any) error
}

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=albums,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=albums/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=albums/finalizers,verbs=update
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=artists,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders,verbs=get
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=download.clustarr.io,resources=downloads,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=qualityprofiles,verbs=get;list;watch
// The Recorder is a k8s.io/client-go/tools/events.EventRecorder, handed in by
// mgr.GetEventRecorder, and it writes events.k8s.io/v1 -- so events.k8s.io is
// the group to grant and the core group is not; see series/reconciler.go's
// identical marker for the occasion this repo learned it.
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconciler reconciles an Album: metadata staleness (publishing its own
// MetadataTask, exactly like Movie/Series/Artist -- Album is a first-class
// metadata target, not a fan-out child like Episode), path (via the owning
// Artist's own resolved path), the track-listing RPC sync, and the
// file/download rollup from a watched MediaFile and Download. See this
// package's doc.go for the full field-manager and track-listing
// rationale.
type Reconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder k8sevents.EventRecorder
	Bus      bus

	// OnReconcile is a test-only hook, called at the top of every Reconcile.
	// It is nil-checked so production callers never need to set it.
	OnReconcile func()
}

// SetupWithManager registers the Album controller, including
// Download/QualityProfile/MediaFile watches so an imported file, a grab and
// a quality-profile edit each wake the right Albums, mirroring
// audiobook.Reconciler.SetupWithManager's shape (app/catalog/controller/
// audiobook is this package's sibling precedent for the quality-evaluation
// half -- see this package's doc.go).
//
// The QualityProfile watch (mapQualityProfile) reaches every Album ranked
// against the edited profile: those that name it through their own
// spec.qualityProfileRef override, and those that inherit it from an Artist
// that names it. The Artist watch (mapArtist) covers the other way an
// inherited profile changes -- the Artist pointing spec.qualityProfileRef
// elsewhere -- along with every other Artist spec edit this reconciler reads
// (its metadata profile's releaseStatuses, which decide the release
// selection). Neither second hop uses the (Album, spec.artistRef) index:
// artist.Reconciler.SetupWithManager registers it (albumByArtistRefIndexKey),
// and a second IndexField call for the same (type, field) on one manager
// cache is a hard "indexer conflict" error at startup (episode.Reconciler's
// own documented gotcha), so both filter a namespaced List in Go -- a cold
// path, since profiles and Artists are edited by hand.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &catalogv1alpha1.MediaFile{}, mediaFileByAlbumIndexKey,
		func(o client.Object) []string {
			mf, ok := o.(*catalogv1alpha1.MediaFile)
			if !ok || mf.Spec.MediaRef.Kind != commonv1.MediaKindAlbum {
				return nil
			}
			return []string{mf.Spec.MediaRef.Name}
		}); err != nil {
		return err
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &downloadv1alpha1.Download{}, downloadByAlbumIndexKey,
		func(o client.Object) []string {
			dl, ok := o.(*downloadv1alpha1.Download)
			if !ok || dl.Spec.Target.Kind != commonv1.MediaKindAlbum {
				return nil
			}
			return []string{dl.Spec.Target.Name}
		}); err != nil {
		return err
	}
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &catalogv1alpha1.Album{}, albumByQualityProfileIndexKey,
		func(o client.Object) []string {
			alb, ok := o.(*catalogv1alpha1.Album)
			if !ok || alb.Spec.QualityProfileRef == nil || *alb.Spec.QualityProfileRef == "" {
				return nil
			}
			return []string{*alb.Spec.QualityProfileRef}
		}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("album").
		For(&catalogv1alpha1.Album{}, builder.WithPredicates(albumPredicate())).
		Watches(&catalogv1alpha1.MediaFile{}, handler.EnqueueRequestsFromMapFunc(r.mapMediaFile), builder.WithPredicates(k8s.GenerationChanged())).
		Watches(&downloadv1alpha1.Download{}, handler.EnqueueRequestsFromMapFunc(r.mapDownload), builder.WithPredicates(downloadPredicate())).
		Watches(&catalogv1alpha1.QualityProfile{}, handler.EnqueueRequestsFromMapFunc(r.mapQualityProfile), builder.WithPredicates(k8s.GenerationChanged())).
		Watches(&catalogv1alpha1.Artist{}, handler.EnqueueRequestsFromMapFunc(r.mapArtist), builder.WithPredicates(k8s.GenerationChanged())).
		WithOptions(controller.Options{RecoverPanic: ptr.To(true), ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}

// albumPredicate wakes this controller on a spec change (GenerationChanged,
// e.g. a user editing spec.monitored or pinning spec.releaseID), on the
// metadata gateway's own write (StatusFieldChanged scoped to
// status.metadata.refreshedAt), or on the grab path's status.pendingGrab --
// the same self-loop-avoidance shape as the Movie/Series/Book predicates.
func albumPredicate() predicate.Predicate {
	return k8s.Or(
		k8s.GenerationChanged(),
		// An annotation-only change bumps no generation.
		k8s.DeadLetteredAnnotationChanged(),
		k8s.StatusFieldChanged(func(o client.Object) metav1.Time {
			alb, ok := o.(*catalogv1alpha1.Album)
			if !ok || alb.Status.Metadata == nil {
				return metav1.Time{}
			}
			return alb.Status.Metadata.RefreshedAt
		}),
		// The grab path's status.pendingGrab write bumps no generation and
		// touches no metadata, so without this arm Phase=Delayed would not
		// appear until something else woke the Album. grabAt changes
		// whenever the pending grab is set, rescheduled or cleared.
		k8s.StatusFieldChanged(func(o client.Object) metav1.Time {
			alb, ok := o.(*catalogv1alpha1.Album)
			if !ok || alb.Status.PendingGrab == nil {
				return metav1.Time{}
			}
			return alb.Status.PendingGrab.GrabAt
		}),
	)
}

// downloadPredicate is the same shape as the movie/episode packages' own: a
// status-only phase transition never bumps generation, so GenerationChanged
// alone would never fire on it.
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
		// A Download being torn down stops counting the moment it is
		// marked (rollup.DownloadNonTerminal), not when its finalizers let
		// it go.
		k8s.StatusFieldChanged(k8s.IsDeleting),
	)
}

func (r *Reconciler) mapMediaFile(_ context.Context, o client.Object) []reconcile.Request {
	mf, ok := o.(*catalogv1alpha1.MediaFile)
	if !ok || mf.Spec.MediaRef.Kind != commonv1.MediaKindAlbum {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: mf.Namespace, Name: mf.Spec.MediaRef.Name}}}
}

// mapDownload needs no List: a Download names its target in spec.target,
// so a Download that appears -- before anything has set the ref, which is
// the whole point of deriving the ref from the Download -- reaches its
// Album directly.
func (r *Reconciler) mapDownload(_ context.Context, o client.Object) []reconcile.Request {
	dl, ok := o.(*downloadv1alpha1.Download)
	if !ok || dl.Spec.Target.Kind != commonv1.MediaKindAlbum {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: dl.Namespace, Name: dl.Spec.Target.Name}}}
}

// activeDownload is the Download status.activeDownloadRef names, derived
// level-style (gap-fix ruling R-5, which makes this reconciler the field's
// only writer): the oldest Download targeting this Album that it owns and
// that rollup.DownloadNonTerminal still counts, or nil. Ownership is by UID,
// so a Download left behind by a deleted Album of the same name is never
// adopted.
func (r *Reconciler) activeDownload(ctx context.Context, alb *catalogv1alpha1.Album) (*downloadv1alpha1.Download, error) {
	var list downloadv1alpha1.DownloadList
	if err := r.List(ctx, &list, client.InNamespace(alb.Namespace), client.MatchingFields{downloadByAlbumIndexKey: alb.Name}); err != nil {
		return nil, err
	}
	return rollup.ActiveDownload(list.Items, func(d *downloadv1alpha1.Download) bool {
		return k8s.IsOwnedBy(d, alb)
	}), nil
}

// mapQualityProfile is the reverse direction from an edited QualityProfile
// to every Album ranked against it: those naming it through their own
// spec.qualityProfileRef override (albumByQualityProfileIndexKey), and those
// without an override whose Artist names it -- AlbumSpec.QualityProfileRef
// "overrides the Artist's QualityProfile", so an Album with an override of
// its own is ranked against that, whatever its Artist names. QualityProfile
// is CLUSTER-scoped while Album and Artist are namespaced, so neither List
// carries client.InNamespace, the same shape as episode.mapQualityProfile's
// own first hop.
func (r *Reconciler) mapQualityProfile(ctx context.Context, o client.Object) []reconcile.Request {
	qp, ok := o.(*catalogv1alpha1.QualityProfile)
	if !ok {
		return nil
	}
	seen := map[types.NamespacedName]bool{}
	var reqs []reconcile.Request
	add := func(alb catalogv1alpha1.Album) {
		key := types.NamespacedName{Namespace: alb.Namespace, Name: alb.Name}
		if !seen[key] {
			seen[key] = true
			reqs = append(reqs, reconcile.Request{NamespacedName: key})
		}
	}

	var overriding catalogv1alpha1.AlbumList
	if err := r.List(ctx, &overriding, client.MatchingFields{albumByQualityProfileIndexKey: qp.Name}); err == nil {
		for _, alb := range overriding.Items {
			add(alb)
		}
	}

	var artists catalogv1alpha1.ArtistList
	if err := r.List(ctx, &artists); err != nil {
		return reqs
	}
	for _, a := range artists.Items {
		if a.Spec.QualityProfileRef != qp.Name {
			continue
		}
		for _, alb := range r.albumsOf(ctx, &a) {
			if ptr.Deref(alb.Spec.QualityProfileRef, "") == "" {
				add(alb)
			}
		}
	}
	return reqs
}

// mapArtist wakes every Album of an Artist whose spec changed: an Album
// inherits its quality profile from the Artist when it has no override, and
// always takes its release selection's accepted statuses from the Artist's
// metadata profile.
func (r *Reconciler) mapArtist(ctx context.Context, o client.Object) []reconcile.Request {
	a, ok := o.(*catalogv1alpha1.Artist)
	if !ok {
		return nil
	}
	albums := r.albumsOf(ctx, a)
	reqs := make([]reconcile.Request, 0, len(albums))
	for _, alb := range albums {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: alb.Namespace, Name: alb.Name}})
	}
	return reqs
}

// albumsOf lists a's Albums by filtering a namespaced List on spec.artistRef
// in Go -- see SetupWithManager for why not through an index.
func (r *Reconciler) albumsOf(ctx context.Context, a *catalogv1alpha1.Artist) []catalogv1alpha1.Album {
	var albums catalogv1alpha1.AlbumList
	if err := r.List(ctx, &albums, client.InNamespace(a.Namespace)); err != nil {
		return nil
	}
	var out []catalogv1alpha1.Album
	for _, alb := range albums.Items {
		if alb.Spec.ArtistRef == a.Name {
			out = append(out, alb)
		}
	}
	return out
}

// Reconcile implements the §8.8 skeleton: get, split on deletion, ensure the
// finalizer WITHOUT an early return, then reconcileNormal.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "album.Reconcile")
	defer span.End()
	if r.OnReconcile != nil {
		r.OnReconcile()
	}
	var alb catalogv1alpha1.Album
	if err := r.Get(ctx, req.NamespacedName, &alb); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if k8s.IsDeleting(&alb) {
		return r.reconcileDelete(ctx, &alb)
	}
	name, err := k8s.FinalizerFor(&alb, r.Scheme)
	if err != nil {
		return ctrl.Result{}, reconcile.TerminalError(err)
	}
	if _, err := k8s.EnsureFinalizer(ctx, r.Client, &alb, name); err != nil {
		return ctrl.Result{}, err
	}
	return r.reconcileNormal(ctx, &alb)
}

func (r *Reconciler) reconcileDelete(ctx context.Context, alb *catalogv1alpha1.Album) (ctrl.Result, error) {
	name, err := k8s.FinalizerFor(alb, r.Scheme)
	if err != nil {
		return ctrl.Result{}, reconcile.TerminalError(err)
	}
	// The deleted event goes out before the finalizer comes off, so a failed
	// removal re-announces it under the same envelope id rather than losing
	// it; an object that never held the finalizer never reaches here.
	if controllerutil.ContainsFinalizer(alb, name) {
		r.publishItem(ctx, alb, events.ActionDeleted, time.Now().UTC())
	}
	if _, err := k8s.RemoveFinalizer(ctx, r.Client, alb, name); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// reconcileNormal: metadata staleness first (own MetadataTask, movie/series/
// artist shape), then the owning Artist and its RootFolder for path
// (series shape, one hop further), then the track-listing RPC sync and the
// file/download rollup (episode shape) last -- an RPC or lookup failure in
// either of those last two surfaces on a condition without blocking the
// rest of this same status patch, per §8.1's ordering.
func (r *Reconciler) reconcileNormal(ctx context.Context, alb *catalogv1alpha1.Album) (ctrl.Result, error) {
	now := time.Now().UTC()
	monitored := ptr.Deref(alb.Spec.Monitored, true)
	conditions := append([]metav1.Condition(nil), alb.Status.Conditions...)
	// The DLQ projector's clustarr.io/dead-lettered annotation becomes the
	// DeadLettered condition here, on the one slice every status apply below
	// declares -- early returns included -- so no apply releases it.
	k8s.MarkDeadLettered(alb, &conditions)

	// Announced before any apply, because the first apply records
	// observedGeneration and so consumes the edge (rollup.ItemAction).
	if action := rollup.ItemAction(alb.Generation, alb.Status.ObservedGeneration, alb.Status.ObservedGeneration != 0); action != "" {
		r.publishItem(ctx, alb, action, now)
	}

	statusAC := catalogac.AlbumStatus().WithObservedGeneration(alb.Generation)

	stale := alb.Status.Metadata == nil
	if !stale {
		ttl := pkgmetadata.RefreshTTL(commonv1.MediaKindAlbum, pkgmetadata.RefreshStateActive, alb.Status.Metadata.RefreshedAt.Time)
		stale = now.Sub(alb.Status.Metadata.RefreshedAt.Time) >= ttl
	}
	metaReady := !stale

	if stale {
		envKey := alb.Namespace + "/" + alb.Name
		mediaKey := events.MediaKey(string(commonv1.MediaKindAlbum), alb.Namespace, alb.Name)
		schemaName, data, err := schema.Encode(schema.MetadataTask{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindAlbum, Name: alb.Name},
		})
		if err != nil {
			return ctrl.Result{}, err
		}
		env := &events.Envelope{
			ID:     events.MsgIDForObject(string(alb.UID), alb.Generation, "metadata"),
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
				k8s.MarkTrue(alb, &conditions, conditionQueueFull, "QueueFull", "metadata work queue is full")
				statusAC = reassertKnownStatus(statusAC, alb)
				statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)
				if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Album(alb.Name, alb.Namespace).WithStatus(statusAC)); err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{RequeueAfter: time.Minute}, nil
			}
			return ctrl.Result{}, pubErr
		}
		k8s.MarkFalse(alb, &conditions, conditionQueueFull, "Published", "metadata task published")
		k8s.MarkFalse(alb, &conditions, catalogv1alpha1.AlbumConditionMetadataReady, "Refreshing", "metadata refresh requested")
	} else {
		k8s.MarkTrue(alb, &conditions, catalogv1alpha1.AlbumConditionMetadataReady, k8s.ReasonReconciled, "metadata is fresh")
	}

	var artistObj catalogv1alpha1.Artist
	if err := r.Get(ctx, types.NamespacedName{Namespace: alb.Namespace, Name: alb.Spec.ArtistRef}, &artistObj); err != nil {
		if apierrors.IsNotFound(err) {
			k8s.MarkFalse(alb, &conditions, k8s.ConditionReady, "ArtistNotFound", "artist %q not found", alb.Spec.ArtistRef)
			statusAC = reassertKnownStatus(statusAC, alb)
			statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)
			if _, perr := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Album(alb.Name, alb.Namespace).WithStatus(statusAC)); perr != nil {
				return ctrl.Result{}, perr
			}
			return ctrl.Result{RequeueAfter: time.Minute}, nil
		}
		return ctrl.Result{}, err
	}

	if alb.Status.Metadata != nil && artistObj.Status.Metadata != nil && artistObj.Status.Path != "" {
		var rf catalogv1alpha1.RootFolder
		if err := r.Get(ctx, types.NamespacedName{Namespace: alb.Namespace, Name: artistObj.Spec.RootFolderRef}, &rf); err != nil {
			if apierrors.IsNotFound(err) {
				k8s.MarkFalse(alb, &conditions, k8s.ConditionReady, "RootFolderNotFound", "rootFolder %q not found", artistObj.Spec.RootFolderRef)
				statusAC = reassertKnownStatus(statusAC, alb)
				statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)
				if _, perr := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Album(alb.Name, alb.Namespace).WithStatus(statusAC)); perr != nil {
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
			Kind:       commonv1.MediaKindAlbum,
			ArtistName: artistObj.Status.Metadata.Name,
			ArtistMbID: artistObj.Spec.MusicBrainzID,
			AlbumTitle: alb.Status.Metadata.Title,
			AlbumMbID:  alb.Spec.ReleaseGroupID,
			Year:       ReleaseYear(alb.Status.Metadata.ReleaseDate),
		}
		pth, err := Path(artistObj.Status.Path, eng, nctx)
		if err != nil {
			return ctrl.Result{}, err
		}
		statusAC = statusAC.WithPath(pth)
	}

	// Every MediaFile of this Album, whole-album and per-track alike: the
	// per-track ones (spec.mediaRef.track) decide which release has the
	// most files and fill status.tracks[].fileRef; all of them feed the
	// whole-album rollup below.
	var mfList catalogv1alpha1.MediaFileList
	if err := r.List(ctx, &mfList, client.InNamespace(alb.Namespace), client.MatchingFields{mediaFileByAlbumIndexKey: alb.Name}); err != nil {
		return ctrl.Result{}, err
	}
	files := FilesByRecording(mfList.Items)

	// Track-listing RPC sync and release selection (per §8.1's ordering): a
	// failure surfaces on conditionTracksSynced without blocking the patch.
	ts := r.syncTracks(ctx, alb, artistObj.Spec.MetadataProfile, files)
	tracksSynced := false
	switch {
	case ts.err != nil:
		k8s.MarkFalse(alb, &conditions, conditionTracksSynced, "RPCError", "track listing RPC failed: %s", ts.err.Error())
	case ts.selection == SelectionPinnedReleaseMissing:
		k8s.MarkFalse(alb, &conditions, conditionTracksSynced, string(ts.selection),
			"spec.releaseID %q is not a release of this group, and spec.anyReleaseOk is false", ptr.Deref(alb.Spec.ReleaseID, ""))
	case ts.selection == SelectionNoAcceptedRelease:
		k8s.MarkFalse(alb, &conditions, conditionTracksSynced, string(ts.selection),
			"no release of this group has tracks and a status the artist's metadata profile accepts (releaseStatuses %v)",
			artistObj.Spec.MetadataProfile.ReleaseStatuses)
	case ts.selected == "":
		tracksSynced = true
		k8s.MarkTrue(alb, &conditions, conditionTracksSynced, string(ts.selection), "the release group lists no releases yet")
	default:
		tracksSynced = true
		k8s.MarkTrue(alb, &conditions, conditionTracksSynced, string(ts.selection), "tracks taken from release %s", ts.selected)
	}
	if ts.truncated {
		k8s.MarkTrue(alb, &conditions, catalogv1alpha1.AlbumConditionInvalid, "TooManyTracks", "the selected release has more than %d tracks", maxTracks)
	} else {
		k8s.MarkFalse(alb, &conditions, catalogv1alpha1.AlbumConditionInvalid, k8s.ReasonReconciled, "track count within limits")
	}
	statusAC = statusAC.WithTracks(ts.tracks...)
	statusAC = statusAC.WithTrackFileCount(countTracksWithFile(ts.tracks))
	if md := selectedReleaseAC(alb, ts.selected); md != nil {
		statusAC = statusAC.WithMetadata(md)
	}

	// File/download rollup (last step): phase, quality and cutoff from every
	// file of the album, its lowest quality deciding (FileState, Lidarr's
	// CutoffSpecification).
	profile, profileProblem, err := r.resolveProfile(ctx, alb, &artistObj)
	if err != nil {
		return ctrl.Result{}, err
	}
	if profileProblem != "" {
		logging.FromContext(ctx).Warn("quality profile unresolved; cutoff not evaluated",
			"album", alb.Name, "namespace", alb.Namespace, "artistRef", alb.Spec.ArtistRef, "problem", profileProblem)
	}
	hasFile, fileQuality, fileFormatScore, cutoffMet := FileState(mfList.Items, profile)
	// Announced before the apply that records the new track fileRefs, so a
	// failed apply re-announces the same edges under the same envelope ids
	// (rollup.MediaFileEvent) rather than losing them.
	for _, e := range TrackFileTransitions(alb.Status.Tracks, ts.tracks, mfList.Items) {
		r.publishFile(ctx, alb, e.Action, e.File, e.MediaFile, now)
	}

	dl, err := r.activeDownload(ctx, alb)
	if err != nil {
		return ctrl.Result{}, err
	}
	overlay, active := rollup.DownloadOverlay(dl)

	phase := Phase(monitored, hasFile, cutoffMet, alb.Status.PendingGrab != nil)
	switch overlay {
	case rollup.OverlayDelayed:
		phase = catalogv1alpha1.AlbumPhaseDelayed
	case rollup.OverlayDownloading:
		phase = catalogv1alpha1.AlbumPhaseDownloading
	}
	statusAC = statusAC.WithPhase(phase)
	statusAC = statusAC.WithCutoffMet(cutoffMet)
	statusAC = statusAC.WithFormatScore(fileFormatScore)
	if fileQuality != nil {
		statusAC = statusAC.WithQuality(*fileQuality)
	}
	if active {
		statusAC = statusAC.WithActiveDownloadRef(dl.Name)
	}
	// With no Download still working on this item, WithActiveDownloadRef is
	// deliberately not called: omitting a field this manager owns releases
	// it under SSA, and since R-5 this manager is its only owner, so the
	// release removes it.

	k8s.MarkReady(alb, &conditions, metaReady && tracksSynced, k8s.ReasonReconciled, "phase=%s", phase)
	statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)

	if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Album(alb.Name, alb.Namespace).WithStatus(statusAC)); err != nil {
		return ctrl.Result{}, err
	}

	if ts.err != nil {
		return ctrl.Result{RequeueAfter: metadataSyncRPCBackoff}, nil
	}
	return ctrl.Result{}, nil
}

// reassertKnownStatus re-adds every field this manager owns besides
// ObservedGeneration/Conditions to statusAC, sourced from alb's current
// (pre-reconcile) status. Used on the three early-return paths (QueueFull,
// ArtistNotFound, RootFolderNotFound): all three are transient failures,
// and without this, PatchStatus's apply would omit every field it does not
// mention, releasing (zeroing) a healthy Album's Tracks/Phase/Path/
// TrackFileCount/Quality/FormatScore/CutoffMet/ActiveDownloadRef and
// status.metadata.selectedReleaseID the next time any of them blips. Mirrors series.reassertKnownStatus, with one
// addition: Tracks is a list whose generated WithTracks APPENDS (CLAUDE.md's
// reassertKnownStatus exception, the same shape as MediaFileStatus's
// Conditions/Sidecars), so this seeds the AC's Tracks field exactly once
// here, from alb's existing status.tracks -- the happy path in
// reconcileNormal never calls both reassertKnownStatus and its own
// WithTracks on the same statusAC, so no apply ever calls WithTracks twice.
func reassertKnownStatus(statusAC *catalogac.AlbumStatusApplyConfiguration, alb *catalogv1alpha1.Album) *catalogac.AlbumStatusApplyConfiguration {
	if len(alb.Status.Tracks) > 0 {
		tracks := make([]*catalogac.TrackApplyConfiguration, 0, len(alb.Status.Tracks))
		for _, tr := range alb.Status.Tracks {
			tc := catalogac.Track().
				WithRecordingID(tr.RecordingID).
				WithMedium(tr.Medium).
				WithNumber(tr.Number).
				WithAbsoluteNumber(tr.AbsoluteNumber).
				WithTitle(tr.Title).
				WithDurationMs(tr.DurationMs).
				WithExplicit(tr.Explicit)
			if tr.FileRef != nil {
				tc = tc.WithFileRef(*tr.FileRef)
			}
			tracks = append(tracks, tc)
		}
		statusAC = statusAC.WithTracks(tracks...)
	}
	if alb.Status.Phase != "" {
		statusAC = statusAC.WithPhase(alb.Status.Phase)
	}
	if alb.Status.Path != "" {
		statusAC = statusAC.WithPath(alb.Status.Path)
	}
	statusAC = statusAC.WithTrackFileCount(alb.Status.TrackFileCount)
	if alb.Status.Quality != nil {
		statusAC = statusAC.WithQuality(*alb.Status.Quality)
	}
	statusAC = statusAC.WithFormatScore(alb.Status.FormatScore)
	statusAC = statusAC.WithCutoffMet(alb.Status.CutoffMet)
	if alb.Status.ActiveDownloadRef != nil {
		statusAC = statusAC.WithActiveDownloadRef(*alb.Status.ActiveDownloadRef)
	}
	if alb.Status.Metadata != nil {
		if md := selectedReleaseAC(alb, alb.Status.Metadata.SelectedReleaseID); md != nil {
			statusAC = statusAC.WithMetadata(md)
		}
	}
	return statusAC
}

// selectedReleaseAC is this reconciler's one leaf inside status.metadata:
// selectedReleaseID, "the release the tracks were taken from". Everything
// else in status.metadata is the metadata gateway's
// (k8s.ManagerCatalogarrMetadata); server-side apply tracks ownership per
// leaf, so the two managers share the struct without either releasing the
// other's fields. Only this reconciler can decide the value -- it is the
// one that selects the release, from the files it watches -- so it is the
// writer, under k8s.ManagerCatalogarr like the rest of its status.
//
// It is sent only once the gateway has written status.metadata (a non-zero
// refreshedAt): applying it to an Album without metadata would create a
// status.metadata holding nothing else, which every reader of
// "status.metadata != nil" -- this reconciler's own path step among them --
// would take for fetched metadata. Returning nil omits the leaf, which
// releases it: no selection is the right value then.
func selectedReleaseAC(alb *catalogv1alpha1.Album, releaseID string) *catalogac.AlbumMetadataApplyConfiguration {
	if releaseID == "" || alb.Status.Metadata == nil || alb.Status.Metadata.RefreshedAt.IsZero() {
		return nil
	}
	return catalogac.AlbumMetadata().WithSelectedReleaseID(releaseID)
}

// trackSync is syncTracks' result: the track list to declare, the release
// it came from ("" for none) and how that release was chosen.
type trackSync struct {
	tracks    []*catalogac.TrackApplyConfiguration
	truncated bool
	selected  string
	selection Selection
	err       error
}

// syncTracks fetches alb's release group via the metadata gateway's
// EXISTING single-entity lookup (rpc.catalogarr.metadata.lookup, kind=album,
// keyed by pkgmetadata.KeyMBReleaseGroup -- the same call
// Registry.Lookup(kind=album) serves for the gateway's own status.metadata
// fetch, which browses the group's releases with their media and
// recordings; no new RPC verb needed, unlike artist.Reconciler's
// syncAlbums), selects a release per SelectRelease, and flattens it via
// BuildTracks with files' per-track fileRefs.
//
// A failed fetch keeps what the Album already has -- its track list
// (fileRefs refreshed from files) and its selected release -- and reports
// the error: this is a transient failure, and declaring an empty list would
// release a healthy listing on every blip.
func (r *Reconciler) syncTracks(ctx context.Context, alb *catalogv1alpha1.Album, profile catalogv1alpha1.MusicMetadataProfile, files map[string]string) trackSync {
	previous := ""
	if alb.Status.Metadata != nil {
		previous = alb.Status.Metadata.SelectedReleaseID
	}
	kept := func(err error) trackSync {
		return trackSync{tracks: TracksFromStatus(alb.Status.Tracks, files), selected: previous, err: err}
	}

	req := schema.MetadataRequest{
		Kind: commonv1.MediaKindAlbum,
		IDs:  map[string]string{pkgmetadata.KeyMBReleaseGroup: alb.Spec.ReleaseGroupID},
	}
	rpcCtx, cancel := context.WithTimeout(ctx, metadataSyncRPCBackoff)
	defer cancel()

	var resp schema.MetadataResponse
	if rpcErr := r.Bus.Request(rpcCtx, events.RPCMetadataLookup, req, &resp); rpcErr != nil {
		return kept(rpcErr)
	}
	if resp.Error != "" {
		return kept(fmt.Errorf("metadata gateway: %s", resp.Error))
	}
	var fetched pkgmetadata.Album
	if len(resp.Result) > 0 {
		if err := json.Unmarshal(resp.Result, &fetched); err != nil {
			return kept(fmt.Errorf("decode album: %w", err))
		}
	}

	release, selection := SelectRelease(alb.Spec, profile, previous, fetched.Releases, files)
	if release == nil {
		return trackSync{selection: selection}
	}
	built, truncated := BuildTracks(release, files)
	return trackSync{tracks: built, truncated: truncated, selected: release.IDs[pkgmetadata.KeyMBRelease], selection: selection}
}

// countTracksWithFile is status.trackFileCount's definition: the number of
// tracks whose FileRef is set, which BuildTracks and TracksFromStatus take
// from the MediaFiles addressing a single track (FilesByRecording).
func countTracksWithFile(tracks []*catalogac.TrackApplyConfiguration) int32 {
	var n int32
	for _, t := range tracks {
		if t.FileRef != nil {
			n++
		}
	}
	return n
}

// resolveProfile fetches the QualityProfile albums are ranked against:
// alb.Spec.QualityProfileRef when set, otherwise artistObj's own
// QualityProfileRef -- AlbumSpec.QualityProfileRef's own doc comment
// ("overrides the Artist's QualityProfile"). Mirrors
// episode.Reconciler.resolveProfile's degrade-rather-than-fail contract
// exactly: only a non-NotFound API error is fatal, every other outcome
// degrades to (nil, problem, nil) rather than failing the whole reconcile.
func (r *Reconciler) resolveProfile(ctx context.Context, alb *catalogv1alpha1.Album, artistObj *catalogv1alpha1.Artist) (*quality.Profile, string, error) {
	ref := ptr.Deref(alb.Spec.QualityProfileRef, "")
	if ref == "" {
		ref = artistObj.Spec.QualityProfileRef
	}
	if ref == "" {
		return nil, "no qualityProfileRef resolved (neither the album nor its artist set one)", nil
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
