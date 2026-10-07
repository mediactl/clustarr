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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/import/importliststate"
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

	// ReasonTokenRefused is Synced=False on a Trakt list whose last sync
	// reported Trakt refusing its token (schema.ListSnapshot.NeedsToken).
	// While Synced reads it, the controller refreshes the token and
	// re-issues the sync (ADR-0019 §7.6, W30).
	ReasonTokenRefused = "TokenRefused"

	// ReasonTokenRefreshed is Synced=Unknown once the controller has
	// refreshed a refused token and re-issued the sync. A re-issued sync
	// refused again finds it, and clears the token for a device flow
	// rather than refreshing on every sync.
	ReasonTokenRefreshed = "TokenRefreshed"
)

// Reconciler is the ImportList state machine (ADR-0019 §7.6): it
// schedules syncs, diffs each snapshot the import agent reports and applies
// the result to the catalog, refreshes Trakt tokens and drives the
// device-code flow. It is the sole writer of ImportList.status and of the
// Trakt token Secret, under k8s.ManagerImport, and of the items a list
// adds, unmonitors or deletes, under k8s.ManagerImportWorker. It never
// fetches a list itself: that is the import agent's job
// (app/import/worker/importlist), on the clustarr.work.import.list.* queue
// this controller publishes to.
type Reconciler struct {
	// Client reads the ImportList and the catalog, applies status, items
	// and the token Secret, and deletes items and MediaFiles.
	Client client.Client

	// APIReader reads a Series with its managedFields before a list
	// re-applies it (the cache strips them). Nil means the manager's API
	// reader at SetupWithManager, or Client in a test that sets neither.
	APIReader client.Reader

	// Bus publishes the sync and recycle tasks and the history event, and
	// holds the exclusions bucket.
	Bus events.Bus

	// Snapshots is the clustarr-importlist bucket: the agent's snapshots
	// and this controller's remembered items. Nil means
	// Bus.KV(events.BucketImportList).
	Snapshots events.KV

	// HTTPClient is used for the Trakt device-code flow's own small
	// requests (never the list fetch, which the worker makes).
	HTTPClient *http.Client

	// TraktBaseURL overrides trakt.DefaultBaseURL for the device-code flow:
	// import's --trakt-base-url (app/import/run.go), which gives the list
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

func (r *Reconciler) apiReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

func (r *Reconciler) snapshots() events.KV {
	if r.Snapshots != nil {
		return r.Snapshots
	}
	return r.Bus.KV(events.BucketImportList)
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
		// No finalizer: the list's keys in clustarr-importlist (its
		// snapshot and remembered items) are TTL'd, and the owned Trakt
		// token Secret carries a controller owner reference, so it is
		// garbage-collected with the ImportList. Nothing here needs
		// cleaning up by hand.
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

	// Gap-fix ruling R-10: a kind the provider cannot yield is an
	// admission failure (ImportListSpec's CEL rules; see this package's
	// doc comment). For any list admitted before the CRD carried them,
	// this is the same verdict, reported: Ready=False naming the
	// kinds, and no sync scheduled or diffed, auth included -- a rejected
	// object would do nothing at all. Every other status field is
	// re-asserted.
	if bad := importliststate.UnyieldableKinds(il.Spec); len(bad) > 0 {
		markSynced(&il, &conditions, nil)
		k8s.MarkReady(&il, &conditions, false, ReasonUnsupportedKind,
			"a %s list yields only %v; spec.kinds also names %s, which it cannot yield, so nothing is synced until spec.kinds is corrected",
			importliststate.ProviderName(il.Spec), importliststate.YieldableKinds(il.Spec), strings.Join(bad, ", "))
		if err := r.applyStatus(ctx, &il, conditions, nil, il.Status.Auth); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Incorporate the import agent's newest report (ADR-0019 §7.6): a
	// complete snapshot newer than status.lastSyncAt is diffed and applied
	// once, and its SyncedAt becomes lastSyncAt. A partial one waits for its
	// remaining chunks, whose writes wake the list again.
	prevSynced := syncedReason(il.Status.Conditions)
	outcome, err := r.incorporate(ctx, &il)
	if err != nil {
		return ctrl.Result{}, err
	}
	markSynced(&il, &conditions, outcome)

	authenticated := true
	reissue := false
	var authRequeue time.Duration
	authState := il.Status.Auth
	if il.Spec.Trakt != nil {
		in := authInput{
			NeedsToken:          syncedReason(conditions) == ReasonTokenRefused,
			RefusedAfterRefresh: outcome != nil && outcome.NeedsToken && prevSynced == ReasonTokenRefreshed,
		}
		auth, err := r.reconcileAuth(ctx, &il, now, in)
		if err != nil {
			k8s.MarkFalse(&il, &conditions, catalogv1alpha1.ImportListConditionAuthenticated,
				k8s.ReasonReconcileError, "%s", err.Error())
			k8s.MarkReady(&il, &conditions, false, k8s.ReasonReconcileError, "%s", err.Error())
			if applyErr := r.applyStatus(ctx, &il, conditions, outcome, il.Status.Auth); applyErr != nil {
				return ctrl.Result{}, applyErr
			}
			return ctrl.Result{RequeueAfter: requeueFor(time.Minute)}, nil
		}
		authenticated = auth.Authenticated
		authRequeue = auth.RequeueAfter
		if auth.Auth != nil {
			authState = auth.Auth
		}
		if authenticated {
			k8s.MarkTrue(&il, &conditions, catalogv1alpha1.ImportListConditionAuthenticated,
				k8s.ReasonReconciled, "trakt credentials accepted")
		} else {
			k8s.MarkFalse(&il, &conditions, catalogv1alpha1.ImportListConditionAuthenticated,
				ReasonNotAuthenticated, "waiting for the user to approve the device code")
		}
		if auth.Refreshed {
			reissue = true
			k8s.MarkUnknown(&il, &conditions, catalogv1alpha1.ImportListConditionSynced,
				ReasonTokenRefreshed, "trakt refused the list's token; it was refreshed and the sync re-issued")
		}
	}

	if !authenticated {
		k8s.MarkReady(&il, &conditions, false, ReasonNotAuthenticated, "not yet authenticated")
		if err := r.applyStatus(ctx, &il, conditions, outcome, authState); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: requeueFor(authRequeue)}, nil
	}

	interval := effectiveRefreshInterval(specForClamp(il.Spec))
	due, nextSyncAt := isDue(il.Status.NextSyncAt, now)
	if due || reissue {
		// A re-issued sync (a refreshed token) gets a Msg-Id of its own, so
		// the work stream's dedup window never swallows it beside the
		// scheduled one of the same minute.
		tag := "list-" + now.Truncate(time.Minute).Format(time.RFC3339)
		if !due {
			tag = "list-token-" + now.Truncate(time.Second).Format(time.RFC3339)
		}
		if err := r.publishSyncTask(ctx, &il, now, tag); err != nil {
			if errors.Is(err, events.ErrQueueFull) {
				k8s.MarkReady(&il, &conditions, false, ReasonQueueFull, "the import-list work queue is full")
				if applyErr := r.applyStatus(ctx, &il, conditions, outcome, authState); applyErr != nil {
					return ctrl.Result{}, applyErr
				}
				return ctrl.Result{RequeueAfter: requeueFor(time.Minute)}, nil
			}
			return ctrl.Result{}, err
		}
		if due {
			nextSyncAt = now.Add(interval)
			logging.FromContext(ctx).Info("importlist: sync scheduled", "nextSyncAt", nextSyncAt)
		} else {
			logging.FromContext(ctx).Info("importlist: sync re-issued after a token refresh")
		}
	}

	k8s.MarkReady(&il, &conditions, true, k8s.ReasonReconciled, "scheduled")
	nextSyncAtMeta := metav1.NewTime(nextSyncAt)
	if err := r.applyStatusFull(ctx, &il, conditions, outcome, authState, &nextSyncAtMeta); err != nil {
		return ctrl.Result{}, err
	}
	requeue := authRequeue
	if until := nextSyncAt.Sub(now); requeue == 0 || until < requeue {
		requeue = until
	}
	return ctrl.Result{RequeueAfter: requeueFor(requeue)}, nil
}

// incorporate reads il's snapshot and, when it is complete and newer than
// status.lastSyncAt, diffs and applies it: the outcome status reports. nil
// means no new snapshot. status.lastSyncAt round-trips in seconds, the
// precision the agent writes SyncedAt in, so a snapshot already applied
// compares equal, never newer.
func (r *Reconciler) incorporate(ctx context.Context, il *catalogv1alpha1.ImportList) (*syncOutcome, error) {
	snap, ok, err := r.readSnapshot(ctx, il)
	if err != nil || !ok {
		return nil, err
	}
	at := snap.SyncedAt.UTC().Truncate(time.Second)
	if il.Status.LastSyncAt != nil && !at.After(il.Status.LastSyncAt.UTC()) {
		return nil, nil
	}
	outcome := r.diff(ctx, il, snap)
	return &outcome, nil
}

// syncedReason is the Synced condition's reason among conditions, or "".
func syncedReason(conditions []metav1.Condition) string {
	if c := meta.FindStatusCondition(conditions, catalogv1alpha1.ImportListConditionSynced); c != nil {
		return c.Reason
	}
	return ""
}

// markSynced sets Synced from the agent's report as this controller
// applied it, not merely from a task having been published: a task can sit
// queued or fail long after a reconcile returns, and Ready is about the
// ImportList's configuration being valid and schedulable, which is a
// different claim from "the last sync actually succeeded".
//
// outcome is nil on a pass that applied no new snapshot: Synced then keeps
// the last applied sync's verdict, seeded into conditions, and reads
// "never synced" only while status.lastSyncAt is unset.
func markSynced(il *catalogv1alpha1.ImportList, conditions *[]metav1.Condition, outcome *syncOutcome) {
	switch {
	case outcome == nil && il.Status.LastSyncAt != nil:
		return
	case outcome == nil:
		k8s.MarkUnknown(il, conditions, catalogv1alpha1.ImportListConditionSynced,
			k8s.ReasonPending, "no sync has completed yet")
	case outcome.NeedsToken:
		k8s.MarkFalse(il, conditions, catalogv1alpha1.ImportListConditionSynced,
			ReasonTokenRefused, "trakt refused the list's token; the controller refreshes it and re-issues the sync: %s", outcome.Error)
	case outcome.Error == "":
		note := ""
		if il.Spec.AutomaticAdd != nil && !*il.Spec.AutomaticAdd {
			note = " (automaticAdd is false: entries are listed, not added)"
		}
		k8s.MarkTrue(il, conditions, catalogv1alpha1.ImportListConditionSynced,
			k8s.ReasonSucceeded, "last sync at %s: %d fetched, %d added, %d excluded, %d removed%s",
			outcome.SyncedAt.UTC().Format(time.RFC3339), outcome.Fetched, outcome.Added,
			outcome.Excluded, outcome.Removed, note)
	default:
		k8s.MarkFalse(il, conditions, catalogv1alpha1.ImportListConditionSynced,
			k8s.ReasonFailed, "%s", outcome.Error)
	}
}

// reconcileAuth builds the Trakt credentials and device flow and drives one
// step of it.
func (r *Reconciler) reconcileAuth(
	ctx context.Context, il *catalogv1alpha1.ImportList, now time.Time, in authInput,
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
	store := NewSecretTokenStore(r.Client, il, r.Clock)
	return reconcileTraktAuth(ctx, flow, store, il.Status.Auth, now, in)
}

// publishSyncTask enqueues one clustarr.work.import.list.<name> message;
// tag makes its Msg-Id.
func (r *Reconciler) publishSyncTask(ctx context.Context, il *catalogv1alpha1.ImportList, now time.Time, tag string) error {
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
		ID:     events.MsgIDForObject(string(il.UID), 0, tag),
		Type:   "import.ListTask",
		Schema: schemaName,
		Source: "import@" + version.String(),
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

// applyStatus re-asserts every status field this manager owns except
// nextSyncAt (see applyStatusFull), layering conditions, an optional
// snapshot outcome and the auth state on top of what is already on the
// object.
// Used by every early-return path so a transient failure (a queue-full
// publish, an auth error) cannot zero out counters a previous successful
// sync already reported -- the "early return built a partial status"
// hazard CLAUDE.md documents.
func (r *Reconciler) applyStatus(
	ctx context.Context, il *catalogv1alpha1.ImportList, conditions []metav1.Condition,
	outcome *syncOutcome, auth *catalogv1alpha1.DeviceAuth,
) error {
	return r.applyStatusFull(ctx, il, conditions, outcome, auth, il.Status.NextSyncAt.DeepCopy())
}

// applyStatusFull is the one status write in this package, under
// k8s.ManagerImport. Every field this manager owns is sent every time --
// including the counters, which come from outcome when this pass applied a
// snapshot and from the object's own current status otherwise -- so that a
// reconcile which does not touch a field never releases it.
func (r *Reconciler) applyStatusFull(
	ctx context.Context, il *catalogv1alpha1.ImportList, conditions []metav1.Condition,
	outcome *syncOutcome, auth *catalogv1alpha1.DeviceAuth, nextSyncAt *metav1.Time,
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
	if outcome != nil {
		status = status.
			WithItemCount(outcome.Fetched).
			WithAddedCount(outcome.Added).
			WithExcludedCount(outcome.Excluded).
			WithRemovedCount(outcome.Removed).
			WithLastSyncAt(metav1.NewTime(outcome.SyncedAt)).
			WithLastError(outcome.Error)
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
	_, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerImport, ac)
	return err
}

// The ImportList controller's RBAC. It is the sole writer of
// importlists/status and of the owned Trakt token Secret (which it also
// reads, with the user-supplied app credentials); it creates, updates,
// unmonitors and deletes the Movie and Series items a list adds; and under
// syncLevel removeAndDelete it reads the item's Episodes, MediaFiles and
// RootFolder and deletes the MediaFiles whose files it has the import agent
// recycle (ADR-0019 §7.6).
//
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=importlists,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=importlists/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;series,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=episodes;rootfolders,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;create;update;patch

// SetupWithManager registers the ImportList controller. Its For()
// predicate passes two changes and nothing else:
//
//   - the generation, i.e. any spec edit, spec.enabled included. This
//     controller's own status writes never bump it, so it cannot loop
//     itself.
//   - the DLQ projector's k8s.AnnotationDeadLettered, which applyStatusFull
//     folds into the DeadLettered condition.
//
// A finished sync wakes the list through SnapshotSource, a watch of the
// agent's snapshot writes in clustarr-importlist. The schedule, the Trakt
// poll cadence and the token's refresh are driven by RequeueAfter instead,
// the same way rootfolderschedule's cron is.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Client == nil {
		r.Client = mgr.GetClient()
	}
	if r.APIReader == nil {
		r.APIReader = mgr.GetAPIReader()
	}
	if r.Bus == nil {
		return fmt.Errorf("importlist: a bus is required")
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("importlist").
		For(&catalogv1alpha1.ImportList{}, builder.WithPredicates(k8s.Or(
			k8s.GenerationChanged(),
			k8s.DeadLetteredAnnotationChanged(),
		))).
		WatchesRawSource(SnapshotSource(r.Bus)).
		WithOptions(controller.Options{RecoverPanic: new(true), ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}

var _ reconcile.Reconciler = (*Reconciler)(nil)
