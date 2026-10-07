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

// Package subtitleprofile reconciles SubtitleProfile's own status (loop
// spec §2.13): it validates each profile -- a duplicate spec.default, a
// language key that does not match its canonical derivation, a filter that
// does not compile -- counts the MediaFiles the remediation loop planned
// with it (through the remediation.mediafile.subtitleProfile index), and
// marks a profile whose selector also matches files another profile wins
// (Overlap). It writes only SubtitleProfile status and never watches
// MediaFile: the loop signals its Wake when a file's winning profile or
// subtitles phase changes (D3).
package subtitleprofile

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	subtitleac "github.com/mediactl/clustarr/api/applyconfiguration/subtitle/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	captionstatus "github.com/mediactl/clustarr/app/caption/status"
	"github.com/mediactl/clustarr/app/caption/subtitleplan"
	"github.com/mediactl/clustarr/app/remediation/mfindex"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// This controller's own RBAC. SubtitleProfile is cluster-scoped. mediafiles
// is read-only: the counts come from the loop's index, and Overlap reads the
// files' labels.
//
// The blank line below is load-bearing: controller-gen only collects
// +kubebuilder:rbac from a comment group that is NOT a declaration's doc
// comment.
//
// +kubebuilder:rbac:groups=subtitle.clustarr.io,resources=subtitleprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=subtitle.clustarr.io,resources=subtitleprofiles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconciler owns SubtitleProfile.status (under k8s.ManagerCaption, via
// app/caption/status.PatchProfile). It writes nothing else.
type Reconciler struct {
	client.Client
	Recorder events.EventRecorder
}

// NewReconciler builds a Reconciler.
func NewReconciler(c client.Client, recorder events.EventRecorder) *Reconciler {
	return &Reconciler{Client: c, Recorder: recorder}
}

// Reconcile validates sp, counts the files the loop planned with it and
// whether its selector overlaps another profile's wins, and applies status
// only when it changed (§2.13: an unchanged apply is skipped).
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "subtitleprofile.Reconcile")
	defer span.End()
	var sp subtitlev1alpha1.SubtitleProfile
	if err := r.Get(ctx, req.NamespacedName, &sp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if k8s.IsDeleting(&sp) {
		return ctrl.Result{}, nil
	}
	var profiles subtitlev1alpha1.SubtitleProfileList
	if err := r.List(ctx, &profiles, client.UnsafeDisableDeepCopy); err != nil {
		return ctrl.Result{}, fmt.Errorf("subtitleprofile: list SubtitleProfiles: %w", err)
	}
	invalid, reason, message := validateProfile(&sp, profiles.Items)
	var planned catalogv1alpha1.MediaFileList
	if err := r.List(ctx, &planned, client.MatchingFields{mfindex.SubtitleProfile: sp.Name}, client.UnsafeDisableDeepCopy); err != nil {
		return ctrl.Result{}, fmt.Errorf("subtitleprofile: list the files planned with %s: %w", sp.Name, err)
	}
	overlapped, err := r.overlapped(ctx, &sp, profiles.Items)
	if err != nil {
		return ctrl.Result{}, err
	}

	next := sp.Status.DeepCopy()
	next.ObservedGeneration, next.WantedKeys = sp.Generation, wantedKeys(sp.Spec)
	next.MatchingFiles = int32(len(planned.Items)) //nolint:gosec // bounded by the library
	conds := next.Conditions
	if invalid {
		k8s.MarkTrue(&sp, &conds, subtitlev1alpha1.SubtitleProfileConditionInvalid, reason, "%s", message)
		k8s.MarkReady(&sp, &conds, false, reason, "%s", message)
	} else {
		k8s.MarkFalse(&sp, &conds, subtitlev1alpha1.SubtitleProfileConditionInvalid, k8s.ReasonReconciled, "profile is valid")
		k8s.MarkReady(&sp, &conds, true, k8s.ReasonReconciled, "%d file(s) planned with this profile", next.MatchingFiles)
	}
	if overlapped {
		k8s.MarkTrue(&sp, &conds, ConditionOverlap, ReasonSelectorOverlap,
			"this profile's selector also matches file(s) a higher-priority profile wins")
	} else {
		k8s.MarkFalse(&sp, &conds, ConditionOverlap, ReasonNoOverlap, "no selector overlap with another profile")
	}
	next.Conditions = conds
	if equality.Semantic.DeepEqual(*next, sp.Status) {
		return ctrl.Result{}, nil
	}
	if invalid && r.Recorder != nil {
		r.Recorder.Eventf(&sp, nil, "Warning", reason, "Reconcile", "%s", message)
	}
	return ctrl.Result{}, captionstatus.PatchProfile(ctx, r.Client, k8s.ManagerCaption, &sp,
		func(ac *subtitleac.SubtitleProfileStatusApplyConfiguration) {
			ac.WithObservedGeneration(next.ObservedGeneration).WithMatchingFiles(next.MatchingFiles).
				WithWantedKeys(next.WantedKeys...).WithConditions(k8s.ConditionACs(conds)...)
		})
}

// overlapped is §2.13's Overlap: a selector pass over the cached video
// files' labels, true when another profile wins a file sp's selector matches.
func (r *Reconciler) overlapped(ctx context.Context, sp *subtitlev1alpha1.SubtitleProfile,
	profiles []subtitlev1alpha1.SubtitleProfile,
) (bool, error) {
	if sp.Spec.Selector == nil {
		return false, nil
	}
	var files catalogv1alpha1.MediaFileList
	if err := r.List(ctx, &files, client.UnsafeDisableDeepCopy); err != nil {
		return false, fmt.Errorf("subtitleprofile: list MediaFiles: %w", err)
	}
	for i := range files.Items {
		mf := &files.Items[i]
		if !subtitleplan.EligibleKind(mf.Spec.MediaRef.Kind) || !subtitleplan.SelectorMatches(sp, mf.Labels) {
			continue
		}
		if w := subtitleplan.WinningProfile(mf.Labels, profiles); w != nil && w.Name != sp.Name {
			return true, nil
		}
	}
	return false, nil
}

// mapAllProfiles fires whenever ANY SubtitleProfile is created,
// generation-changed or deleted, and enqueues every profile (including the
// one that changed). This is what makes the cross-profile rulings in
// profile.go (duplicate spec.default, selector overlap, a losing profile's
// Overlap condition) converge: a second profile turning on spec.default has
// to re-validate the first one too, and neither profile's own For-watch
// would ever see the other's change.
func (r *Reconciler) mapAllProfiles(ctx context.Context, _ client.Object) []reconcile.Request {
	var list subtitlev1alpha1.SubtitleProfileList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	reqs := make([]reconcile.Request, 0, len(list.Items))
	for i := range list.Items {
		reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: list.Items[i].Name}})
	}
	return reqs
}

// SetupWithManager registers the controller (name subtitleprofile) and the
// index it counts through. Its triggers are its own generation, any
// profile's generation (a second default and an overlap converge across
// profiles), and the loop's Wake.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager, wake *Wake) error {
	if err := mfindex.RegisterSubtitleProfile(context.Background(), mgr.GetFieldIndexer()); err != nil {
		return err
	}
	all := handler.EnqueueRequestsFromMapFunc(r.mapAllProfiles)
	return ctrl.NewControllerManagedBy(mgr).
		Named("subtitleprofile").
		For(&subtitlev1alpha1.SubtitleProfile{}, builder.WithPredicates(k8s.GenerationChanged())).
		Watches(&subtitlev1alpha1.SubtitleProfile{}, all, builder.WithPredicates(k8s.GenerationChanged())).
		WatchesRawSource(source.Channel(wake.Channel(), all)).
		WithOptions(controller.Options{RecoverPanic: new(true), ReconciliationTimeout: 5 * time.Minute}).
		Complete(r)
}
