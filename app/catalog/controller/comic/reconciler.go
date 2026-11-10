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
	"github.com/mediactl/clustarr/app/catalog/metadatainputs"
	"github.com/mediactl/clustarr/app/remediation/childsync"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/naming"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
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

// issueSyncKey is the Comic's childsync key: its spec generation, its
// metadata refresh and whether spec.addOptions were applied (the first
// complete fan-out flips the flag, so it is part of the key).
func issueSyncKey(c *catalogv1alpha1.Comic, addOptionsApplied bool) childsync.Key {
	var refreshed time.Time
	if c.Status.Metadata != nil {
		refreshed = c.Status.Metadata.RefreshedAt.Time
	}
	return childsync.KeyAt(c.Generation, refreshed, addOptionsApplied)
}

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
// conditions, the issue-listing RPC fan-out into owned Issue objects
// (behind the childsync memo, so a pass that moved none of its inputs fetches
// nothing) and the rollup of its Issues. It is the sole writer of
// status.observedGeneration, status.conditions (Ready/MetadataReady/
// IssuesSynced), status.path, status.issueFileCount,
// status.downloadingIssueCount, status.addOptionsApplied, the
// ContainerRollup fields (phase, the child counts, the release dates;
// 2026-11-10 design §4.1, §5.2) and status.nextPullDate (D10);
// status.metadata belongs to the metadata gateway (k8s.ManagerCatalogMetadata,
// task G2-1) and this reconciler never builds a ComicStatusApplyConfiguration
// that calls WithMetadata.
//
// spec.addOptions is applied once (D10): the first complete fan-out gives
// each Issue spec.addOptions.monitor's answer (DesiredIssues,
// InitialIssueMonitored) and the pass records status.addOptionsApplied,
// after which a new issue follows spec.monitorNewIssues. The
// searchForMissing half is read as the Series reads its own, which is as
// the search planner does: a monitored Issue never searched gets its one
// live search, and a Comic with two or more missing is asked for as a
// volume pack first (W1.3 of the design's plan), so an added comic is
// searched without this reconciler asking; nothing reads a false yet
// (neither here nor for a Series).
//
// The per-Issue provider fields this reconciler writes
// (sourceID/title/date) are applied under the distinct
// k8s.ManagerCatalogFanout field manager, never k8s.ManagerCatalog
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

	// issueSync remembers each Comic's last complete fan-out in this
	// process; its zero value is ready to use.
	issueSync childsync.Memo
}

// RegisterIndexes registers the Issue-by-spec.comicRef index under its
// existing name. The remediation loop registers it once
// (remediation.RegisterIndexes) since the Comic became an item key
// (ADR-0019 A3.2).
func RegisterIndexes(ctx context.Context, idx client.FieldIndexer) error {
	return idx.IndexField(ctx, &catalogv1alpha1.Issue{}, issueByComicRefIndexKey,
		func(o client.Object) []string {
			iss, ok := o.(*catalogv1alpha1.Issue)
			if !ok {
				return nil
			}
			return []string{iss.Spec.ComicRef}
		})
}

// IssueByComicRefIndex is the field index of Issues by spec.comicRef, for
// other readers of the cache.
const IssueByComicRefIndex = issueByComicRefIndexKey

// Watches is the Comic's item path on the remediation loop (ADR-0019 §6.1,
// A3.2): the Comic itself (a spec change, the metadata gateway's write, the
// DLQ annotation, a person's download intent) and its Issues by
// spec.comicRef when one's rollup inputs move (issueRollupKey) -- never
// this reconciler's own writes to them (SourceID/Title/Date), which would
// loop it. The controller named "comic" is gone; the recorder keeps the
// name.
func (r *Reconciler) Watches() []rollup.Watch {
	return []rollup.Watch{
		{Object: &catalogv1alpha1.Comic{}, Map: rollup.Self, Predicates: []predicate.Predicate{
			k8s.Or(comicPredicate(), k8s.AnnotationsChanged(catalogv1alpha1.DownloadIntentAnnotations()...)),
		}},
		{Object: &catalogv1alpha1.Issue{}, Map: mapIssueToComic, Predicates: []predicate.Predicate{issueRollupChanged()}},
	}
}

var _ rollup.Item = (*Reconciler)(nil)

// issueRollupKey is what of an Issue the Comic's rollup reads: its file and
// its download phase (issueFileCount, downloadingIssueCount), its phase
// (the ContainerRollup's counts) and its own monitor flag (which child
// counts toward the release dates). Its dates are left out on purpose: the
// fan-out writes them, and reads what it wrote in the same pass.
type issueRollupKey struct {
	hasFile       bool
	downloadPhase commonv1.DownloadPhase
	phase         commonv1.LeafPhase
	monitored     bool
}

// issueRollupChanged wakes the Comic when an Issue's rollup inputs change.
func issueRollupChanged() predicate.Predicate {
	return k8s.StatusFieldChanged(func(o client.Object) issueRollupKey {
		iss, ok := o.(*catalogv1alpha1.Issue)
		if !ok {
			return issueRollupKey{}
		}
		return issueRollupKey{
			hasFile: iss.Status.HasFile, downloadPhase: iss.Status.DownloadPhase,
			phase: iss.Status.Phase, monitored: ptr.Deref(iss.Spec.Monitored, true),
		}
	})
}

// mapIssueToComic maps an Issue to the Comic its spec.comicRef names.
func mapIssueToComic(_ context.Context, o client.Object) []reconcile.Request {
	iss, ok := o.(*catalogv1alpha1.Issue)
	if !ok || iss.Spec.ComicRef == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: iss.Namespace, Name: iss.Spec.ComicRef}}}
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

// ReconcileItem is the Comic's item path on the remediation loop (ADR-0019
// A3.2); it keeps the §8.8 skeleton: get, split on deletion, ensure the
// finalizer WITHOUT an early return, then reconcileNormal.
func (r *Reconciler) ReconcileItem(ctx context.Context, nn types.NamespacedName) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "comic.Reconcile")
	defer span.End()
	if r.OnReconcile != nil {
		r.OnReconcile()
	}
	var c catalogv1alpha1.Comic
	if err := r.Get(ctx, nn, &c); err != nil {
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
	r.issueSync.Forget(c.UID)
	// The grabs go first (ADR-0019 §6.8): while the downloads stage moves
	// every entry to Removing, the pass applies its contribution over the
	// stored status -- the complete declaration, so nothing is released --
	// and the Comic's own finalizer waits for FinalizerTransfers.
	if itempass.HasContribution(itempass.From(ctx)) {
		statusAC := reassertKnownStatus(catalogac.ComicStatus().WithObservedGeneration(c.Status.ObservedGeneration), c)
		statusAC = statusAC.WithConditions(k8s.ConditionACs(c.Status.Conditions)...)
		if conflicted, err := itemstatus.Apply(ctx, r.Client, c, catalogac.Comic(c.Name, c.Namespace).WithStatus(withEntries(ctx, statusAC, c))); err != nil || conflicted {
			return itemstatus.Requeue(conflicted), err
		}
	}
	if controllerutil.ContainsFinalizer(c, catalogv1alpha1.FinalizerTransfers) {
		return ctrl.Result{}, nil
	}
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
	monitored := ptr.Deref(c.Spec.Monitored, true)
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
		ttl := metadata.RefreshTTL(commonv1.MediaKindComic, comicRefreshState(c), c.Status.Metadata.RefreshedAt.Time)
		stale = now.Sub(c.Status.Metadata.RefreshedAt.Time) >= ttl
	}
	metaReady := !stale
	// A recent document the gateway wrote before it learned a field is
	// refreshed once, at once, and the item stays ready meanwhile: every
	// item in an upgraded library would otherwise drop to Pending together
	// (Movie's rule; the 2026-11-10 design D8 gives every kind a
	// schemaVersion).
	outdated := !stale && c.Status.Metadata.SchemaVersion < metadata.SchemaVersion

	if stale || outdated {
		// The task carries what the refresh asks for -- the item and its
		// inputs, answered in clustarr-item-metadata (ADR-0019 §7.1) -- under
		// Msg-Id metadata/<uid>/<inputsHash>.
		subject, env, err := metadatainputs.Task(c, metadatainputs.For(c), events.PriorityNormal, now)
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
					k8s.MarkTrue(c, &conditions, conditionQueueFull, "QueueFull", "metadata work queue is full")
					statusAC = reassertKnownStatus(statusAC, c)
					statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)
					if conflicted, err := itemstatus.Apply(ctx, r.Client, c, catalogac.Comic(c.Name, c.Namespace).WithStatus(withEntries(ctx, statusAC, c))); err != nil || conflicted {
						return itemstatus.Requeue(conflicted), err
					}
					return ctrl.Result{RequeueAfter: time.Minute}, nil
				}
				return ctrl.Result{}, pubErr
			}
			itempass.Published(itempass.From(ctx), metadatainputs.Durable, seq)
			k8s.MarkFalse(c, &conditions, conditionQueueFull, "Published", "metadata task published")
		}
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
				if conflicted, perr := itemstatus.Apply(ctx, r.Client, c, catalogac.Comic(c.Name, c.Namespace).WithStatus(withEntries(ctx, statusAC, c))); perr != nil || conflicted {
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
	// surfaces on IssuesSynced/Ready without blocking the patch above. The
	// fan-out reads the object's PRE-reconcile status.addOptionsApplied
	// (DesiredIssues): the add-time monitor mode until one complete fan-out
	// has applied it.
	issuesSynced, issues, syncErr := r.syncIssues(ctx, c, now)
	if syncErr != nil {
		k8s.MarkFalse(c, &conditions, catalogv1alpha1.ComicConditionIssuesSynced, events.ClassifyRequestError(syncErr).Reason(), "issue listing RPC failed: %s", syncErr.Error())
	} else {
		k8s.MarkTrue(c, &conditions, catalogv1alpha1.ComicConditionIssuesSynced, k8s.ReasonReconciled, "issues synced")
	}
	// Applied once (2026-11-10 design D10): true from the first complete
	// fan-out on, and sent on every apply after that -- server-side apply
	// releases a field a manager stops sending (movie.Reconciler's note on
	// the same flag). A first pass whose fan-out failed leaves it false, so
	// the next one still applies the add-time mode to the issues it missed.
	addOptionsApplied := c.Status.AddOptionsApplied || issuesSynced
	statusAC = statusAC.WithAddOptionsApplied(addOptionsApplied)

	var issueFileCount, downloading int32
	children := make([]rollup.ContainerChild, 0, len(issues))
	for _, iss := range issues {
		if iss.Status.HasFile {
			issueFileCount++
		}
		// downloadingIssueCount (ruling R11): the Issues whose view of this
		// Comic's entries reads a non-terminal download phase.
		if downloadingPhase(iss.Status.DownloadPhase) {
			downloading++
		}
		// The release date the Issue's own phase reads (its cover date,
		// issue/reconciler.go's released), so the rollup's dates and its
		// counts agree on what is out; the pull date below is the store
		// date, the day it reaches the shops.
		children = append(children, rollup.ContainerChild{
			Monitored: ptr.Deref(iss.Spec.Monitored, true), Phase: iss.Status.Phase,
			HasFile: iss.Status.HasFile, ReleaseDate: iss.Status.Date,
		})
	}
	statusAC = statusAC.WithIssueFileCount(issueFileCount).WithDownloadingIssueCount(downloading)
	// The ContainerRollup (2026-11-10 design §4.1, §5.2): the Series'
	// rollup for the Comic, Pending while its metadata or its issue list is
	// not in yet, as series.Phase reads it.
	cr := rollup.Container(monitored, metaReady && issuesSynced, children, now)
	statusAC = withContainerRollup(statusAC, cr)
	if next := NextPullDate(issues, now); next != nil {
		statusAC = statusAC.WithNextPullDate(*next)
	}

	k8s.MarkReady(c, &conditions, metaReady && issuesSynced, k8s.ReasonReconciled, "metadataReady=%t issuesSynced=%t", metaReady, issuesSynced)
	statusAC = statusAC.WithConditions(k8s.ConditionACs(conditions)...)

	if conflicted, err := itemstatus.Apply(ctx, r.Client, c, catalogac.Comic(c.Name, c.Namespace).WithStatus(withEntries(ctx, statusAC, c))); err != nil || conflicted {
		return itemstatus.Requeue(conflicted), err
	}
	// The phase edge as a Kubernetes Event, as every kind records it
	// (2026-11-10 design D14, §5.2; rollup.Reporter is Movie's recorder).
	rollup.Reporter{Recorder: r.Recorder}.PhaseEdge(c, string(c.Status.Phase), string(cr.Phase))

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
// (zeroing) a healthy Comic's Path/IssueFileCount/NextPullDate/
// AddOptionsApplied and its ContainerRollup the next time either happens.
// Same mechanism as series.reassertKnownStatus; see CLAUDE.md's "Gotchas
// found the hard way" for the general rule and its cost history.
func reassertKnownStatus(statusAC *catalogac.ComicStatusApplyConfiguration, c *catalogv1alpha1.Comic) *catalogac.ComicStatusApplyConfiguration {
	if c.Status.Path != "" {
		statusAC = statusAC.WithPath(c.Status.Path)
	}
	statusAC = statusAC.WithIssueFileCount(c.Status.IssueFileCount)
	statusAC = statusAC.WithDownloadingIssueCount(c.Status.DownloadingIssueCount)
	statusAC = statusAC.WithAddOptionsApplied(c.Status.AddOptionsApplied)
	statusAC = withContainerRollup(statusAC, c.Status.ContainerRollup)
	if c.Status.NextPullDate != nil {
		statusAC = statusAC.WithNextPullDate(*c.Status.NextPullDate)
	}
	return statusAC
}

// withContainerRollup declares the ContainerRollup fields this manager owns
// (2026-11-10 design §4.1) on statusAC: the phase and the three counts
// always (a zero count is a value, not an absence), each release date only
// when there is one, so a date that is no more is released.
func withContainerRollup(statusAC *catalogac.ComicStatusApplyConfiguration, cr catalogv1alpha1.ContainerRollup) *catalogac.ComicStatusApplyConfiguration {
	if cr.Phase != "" {
		statusAC = statusAC.WithPhase(cr.Phase)
	}
	statusAC = statusAC.
		WithMissingChildCount(cr.MissingChildCount).
		WithDownloadingChildCount(cr.DownloadingChildCount).
		WithCutoffUnmetChildCount(cr.CutoffUnmetChildCount)
	if cr.NextReleaseDate != nil {
		statusAC = statusAC.WithNextReleaseDate(*cr.NextReleaseDate)
	}
	if cr.PreviousReleaseDate != nil {
		statusAC = statusAC.WithPreviousReleaseDate(*cr.PreviousReleaseDate)
	}
	return statusAC
}

// comicRefreshState is the metadata.RefreshTTL bucket a Comic's own cached
// metadata puts it in (app/catalog/metadata/refreshstate.go's
// comicRefreshState, which the gateway applies to the freshly fetched
// volume): a volume whose run has ended (status.metadata.status, written
// since the 2026-11-10 design §4.7; ComicVine's from Mylar3's 55-day rule
// on the latest issue, MangaDex's from its own status) drops to the 30-day
// cadence, and anything else -- continuing, or a status the provider did
// not say -- stays on the daily one, the conservative answer when the
// status is unknown.
func comicRefreshState(c *catalogv1alpha1.Comic) string {
	if c.Status.Metadata != nil && c.Status.Metadata.Status == catalogv1alpha1.ComicRunStatusEnded {
		return metadata.RefreshStateCompleted
	}
	return metadata.RefreshStateOngoing
}

// syncIssues lists c's currently owned Issues and, when the childsync memo says
// an input moved since the last complete fan-out in this process, requests
// its issue list from the metadata gateway (lookupIssues,
// app/catalog/metadata/rpc.go, task f665aa9) and ensures each desired Issue
// exists with its provider-sourced status fields. It returns the Issues as
// this pass leaves them, for the rollup: every Issue the List found, with
// the ones this pass ensured carrying the dates, number and monitor flag
// it just wrote (a new Issue is built from what ensureIssue created; its
// own reconciler writes its phase next, which wakes this Comic again), so
// the rollup reflects this reconcile rather than the one before it. On a
// failure it still returns everything it knows -- the pre-fan-out list plus
// whatever it ensured before failing -- and the memo records nothing, so
// the next pass fetches again. This is series.syncEpisodes' shape.
func (r *Reconciler) syncIssues(ctx context.Context, c *catalogv1alpha1.Comic, now time.Time) (synced bool, issues []catalogv1alpha1.Issue, err error) {
	var issueList catalogv1alpha1.IssueList
	if err := r.List(ctx, &issueList, client.InNamespace(c.Namespace), client.MatchingFields{issueByComicRefIndexKey: c.Name}); err != nil {
		return false, nil, err
	}
	existing := make(map[string]catalogv1alpha1.Issue, len(issueList.Items))
	existingNames := make(map[string]bool, len(issueList.Items))
	for _, iss := range issueList.Items {
		existing[iss.Name] = iss
		existingNames[iss.Name] = true
	}
	done := func() []catalogv1alpha1.Issue {
		out := make([]catalogv1alpha1.Issue, 0, len(existing))
		for _, iss := range existing {
			out = append(out, iss)
		}
		return out
	}

	if !r.issueSync.Due(c.UID, issueSyncKey(c, c.Status.AddOptionsApplied), c.Status.Metadata != nil) {
		// Nothing a fan-out reads has moved since the last complete one:
		// the Issues the List found are what it would leave.
		return true, done(), nil
	}

	req := schema.MetadataRequest{
		Kind: commonv1.MediaKindIssue,
		IDs:  map[string]string{SourceKey(c.Spec.Source): c.Spec.SourceID},
	}
	rpcCtx, cancel := context.WithTimeout(ctx, issueSyncRPCBackoff)
	defer cancel()

	var resp schema.MetadataResponse
	// An idempotent lookup: every class retries on the reconciler's
	// requeue, and the condition names the class (ADR-0020 §4).
	if rpcErr := r.Bus.Request(rpcCtx, events.RPCMetadataLookup, req, &resp); rpcErr != nil {
		return false, done(), events.DescribeRequestError(events.RPCMetadataLookup, rpcErr)
	}
	if resp.Error != "" {
		return false, done(), fmt.Errorf("metadata gateway: %s", resp.Error)
	}

	fetched := make([]metadata.ComicIssue, 0, len(resp.Results))
	for _, raw := range resp.Results {
		var iss metadata.ComicIssue
		if err := json.Unmarshal(raw, &iss); err != nil {
			return false, done(), fmt.Errorf("decode issue: %w", err)
		}
		fetched = append(fetched, iss)
	}

	desired := DesiredIssues(c, c.Status.AddOptionsApplied, existingNames, fetched, now)
	for _, d := range desired {
		if err := r.ensureIssue(ctx, c, d); err != nil {
			return false, done(), err
		}
		existing[d.Name] = ensuredIssue(existing[d.Name], c, d)
	}
	// Recorded with addOptionsApplied true: the pass that completes the
	// first fan-out also flips the flag, so the next pass's key matches
	// and the flip costs no second fetch.
	r.issueSync.Synced(c.UID, issueSyncKey(c, true), c.Status.Metadata != nil)
	return true, done(), nil
}

// ensuredIssue is the Issue named d.Name as ensureIssue leaves it, for the
// rollup of the pass that wrote it: the listed object (was, the zero Issue
// for one this pass created) with the fields ensureIssue applied -- the
// dates, and for a created one its spec too. Its phase is its own
// reconciler's to write.
func ensuredIssue(was catalogv1alpha1.Issue, c *catalogv1alpha1.Comic, d DesiredIssue) catalogv1alpha1.Issue {
	iss := was
	if iss.Name == "" {
		iss.Name, iss.Namespace = d.Name, c.Namespace
		iss.Spec = catalogv1alpha1.IssueSpec{ComicRef: c.Name, Number: d.Number, CalculatedNumberCentis: d.CalculatedNumberCentis, Monitored: d.Monitored}
	}
	iss.Status.Date, iss.Status.StoreDate = nil, nil
	if d.Date != nil {
		t := metav1.NewTime(*d.Date)
		iss.Status.Date = &t
	}
	if d.StoreDate != nil {
		t := metav1.NewTime(*d.StoreDate)
		iss.Status.StoreDate = &t
	}
	return iss
}

// ensureIssue gets or creates the Issue named d.Name, then patches its
// provider-sourced status fields under k8s.ManagerCatalogFanout.
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
	// The 2026-11-10 design's fields (§4.7, §5.4): each sent when the
	// provider has it, so a provider that stops sending one releases it, as
	// the dates above are handled.
	if d.StoreDate != nil {
		statusAC = statusAC.WithStoreDate(metav1.NewTime(*d.StoreDate))
	}
	if d.Overview != "" {
		statusAC = statusAC.WithOverview(d.Overview)
	}
	if d.CoverURL != "" && len(d.CoverURL) <= 512 {
		statusAC = statusAC.WithCoverURL(d.CoverURL)
	}
	for i, cr := range d.Credits {
		if i == 32 || cr.Name == "" {
			break
		}
		statusAC = statusAC.WithCredits(catalogac.Credit().WithName(cr.Name).WithRole(cr.Role))
	}
	if len(d.SourceIDs) > 0 {
		statusAC = statusAC.WithSourceIDs(d.SourceIDs)
	}

	// k8s.ManagerCatalogFanout, not k8s.ManagerCatalog: this
	// reconciler writes an Issue it owns but does not itself compute the
	// acquisition state for -- see this package's doc.go and
	// k8s.ManagerCatalogFanout's own doc comment for the full reasoning.
	_, err = k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogFanout, catalogac.Issue(d.Name, c.Namespace).WithStatus(statusAC))
	return err
}

// withEntries folds the grab entries, the download phase, the intent nonces
// and release N's legacy record into a catalog status apply: this pass's
// decisions when the item stages made them, else the stored values, on
// every apply site -- the complete declaration (ADR-0019 §7.0, Review
// Focus 4). It is called once per apply: the list setters append.
func withEntries(ctx context.Context, ac *catalogac.ComicStatusApplyConfiguration, o *catalogv1alpha1.Comic) *catalogac.ComicStatusApplyConfiguration {
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

// downloadingPhase reports an entry phase that still delivers content: set,
// and neither Imported, Failed, Blocklisted nor Removing.
func downloadingPhase(p commonv1.DownloadPhase) bool {
	switch p {
	case "", commonv1.DownloadPhaseImported, commonv1.DownloadPhaseFailed,
		commonv1.DownloadPhaseBlocklisted, commonv1.DownloadPhaseRemoving:
		return false
	}
	return true
}
