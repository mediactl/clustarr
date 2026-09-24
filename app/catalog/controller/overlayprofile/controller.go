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

package overlayprofile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/artwork"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Conditions and reasons this controller sets beyond k8s.Reason*.
const (
	// ConditionOverlap is True on a profile whose selector matches an item
	// that a lower-named profile wins (spec §C.4, as TranscodeProfile).
	// Informational: the profile still renders every item it does win.
	ConditionOverlap = "Overlap"

	// ReasonSelectorOverlap is Overlap's reason when True.
	ReasonSelectorOverlap = "SelectorOverlap"

	// ReasonNoOverlap is Overlap's reason when False.
	ReasonNoOverlap = "NoOverlap"

	// ReasonInvalidSelector is Ready's reason when spec.selector cannot be
	// parsed. Such a profile selects nothing.
	ReasonInvalidSelector = "InvalidSelector"
)

// Hash is status.hash: artwork.ProfileHash, the conversion the renderer
// draws from and stamps into every overlay's inputs digest.
func Hash(spec catalogv1alpha1.OverlayProfileSpec) string { return artwork.ProfileHash(spec) }

// Reconciler owns OverlayProfile.status under k8s.ManagerCatalogarr and
// publishes the RenderOverlay tasks its selection calls for. It writes
// nothing else: status.overlay is the renderer's.
type Reconciler struct {
	Client client.Client
	Bus    events.Publisher
}

// Reconcile resolves which Movies and Series in req's namespace the profile
// wins (artwork.Winner: of the profiles that select an item, the lowest
// name), publishes a render task for every item whose status.overlay
// disagrees with what the renderer would now draw -- an item it wins, or
// one whose overlay names it and which it no longer wins -- and applies
// hash, selection and conditions in one apply.
//
// A deleted profile has no status to write but still releases the items it
// rendered, so the NotFound path runs the same publishing loop.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "overlayprofile.Reconcile")
	defer span.End()
	log := logging.FromContext(ctx).With("overlayprofile", req.String())

	var profiles catalogv1alpha1.OverlayProfileList
	if err := r.Client.List(ctx, &profiles, client.InNamespace(req.Namespace)); err != nil {
		return ctrl.Result{}, fmt.Errorf("overlayprofile: list OverlayProfiles: %w", err)
	}
	var self *catalogv1alpha1.OverlayProfile
	for i := range profiles.Items {
		if profiles.Items[i].Name == req.Name && !k8s.IsDeleting(&profiles.Items[i]) {
			self = &profiles.Items[i]
		}
	}
	items, err := r.items(ctx, req.Namespace)
	if err != nil {
		return ctrl.Result{}, err
	}

	var (
		selected   int32
		overlapped bool
		published  int
		pubErrs    []error
	)
	for _, it := range items {
		winner := artwork.Winner(profiles.Items, it)
		mine := winner != nil && winner.Name == req.Name
		switch {
		case mine:
			selected++
		case self != nil && artwork.Selects(self, it):
			overlapped = true
		}
		named := it.Overlay != nil && it.Overlay.ProfileRef == req.Name
		if !mine && !named {
			continue
		}
		want := artwork.Plan(it, profiles.Items, it.PosterDigest)
		if want.RecordedBy(it.Overlay) {
			continue
		}
		if err := artwork.Publish(ctx, r.Bus, it, Token(want, it), artwork.ReasonProfile); err != nil {
			pubErrs = append(pubErrs, err)
			continue
		}
		published++
	}
	if published > 0 {
		log.Debug("overlayprofile: published render tasks", "count", published)
	}
	pubErr := errors.Join(pubErrs...)
	if self == nil {
		return ctrl.Result{}, pubErr
	}

	// Re-Get immediately before the apply: the loop above is a List and a
	// publish per stale item, the read-work-apply shape CLAUDE.md's
	// lost-update rule names. The conditions are seeded from the fresh
	// read; hash and selection come from the spec the selection was
	// computed against, so the two always describe one generation.
	var fresh catalogv1alpha1.OverlayProfile
	if err := r.Client.Get(ctx, req.NamespacedName, &fresh); err != nil {
		return ctrl.Result{}, errors.Join(client.IgnoreNotFound(err), pubErr)
	}
	conditions := append([]metav1.Condition(nil), fresh.Status.Conditions...)
	switch selErr := validSelector(self); {
	case selErr != nil:
		k8s.MarkReady(&fresh, &conditions, false, ReasonInvalidSelector, "spec.selector: %v", selErr)
	case self.Spec.Selector == nil:
		k8s.MarkReady(&fresh, &conditions, true, k8s.ReasonReconciled, "spec.selector is unset; the profile selects nothing")
	default:
		k8s.MarkReady(&fresh, &conditions, true, k8s.ReasonReconciled, "%d item(s) selected", selected)
	}
	if overlapped {
		k8s.MarkTrue(&fresh, &conditions, ConditionOverlap, ReasonSelectorOverlap,
			"the selector also matches item(s) a lower-named profile wins; those keep the other profile's overlay")
	} else {
		k8s.MarkFalse(&fresh, &conditions, ConditionOverlap, ReasonNoOverlap, "no selector overlap with another profile")
	}

	ac := catalogac.OverlayProfile(fresh.Name, fresh.Namespace).WithStatus(catalogac.OverlayProfileStatus().
		WithHash(Hash(self.Spec)).
		WithSelected(selected).
		WithConditions(k8s.ConditionACs(conditions)...))
	if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr, ac); err != nil {
		tracing.RecordError(span, err)
		return ctrl.Result{}, errors.Join(err, pubErr)
	}
	return ctrl.Result{}, pubErr
}

// validSelector reports why p's selector cannot be parsed, or nil. A nil
// selector is valid and selects nothing.
func validSelector(p *catalogv1alpha1.OverlayProfile) error {
	if p.Spec.Selector == nil {
		return nil
	}
	_, err := metav1.LabelSelectorAsSelector(p.Spec.Selector)
	return err
}

// Token is the digest slot of a render task's Msg-Id
// (schema.MsgIDForRenderOverlay): the wanted profile, the wanted inputs
// digest and the item's resourceVersion.
//
// The resourceVersion is what keeps a flip-back from being swallowed. A
// Msg-Id that is a function of the wanted state alone repeats whenever the
// state returns to a value it had inside the bus's duplicate window -- a
// label removed and restored, a geometry edit reverted -- and the second
// task is absorbed while the overlay is wrong. Any such return changes the
// item or its status, and so its resourceVersion; a hot loop over an
// unchanged item still publishes once. Only a stale item is published at
// all, so the churn is bounded by what actually needs rendering.
func Token(want artwork.Want, it artwork.Item) string {
	sum := sha256.Sum256([]byte(want.ProfileName() + "\n" + want.InputsDigest + "\n" + it.Object.GetResourceVersion()))
	return hex.EncodeToString(sum[:])
}

// items lists the Movies and Series in ns.
func (r *Reconciler) items(ctx context.Context, ns string) ([]artwork.Item, error) {
	var movies catalogv1alpha1.MovieList
	if err := r.Client.List(ctx, &movies, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("overlayprofile: list Movies: %w", err)
	}
	var series catalogv1alpha1.SeriesList
	if err := r.Client.List(ctx, &series, client.InNamespace(ns)); err != nil {
		return nil, fmt.Errorf("overlayprofile: list Series: %w", err)
	}
	out := make([]artwork.Item, 0, len(movies.Items)+len(series.Items))
	for i := range movies.Items {
		it, err := artwork.ItemOf(&movies.Items[i])
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	for i := range series.Items {
		it, err := artwork.ItemOf(&series.Items[i])
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, nil
}

// mapNamespaceProfiles enqueues every profile in o's namespace. A profile's
// selection is a whole-namespace computation (who wins an item depends on
// every profile), so a profile change re-resolves its siblings -- which is
// what makes Overlap converge -- and an item's label change re-resolves
// every profile that might now win or lose it.
func (r *Reconciler) mapNamespaceProfiles(ctx context.Context, o client.Object) []reconcile.Request {
	var list catalogv1alpha1.OverlayProfileList
	if err := r.Client.List(ctx, &list, client.InNamespace(o.GetNamespace())); err != nil {
		logging.FromContext(ctx).Warn("overlayprofile: list profiles for a watch event", "error", err)
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{
			Namespace: list.Items[i].Namespace, Name: list.Items[i].Name,
		}})
	}
	return reqs
}

// SetupWithManager registers the controller; catalogarr's setupControllers
// calls it.
//
// Items are watched for creation, deletion and label changes only: those
// decide selection. A poster or rating change is the gateway's to publish
// (spec §B.4), and the renderer's own status.overlay writes must not wake
// every profile.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	items := builder.WithPredicates(predicate.LabelChangedPredicate{})
	return ctrl.NewControllerManagedBy(mgr).
		Named("overlayprofile").
		For(&catalogv1alpha1.OverlayProfile{}, builder.WithPredicates(k8s.GenerationChanged())).
		Watches(&catalogv1alpha1.OverlayProfile{}, handler.EnqueueRequestsFromMapFunc(r.mapNamespaceProfiles),
			builder.WithPredicates(k8s.GenerationChanged())).
		Watches(&catalogv1alpha1.Movie{}, handler.EnqueueRequestsFromMapFunc(r.mapNamespaceProfiles), items).
		Watches(&catalogv1alpha1.Series{}, handler.EnqueueRequestsFromMapFunc(r.mapNamespaceProfiles), items).
		WithOptions(controller.Options{
			RecoverPanic:          ptr.To(true),
			ReconciliationTimeout: 5 * time.Minute,
		}).
		Complete(r)
}
