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

package importexclusion

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

const (
	// AnnotationKeys records the clustarr-import-exclusions keys this
	// controller last wrote, comma-joined and sorted. It is what makes
	// stale-key cleanup and finalizer cleanup exact rather than
	// best-effort: after a spec edit the old ids are no longer anywhere in
	// the spec, so the resource itself is the only place they can be
	// remembered.
	AnnotationKeys = "catalog.clustarr.io/exclusion-keys"

	// resyncInterval re-asserts the index periodically. The bucket is
	// durable, but a bucket recreated from scratch (a fresh cluster, a
	// restored backup) would otherwise stay empty until somebody edited
	// every exclusion.
	resyncInterval = time.Hour
)

// Reason tokens local to ImportExclusion, alongside the shared pkg/k8s set.
const ReasonNoRecognizedID = "NoRecognizedExternalID"

// recognizedIDKeys is the closed set of provider keys the index understands,
// from the CRD's own ExclusionIDKey* constants. An unrecognised key is
// ignored rather than indexed: an id nothing looks up is a typo, and
// indexing it would quietly promise a block that never happens.
var recognizedIDKeys = []string{
	catalogv1alpha1.ExclusionIDKeyTMDB,
	catalogv1alpha1.ExclusionIDKeyTVDB,
	catalogv1alpha1.ExclusionIDKeyIMDB,
	catalogv1alpha1.ExclusionIDKeyMusicBrainz,
	catalogv1alpha1.ExclusionIDKeyOpenLibrary,
	catalogv1alpha1.ExclusionIDKeyASIN,
	catalogv1alpha1.ExclusionIDKeyComicVine,
	catalogv1alpha1.ExclusionIDKeyMangaDex,
}

// finalizerName is §2's "<group>/<kind-lowercase>" convention.
var finalizerName = k8s.FinalizerName(
	catalogv1alpha1.GroupVersion.WithKind("ImportExclusion").GroupKind())

// Reconciler keeps the clustarr-import-exclusions bucket in step with the
// ImportExclusion resources, and is the single writer of their status.
type Reconciler struct {
	// Client reads the exclusion and applies its annotation and status.
	Client client.Client

	// Bus holds the exclusion index.
	Bus events.Bus
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "importexclusion.Reconcile")
	defer span.End()
	ctx = logging.NewContext(ctx, logging.FromContext(ctx).With("importExclusion", req.String()))

	var ex catalogv1alpha1.ImportExclusion
	if err := r.Client.Get(ctx, req.NamespacedName, &ex); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if k8s.IsDeleting(&ex) {
		return ctrl.Result{}, r.finalize(ctx, &ex)
	}

	if _, err := k8s.EnsureFinalizer(ctx, r.Client, &ex, finalizerName); err != nil {
		return ctrl.Result{}, err
	}

	desired := desiredEntries(&ex)
	if len(desired) == 0 {
		// Nothing in ExternalIDs is a key anything looks up, so this
		// exclusion can never block anything. The CEL rule guarantees the
		// map is non-empty, so this is always a typo or an unsupported
		// provider, and no retry will fix it.
		conditions := append([]metav1.Condition(nil), ex.Status.Conditions...)
		k8s.MarkReady(&ex, &conditions, false, ReasonNoRecognizedID,
			"externalIDs has no recognised provider key; want one of %s",
			strings.Join(recognizedIDKeys, ", "))
		if err := r.applyStatus(ctx, &ex, conditions); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, reconcile.TerminalError(fmt.Errorf(
			"importexclusion: %s: externalIDs has no recognised provider key", req.NamespacedName))
	}

	// Anything indexed last time that the spec no longer names has to go
	// first: after an edit the old id is nowhere in the spec, so the
	// annotation is the only record that it was ever blocked.
	wanted := make(map[string]bool, len(desired))
	for key := range desired {
		wanted[key] = true
	}
	kv := r.Bus.KV(events.BucketImportExclusions)
	for _, key := range previousKeys(&ex) {
		if wanted[key] {
			continue
		}
		if err := kv.Delete(ctx, key); err != nil {
			return ctrl.Result{}, fmt.Errorf("importexclusion: delete stale key %q: %w", key, err)
		}
	}

	for _, key := range sortedKeys(desired) {
		data, err := desired[key].Encode()
		if err != nil {
			return ctrl.Result{}, err
		}
		if _, err := kv.Put(ctx, key, data); err != nil {
			return ctrl.Result{}, fmt.Errorf("importexclusion: index key %q: %w", key, err)
		}
	}

	// Recorded only after every key is in place, so a failure part-way
	// leaves the previous record and the next reconcile redoes the whole
	// set rather than forgetting what it had written.
	if err := r.recordKeys(ctx, &ex, sortedKeys(desired)); err != nil {
		return ctrl.Result{}, err
	}

	conditions := append([]metav1.Condition(nil), ex.Status.Conditions...)
	k8s.MarkReady(&ex, &conditions, true, k8s.ReasonReconciled,
		"%d external id(s) indexed", len(desired))
	if err := r.applyStatus(ctx, &ex, conditions); err != nil {
		return ctrl.Result{}, err
	}
	logging.FromContext(ctx).Info("exclusion indexed", "keys", len(desired))
	return ctrl.Result{RequeueAfter: resyncInterval}, nil
}

// finalize removes every key this controller wrote for ex and then drops the
// finalizer. The bucket has no TTL, so without this an exclusion the user
// deleted would go on blocking imports forever.
func (r *Reconciler) finalize(ctx context.Context, ex *catalogv1alpha1.ImportExclusion) error {
	if !k8s.HasFinalizer(ex, finalizerName) {
		return nil
	}

	keys := previousKeys(ex)
	if len(keys) == 0 {
		// Never recorded (deleted before the first successful reconcile).
		// Fall back to what the spec asks for, so a partial first pass
		// still cleans up after itself.
		keys = sortedKeys(desiredEntries(ex))
	}
	kv := r.Bus.KV(events.BucketImportExclusions)
	for _, key := range keys {
		if err := kv.Delete(ctx, key); err != nil {
			return fmt.Errorf("importexclusion: delete key %q: %w", key, err)
		}
	}

	_, err := k8s.RemoveFinalizer(ctx, r.Client, ex, finalizerName)
	return err
}

// desiredEntries maps every recognised (provider, id) pair to the value that
// belongs at its key.
func desiredEntries(ex *catalogv1alpha1.ImportExclusion) map[string]events.ExclusionEntry {
	entry := events.ExclusionEntry{
		Namespace: ex.Namespace,
		Name:      ex.Name,
		Kind:      string(ex.Spec.Kind),
		Reason:    ex.Spec.Reason,
	}
	out := make(map[string]events.ExclusionEntry, len(ex.Spec.ExternalIDs))
	for _, provider := range recognizedIDKeys {
		value := strings.TrimSpace(ex.Spec.ExternalIDs[provider])
		if value == "" {
			continue
		}
		out[events.ExclusionKey(provider, value)] = entry
	}
	return out
}

func sortedKeys(entries map[string]events.ExclusionEntry) []string {
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// previousKeys reads the keys recorded by the last successful reconcile.
func previousKeys(ex *catalogv1alpha1.ImportExclusion) []string {
	raw := strings.TrimSpace(ex.Annotations[AnnotationKeys])
	if raw == "" {
		return nil
	}
	var out []string
	for _, key := range strings.Split(raw, ",") {
		if key = strings.TrimSpace(key); key != "" {
			out = append(out, key)
		}
	}
	return out
}

// recordKeys stores the applied key set in the annotation. It is metadata,
// not status, so it does not compete with anything for the status
// subresource's single writer.
func (r *Reconciler) recordKeys(ctx context.Context, ex *catalogv1alpha1.ImportExclusion, keys []string) error {
	ac := catalogac.ImportExclusion(ex.Name, ex.Namespace).
		WithAnnotations(map[string]string{AnnotationKeys: strings.Join(keys, ",")})
	_, err := k8s.Apply(ctx, r.Client, k8s.ManagerImportarr, ac)
	return err
}

// applyStatus is the one status write in this package.
//
// Every field this manager owns is sent every time, including matchCount and
// lastMatchedAt, which the import-list path (a later task, sharing this same
// field manager) maintains: server-side apply replaces a manager's ownership
// set on every apply, so omitting them here would release them and read as a
// match counter resetting to zero on every reconcile.
func (r *Reconciler) applyStatus(
	ctx context.Context,
	ex *catalogv1alpha1.ImportExclusion,
	conditions []metav1.Condition,
) error {
	status := catalogac.ImportExclusionStatus().
		WithObservedGeneration(ex.Generation).
		WithMatchCount(ex.Status.MatchCount).
		WithConditions(k8s.ConditionACs(conditions)...)
	if ex.Status.LastMatchedAt != nil {
		status = status.WithLastMatchedAt(*ex.Status.LastMatchedAt)
	}
	_, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerImportarr,
		catalogac.ImportExclusion(ex.Name, ex.Namespace).WithStatus(status))
	return err
}

// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=importexclusions,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=importexclusions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=importexclusions/finalizers,verbs=update

// SetupWithManager registers the ImportExclusion controller. The predicate is
// GenerationChanged so this controller's own annotation and status writes,
// neither of which bumps the generation, cannot loop it; the bucket is
// re-asserted on the resync interval instead.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.Client == nil {
		r.Client = mgr.GetClient()
	}
	if r.Bus == nil {
		return fmt.Errorf("importexclusion: a bus is required")
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("importexclusion").
		For(&catalogv1alpha1.ImportExclusion{},
			builder.WithPredicates(k8s.Or(k8s.GenerationChanged(), k8s.Deleting()))).
		WithOptions(controller.Options{RecoverPanic: ptr.To(true), ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}

var _ reconcile.Reconciler = (*Reconciler)(nil)
