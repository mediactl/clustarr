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

package torrent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/grabarr/engine"
	"github.com/mediactl/clustarr/grabarr/status"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

const (
	// notReadyRequeue is how long Reconcile waits before trying again while
	// [Engine.ReAttach] has not yet completed (R4).
	notReadyRequeue = 2 * time.Second

	// defaultPollInterval is how often an active transfer is re-polled and
	// its telemetry re-applied -- spec §6.3's "polls Stats() every 5s, SSA
	// telemetry every <=10s" collapse to one cadence here since polling and
	// applying happen together (see doc.go's "Field manager" section for why
	// that is safe).
	//
	// resolve/add failures are returned as plain errors rather than a
	// separate constant here, so controller-runtime's own rate-limiting queue
	// backs them off -- a spec.source.indexerDownload resolve failure
	// (indexer down, RPC timeout) is exactly the transient case that limiter
	// is for.
	defaultPollInterval = 5 * time.Second
)

// Reconciler drives one Download labelled for this engine ordinal through
// [Engine.Client]: adding it, keeping its pause/seed-criteria state in sync
// with spec, applying telemetry, and removing it once policy says it may go.
// Field manager: k8s.ManagerGrabarrEngine, applied exclusively through
// download.ApplyStatus -- see doc.go.
type Reconciler struct {
	// Client is the controller-runtime client shared with the rest of this
	// engine pod's manager.
	Client client.Client

	// Engine wraps the embedded download.Client and the re-attach gate.
	Engine *Engine

	// EngineID is this pod's "<client>-<ordinal>" identity
	// (grabarr.Options.Engine); only Downloads labelled with it are watched.
	EngineID string

	// StateDir is where re-attach descriptors are persisted; normally the
	// same directory as Engine.StateDir.
	StateDir string

	// HTTPClient fetches spec.source.torrentURL and an indexerDownload
	// resolve's RedirectURL. Nil uses http.DefaultClient.
	HTTPClient *http.Client

	// Resolver resolves spec.source.indexerDownload through indexarr's
	// download RPC. Nil means a Download using that source member cannot be
	// added -- see [resolveSource].
	Resolver IndexerResolver

	// PollInterval overrides [defaultPollInterval] for tests that would
	// otherwise wait seconds between polls.
	PollInterval time.Duration

	// EpisodeReader reads the catalog Episodes a pack Download targets, so
	// the transfer fetches only their files ([resolveSelection]). It is read
	// once per Download, at its first Add, so production passes the
	// manager's uncached mgr.GetAPIReader() rather than starting an Episode
	// informer in every engine pod. Nil disables file selection: every file
	// of every torrent is wanted, the behaviour before selection existed.
	EpisodeReader client.Reader
}

func (r *Reconciler) httpClient() *http.Client {
	if r.HTTPClient != nil {
		return r.HTTPClient
	}
	return http.DefaultClient
}

func (r *Reconciler) pollInterval() time.Duration {
	if r.PollInterval > 0 {
		return r.PollInterval
	}
	return defaultPollInterval
}

// Reconcile implements reconcile.Reconciler.
//
// The re-attach gate is the FIRST thing checked, before even a Get: while
// ReAttach has not completed, this method does no work of any kind against
// [Engine.Client] -- not Add, not Get, not Remove -- and simply asks to be
// asked again shortly. That is R4: an engine which has not finished loading
// its own previously-running transfers must not act as though it has none.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	if !r.Engine.Ready() {
		return ctrl.Result{RequeueAfter: notReadyRequeue}, nil
	}

	ctx, span := tracing.Start(ctx, "torrent.Reconcile")
	defer span.End()
	log := logging.FromContext(ctx).With("download", req.NamespacedName)

	var dl downloadv1alpha1.Download
	if err := r.Client.Get(ctx, req.NamespacedName, &dl); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if dl.Labels[downloadv1alpha1.LabelEngine] != r.EngineID {
		// The watch predicate already filters this; defensive only, e.g. a
		// stale informer event delivered after a relabel.
		return ctrl.Result{}, nil
	}

	if k8s.IsDeleting(&dl) {
		return r.reconcileDeleting(ctx, log, &dl)
	}

	// The engine finalizer goes on before the transfer does, so no transfer
	// ever exists for a Download that could be deleted without this engine
	// hearing of it (ruling R-6; grabarr/engine's package doc). A write here
	// does not end the reconcile -- pkg/k8s.EnsureFinalizer's "finalizer
	// without early return".
	if _, err := k8s.EnsureFinalizer(ctx, r.Client, &dl, engine.Finalizer); err != nil {
		return ctrl.Result{}, err
	}

	if dl.Status.DownloadID == "" {
		return r.add(ctx, &dl)
	}

	result, err := r.sync(ctx, &dl, dl.Status.DownloadID)
	if err != nil {
		log.ErrorContext(ctx, "torrent: sync failed", "error", err)
	}
	return result, err
}

// add resolves dl's payload and calls [Engine.Client.Add] for the first
// time. The resolve is the slow step (an HTTP fetch or an RPC round trip),
// so it happens BEFORE the only Get this method needs -- the one immediately
// preceding [status.Patch] -- rather than the object being read once up
// front and carried across it. See doc.go's "Field manager" section for why
// this matters less here than it would with a seeded apply configuration:
// the eventual status write is download.ApplyStatus(item), a full
// declaration computed from the live download.Client.Get that follows Add,
// not from anything read before the resolve.
func (r *Reconciler) add(ctx context.Context, dl *downloadv1alpha1.Download) (ctrl.Result, error) {
	res, err := resolveSource(ctx, r.httpClient(), r.Resolver, dl.Namespace, dl.Spec.Source)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("torrent: resolve %s/%s: %w", dl.Namespace, dl.Name, err)
	}

	category := r.category(ctx, dl)
	seedCriteria := r.seedCriteria(ctx, dl)

	// A selection that cannot be resolved -- an Episode deleted since the
	// grab, a catalog read that failed -- degrades to the whole torrent
	// rather than failing the add: fetching more than needed is the safe
	// direction, and a transfer blocked on the catalog helps nobody.
	selection, err := resolveSelection(ctx, r.EpisodeReader, downloadTarget{namespace: dl.Namespace, target: dl.Spec.Target})
	if err != nil {
		logging.FromContext(ctx).WarnContext(ctx, "torrent: file selection unavailable; fetching every file", "error", err)
		selection = nil
	}

	addReq := download.AddRequest{
		Name:             dl.Name,
		Magnet:           res.Magnet,
		Payload:          res.Payload,
		ExpectedInfoHash: res.ExpectedInfoHash,
		Category:         category,
		Paused:           dl.Spec.Paused,
		Priority:         dl.Spec.Priority,
		SeedCriteria:     seedCriteria,
		WantFile:         selection.selector(),
	}

	id, err := r.Engine.Client.Add(ctx, addReq)
	if err != nil {
		// errors.Is(err, download.ErrPayloadMismatch) is a blocklist-worthy
		// event (the interface doc's own words), but this engine has no
		// channel to write status.failureReason -- that field is
		// k8s.ManagerGrabarr's, and the controller has no way to learn this
		// specific reason from status alone (see this task's report for why
		// that is flagged as a cross-task gap rather than something fixed
		// here). Returning the error either way lets controller-runtime's
		// own backoff retry it rather than spinning tightly.
		return ctrl.Result{}, fmt.Errorf("torrent: add %s/%s: %w", dl.Namespace, dl.Name, err)
	}

	// Get before persisting: the descriptor records the client's own
	// AddedAt, which for an idempotent re-Add of a transfer the client
	// already held is the original add, not now.
	item, err := r.Engine.Client.Get(ctx, id)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("torrent: get %s after add: %w", id, err)
	}

	if err := saveDescriptor(r.StateDir, id, res.Payload, descriptor{
		Name:             dl.Name,
		Category:         category,
		Magnet:           res.Magnet,
		ExpectedInfoHash: res.ExpectedInfoHash,
		Priority:         dl.Spec.Priority,
		Paused:           dl.Spec.Paused,
		SeedCriteria:     seedCriteria,
		Selection:        selection,
		AddedAt:          item.AddedAt,
	}); err != nil {
		// The transfer is already running in-process; a descriptor write
		// failure means it will not survive THIS engine's next restart, not
		// that it is unsafe now. Logged and returned as an error so the
		// reconcile retries the save (Add is idempotent on id, so retrying
		// Add too is harmless).
		return ctrl.Result{}, fmt.Errorf("torrent: persist descriptor for %s: %w", id, err)
	}

	if err := r.applyTelemetry(ctx, client.ObjectKeyFromObject(dl), item); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.pollInterval()}, nil
}

// sync keeps an already-added transfer's pause and seed-criteria state
// matched to spec, marks it imported and removes it once policy allows, and
// applies telemetry every poll.
func (r *Reconciler) sync(ctx context.Context, dl *downloadv1alpha1.Download, id string) (ctrl.Result, error) {
	log := logging.FromContext(ctx).With("id", id)

	item, err := r.Engine.Client.Get(ctx, id)
	if err != nil {
		if errors.Is(err, download.ErrNotFound) {
			return r.handleMissingTransfer(ctx, dl, id)
		}
		return ctrl.Result{}, fmt.Errorf("torrent: get %s: %w", id, err)
	}

	// Pause/Resume are both documented no-ops on a transfer already in the
	// requested state, so calling them every poll based on spec.paused alone
	// -- rather than diffing against the previous poll -- is exactly the
	// "level-driven reconcile that cannot know current state" the interface
	// doc anticipates.
	if dl.Spec.Paused && item.Status != download.StatusPaused {
		if err := r.Engine.Client.Pause(ctx, id); err != nil {
			return ctrl.Result{}, fmt.Errorf("torrent: pause %s: %w", id, err)
		}
	} else if !dl.Spec.Paused && item.Status == download.StatusPaused {
		if err := r.Engine.Client.Resume(ctx, id); err != nil {
			return ctrl.Result{}, fmt.Errorf("torrent: resume %s: %w", id, err)
		}
	}

	sc := r.seedCriteria(ctx, dl)
	if sc != nil {
		if err := r.Engine.Client.SetSeedCriteria(ctx, id, *sc); err != nil && !errors.Is(err, download.ErrNotFound) {
			return ctrl.Result{}, fmt.Errorf("torrent: set seed criteria %s: %w", id, err)
		}
	}

	// Keep the persisted descriptor's mutable fields current so a restart
	// re-attaches in the state spec currently asks for, not the state Add
	// originally saw -- see updateDescriptorState's own doc comment.
	if err := updateDescriptorState(r.StateDir, id, dl.Spec.Paused, sc); err != nil {
		log.ErrorContext(ctx, "torrent: update persisted descriptor failed", "error", err)
	}

	// status.import is read-only here (R3, and grabarr/status's own doc):
	// importarr's file-import worker (D2-7) is the only writer. Once it
	// reports Imported, MarkImported releases this engine's claim on the
	// files; CanMoveFiles/CanBeRemoved on the NEXT Get reflect that, which is
	// why item is re-read afterward rather than assumed.
	if dl.Status.Import != nil && dl.Status.Import.State == downloadv1alpha1.ImportPhaseImported {
		if err := r.Engine.Client.MarkImported(ctx, id); err != nil && !errors.Is(err, download.ErrNotFound) {
			return ctrl.Result{}, fmt.Errorf("torrent: mark imported %s: %w", id, err)
		}
		item, err = r.Engine.Client.Get(ctx, id)
		if err != nil {
			if errors.Is(err, download.ErrNotFound) {
				return r.handleMissingTransfer(ctx, dl, id)
			}
			return ctrl.Result{}, fmt.Errorf("torrent: get %s after mark imported: %w", id, err)
		}
	}

	if item.CanBeRemoved && removeOnImport(dl) {
		log.InfoContext(ctx, "torrent: seed goal met and imported; removing")
		if err := r.applyTelemetry(ctx, client.ObjectKeyFromObject(dl), item); err != nil {
			return ctrl.Result{}, err
		}
		if err := r.Engine.Client.Remove(ctx, id, false); err != nil && !errors.Is(err, download.ErrNotFound) {
			return ctrl.Result{}, fmt.Errorf("torrent: remove %s: %w", id, err)
		}
		if err := removeDescriptor(r.StateDir, id); err != nil {
			log.ErrorContext(ctx, "torrent: remove descriptor failed", "error", err)
		}
		return ctrl.Result{}, nil
	}

	if err := r.applyTelemetry(ctx, client.ObjectKeyFromObject(dl), item); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.pollInterval()}, nil
}

// handleMissingTransfer distinguishes "this engine removed the transfer
// itself" (no descriptor left, expected, terminal) from "this engine has
// lost track of a transfer it should still have" (descriptor still present,
// a genuine anomaly worth retrying) -- both surface identically as
// [download.ErrNotFound] from Get, and only the state directory on disk
// tells them apart.
func (r *Reconciler) handleMissingTransfer(ctx context.Context, dl *downloadv1alpha1.Download, id string) (ctrl.Result, error) {
	if _, err := os.Stat(sidecarFileName(r.StateDir, id)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// Already cleaned up by this engine's own remove-on-policy path;
			// nothing left to do until a spec change re-triggers a reconcile.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("torrent: stat descriptor for %s: %w", id, err)
	}
	return ctrl.Result{}, fmt.Errorf("torrent: %s has a persisted descriptor but %w", id, download.ErrNotFound)
}

// reconcileDeleting is this engine's half of the teardown protocol (ruling
// R-6, grabarr/engine's package doc): remove dl's transfer from the client,
// drop the persisted re-attach descriptor, then drop [engine.Finalizer] --
// in that order, so the Download controller's removeDataOnDelete finalizer,
// which waits for this one, never deletes files this engine still holds
// open.
//
// Remove honours spec.removeDataOnDelete itself, while the engine is
// certain the transfer has released its files. The controller's own
// finalizer then removes status.outputPath too; for the ordinary path that
// finds nothing left, and for the path where this engine is gone and the
// controller stopped waiting, it is the only removal there is.
//
// A Download with no status.downloadID never had a transfer recorded, so
// there is nothing to remove, but the finalizer still goes: it was added
// before the transfer, and holding the object for a transfer that never
// existed would wedge its deletion until the controller's timeout.
func (r *Reconciler) reconcileDeleting(ctx context.Context, log *slog.Logger, dl *downloadv1alpha1.Download) (ctrl.Result, error) {
	if id := dl.Status.DownloadID; id != "" {
		deleteData := dl.Spec.RemoveDataOnDelete == nil || *dl.Spec.RemoveDataOnDelete
		if err := r.Engine.Client.Remove(ctx, id, deleteData); err != nil && !errors.Is(err, download.ErrNotFound) {
			return ctrl.Result{}, fmt.Errorf("torrent: remove %s on delete: %w", id, err)
		}
		if err := removeDescriptor(r.StateDir, id); err != nil {
			// Logged, not retried: holding the Download hostage to a state-dir
			// write is worse than the cost of a descriptor left behind, which
			// is one re-attach on the next restart that the orphan reaper then
			// removes -- descriptor and all.
			log.ErrorContext(ctx, "torrent: remove descriptor on delete failed", "id", id, "error", err)
		}
		log.InfoContext(ctx, "torrent: removed transfer for a deleting Download", "id", id, "deleteData", deleteData)
	}
	if _, err := k8s.RemoveFinalizer(ctx, r.Client, dl, engine.Finalizer); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// applyTelemetry re-Gets dl's key immediately before applying, then
// server-side-applies item as a complete k8s.ManagerGrabarrEngine
// declaration. The re-Get is defence in depth (see doc.go): item's content
// already came entirely from a live download.Client.Get, never from a
// Download read earlier in this reconcile, so staleness in the OLD read
// cannot leak into the write regardless -- but the target object's identity
// should still be current, and the discipline is the project's blanket rule.
func (r *Reconciler) applyTelemetry(ctx context.Context, key types.NamespacedName, item download.Item) error {
	var fresh downloadv1alpha1.Download
	if err := r.Client.Get(ctx, key, &fresh); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("torrent: get %s before telemetry apply: %w", key, err)
	}
	if err := status.Patch(ctx, r.Client, k8s.ManagerGrabarrEngine, &fresh,
		func(ac *downloadac.DownloadStatusApplyConfiguration) {
			*ac = *download.ApplyStatus(item)
		}); err != nil {
		return fmt.Errorf("torrent: apply telemetry for %s: %w", key, err)
	}
	return nil
}

// category resolves AddRequest.Category from DownloadClient.spec.categories,
// falling back to the target media kind's own name when the client sets no
// override for it or the client cannot be read -- exactly what
// [download.AddRequest.Category]'s doc comment specifies ("Empty means the
// media kind's own name").
func (r *Reconciler) category(ctx context.Context, dl *downloadv1alpha1.Download) string {
	kind := string(dl.Spec.Target.Kind)
	dc := r.downloadClient(ctx, dl)
	if dc != nil {
		if c, ok := dc.Spec.Categories[kind]; ok && c != "" {
			return c
		}
	}
	return kind
}

// seedCriteria merges dl.Spec.SeedCriteria over the DownloadClient's default
// (DownloadClientSpec.Torrent.Seed), per DownloadSpec.SeedCriteria's own doc:
// "overrides the client's default seed goal for this Download."
func (r *Reconciler) seedCriteria(ctx context.Context, dl *downloadv1alpha1.Download) *commonv1alpha1.SeedCriteria {
	if dl.Spec.SeedCriteria != nil {
		return dl.Spec.SeedCriteria
	}
	dc := r.downloadClient(ctx, dl)
	if dc == nil || dc.Spec.Torrent == nil {
		return nil
	}
	return dc.Spec.Torrent.Seed
}

// downloadClient reads dl's DownloadClient (dl.Spec.ClientRef, already
// pinned by the Download controller before this engine ever sees the object
// -- see spec §6.3's "download" paragraph). A read failure is logged and
// treated as "no client-level default available" rather than failing the
// reconcile: category and seed-criteria both have a documented fallback, and
// the alternative -- refusing to add or sync a transfer because its own
// client object is momentarily unreadable -- is worse than falling back.
func (r *Reconciler) downloadClient(ctx context.Context, dl *downloadv1alpha1.Download) *downloadv1alpha1.DownloadClient {
	if dl.Spec.ClientRef == "" {
		return nil
	}
	var dc downloadv1alpha1.DownloadClient
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: dl.Namespace, Name: dl.Spec.ClientRef}, &dc); err != nil {
		logging.FromContext(ctx).WarnContext(ctx, "torrent: read DownloadClient failed", "clientRef", dl.Spec.ClientRef, "error", err)
		return nil
	}
	return &dc
}

// removeOnImport is DownloadSpec.RemoveOnImport, restated for the in-memory
// zero value the same way grabarr/controller/downloadclient.torrentReplicas
// restates spec.replicas' floor: RemoveOnImport carries
// +kubebuilder:default=true, which the apiserver's structural defaulting
// applies to any object that reached it, but an object built directly in a
// test never sees that defaulting.
func removeOnImport(dl *downloadv1alpha1.Download) bool {
	return dl.Spec.RemoveOnImport == nil || *dl.Spec.RemoveOnImport
}

// SetupWithManager registers the torrent engine's Download controller,
// filtered to Downloads carrying downloadv1alpha1.LabelEngine == r.EngineID
// via k8s.HasLabel -- the same §10 helper grabarr/engine/usenet's sibling
// reconciler uses for the identical filter. It intentionally applies no
// k8s.GenerationChanged predicate on top, for the same reason usenet's
// SetupWithManager gives: deletionTimestamp and status.import are both
// metadata/status changes that never move metadata.generation, and this
// reconciler branches on both first.
//
// Wiring [Engine.ReAttach] to run before mgr.Start returns control, and
// [Engine.HealthzCheck] into the manager's readyz set, is D2-8's job
// (grabarr/run.go) -- see doc.go.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("torrent-engine").
		For(&downloadv1alpha1.Download{}, builder.WithPredicates(k8s.HasLabel(downloadv1alpha1.LabelEngine, r.EngineID))).
		WithOptions(controller.Options{ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}
