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

// Package subtitleprofile reconciles SubtitleProfile: it validates each
// profile (the "Invalid" shapes the SubtitleRequest controller and
// pkg/subtitles.Plan can never act on -- a duplicate spec.default, or a
// language key that does not match its own canonical derivation) and is the
// mapper that gives captionarr's other controllers something to do -- for
// every video-kind MediaFile a profile wins (its own spec.selector, or
// spec.default when no selector-matching profile claims the file) whose
// Movie or Episode still exists (itemSet.manages: an import list's
// removeAndKeep keeps a file Clustarr no longer manages), it
// ensures the one SubtitleRequest that names it, deterministic on the
// MediaFile's own name (SubtitleRequest's own doc comment: "owned by the
// MediaFile and shares its name") so a re-reconcile's apply is a no-op and a
// profile edit updates the existing request in place rather than creating a
// second one.
//
// It never plans a subtitle search itself (pkg/subtitles.Plan needs the
// MediaFile's probed audio languages and the request's existing/sidecar
// scan, which is the SubtitleRequest controller's job, task F-4, run from
// inside the same manager process but a distinct reconciler) -- this package
// only ever writes SubtitleRequest.spec (via k8s.Apply, main resource, not
// status) and SubtitleProfile.status (via app/caption/status.PatchProfile).
package subtitleprofile

import (
	"context"
	"errors"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	subtitleac "github.com/mediactl/clustarr/api/applyconfiguration/subtitle/subtitle/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/controller/subtitlerequest"
	"github.com/mediactl/clustarr/app/caption/itemindex"
	captionarrstatus "github.com/mediactl/clustarr/app/caption/status"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// This controller's own RBAC. SubtitleProfile is cluster-scoped (no
// namespaces verb needed); subtitlerequests needs create (and update,
// alongside patch, for the same server-side-apply create-if-absent reason
// app/squash/controller/transcodeprofile/controller.go's own marker comment
// documents) because this package is captionarr's only creator of them.
// mediafiles is read-only: this controller only ever reads a MediaFile's
// labels, kind and status.probeHash to decide whether and for whom to
// ensure a request. movies and episodes are read only to learn whether a
// file's item still exists (itemSet.manages).
//
// The blank line below is load-bearing -- see
// cmd/clustarr.TestRBACMarkersArePackageLevel: controller-gen only collects
// +kubebuilder:rbac from a comment group that is NOT a declaration's doc
// comment, and attaching this block to SetupWithManager would make every
// rule in it silently absent from config/rbac/role.yaml.
//
// +kubebuilder:rbac:groups=subtitle.clustarr.io,resources=subtitleprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=subtitle.clustarr.io,resources=subtitleprofiles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=subtitle.clustarr.io,resources=subtitlerequests,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;episodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconciler owns SubtitleProfile.status (under k8s.ManagerCaptionarr, via
// app/caption/status.PatchProfile) and is the sole creator of SubtitleRequest
// objects (also k8s.ManagerCaptionarr, on the main resource). It never
// writes MediaFile or SubtitleRequest.status.
type Reconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
}

// NewReconciler builds a Reconciler.
func NewReconciler(c client.Client, scheme *runtime.Scheme, recorder events.EventRecorder) *Reconciler {
	return &Reconciler{Client: c, Scheme: scheme, Recorder: recorder}
}

// Reconcile validates sp, resolves which video-kind MediaFiles it wins
// against every other SubtitleProfile (selectFiles), ensures the
// SubtitleRequest for each winning file, and patches status once.
//
// Like transcodeprofile.Reconciler.Reconcile, it lists every SubtitleProfile
// and every eligible MediaFile on each pass rather than filtering
// server-side: resolving "who wins this file" is inherently a
// whole-collection computation, and a raw/uncached client used directly in a
// test cannot send a fieldSelector on a path the CRD does not declare
// selectable.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "subtitleprofile.Reconcile")
	defer span.End()
	log := logging.FromContext(ctx).With("subtitleprofile", req.Name)

	var sp subtitlev1alpha1.SubtitleProfile
	if err := r.Get(ctx, req.NamespacedName, &sp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if k8s.IsDeleting(&sp) {
		return ctrl.Result{}, nil
	}

	var profileList subtitlev1alpha1.SubtitleProfileList
	if err := r.List(ctx, &profileList); err != nil {
		return ctrl.Result{}, fmt.Errorf("subtitleprofile: list SubtitleProfiles: %w", err)
	}

	invalid, invalidReason, invalidMessage := validateProfile(&sp, profileList.Items)
	wanted := wantedKeys(sp.Spec)

	// The four library-sized Lists below are read straight from the cache,
	// without a deep copy (client.UnsafeDisableDeepCopy, a no-op on a raw
	// client): on the owner's library they are tens of thousands of
	// objects per reconcile. Nothing in this reconcile writes to any of
	// them -- selectFiles, MayWant, newItemSet, unneeded and
	// requestCurrent only read, and the delete and the apply below are
	// built from fresh objects -- and nothing may: they are the cache's
	// own copies.
	var mfList catalogv1alpha1.MediaFileList
	if err := r.List(ctx, &mfList, client.UnsafeDisableDeepCopy); err != nil {
		return ctrl.Result{}, fmt.Errorf("subtitleprofile: list MediaFiles: %w", err)
	}
	// Once per reconcile, the items those files may reference: a MediaFile
	// whose Movie or Episode is gone -- one an import list's removeAndKeep
	// left behind -- is not selected (itemSet.manages).
	var movies catalogv1alpha1.MovieList
	if err := r.List(ctx, &movies, client.UnsafeDisableDeepCopy); err != nil {
		return ctrl.Result{}, fmt.Errorf("subtitleprofile: list Movies: %w", err)
	}
	var episodes catalogv1alpha1.EpisodeList
	if err := r.List(ctx, &episodes, client.UnsafeDisableDeepCopy); err != nil {
		return ctrl.Result{}, fmt.Errorf("subtitleprofile: list Episodes: %w", err)
	}
	matching, overlapped := selectFiles(&sp, profileList.Items, defaultWinner(profileList.Items), mfList.Items,
		newItemSet(movies.Items, episodes.Items))

	var requests subtitlev1alpha1.SubtitleRequestList
	if err := r.List(ctx, &requests, client.UnsafeDisableDeepCopy); err != nil {
		return ctrl.Result{}, fmt.Errorf("subtitleprofile: list SubtitleRequests: %w", err)
	}
	existing := make(map[types.NamespacedName]*subtitlev1alpha1.SubtitleRequest, len(requests.Items))
	for i := range requests.Items {
		existing[client.ObjectKeyFromObject(&requests.Items[i])] = &requests.Items[i]
	}

	ensured, needNone, unprobed := 0, 0, 0
	var ensureErrs []error
	if !invalid {
		for _, mf := range matching {
			// A request only where a subtitle may be wanted: none for a file
			// not probed yet (the probe watch brings it back), and none for
			// one whose tagged audio proves the profile wants nothing -- 87%
			// of the owner's library, whose requests were Satisfied with no
			// items. subtitlerequest.MayWant is the request controller's own
			// planner, so the two cannot disagree about a want.
			if mf.Status.MediaInfo == nil {
				unprobed++
				continue
			}
			if !subtitlerequest.MayWant(sp.Spec, mf.Status.MediaInfo) {
				needNone++
				if sr, ok := existing[client.ObjectKeyFromObject(mf)]; ok && unneeded(sr) {
					// Deleted through a fresh object, never the cache's own.
					gone := &subtitlev1alpha1.SubtitleRequest{ObjectMeta: metav1.ObjectMeta{Name: sr.Name, Namespace: sr.Namespace}}
					uid := sr.UID
					if err := r.Delete(ctx, gone, client.Preconditions{UID: &uid}); client.IgnoreNotFound(err) != nil {
						ensureErrs = append(ensureErrs, fmt.Errorf("subtitleprofile: delete unneeded SubtitleRequest %s: %w",
							client.ObjectKeyFromObject(sr), err))
					}
				}
				continue
			}
			if r.requestCurrent(existing[client.ObjectKeyFromObject(mf)], &sp, mf) {
				ensured++
				continue
			}
			if err := r.ensureSubtitleRequest(ctx, &sp, mf); err != nil {
				ensureErrs = append(ensureErrs, err)
				continue
			}
			ensured++
		}
	}
	if len(ensureErrs) > 0 {
		log.Error("ensure SubtitleRequest", "error", errors.Join(ensureErrs...))
	}

	// Re-Get immediately before the status apply: everything above is a
	// List/Apply round trip against the apiserver, which is exactly the
	// read-then-slow-work-then-apply shape CLAUDE.md's "lost update" hazard
	// describes. sp is seeded fresh so the apply carries forward whatever
	// concurrent state landed since the Get at the top of this func, rather
	// than silently reverting it.
	var fresh subtitlev1alpha1.SubtitleProfile
	if err := r.Get(ctx, req.NamespacedName, &fresh); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	conditions := append([]metav1.Condition(nil), fresh.Status.Conditions...)
	if invalid {
		k8s.MarkTrue(&fresh, &conditions, subtitlev1alpha1.SubtitleProfileConditionInvalid, invalidReason, "%s", invalidMessage)
		k8s.MarkReady(&fresh, &conditions, false, invalidReason, "%s", invalidMessage)
		if r.Recorder != nil {
			r.Recorder.Eventf(&fresh, nil, "Warning", invalidReason, "Reconcile", invalidMessage)
		}
	} else {
		k8s.MarkFalse(&fresh, &conditions, subtitlev1alpha1.SubtitleProfileConditionInvalid, k8s.ReasonReconciled, "profile is valid")
		k8s.MarkReady(&fresh, &conditions, true, k8s.ReasonReconciled,
			"%d matching file(s): %d request(s) ensured, %d wanting none, %d not probed yet", len(matching), ensured, needNone, unprobed)
	}
	if overlapped {
		k8s.MarkTrue(&fresh, &conditions, ConditionOverlap, ReasonSelectorOverlap,
			"this profile's selector also matches file(s) a higher-priority profile claims; no request was ensured for them")
	} else {
		k8s.MarkFalse(&fresh, &conditions, ConditionOverlap, ReasonNoOverlap, "no selector overlap with another profile")
	}

	err := captionarrstatus.PatchProfile(ctx, r.Client, k8s.ManagerCaptionarr, &fresh,
		func(ac *subtitleac.SubtitleProfileStatusApplyConfiguration) {
			ac.WithObservedGeneration(fresh.Generation).
				WithMatchingFiles(int32(len(matching))).
				WithWantedKeys(wanted...).
				WithConditions(k8s.ConditionACs(conditions)...)
		})
	if err != nil {
		return ctrl.Result{}, err
	}
	if len(ensureErrs) > 0 {
		return ctrl.Result{}, errors.Join(ensureErrs...)
	}
	return ctrl.Result{}, nil
}

// unneeded reports whether sr, for a file that now wants no subtitle, can be
// deleted: it tracks nothing (no status.items) and carries no user override
// in spec (languages, minScoreOverride, forceSearch), so deleting it loses
// no history and no choice anyone made. Anything else is left to the
// request controller.
func unneeded(sr *subtitlev1alpha1.SubtitleRequest) bool {
	return len(sr.Status.Items) == 0 && len(sr.Spec.Languages) == 0 &&
		sr.Spec.MinScoreOverride == nil && !sr.Spec.ForceSearch && !k8s.IsDeleting(sr)
}

// requestCurrent reports whether sr already carries exactly the declaration
// ensureSubtitleRequest would apply for mf under sp -- mediaFileRef, the
// winning profileRef, and mf's controller owner reference as the one
// owner -- so that apply would change nothing and is skipped. About ten
// thousand no-op applies per profile reconcile on the owner's library.
//
// Skipping is safe because that apply is the whole of k8s.ManagerCaptionarr's
// Apply-operation entry on SubtitleRequest's main resource: nothing else
// applies it under that manager (the request controller's forceSearch
// reset is a merge patch, an Update entry of its own, and status is a
// subresource), and no version of this controller ever applied more. So
// an apply whose values the object already holds releases nothing and
// sets nothing; at most it would claim ownership of a value someone else
// wrote, which changes no value and which the next differing reconcile
// claims anyway, by force. Equality is judged on the object read this
// reconcile, so a change under it -- another profileRef, a replaced
// MediaFile's new UID, a second owner -- is applied as before; a cache a
// moment behind defers that to the next reconcile, as a missed apply
// always did.
func (r *Reconciler) requestCurrent(sr *subtitlev1alpha1.SubtitleRequest, sp *subtitlev1alpha1.SubtitleProfile, mf *catalogv1alpha1.MediaFile) bool {
	if sr == nil || k8s.IsDeleting(sr) || sr.Spec.MediaFileRef != mf.Name || sr.Spec.ProfileRef != sp.Name ||
		len(sr.OwnerReferences) != 1 {
		return false
	}
	// The same reference ensureSubtitleRequest renders (k8s.OwnerReferenceAC
	// is this, as an apply configuration).
	want, err := k8s.OwnerReference(mf, r.Scheme)
	if err != nil {
		return false // ensureSubtitleRequest reports it
	}
	got := sr.OwnerReferences[0]
	return got.APIVersion == want.APIVersion && got.Kind == want.Kind && got.Name == want.Name && got.UID == want.UID &&
		ptr.Equal(got.Controller, want.Controller) && ptr.Equal(got.BlockOwnerDeletion, want.BlockOwnerDeletion)
}

// ensureSubtitleRequest creates (or, idempotently, re-applies) the
// SubtitleRequest for mf, owned by mf and named after it. Unlike
// squasharr's TranscodeJob (immutable spec, a new name per profile hash),
// SubtitleRequestSpec.ProfileRef is not CEL-immutable and this controller's
// apply only ever sets mediaFileRef and profileRef -- so a later reconcile
// that resolves a DIFFERENT winning profile for the same file re-applies
// under the same k8s.ManagerCaptionarr identity and simply updates
// profileRef server-side, with no conflict and no second object. Every
// other spec field (languages, minScoreOverride, forceSearch) is a
// user/SubtitleRequest-controller-owned override this apply deliberately
// never declares, so it can never release them.
func (r *Reconciler) ensureSubtitleRequest(
	ctx context.Context,
	sp *subtitlev1alpha1.SubtitleProfile,
	mf *catalogv1alpha1.MediaFile,
) error {
	ownerRef, err := k8s.OwnerReferenceAC(mf, r.Scheme)
	if err != nil {
		return fmt.Errorf("subtitleprofile: owner reference for MediaFile %s/%s: %w", mf.Namespace, mf.Name, err)
	}

	spec := subtitleac.SubtitleRequestSpec().
		WithMediaFileRef(mf.Name).
		WithProfileRef(sp.Name)

	request := subtitleac.SubtitleRequest(mf.Name, mf.Namespace).
		WithOwnerReferences(ownerRef).
		WithSpec(spec)

	if _, err := k8s.Apply(ctx, r.Client, k8s.ManagerCaptionarr, request); err != nil {
		return fmt.Errorf("subtitleprofile: ensure SubtitleRequest %s/%s: %w", mf.Namespace, mf.Name, err)
	}
	return nil
}

// extractProbeHash is this package's own restatement of the §10-documented
// predicate function (app/squash/controller/transcodeprofile/controller.go's
// own extractProbeHash carries the identical doc comment) -- not imported,
// because it is a closure over this package's concrete MediaFile type. This
// is the watch that wakes a SubtitleProfile reconcile when a MediaFile is
// probed: catalogarr's mediafile controller writes status.probeHash under
// its OWN field manager, which never bumps metadata.generation, so a
// GenerationChanged-only watch would never fire for it -- k8s.StatusFieldChanged
// is the whole reason this predicate function exists rather than
// builder.WithPredicates(k8s.GenerationChanged()) on this Watches call too.
func extractProbeHash(o client.Object) string {
	mf, ok := o.(*catalogv1alpha1.MediaFile)
	if !ok {
		return ""
	}
	return mf.Status.ProbeHash
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

// mapMediaFileToProfiles enqueues every profile that could plausibly win mf:
// every profile carrying spec.default (its fallback role may now apply or
// stop applying) plus every profile whose selector matches mf's labels.
// Ineligible-kind files (see eligibleKind) never reach here at all, so an
// audio, book or comic MediaFile never wakes a subtitle profile.
func (r *Reconciler) mapMediaFileToProfiles(ctx context.Context, o client.Object) []reconcile.Request {
	mf, ok := o.(*catalogv1alpha1.MediaFile)
	if !ok || !eligibleKind(mf.Spec.MediaRef.Kind) {
		return nil
	}
	var list subtitlev1alpha1.SubtitleProfileList
	if err := r.List(ctx, &list); err != nil {
		return nil
	}
	var reqs []reconcile.Request
	for i := range list.Items {
		p := &list.Items[i]
		if p.Spec.Default || selectorMatches(p, mf) {
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Name: p.Name}})
		}
	}
	return reqs
}

// mapItemToProfiles enqueues the profiles that could win a MediaFile of the
// Movie or Episode o: the item appearing (an import list re-adding it) makes
// its kept file eligible again, and the item going (removeAndKeep) stops
// counting the file -- neither touches the MediaFile, so its own watch never
// fires for them.
func (r *Reconciler) mapItemToProfiles(ctx context.Context, o client.Object) []reconcile.Request {
	var kind commonv1.MediaKind
	switch o.(type) {
	case *catalogv1alpha1.Movie:
		kind = commonv1.MediaKindMovie
	case *catalogv1alpha1.Episode:
		kind = commonv1.MediaKindEpisode
	default:
		return nil
	}
	// Through the item index, never a namespace-wide List: see itemindex.
	files, err := itemindex.MediaFilesOf(ctx, r, o.GetNamespace(), kind, o.GetName())
	if err != nil {
		logging.FromContext(ctx).Error("list MediaFiles for a catalog item change",
			"item", client.ObjectKeyFromObject(o), "err", err)
		return nil
	}
	seen := map[string]bool{}
	var reqs []reconcile.Request
	for i := range files {
		mf := &files[i]
		for _, req := range r.mapMediaFileToProfiles(ctx, mf) {
			if !seen[req.Name] {
				seen[req.Name] = true
				reqs = append(reqs, req)
			}
		}
	}
	return reqs
}

// createdOrDeleted passes creates and deletes only: an item's existence is
// all [itemSet.manages] reads, and catalogarr updates Movie and Episode
// status far too often for every update to replan every profile.
func createdOrDeleted() predicate.Predicate {
	return predicate.Funcs{UpdateFunc: func(event.UpdateEvent) bool { return false }}
}

// SetupWithManager registers the SubtitleProfile controller; captionarr's
// run.go setupControllers calls it for the controller role.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := itemindex.Register(context.Background(), mgr); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("subtitleprofile").
		For(&subtitlev1alpha1.SubtitleProfile{}, builder.WithPredicates(k8s.GenerationChanged())).
		Watches(&subtitlev1alpha1.SubtitleProfile{}, handler.EnqueueRequestsFromMapFunc(r.mapAllProfiles),
			builder.WithPredicates(k8s.GenerationChanged())).
		Watches(&catalogv1alpha1.MediaFile{}, handler.EnqueueRequestsFromMapFunc(r.mapMediaFileToProfiles),
			builder.WithPredicates(k8s.StatusFieldChanged(extractProbeHash))).
		Watches(&catalogv1alpha1.Movie{}, handler.EnqueueRequestsFromMapFunc(r.mapItemToProfiles),
			builder.WithPredicates(createdOrDeleted())).
		Watches(&catalogv1alpha1.Episode{}, handler.EnqueueRequestsFromMapFunc(r.mapItemToProfiles),
			builder.WithPredicates(createdOrDeleted())).
		WithOptions(controller.Options{
			RecoverPanic:          new(true),
			ReconciliationTimeout: 5 * time.Minute,
		}).
		Complete(r)
}
