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

package comic

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
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/version"
)

// conditionQueueFull mirrors movie.MovieConditionQueueFull's name and
// semantics; comic_types.go declares no ComicConditionQueueFull constant of
// its own (unlike Comic's Ready/MetadataReady/IssuesSynced, which it does
// declare), so this is a local, package-scoped equivalent rather than an
// invented API constant -- the identical rationale as series.conditionQueueFull.
const conditionQueueFull = "QueueFull"

// issueByComicRefIndexKey indexes Issue by the Comic that owns it
// (spec.comicRef), so the reconciler can List a Comic's own Issues without
// scanning the whole namespace.
const issueByComicRefIndexKey = ".spec.comicRef"

// issueSyncRPCBackoff is the RequeueAfter used when the issue-listing RPC
// fails, a concrete short backoff per §8.8 (RequeueAfter only, never a bare
// error-triggered exponential backoff for a known-transient dependency).
const issueSyncRPCBackoff = 30 * time.Second

// bus is the subset of events.Bus this reconciler actually calls: Publish
// for the metadata-staleness task and Request for the issue-listing RPC.
// Narrower than the full events.Requester per the same rationale as
// series.bus.
type bus interface {
	events.Publisher
	Request(ctx context.Context, subject string, in, out any) error
}

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=comics,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=comics/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=comics/finalizers,verbs=update
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=issues,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=issues/status,verbs=get;update;patch
// The Recorder is a k8s.io/client-go/tools/events.EventRecorder, handed in by
// mgr.GetEventRecorder, and it writes events.k8s.io/v1 -- so events.k8s.io is
// the group to grant and the core group is not; see series/reconciler.go's
// identical marker and comment for the occasion this repo learned it.
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconciler reconciles a Comic: metadata staleness (publishing a
// MetadataTask when the cache is missing or past its RefreshTTL), path and
// conditions, and the issue-listing RPC fan-out into owned Issue objects. It
// is the sole writer of status.observedGeneration, status.conditions
// (Ready/MetadataReady/IssuesSynced), status.path and status.issueFileCount;
// status.metadata belongs to the metadata gateway (k8s.ManagerCatalogarrMetadata,
// task G2-1) and this reconciler never builds a ComicStatusApplyConfiguration
// that calls WithMetadata.
//
// The per-Issue provider fields this reconciler writes
// (sourceID/title/date) are applied under the distinct
// k8s.ManagerCatalogarrFanout field manager, never k8s.ManagerCatalogarr
// (the Issue controller's own reconciler uses that one for
// State/Conditions/HasFile/etc). See this package's doc.go for the full
// reasoning.
type Reconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder k8sevents.EventRecorder
	Bus      bus

	// OnReconcile is a test-only hook, called at the top of every Reconcile.
	// It is nil-checked so production callers never need to set it.
	OnReconcile func()
}

// SetupWithManager registers the Comic controller: the finalizer/
// metadata-refresh predicate on Comic itself, and Owns(&Issue{}) guarded by
// StatusFieldChanged on HasFile so an Issue's own HasFile flip (from the
// Issue controller's MediaFile watch) re-triggers this reconciler's
// IssueFileCount rollup, without this reconciler's own writes to the SAME
// owned Issues (SourceID/Title/Date, never HasFile) looping it.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), &catalogv1alpha1.Issue{}, issueByComicRefIndexKey,
		func(o client.Object) []string {
			iss, ok := o.(*catalogv1alpha1.Issue)
			if !ok {
				return nil
			}
			return []string{iss.Spec.ComicRef}
		}); err != nil {
		return err
	}

	return ctrl.NewControllerManagedBy(mgr).
		Named("comic").
		For(&catalogv1alpha1.Comic{}, builder.WithPredicates(comicPredicate())).
		Owns(&catalogv1alpha1.Issue{}, builder.WithPredicates(k8s.StatusFieldChanged(func(o client.Object) bool {
			iss, ok := o.(*catalogv1alpha1.Issue)
			return ok && iss.Status.HasFile
		}))).
		WithOptions(controller.Options{RecoverPanic: ptr.To(true), ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}

// comicPredicate wakes this controller on a spec change (GenerationChanged)
// or on the metadata gateway's own write (StatusFieldChanged scoped to
// status.metadata.refreshedAt) -- the same self-loop-avoidance shape as the
// Series predicate.
func comicPredicate() predicate.Predicate {
	return k8s.Or(
		k8s.GenerationChanged(),
		// An annotation-only change bumps no generation.
		k8s.DeadLetteredAnnotationChanged(),
		k8s.StatusFieldChanged(func(o client.Object) metav1.Time {
			c, ok := o.(*catalogv1alpha1.Comic)
			if !ok || c.Status.Metadata == nil {
				return metav1.Time{}
			}
			return c.Status.Metadata.RefreshedAt
		}),
	)
}

// Reconcile implements the §8.8 skeleton: get, split on deletion, ensure the
// finalizer WITHOUT an early return, then reconcileNormal.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "comic.Reconcile")
	defer span.End()
	if r.OnReconcile != nil {
		r.OnReconcile()
	}
	var c catalogv1alpha1.Comic
	if err := r.Get(ctx, req.NamespacedName, &c); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if k8s.IsDeleting(&c) {
		return r.reconcileDelete(ctx, &c)
	}
	name, err := k8s.FinalizerFor(&c, r.Scheme)
	if err != nil {
		return ctrl.Result{}, reconcile.TerminalError(err)
	}
	if _, err := k8s.EnsureFinalizer(ctx, r.Client, &c, name); err != nil {
		return ctrl.Result{}, err
	}
	return r.reconcileNormal(ctx, &c)
}

// reconcileDelete removes the finalizer. Owned Issues are garbage collected
// by the apiserver via their controller reference; there is nothing else
// owned outside Kubernetes at this phase.
func (r *Reconciler) reconcileDelete(ctx context.Context, c *catalogv1alpha1.Comic) (ctrl.Result, error) {
	name, err := k8s.FinalizerFor(c, r.Scheme)
	if err != nil {
		return ctrl.Result{}, reconcile.TerminalError(err)
	}
	// The deleted event goes out before the finalizer comes off, so a failed
	// removal re-announces it under the same envelope id rather than losing
	// it; an object that never held the finalizer never reaches here.
	if controllerutil.ContainsFinalizer(c, name) {
		r.publishItem(ctx, c, events.ActionDeleted, time.Now().UTC())
	}
	if _, err := k8s.RemoveFinalizer(ctx, r.Client, c, name); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// reconcileNormal mirrors the Series reconciler's structure (metadata
// staleness decided and a MetadataTask published when needed, path computed
// once metadata is cached), then adds the issue-listing RPC fan-out as the
// last step, per §8.1's ordering: an RPC failure surfaces on
// IssuesSynced/Ready but never blocks the rest of this same status patch
// from landing. A ComicVine and a MangaDex Comic take the same path; only
// the id the issue listing is keyed by differs (sourceIDs).
func (r *Reconciler) reconcileNormal(ctx context.Context, c *catalogv1alpha1.Comic) (ctrl.Result, error) {
	now := time.Now().UTC()
	conditions := append([]metav1.Condition(nil), c.Status.Conditions...)
	// The DLQ projector's clustarr.io/dead-lettered annotation becomes the
	// DeadLettered condition here, on the one slice every status apply below
	// declares -- early returns included -- so no apply releases it.
	k8s.MarkDeadLettered(c, &conditions)

	// Announced before any apply, because the first apply records
	// observedGeneration and so consumes the edge (rollup.ItemAction).
	if action := rollup.ItemAction(c.Generation, c.Status.ObservedGeneration, c.Status.ObservedGeneration != 0); action != "" {
		r.publishItem(ctx, c, action, now)
	}
	statusAC := catalogac.ComicStatus().WithObservedGeneration(c.Generation)

	stale := c.Status.Metadata == nil
	if !stale {
		// RefreshStateOngoing, not a bucket derived from c.Status.Metadata:
		// ComicMetadata carries no run-status field to derive one from (see
		// this package's doc.go and app/catalog/metadata/refreshstate.go's
		// comicRefreshState, which the metadata worker uses on the FRESHLY
		// FETCHED provider value it has and this reconciler does not). Both
		// sides agree today because pkg/metadata/clients/comicvine.Client.
		// Volume does not map ComicVine's status field yet (the TODO on
		// Volume) so the worker's own comicRefreshState always answers
		// RefreshStateOngoing too -- an honest reflection of the data
		// actually available, not a mismatch to work around here.
		ttl := metadata.RefreshTTL(commonv1.MediaKindComic, metadata.RefreshStateOngoing, c.Status.Metadata.RefreshedAt.Time)
		stale = now.Sub(c.Status.Metadata.RefreshedAt.Time) >= ttl
	}
	metaReady := !stale

	if stale {
		envKey := c.Namespace + "/" + c.Name
		mediaKey := events.MediaKey(string(commonv1.MediaKindComic), c.Namespace, c.Name)
		schemaName, data, err := schema.Encode(schema.MetadataTask{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindComic, Name: c.Name},
		})
		if err != nil {
			return ctrl.Result{}, err
		}
		env := &events.Envelope{
			ID:     events.MsgIDForObject(string(c.UID), c.Generation, "metadata"),
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
				k8s.MarkTrue(c, &conditions, conditionQueueFull, "QueueFull", "metadata work queue is full")
				statusAC = reassertKnownStatus(statusAC, c)
				statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)
				if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Comic(c.Name, c.Namespace).WithStatus(statusAC)); err != nil {
					return ctrl.Result{}, err
				}
				return ctrl.Result{RequeueAfter: time.Minute}, nil
			}
			return ctrl.Result{}, pubErr
		}
		k8s.MarkFalse(c, &conditions, conditionQueueFull, "Published", "metadata task published")
		k8s.MarkFalse(c, &conditions, catalogv1alpha1.ComicConditionMetadataReady, "Refreshing", "metadata refresh requested")
	} else {
		k8s.MarkTrue(c, &conditions, catalogv1alpha1.ComicConditionMetadataReady, k8s.ReasonReconciled, "metadata is fresh")
	}

	if c.Status.Metadata != nil {
		var rf catalogv1alpha1.RootFolder
		if err := r.Get(ctx, types.NamespacedName{Namespace: c.Namespace, Name: c.Spec.RootFolderRef}, &rf); err != nil {
			if apierrors.IsNotFound(err) {
				k8s.MarkFalse(c, &conditions, k8s.ConditionReady, "RootFolderNotFound", "rootFolder %q not found", c.Spec.RootFolderRef)
				statusAC = reassertKnownStatus(statusAC, c)
				statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)
				if _, perr := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Comic(c.Name, c.Namespace).WithStatus(statusAC)); perr != nil {
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
			Kind:             commonv1.MediaKindComic,
			ComicSeriesTitle: c.Status.Metadata.Title,
		}
		pth, err := Path(rf.Spec.Path, c.Spec.Folder, eng, nctx)
		if err != nil {
			return ctrl.Result{}, err
		}
		statusAC = statusAC.WithPath(pth)
	}

	// Issue-listing RPC fan-out (last step, per §8.1): a failure here
	// surfaces on IssuesSynced/Ready without blocking the patch above.
	// issuesSyncedBefore reads the object's PRE-reconcile condition (never
	// the local, in-progress `conditions` copy, which has not touched
	// IssuesSynced yet at this point regardless) -- see DesiredIssues' doc
	// comment for why this stands in for Series' status.addOptionsApplied.
	issuesSyncedBefore := k8s.IsConditionTrue(c.Status.Conditions, catalogv1alpha1.ComicConditionIssuesSynced)
	issuesSynced, issues, syncErr := r.syncIssues(ctx, c, issuesSyncedBefore)
	if syncErr != nil {
		k8s.MarkFalse(c, &conditions, catalogv1alpha1.ComicConditionIssuesSynced, "RPCError", "issue listing RPC failed: %s", syncErr.Error())
	} else {
		k8s.MarkTrue(c, &conditions, catalogv1alpha1.ComicConditionIssuesSynced, k8s.ReasonReconciled, "issues synced")
	}

	var issueFileCount int32
	for _, iss := range issues {
		if iss.Status.HasFile {
			issueFileCount++
		}
	}
	statusAC = statusAC.WithIssueFileCount(issueFileCount)

	k8s.MarkReady(c, &conditions, metaReady && issuesSynced, k8s.ReasonReconciled, "metadataReady=%t issuesSynced=%t", metaReady, issuesSynced)
	statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)

	if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, catalogac.Comic(c.Name, c.Namespace).WithStatus(statusAC)); err != nil {
		return ctrl.Result{}, err
	}

	if syncErr != nil {
		return ctrl.Result{RequeueAfter: issueSyncRPCBackoff}, nil
	}
	return ctrl.Result{}, nil
}

// reassertKnownStatus re-adds every field this manager owns besides
// ObservedGeneration/Conditions to statusAC, sourced from c's current
// (pre-reconcile) status. Used on every early-return path (QueueFull,
// RootFolderNotFound): each is a transient failure, and without this,
// PatchStatus's apply would omit every field it does not mention, releasing
// (zeroing) a healthy Comic's Path/IssueFileCount/NextPullDate the next time
// either happens.
// Same mechanism as series.reassertKnownStatus; see CLAUDE.md's "Gotchas
// found the hard way" for the general rule and its cost history.
func reassertKnownStatus(statusAC *catalogac.ComicStatusApplyConfiguration, c *catalogv1alpha1.Comic) *catalogac.ComicStatusApplyConfiguration {
	if c.Status.Path != "" {
		statusAC = statusAC.WithPath(c.Status.Path)
	}
	statusAC = statusAC.WithIssueFileCount(c.Status.IssueFileCount)
	if c.Status.NextPullDate != nil {
		statusAC = statusAC.WithNextPullDate(*c.Status.NextPullDate)
	}
	return statusAC
}

// syncIssues lists c's currently owned Issues, requests its issue list from
// the metadata gateway (lookupIssues, app/catalog/metadata/rpc.go, task
// f665aa9), ensures each desired Issue exists with its provider-sourced
// status fields, and returns the full (pre-fan-out) owned Issue list for the
// IssueFileCount rollup -- self-correcting: a newly created Issue's own
// Create event passes Owns()'s StatusFieldChanged predicate (Creates always
// pass), so a rollup based on the pre-fan-out list here is completed by the
// very next reconcile it triggers, rather than needing a second List call
// against a cache that may not yet see this pass's own creates. This is
// series.syncEpisodes' exact shape.
func (r *Reconciler) syncIssues(ctx context.Context, c *catalogv1alpha1.Comic, issuesSyncedBefore bool) (synced bool, existing []catalogv1alpha1.Issue, err error) {
	var issueList catalogv1alpha1.IssueList
	if err := r.List(ctx, &issueList, client.InNamespace(c.Namespace), client.MatchingFields{issueByComicRefIndexKey: c.Name}); err != nil {
		return false, nil, err
	}
	existingNames := make(map[string]bool, len(issueList.Items))
	for _, iss := range issueList.Items {
		existingNames[iss.Name] = true
	}

	req := schema.MetadataRequest{
		Kind: commonv1.MediaKindIssue,
		IDs:  map[string]string{SourceKey(c.Spec.Source): c.Spec.SourceID},
	}
	rpcCtx, cancel := context.WithTimeout(ctx, issueSyncRPCBackoff)
	defer cancel()

	var resp schema.MetadataResponse
	if rpcErr := r.Bus.Request(rpcCtx, events.RPCMetadataLookup, req, &resp); rpcErr != nil {
		return false, issueList.Items, rpcErr
	}
	if resp.Error != "" {
		return false, issueList.Items, fmt.Errorf("metadata gateway: %s", resp.Error)
	}

	fetched := make([]metadata.ComicIssue, 0, len(resp.Results))
	for _, raw := range resp.Results {
		var iss metadata.ComicIssue
		if err := json.Unmarshal(raw, &iss); err != nil {
			return false, issueList.Items, fmt.Errorf("decode issue: %w", err)
		}
		fetched = append(fetched, iss)
	}

	desired := DesiredIssues(c, issuesSyncedBefore, existingNames, fetched)
	for _, d := range desired {
		if err := r.ensureIssue(ctx, c, d); err != nil {
			return false, issueList.Items, err
		}
	}
	return true, issueList.Items, nil
}

// ensureIssue gets or creates the Issue named d.Name, then patches its
// provider-sourced status fields under k8s.ManagerCatalogarrFanout.
// spec.monitored is only ever set on Create (d.Monitored != nil there per
// DesiredIssues' contract); an already-existing Issue's spec.monitored is
// never touched, since it belongs to the user after creation.
//
// SourceID and Title are sent unconditionally (plain scalars in
// metadata.ComicIssue, so there is no way to tell "the provider has no
// title for this issue" apart from "the provider's title happens to be
// empty" -- both look the same once they reach DesiredIssue), the same
// policy series.ensureEpisode uses for Title/Overview/RuntimeMinutes/TvdbID.
// Date keeps a nil-guard: DesiredIssue.Date is a *time.Time because
// metadata.ComicIssue's CoverDate/StoreDate are themselves pointers, so the
// provider DOES distinguish "no date for this issue" from a real value, and
// omitting the field when nil deliberately releases (clears) a previously
// cached value under SSA once the provider stops sending one -- series'
// AirDate/AbsoluteNumber guard, and movie's ActiveDownloadRef clearing on
// !active, are the same documented convention.
func (r *Reconciler) ensureIssue(ctx context.Context, c *catalogv1alpha1.Comic, d DesiredIssue) error {
	var iss catalogv1alpha1.Issue
	key := types.NamespacedName{Namespace: c.Namespace, Name: d.Name}
	err := r.Get(ctx, key, &iss)
	switch {
	case apierrors.IsNotFound(err):
		iss = catalogv1alpha1.Issue{
			ObjectMeta: metav1.ObjectMeta{Name: d.Name, Namespace: c.Namespace},
			Spec: catalogv1alpha1.IssueSpec{
				ComicRef:               c.Name,
				Number:                 d.Number,
				CalculatedNumberCentis: d.CalculatedNumberCentis,
			},
		}
		if d.Monitored != nil {
			iss.Spec.Monitored = d.Monitored
		}
		if err := k8s.SetControllerReference(c, &iss, r.Scheme); err != nil {
			return err
		}
		if err := r.Create(ctx, &iss); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
	case err != nil:
		return err
	}

	// A provider answer without an id under the comic's source key (a
	// ComicVine volume listed by Metron, which keys issues by its own id;
	// a MangaDex volume, which has none) keeps the id this Issue already
	// carries rather than clearing it: the field is a complete declaration
	// of this manager's set, so sending "" would release a known id.
	sourceID := d.SourceID
	if sourceID == "" {
		sourceID = iss.Status.SourceID
	}
	statusAC := catalogac.IssueStatus().
		WithSourceID(sourceID).
		WithTitle(d.Title)
	if d.Date != nil {
		statusAC = statusAC.WithDate(metav1.NewTime(*d.Date))
	}

	// k8s.ManagerCatalogarrFanout, not k8s.ManagerCatalogarr: this
	// reconciler writes an Issue it owns but does not itself compute the
	// acquisition state for -- see this package's doc.go and
	// k8s.ManagerCatalogarrFanout's own doc comment for the full reasoning.
	_, err = k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarrFanout, catalogac.Issue(d.Name, c.Namespace).WithStatus(statusAC))
	return err
}
