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

package usenet

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/engine"
	grabarrstatus "github.com/mediactl/clustarr/app/grab/status"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Reason tokens for the Warning events this reconciler records.
const (
	// ReasonResolveFailed means the payload could not be turned into .nzb
	// bytes -- an indexer RPC failure, an unsupported source shape, or a
	// direct fetch that failed.
	ReasonResolveFailed = "PayloadResolveFailed"
	// ReasonAddFailed means the client rejected an otherwise-resolved
	// payload.
	ReasonAddFailed = "DownloadAddFailed"
)

// DefaultPollInterval requeues a Download that has not reached a terminal
// state, so status.downloadedBytes and the rest of the telemetry set keep
// moving even though nothing in the cluster told the informer to look again
// -- an in-flight transfer changes only inside this process's own goroutines.
const DefaultPollInterval = 5 * time.Second

// DefaultResolveTimeout bounds [Resolver.Resolve] so a hanging indexer RPC or
// a stalled direct fetch cannot hold this reconciler's worker goroutine for
// the whole controller.Options.ReconciliationTimeout.
const DefaultResolveTimeout = 60 * time.Second

// Reconciler drives one usenet engine replica's Downloads: it ensures a
// resolved payload has been added to the embedded [download.Client], keeps
// status telemetry moving under k8s.ManagerGrabarrEngine, and removes a
// transfer from the client once it is imported or the Download is deleted.
//
// It is filtered, at the watch, to Downloads carrying
// downloadv1alpha1.LabelEngine == Engine -- see [Reconciler.SetupWithManager]
// -- so a replica never sees a sibling's work. Field manager:
// k8s.ManagerGrabarrEngine, the telemetry subset only (grabarr/status.go);
// this reconciler never sets status.phase, status.conditions, status.engine
// or status.import. The one metadata it writes is grabarr/engine's
// [engine.Finalizer] -- see doc.go's "The engine finalizer".
type Reconciler struct {
	Client   client.Client
	Download download.Client
	Resolver *Resolver
	Recorder events.EventRecorder

	// Engine is this replica's identity, "<client>-0" -- always ordinal 0 for
	// usenet, since DownloadClientSpec's own CEL rule caps usenet clients at
	// replicas==1. It is the label value the watch is filtered to.
	Engine string

	// Categories mirrors DownloadClientSpec.Categories, resolved once at
	// construction alongside the client itself (see doc.go's wiring example)
	// rather than re-Get on every reconcile: the category a transfer lands
	// under must not change out from under a running download because an
	// operator edited the DownloadClient mid-transfer.
	Categories map[string]string

	// PollInterval requeues a non-terminal Download. Zero means
	// [DefaultPollInterval].
	PollInterval time.Duration

	// ResolveTimeout bounds payload resolution. Zero means
	// [DefaultResolveTimeout].
	ResolveTimeout time.Duration
}

func (r *Reconciler) pollInterval() time.Duration {
	if r.PollInterval > 0 {
		return r.PollInterval
	}
	return DefaultPollInterval
}

func (r *Reconciler) resolveTimeout() time.Duration {
	if r.ResolveTimeout > 0 {
		return r.ResolveTimeout
	}
	return DefaultResolveTimeout
}

// category is the subdirectory a transfer's files land under: dc.spec.categories
// keyed by the target's media kind, or the kind's own name when the client
// declares no override -- AddRequest.Category's own doc comment.
func category(categories map[string]string, kind commonv1alpha1.MediaKind) string {
	if c, ok := categories[string(kind)]; ok && c != "" {
		return c
	}
	return string(kind)
}

// imported reports whether importarr's file-import worker has finished with
// dl. Once true this reconciler stops managing the transfer entirely (see
// [Reconciler.reconcileImported]): re-running getOrAdd after the client has
// forgotten an imported-and-removed job would re-download a release that is
// already in the library.
func imported(dl *downloadv1alpha1.Download) bool {
	return dl.Status.Import != nil && dl.Status.Import.State == downloadv1alpha1.ImportPhaseImported
}

// removeOnImport resolves spec.removeOnImport's pointer against its CRD
// default (true) -- the same defaulting shape as [PostProcessFromSpec], for a
// field this reconciler reads rather than one pkg/download/usenet reads.
func removeOnImport(dl *downloadv1alpha1.Download) bool {
	if dl.Spec.RemoveOnImport == nil {
		return true
	}
	return *dl.Spec.RemoveOnImport
}

// removeDataOnDelete resolves spec.removeDataOnDelete's pointer against its
// CRD default (true).
func removeDataOnDelete(dl *downloadv1alpha1.Download) bool {
	if dl.Spec.RemoveDataOnDelete == nil {
		return true
	}
	return *dl.Spec.RemoveDataOnDelete
}

func terminal(status download.Status) bool {
	return status == download.StatusCompleted || status == download.StatusFailed
}

// Reconcile implements reconcile.Reconciler.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "usenetengine.Reconcile")
	defer span.End()
	log := logging.FromContext(ctx).With("download", req.NamespacedName, "engine", r.Engine)

	var dl downloadv1alpha1.Download
	if err := r.Client.Get(ctx, req.NamespacedName, &dl); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// The watch is already filtered to this label (SetupWithManager), so a
	// mismatch here means the label changed between the event and this Get,
	// or the reconciler itself was constructed with the wrong identity.
	// Either way this replica must not touch a transfer it was not assigned.
	if dl.Labels[downloadv1alpha1.LabelEngine] != r.Engine {
		return ctrl.Result{}, nil
	}

	if k8s.IsDeleting(&dl) {
		return r.reconcileDeleting(ctx, log, &dl)
	}

	// The engine finalizer goes on before the transfer does (ruling R-6;
	// grabarr/engine's package doc). A write here does not end the
	// reconcile -- pkg/k8s.EnsureFinalizer's "finalizer without early
	// return".
	if _, err := k8s.EnsureFinalizer(ctx, r.Client, &dl, engine.Finalizer); err != nil {
		return ctrl.Result{}, err
	}

	if imported(&dl) {
		return r.reconcileImported(ctx, &dl)
	}

	// Before getOrAdd, which re-adds a transfer the client does not know:
	// a failed Download's removed job must stay removed.
	if engine.Stopped(&dl) {
		return r.reconcileStopped(ctx, log, &dl)
	}

	item, err := r.getOrAdd(ctx, log, &dl)
	if err != nil {
		if r.Recorder != nil {
			r.Recorder.Eventf(&dl, nil, "Warning", ReasonAddFailed, "Reconcile", "usenet engine: %s", err)
		}
		return ctrl.Result{}, err
	}

	// A spec.priority edited after the Add. The client ignores the class
	// the job already has, so this is level-driven like syncPause.
	if err := r.Download.SetPriority(ctx, item.ID, dl.Spec.Priority); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.syncPause(ctx, &dl, item); err != nil {
		return ctrl.Result{}, err
	}
	// Pause/Resume can change the client's view of the transfer; refresh
	// before rendering telemetry rather than patching a status the call
	// above just made stale.
	item, err = r.Download.Get(ctx, item.ID)
	if err != nil {
		return ctrl.Result{}, err
	}

	if err := r.patchTelemetry(ctx, req.NamespacedName, item); err != nil {
		return ctrl.Result{}, err
	}

	if terminal(item.Status) {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: r.pollInterval()}, nil
}

// getOrAdd ensures dl has a transfer in the client and returns its current
// observation.
//
// If status.downloadID is unset, or the client no longer recognises it
// ([download.ErrNotFound] -- this engine restarted and re-attach did not find
// it, or it was never added in the first place), it resolves the payload and
// calls Add. Add is idempotent on the payload's content hash
// (pkg/download/usenet's own doc comment on it), so calling it again for a
// transfer the client secretly already holds under the same id is always
// safe; at worst this duplicates a resolve, never a transfer.
func (r *Reconciler) getOrAdd(ctx context.Context, log *slog.Logger, dl *downloadv1alpha1.Download) (download.Item, error) {
	if id := dl.Status.DownloadID; id != "" {
		item, err := r.Download.Get(ctx, id)
		if err == nil {
			return item, nil
		}
		if !errors.Is(err, download.ErrNotFound) {
			return download.Item{}, err
		}
		log.Warn("usenet engine: previously reported download id is unknown to the client; re-adding", "id", id)
	}

	resolveCtx, cancel := context.WithTimeout(ctx, r.resolveTimeout())
	payload, err := r.Resolver.Resolve(resolveCtx, dl.Namespace, dl.Spec.Source)
	cancel()
	if err != nil {
		return download.Item{}, fmt.Errorf("usenetengine: resolve payload for %s/%s: %w", dl.Namespace, dl.Name, err)
	}

	id, err := r.Download.Add(ctx, download.AddRequest{
		Name:     dl.Name,
		Payload:  payload,
		Category: category(r.Categories, dl.Spec.Target.Kind),
		Paused:   dl.Spec.Paused,
		Priority: dl.Spec.Priority,
	})
	if err != nil {
		return download.Item{}, fmt.Errorf("usenetengine: add %s/%s: %w", dl.Namespace, dl.Name, err)
	}
	return r.Download.Get(ctx, id)
}

// syncPause reconciles spec.paused against the client's own view, calling
// Pause/Resume only on a mismatch -- both are safe to call redundantly
// ([download.Client.Resume]'s own doc says so), but skipping the call when
// nothing disagrees avoids a checkpoint write every single poll.
//
// A job the health action paused (item.HealthPaused, DownloadClient
// spec.usenet.healthAction=pause) is not a mismatch with spec.paused=false:
// it waits for an operator, so it is never resumed from here. Setting
// spec.paused to true is the operator answering -- the Pause turns the
// health pause into an ordinary one with the health check off -- and setting
// it back to false then resumes the job as usual.
func (r *Reconciler) syncPause(ctx context.Context, dl *downloadv1alpha1.Download, item download.Item) error {
	paused := item.Status == download.StatusPaused
	switch {
	case dl.Spec.Paused && (!paused || item.HealthPaused):
		return r.Download.Pause(ctx, item.ID)
	case !dl.Spec.Paused && paused && !item.HealthPaused:
		return r.Download.Resume(ctx, item.ID)
	}
	return nil
}

// patchTelemetry declares item as the complete k8s.ManagerGrabarrEngine set
// on dl.
//
// It re-Gets dl immediately before applying rather than reusing the object
// this Reconcile call started with. getOrAdd's payload resolution can be an
// RPC to indexarr or a direct HTTP fetch -- slow, relative to an in-process
// map lookup -- and grabarr/status.Patch's seed is only as fresh as the
// object it is handed. Applying a snapshot read before that call would
// silently roll back whatever the Download controller (phase, conditions,
// status.engine) or importarr (status.import) wrote in the meantime: a lost
// update, not a server-side-apply release, which is exactly the class of bug
// no release-regression test in this tree can see (CLAUDE.md;
// indexarr/worker/rss/worker.go does the same re-Get with the comment "the
// poll closes the window").
//
// mutate replaces the seeded apply configuration wholesale with
// download.ApplyStatus(item) rather than calling ac.WithFiles or similar on
// top of it: status.files is a listType=map and WithFiles APPENDS, so seeding
// from grabarr/status.EngineFields (itself built from the live, possibly
// different, file list) and then appending item's files would duplicate every
// entry that survived and fail the apply outright on the listType=map merge.
func (r *Reconciler) patchTelemetry(ctx context.Context, key client.ObjectKey, item download.Item) error {
	var fresh downloadv1alpha1.Download
	if err := r.Client.Get(ctx, key, &fresh); err != nil {
		return client.IgnoreNotFound(err)
	}
	return grabarrstatus.Patch(ctx, r.Client, k8s.ManagerGrabarrEngine, &fresh,
		func(ac *downloadac.DownloadStatusApplyConfiguration) {
			*ac = *download.ApplyStatus(item)
		})
}

// reconcileImported is the whole reconcile once importarr has finished:
// release the client's claim on the transfer and, per spec.removeOnImport,
// remove it. It never falls through to getOrAdd, which is what stops a
// removed, already-imported transfer from being silently re-downloaded the
// next time this reconciler runs and finds status.downloadID pointing at a
// job the client no longer holds.
//
// It deliberately does not also patch telemetry: once imported there is
// nothing left for status.downloadedBytes or status.stage to usefully say,
// and MarkImported/Remove are themselves idempotent, so a Download that stays
// in this state (removeOnImport=false) is reconciled as a no-op on every
// later pass rather than accumulating pointless status writes.
func (r *Reconciler) reconcileImported(ctx context.Context, dl *downloadv1alpha1.Download) (ctrl.Result, error) {
	id := dl.Status.DownloadID
	if id == "" {
		return ctrl.Result{}, nil
	}
	if err := r.Download.MarkImported(ctx, id); err != nil && !errors.Is(err, download.ErrNotFound) {
		return ctrl.Result{}, err
	}
	if removeOnImport(dl) {
		if err := r.Download.Remove(ctx, id, false); err != nil && !errors.Is(err, download.ErrNotFound) {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

// reconcileStopped removes the job of a Download the controller has failed
// or blocklisted (or that is labelled blocklisted), and adds nothing for one
// that never had a job: Sonarr's "Remove Failed", on by default there. A
// failed usenet job is terminal inside the client already, but it keeps its
// scratch directory -- up to the whole release on a volume sized for a few
// -- and, if it published, its content on the shared volume; Remove
// discards the scratch job always and the published content per
// spec.removeDataOnDelete. The Download object stays as the record and,
// blocklisted, as the blocklist entry.
//
// It acts on the controller's verdict, never on the job's own status, so
// status.engineFailureReason is recorded as status.failureReason before the
// job that reported it is gone; and it writes no telemetry, so that report
// is not released afterwards.
func (r *Reconciler) reconcileStopped(ctx context.Context, log *slog.Logger, dl *downloadv1alpha1.Download) (ctrl.Result, error) {
	id := dl.Status.DownloadID
	if id == "" {
		return ctrl.Result{}, nil
	}
	deleteData := removeDataOnDelete(dl)
	err := r.Download.Remove(ctx, id, deleteData)
	switch {
	case errors.Is(err, download.ErrNotFound):
	case err != nil:
		return ctrl.Result{}, err
	default:
		log.Info("usenet engine: removed the job of a failed Download",
			"id", id, "phase", dl.Status.Phase, "reason", dl.Status.FailureReason, "deleteData", deleteData)
	}
	return ctrl.Result{}, nil
}

// reconcileDeleting is this engine's half of the teardown protocol (ruling
// R-6, grabarr/engine's package doc): remove dl's transfer from the client
// -- stopping its fetch goroutines and discarding its scratch job, and
// honouring spec.removeDataOnDelete for the published content -- then drop
// [engine.Finalizer]. The Download controller's removeDataOnDelete
// finalizer waits for this one, so it never removes files a running job
// still has open.
//
// A Download with no status.downloadID never had a transfer recorded; the
// finalizer still goes, since it was added before any Add and holding the
// object for a transfer that never existed would wedge its deletion.
func (r *Reconciler) reconcileDeleting(ctx context.Context, log *slog.Logger, dl *downloadv1alpha1.Download) (ctrl.Result, error) {
	if id := dl.Status.DownloadID; id != "" {
		deleteData := removeDataOnDelete(dl)
		if err := r.Download.Remove(ctx, id, deleteData); err != nil && !errors.Is(err, download.ErrNotFound) {
			return ctrl.Result{}, err
		}
		log.Info("usenet engine: removed transfer for a deleting Download", "id", id, "deleteData", deleteData)
	}
	if _, err := k8s.RemoveFinalizer(ctx, r.Client, dl, engine.Finalizer); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager registers this reconciler, filtered to Downloads carrying
// downloadv1alpha1.LabelEngine == r.Engine (k8s.HasLabel, the §10 helper
// grabarr's engine pods use for exactly this).
//
// It intentionally applies no k8s.GenerationChanged filter on top: unlike
// DownloadClient's controller (which only cares about spec edits),
// deletionTimestamp and status.import -- the two signals
// [Reconciler.Reconcile] branches on first -- are both metadata/status
// changes that never move metadata.generation. Filtering on generation would
// make this reconciler blind to the Download being deleted and to importarr
// finishing an import.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("usenet-engine").
		For(&downloadv1alpha1.Download{}, builder.WithPredicates(k8s.HasLabel(downloadv1alpha1.LabelEngine, r.Engine))).
		Complete(r)
}
