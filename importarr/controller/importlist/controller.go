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

package importlist

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	worker "github.com/mediactl/clustarr/importarr/worker/importlist"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/importlist/trakt"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/version"
)

// Reason tokens local to ImportList, alongside the shared pkg/k8s set.
const (
	ReasonNotAuthenticated = "NotAuthenticated"
	ReasonQueueFull        = "QueueFull"

	// ReasonUnsupportedKind is Ready=False on a list whose spec.kinds names
	// a kind its provider cannot yield (gap-fix ruling R-10): a list
	// admission should have rejected. Nothing is scheduled for it.
	ReasonUnsupportedKind = "UnsupportedKind"
)

// Reconciler schedules ImportList syncs and drives the Trakt device-code
// authorization flow. It is the sole writer of ImportList.status, under
// k8s.ManagerImportarr -- see that constant's own doc comment, which
// already names this task's resource -- and it never fetches a list
// itself: that is importarr/worker/importlist's job, on the
// clustarr.work.importarr.list.* queue this controller publishes to.
type Reconciler struct {
	// Client reads the ImportList and applies its status.
	Client client.Client

	// Bus publishes the sync task and holds the worker's result checkpoint.
	Bus events.Bus

	// HTTPClient is used for the Trakt device-code flow's own small
	// requests (never the list fetch, which the worker makes).
	HTTPClient *http.Client

	// TraktBaseURL overrides trakt.DefaultBaseURL for the device-code flow:
	// importarr's --trakt-base-url (importarr/run.go), which gives the list
	// worker the same host, and tests that point it at an httptest server.
	TraktBaseURL string

	// Clock is the time source, injected so tests are deterministic.
	Clock func() time.Time
}

func (r *Reconciler) now() time.Time {
	if r.Clock != nil {
		return r.Clock().UTC()
	}
	return time.Now().UTC()
}

func (r *Reconciler) httpClient() *http.Client {
	if r.HTTPClient != nil {
		return r.HTTPClient
	}
	return http.DefaultClient
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "importlist.Reconcile")
	defer span.End()
	ctx = logging.NewContext(ctx, logging.FromContext(ctx).With("importList", req.String()))

	var il catalogv1alpha1.ImportList
	if err := r.Client.Get(ctx, req.NamespacedName, &il); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if k8s.IsDeleting(&il) {
		// No finalizer: the worker's KV bookkeeping (clustarr-importlist,
		// clustarr-progress) is TTL'd, and the owned Trakt token Secret
		// carries a controller owner reference, so it is garbage-collected
		// with the ImportList. Nothing here needs cleaning up by hand.
		return ctrl.Result{}, nil
	}

	now := r.now()
	conditions := append([]metav1.Condition(nil), il.Status.Conditions...)

	if il.Spec.Enabled != nil && !*il.Spec.Enabled {
		k8s.MarkReady(&il, &conditions, false, k8s.ReasonDisabled, "spec.enabled is false")
		if err := r.applyStatus(ctx, &il, conditions, nil, il.Status.Auth); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: idleRecheck}, nil
	}

	// Adopt whatever the worker has checkpointed since the last reconcile,
	// before deciding whether a new sync is due. The worker never writes
	// ImportList.status itself (see k8s.ManagerImportarr's doc comment);
	// this is the read half of that split, the same one
	// importarr/controller/libraryscan runs against
	// importarr/worker/rescan's Progress checkpoint. The worker's
	// AnnotationSyncedAt stamp is what brings a finished sync here.
	checkpoint, hasCheckpoint, err := r.pollResult(ctx, &il)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Gap-fix ruling R-10: a kind the provider cannot yield is an
	// admission failure. Until the ImportList CRD carries the CEL rule
	// (see this package's doc comment), and for any list admitted before
	// it did, this is the same verdict, reported: Ready=False naming the
	// kinds, and no sync scheduled, auth included -- a rejected object
	// would do nothing at all. Every other status field is re-asserted.
	if bad := worker.UnyieldableKinds(il.Spec); len(bad) > 0 {
		markSynced(&il, &conditions, checkpoint, hasCheckpoint)
		k8s.MarkReady(&il, &conditions, false, ReasonUnsupportedKind,
			"a %s list yields only %v; spec.kinds also names %s, which it cannot yield, so nothing is synced until spec.kinds is corrected",
			worker.ProviderName(il.Spec), worker.YieldableKinds(il.Spec), strings.Join(bad, ", "))
		if err := r.applyStatus(ctx, &il, conditions, checkpointOrNil(checkpoint, hasCheckpoint), il.Status.Auth); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	authenticated := true
	var authRequeue time.Duration
	authState := il.Status.Auth
	if il.Spec.Trakt != nil {
		outcome, err := r.reconcileAuth(ctx, &il, now)
		if err != nil {
			k8s.MarkFalse(&il, &conditions, catalogv1alpha1.ImportListConditionAuthenticated,
				k8s.ReasonReconcileError, "%s", err.Error())
			k8s.MarkReady(&il, &conditions, false, k8s.ReasonReconcileError, "%s", err.Error())
			if applyErr := r.applyStatus(ctx, &il, conditions, checkpointOrNil(checkpoint, hasCheckpoint), il.Status.Auth); applyErr != nil {
				return ctrl.Result{}, applyErr
			}
			return ctrl.Result{RequeueAfter: requeueFor(time.Minute)}, nil
		}
		authenticated = outcome.Authenticated
		authRequeue = outcome.RequeueAfter
		if outcome.Auth != nil {
			authState = outcome.Auth
		}
		if authenticated {
			k8s.MarkTrue(&il, &conditions, catalogv1alpha1.ImportListConditionAuthenticated,
				k8s.ReasonReconciled, "trakt credentials accepted")
		} else {
			k8s.MarkFalse(&il, &conditions, catalogv1alpha1.ImportListConditionAuthenticated,
				ReasonNotAuthenticated, "waiting for the user to approve the device code")
		}
	}

	if !authenticated {
		k8s.MarkReady(&il, &conditions, false, ReasonNotAuthenticated, "not yet authenticated")
		if err := r.applyStatus(ctx, &il, conditions, checkpointOrNil(checkpoint, hasCheckpoint), authState); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: requeueFor(authRequeue)}, nil
	}

	interval := effectiveRefreshInterval(specForClamp(il.Spec))
	due, nextSyncAt := isDue(il.Status.NextSyncAt, now)
	if due {
		if err := r.publishSyncTask(ctx, &il, now); err != nil {
			if errors.Is(err, events.ErrQueueFull) {
				k8s.MarkReady(&il, &conditions, false, ReasonQueueFull, "the import-list work queue is full")
				if applyErr := r.applyStatus(ctx, &il, conditions, checkpointOrNil(checkpoint, hasCheckpoint), authState); applyErr != nil {
					return ctrl.Result{}, applyErr
				}
				return ctrl.Result{RequeueAfter: requeueFor(time.Minute)}, nil
			}
			return ctrl.Result{}, err
		}
		nextSyncAt = now.Add(interval)
		logging.FromContext(ctx).Info("importlist: sync scheduled", "nextSyncAt", nextSyncAt)
	}

	markSynced(&il, &conditions, checkpoint, hasCheckpoint)
	k8s.MarkReady(&il, &conditions, true, k8s.ReasonReconciled, "scheduled")
	nextSyncAtMeta := metav1.NewTime(nextSyncAt)
	if err := r.applyStatusFull(ctx, &il, conditions, checkpointOrNil(checkpoint, hasCheckpoint), authState, &nextSyncAtMeta); err != nil {
		return ctrl.Result{}, err
	}
	requeue := authRequeue
	if until := nextSyncAt.Sub(now); requeue == 0 || until < requeue {
		requeue = until
	}
	return ctrl.Result{RequeueAfter: requeueFor(requeue)}, nil
}

// markSynced sets Synced from the worker's own report, not merely from a
// task having been published: a task can sit queued or fail long after a
// reconcile returns, and Ready is about the ImportList's configuration being
// valid and schedulable, which is a different claim from "the last sync
// actually succeeded".
//
// The checkpoint lives in clustarr-progress, whose TTL is ten minutes, so
// it is normally gone by the next scheduled reconcile. A missing checkpoint
// therefore means "never synced" only while status.lastSyncAt is unset;
// after that, the Synced condition already on the object (seeded into
// conditions) is the last sync's verdict and is kept as it is.
func markSynced(il *catalogv1alpha1.ImportList, conditions *[]metav1.Condition, checkpoint worker.Result, ok bool) {
	switch {
	case !ok && il.Status.LastSyncAt != nil:
		return
	case !ok:
		k8s.MarkUnknown(il, conditions, catalogv1alpha1.ImportListConditionSynced,
			k8s.ReasonPending, "no sync has completed yet")
	case checkpoint.Error == "":
		note := ""
		if il.Spec.AutomaticAdd != nil && !*il.Spec.AutomaticAdd {
			note = " (automaticAdd is false: entries are listed, not added)"
		}
		k8s.MarkTrue(il, conditions, catalogv1alpha1.ImportListConditionSynced,
			k8s.ReasonSucceeded, "last sync at %s: %d fetched, %d added, %d excluded, %d removed%s",
			checkpoint.SyncedAt.UTC().Format(time.RFC3339), checkpoint.Fetched, checkpoint.Added,
			checkpoint.Excluded, checkpoint.Removed, note)
	default:
		k8s.MarkFalse(il, conditions, catalogv1alpha1.ImportListConditionSynced,
			k8s.ReasonFailed, "%s", checkpoint.Error)
	}
}

// reconcileAuth builds the Trakt credentials and device flow and drives one
// step of it.
func (r *Reconciler) reconcileAuth(
	ctx context.Context, il *catalogv1alpha1.ImportList, now time.Time,
) (authOutcome, error) {
	if il.Spec.SecretRef == nil {
		return authOutcome{}, fmt.Errorf("trakt requires spec.secretRef with clientID and clientSecret keys")
	}
	var sec corev1.Secret
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: il.Namespace, Name: il.Spec.SecretRef.Name}, &sec); err != nil {
		return authOutcome{}, fmt.Errorf("read secret %s/%s: %w", il.Namespace, il.Spec.SecretRef.Name, err)
	}
	creds := trakt.Credentials{ClientID: string(sec.Data["clientID"]), ClientSecret: string(sec.Data["clientSecret"])}
	if creds.ClientID == "" || creds.ClientSecret == "" {
		return authOutcome{}, fmt.Errorf("secret %s/%s has no clientID/clientSecret keys", il.Namespace, il.Spec.SecretRef.Name)
	}

	opts := []trakt.Option{trakt.WithHTTPClient(r.httpClient())}
	if r.TraktBaseURL != "" {
		opts = append(opts, trakt.WithBaseURL(r.TraktBaseURL))
	}
	flow := trakt.NewDeviceFlow(creds, opts...)
	store := worker.NewSecretTokenStore(r.Client, il)
	return reconcileTraktAuth(ctx, flow, store, il.Status.Auth, now)
}

// pollResult reads the worker's result checkpoint, when one exists.
func (r *Reconciler) pollResult(ctx context.Context, il *catalogv1alpha1.ImportList) (worker.Result, bool, error) {
	entry, err := r.Bus.KV(events.BucketProgress).Get(ctx, worker.ResultKey(string(il.UID)))
	if err != nil {
		if errors.Is(err, events.ErrKeyNotFound) {
			return worker.Result{}, false, nil
		}
		return worker.Result{}, false, err
	}
	res, err := worker.DecodeResult(entry.Value)
	if err != nil {
		return worker.Result{}, false, err
	}
	return res, true, nil
}

// publishSyncTask enqueues one clustarr.work.importarr.list.<name> message.
func (r *Reconciler) publishSyncTask(ctx context.Context, il *catalogv1alpha1.ImportList, now time.Time) error {
	task := schema.ListTask{ListRef: schema.Ref{Namespace: il.Namespace, Name: il.Name, UID: string(il.UID)}}
	schemaName, data, err := schema.Encode(task)
	if err != nil {
		return err
	}
	env := &events.Envelope{
		// The minute-truncated timestamp, not the generation, makes the
		// dedup ID vary tick to tick: the work stream's Duplicates window
		// is 1h (topology.go), shorter than an arr-provider list's 15m
		// floor, so a generation-keyed ID (which does not change between
		// ticks of an unedited spec) would make JetStream silently drop
		// every second and third scheduled sync within the same hour.
		ID:     events.MsgIDForObject(string(il.UID), 0, "list-"+now.Truncate(time.Minute).Format(time.RFC3339)),
		Type:   "importarr.ListTask",
		Schema: schemaName,
		Source: "importarr@" + version.String(),
		Key:    il.Namespace + "/" + il.Name,
		Time:   now,
		Data:   data,
	}
	_, err = r.Bus.Publish(ctx, events.WorkListSubject(il.Name), env)
	return err
}

// isDue reports whether a sync should fire now, and the NextSyncAt to keep
// reporting when it should not: nil (never synced) is always due; otherwise
// due once now has reached the recorded time.
func isDue(nextSyncAt *metav1.Time, now time.Time) (due bool, next time.Time) {
	if nextSyncAt == nil {
		return true, now
	}
	if !now.Before(nextSyncAt.Time) {
		return true, now
	}
	return false, nextSyncAt.Time
}

// specForClamp adapts an ImportListSpec into the small struct
// effectiveRefreshInterval takes, so the clamp arithmetic in schedule.go has
// no dependency on the CRD package (see catalogImportListSpec's doc
// comment).
func specForClamp(spec catalogv1alpha1.ImportListSpec) catalogImportListSpec {
	return catalogImportListSpec{
		Trakt: spec.Trakt != nil, Plex: spec.Plex != nil, Tmdb: spec.Tmdb != nil,
		Mdblist: spec.Mdblist != nil, StevenLu: spec.StevenLu != nil, ImdbCSV: spec.ImdbCSV != nil,
		Custom: spec.Custom != nil, Arr: spec.Arr != nil, Requested: spec.RefreshInterval.Duration,
	}
}

func checkpointOrNil(res worker.Result, ok bool) *worker.Result {
	if !ok {
		return nil
	}
	return &res
}

// applyStatus re-asserts every status field this manager owns except
// nextSyncAt (see applyStatusFull), layering conditions, an optional worker
// checkpoint and the auth state on top of what is already on the object.
// Used by every early-return path so a transient failure (a queue-full
// publish, an auth error) cannot zero out counters a previous successful
// sync already reported -- the "early return built a partial status"
// hazard CLAUDE.md documents.
func (r *Reconciler) applyStatus(
	ctx context.Context, il *catalogv1alpha1.ImportList, conditions []metav1.Condition,
	checkpoint *worker.Result, auth *catalogv1alpha1.DeviceAuth,
) error {
	return r.applyStatusFull(ctx, il, conditions, checkpoint, auth, il.Status.NextSyncAt.DeepCopy())
}

// applyStatusFull is the one status write in this package, under
// k8s.ManagerImportarr. Every field this manager owns is sent every time --
// including the counters, which come from checkpoint when the worker has
// reported one and from the object's own current status otherwise -- so
// that a reconcile which does not touch a field never releases it.
func (r *Reconciler) applyStatusFull(
	ctx context.Context, il *catalogv1alpha1.ImportList, conditions []metav1.Condition,
	checkpoint *worker.Result, auth *catalogv1alpha1.DeviceAuth, nextSyncAt *metav1.Time,
) error {
	// DeadLettered is folded here, the one place every path's conditions
	// are rendered, so no early return can leave it out of the declaration.
	conditions = append([]metav1.Condition(nil), conditions...)
	k8s.MarkDeadLettered(il, &conditions)

	status := catalogac.ImportListStatus().
		WithObservedGeneration(il.Generation).
		WithConditions(k8s.ConditionACs(conditions)...).
		WithItemCount(il.Status.ItemCount).
		WithAddedCount(il.Status.AddedCount).
		WithExcludedCount(il.Status.ExcludedCount).
		WithRemovedCount(il.Status.RemovedCount).
		WithLastError(il.Status.LastError)

	if il.Status.LastSyncAt != nil {
		status = status.WithLastSyncAt(*il.Status.LastSyncAt)
	}
	if nextSyncAt != nil {
		status = status.WithNextSyncAt(*nextSyncAt)
	}
	if checkpoint != nil {
		status = status.
			WithItemCount(checkpoint.Fetched).
			WithAddedCount(checkpoint.Added).
			WithExcludedCount(checkpoint.Excluded).
			WithRemovedCount(checkpoint.Removed).
			WithLastSyncAt(metav1.NewTime(checkpoint.SyncedAt)).
			WithLastError(checkpoint.Error)
	}
	if auth != nil {
		ac := catalogac.DeviceAuth().WithState(auth.State)
		if auth.UserCode != "" {
			ac = ac.WithUserCode(auth.UserCode)
		}
		if auth.VerificationURL != "" {
			ac = ac.WithVerificationURL(auth.VerificationURL)
		}
		if auth.ExpiresAt != nil {
			ac = ac.WithExpiresAt(*auth.ExpiresAt)
		}
		if auth.TokenExpiresAt != nil {
			ac = ac.WithTokenExpiresAt(*auth.TokenExpiresAt)
		}
		status = status.WithAuth(ac)
	}

	ac := catalogac.ImportList(il.Name, il.Namespace).WithStatus(status)
	_, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerImportarr, ac)
	return err
}

// The ImportList controller's RBAC. It is the sole writer of
// importlists/status; it also patches Secrets (the owned Trakt token/device
// code) and reads them (the user-supplied app credentials).
//
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=importlists,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=importlists/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create;update;patch

// SetupWithManager registers the ImportList controller. Its For()
// predicate passes three changes and nothing else:
//
//   - the generation, i.e. any spec edit, spec.enabled included. This
//     controller's own status writes never bump it, so it cannot loop
//     itself.
//   - the worker's [worker.AnnotationSyncedAt] stamp, so a finished sync's
//     Result is projected into status as soon as it lands. Without it the
//     only other wake-up is the RequeueAfter at nextSyncAt, and status lags
//     a whole refresh interval (up to 24h for stevenLu) behind the worker.
//   - the DLQ projector's k8s.AnnotationDeadLettered, which applyStatusFull
//     folds into the DeadLettered condition.
//
// The schedule and the Trakt poll cadence are driven by RequeueAfter
// instead of a watch, the same way rootfolderschedule's cron and
// libraryscan's checkpoint poll are.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Client == nil {
		r.Client = mgr.GetClient()
	}
	if r.Bus == nil {
		return fmt.Errorf("importlist: a bus is required")
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("importlist").
		For(&catalogv1alpha1.ImportList{}, builder.WithPredicates(k8s.Or(
			k8s.GenerationChanged(),
			SyncCompleted(),
			k8s.DeadLetteredAnnotationChanged(),
		))).
		WithOptions(controller.Options{RecoverPanic: ptr.To(true), ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}

// SyncCompleted passes an update whose [worker.AnnotationSyncedAt] value
// changed: the worker finished a sync. Creates and deletes pass too.
func SyncCompleted() predicate.Predicate {
	return k8s.StatusFieldChanged(func(o client.Object) string {
		return o.GetAnnotations()[worker.AnnotationSyncedAt]
	})
}

var _ reconcile.Reconciler = (*Reconciler)(nil)
