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

// Package transcodeprofile reconciles TranscodeProfile: it hashes and
// validates each profile (pkg/transcode.ProfileHash, the "Invalid" shapes
// pkg/transcode.Plan can never succeed against) and is the mapper that makes
// the rest of Phase E do anything -- for every MediaFile a profile wins (its
// own spec.selector, or spec.default when no selector-matching profile
// claims the file), it creates the TranscodeJob that names it, deterministic
// on (MediaFile, profile hash) so a re-reconcile creates nothing new and a
// profile edit creates exactly one.
//
// It never plans a transcode itself (pkg/transcode.Plan needs the MediaFile's
// stored probe and Capabilities, which is task E-2's TranscodeJob
// controller's job, run from inside the same manager process but a distinct
// reconciler) and never touches batch/v1 -- this package only ever writes
// TranscodeJob.spec (via k8s.Apply, main resource, not status) and
// TranscodeProfile.status (via squasharr/status.PatchProfile).
package transcodeprofile

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

	transcodeac "github.com/mediactl/clustarr/api/applyconfiguration/transcode/transcode/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	squasharrstatus "github.com/mediactl/clustarr/squasharr/status"
	"github.com/mediactl/clustarr/squasharr/task"
)

// This controller's own RBAC, on top of what squasharr/status already grants
// for the /status subresources it writes through. The blank line below is
// load-bearing -- see squasharr/status/doc.go and
// cmd/clustarr.TestRBACMarkersArePackageLevel: controller-gen only collects
// +kubebuilder:rbac from a comment group that is NOT a declaration's doc
// comment, and attaching this block to SetupWithManager would make every
// rule in it silently absent from config/rbac/role.yaml.
//
// transcodejobs needs create (and update, alongside patch, for the same
// reason mediafile_controller.go's own main-resource marker lists all three:
// server-side apply's create-if-absent path is not covered by patch alone)
// because this package is the one thing in Phase E that creates them, and
// delete because it replaces a job that failed on a changed source (spec
// §18.3); every other transcode.clustarr.io marker elsewhere in squasharr
// only reads or writes status. mediafiles is read-only: this controller only
// ever reads a MediaFile's spec.path and status to decide whether and what
// to create.
//
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodeprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodejobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=movies;episodes,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconciler owns TranscodeProfile.status (under k8s.ManagerSquasharr, via
// squasharr/status.PatchProfile) and is the sole creator of TranscodeJob
// objects (also k8s.ManagerSquasharr, on the main resource). It never writes
// MediaFile or TranscodeJob.status.
type Reconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
}

// NewReconciler builds a Reconciler.
func NewReconciler(c client.Client, scheme *runtime.Scheme, recorder events.EventRecorder) *Reconciler {
	return &Reconciler{Client: c, Scheme: scheme, Recorder: recorder}
}

// Reconcile computes tp's hash and validity, resolves which MediaFiles it
// wins against every other TranscodeProfile (selectFiles) among those whose
// Movie or Episode still exists (managedFiles), creates the
// TranscodeJob for each winning file that is probed and not already
// transcoded to tp's current hash, counts this profile's pending/running
// TranscodeJobs, and patches status once.
//
// It lists every TranscodeProfile, every eligible MediaFile and every
// TranscodeJob on each pass rather than filtering server-side: the same
// client-side-filter choice mediafile_controller.go's
// latestUnincorporatedTranscode documents (a raw/uncached client used
// directly in a test cannot send a fieldSelector on a path the CRD does not
// declare selectable) applies identically here, and unlike a single
// MediaFile's TranscodeJobs, resolving "who wins this file" is inherently a
// whole-collection computation -- it cannot be answered from tp alone.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "transcodeprofile.Reconcile")
	defer span.End()
	log := logging.FromContext(ctx).With("transcodeprofile", req.Name)

	var tp transcodev1alpha1.TranscodeProfile
	if err := r.Get(ctx, req.NamespacedName, &tp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if k8s.IsDeleting(&tp) {
		return ctrl.Result{}, nil
	}

	var profileList transcodev1alpha1.TranscodeProfileList
	if err := r.List(ctx, &profileList); err != nil {
		return ctrl.Result{}, fmt.Errorf("transcodeprofile: list TranscodeProfiles: %w", err)
	}

	hash := profileHash(tp.Spec)
	invalid, invalidReason, invalidMessage := validateProfile(&tp, profileList.Items)

	var mfList catalogv1alpha1.MediaFileList
	if err := r.List(ctx, &mfList); err != nil {
		return ctrl.Result{}, fmt.Errorf("transcodeprofile: list MediaFiles: %w", err)
	}
	items, err := r.catalogItems(ctx)
	if err != nil {
		return ctrl.Result{}, err
	}
	matching, overlapped := selectFiles(&tp, profileList.Items, defaultWinner(profileList.Items),
		managedFiles(mfList.Items, items))

	var jobList transcodev1alpha1.TranscodeJobList
	if err := r.List(ctx, &jobList); err != nil {
		return ctrl.Result{}, fmt.Errorf("transcodeprofile: list TranscodeJobs: %w", err)
	}
	pending, running := countJobs(jobList.Items, tp.Name)
	jobsByName := make(map[types.NamespacedName]*transcodev1alpha1.TranscodeJob, len(jobList.Items))
	for i := range jobList.Items {
		jobsByName[client.ObjectKeyFromObject(&jobList.Items[i])] = &jobList.Items[i]
	}

	created := 0
	var createErrs []error
	if !invalid {
		tag := profileTag(tp.Name, hash)
		for _, mf := range matching {
			if !probed(mf) || alreadyTranscoded(mf, tag) {
				continue
			}
			// A job that failed because its source changed can never
			// succeed: its sourceProbeHash is immutable. Once the MediaFile
			// has a new probe, replace the job so the next pass plans the
			// new file (spec §18.3). It is the job ensureTranscodeJob would
			// otherwise re-apply -- same name, since the name is (file,
			// profile hash) -- where the new sourceProbeHash would be
			// refused as immutable.
			key := types.NamespacedName{Namespace: mf.Namespace, Name: transcodeJobName(mf.Name, hash)}
			if old, ok := jobForFile(jobsByName, key, tp.Name, mf.Name); ok && old.Spec.SourceProbeHash != mf.Status.ProbeHash {
				if sourceChangedSince(old, mf) {
					if err := r.Delete(ctx, old, client.Preconditions{UID: &old.UID}); client.IgnoreNotFound(err) != nil {
						createErrs = append(createErrs, fmt.Errorf("transcodeprofile: replace TranscodeJob %s: %w", key, err))
					} else {
						log.Info("replacing a TranscodeJob whose source changed", "transcodeJob", key.String(),
							"sourceProbeHash", old.Spec.SourceProbeHash, "probeHash", mf.Status.ProbeHash)
					}
					continue
				}
				// Otherwise the job was made for an earlier probe of this
				// file and is not done with it: re-applying the new probe
				// hash would be refused as immutable on every pass. It is
				// left to its worker, whose SourceChanged report makes it
				// replaceable above, or -- blocked -- to the user's delete
				// (spec §18.4).
				log.Info("leaving a TranscodeJob made for an earlier probe of its file", "mediaFile",
					mf.Namespace+"/"+mf.Name, "transcodeJob", key.String(), "phase", string(old.Status.Phase),
					"sourceProbeHash", old.Spec.SourceProbeHash, "probeHash", mf.Status.ProbeHash)
				continue
			}
			if err := r.ensureTranscodeJob(ctx, &tp, mf, hash); err != nil {
				createErrs = append(createErrs, err)
				continue
			}
			created++
		}
	}
	if len(createErrs) > 0 {
		log.Error("create TranscodeJob", "error", errors.Join(createErrs...))
	}

	// Re-Get immediately before the status apply: everything above is a
	// List/Apply round trip against the apiserver, which is exactly the
	// read-then-slow-work-then-apply shape CLAUDE.md's "lost update" hazard
	// describes. tp is seeded fresh so the apply carries forward whatever
	// concurrent state landed since the Get at the top of this func, rather
	// than silently reverting it.
	var fresh transcodev1alpha1.TranscodeProfile
	if err := r.Get(ctx, req.NamespacedName, &fresh); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	conditions := append([]metav1.Condition(nil), fresh.Status.Conditions...)
	if invalid {
		k8s.MarkTrue(&fresh, &conditions, transcodev1alpha1.TranscodeProfileConditionInvalid, invalidReason, "%s", invalidMessage)
		k8s.MarkReady(&fresh, &conditions, false, invalidReason, "%s", invalidMessage)
		if r.Recorder != nil {
			r.Recorder.Eventf(&fresh, nil, "Warning", invalidReason, "Reconcile", invalidMessage)
		}
	} else {
		k8s.MarkFalse(&fresh, &conditions, transcodev1alpha1.TranscodeProfileConditionInvalid, k8s.ReasonReconciled, "profile is valid")
		k8s.MarkReady(&fresh, &conditions, true, k8s.ReasonReconciled,
			"%d matching file(s), %d job(s) created this pass", len(matching), created)
	}
	if overlapped {
		k8s.MarkTrue(&fresh, &conditions, ConditionOverlap, ReasonSelectorOverlap,
			"this profile's selector also matches file(s) a higher-priority profile claims; no job was created for them")
	} else {
		k8s.MarkFalse(&fresh, &conditions, ConditionOverlap, ReasonNoOverlap, "no selector overlap with another profile")
	}

	err = squasharrstatus.PatchProfile(ctx, r.Client, k8s.ManagerSquasharr, &fresh,
		func(ac *transcodeac.TranscodeProfileStatusApplyConfiguration) {
			ac.WithObservedGeneration(fresh.Generation).
				WithHash(hash).
				WithMatchingFiles(int32(len(matching))).
				WithPendingJobs(pending).
				WithRunningJobs(running).
				WithConditions(k8s.ConditionACs(conditions)...)
		})
	if err != nil {
		return ctrl.Result{}, err
	}
	if len(createErrs) > 0 {
		return ctrl.Result{}, errors.Join(createErrs...)
	}
	return ctrl.Result{}, nil
}

// catalogItems lists, once per reconcile, every Movie and Episode that
// exists, as metadata only: [managedFiles] needs their names, and a metadata
// informer holds a fraction of what full objects would (a library's
// Episodes number in the thousands).
func (r *Reconciler) catalogItems(ctx context.Context) (map[itemKey]bool, error) {
	items := map[itemKey]bool{}
	for kind, listKind := range managedItemLists {
		list := &metav1.PartialObjectMetadataList{}
		list.SetGroupVersionKind(catalogv1alpha1.GroupVersion.WithKind(listKind))
		if err := r.List(ctx, list); err != nil {
			return nil, fmt.Errorf("transcodeprofile: list %s: %w", listKind, err)
		}
		for i := range list.Items {
			items[itemKey{kind: kind, namespace: list.Items[i].Namespace, name: list.Items[i].Name}] = true
		}
	}
	return items, nil
}

// createdOrDeleted passes an item's create and delete, the two events that
// change whether its files are candidates; an item's updates never do.
func createdOrDeleted() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return true },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		UpdateFunc:  func(event.UpdateEvent) bool { return false },
		GenericFunc: func(event.GenericEvent) bool { return false },
	}
}

// countJobs sums pending (Pending/Planned/Queued -- admitted but not yet
// encoding) and running (Running/Verifying) TranscodeJobs for profileName.
// Succeeded, Failed and Skipped are terminal and counted in neither: they are
// history, not load.
func countJobs(jobs []transcodev1alpha1.TranscodeJob, profileName string) (pending, running int32) {
	for i := range jobs {
		tj := &jobs[i]
		if tj.Spec.ProfileRef != profileName {
			continue
		}
		switch tj.Status.Phase {
		case transcodev1alpha1.TranscodeJobPhasePending, transcodev1alpha1.TranscodeJobPhasePlanned, transcodev1alpha1.TranscodeJobPhaseQueued:
			pending++
		case transcodev1alpha1.TranscodeJobPhaseRunning, transcodev1alpha1.TranscodeJobPhaseVerifying:
			running++
		}
	}
	return pending, running
}

// jobForFile returns the listed TranscodeJob named key, when it is this
// profile's job for the MediaFile mfName.
func jobForFile(jobs map[types.NamespacedName]*transcodev1alpha1.TranscodeJob, key types.NamespacedName,
	profile, mfName string,
) (*transcodev1alpha1.TranscodeJob, bool) {
	tj, ok := jobs[key]
	if !ok || tj.Spec.ProfileRef != profile || tj.Spec.MediaFileRef != mfName {
		return nil, false
	}
	return tj, true
}

// sourceChangedSince reports whether tj failed because its source changed
// and mf now carries a different probe: the job can never succeed, and the
// file it names is a new one to plan (spec §18.3).
func sourceChangedSince(tj *transcodev1alpha1.TranscodeJob, mf *catalogv1alpha1.MediaFile) bool {
	return tj.Status.Phase == transcodev1alpha1.TranscodeJobPhaseFailed &&
		failedReason(tj) == string(task.ReasonSourceChanged) &&
		mf.Status.ProbeHash != "" && tj.Spec.SourceProbeHash != mf.Status.ProbeHash &&
		!k8s.IsDeleting(tj)
}

// failedReason is the reason of tj's Failed condition, or "".
func failedReason(tj *transcodev1alpha1.TranscodeJob) string {
	if c := k8s.FindCondition(tj.Status.Conditions, transcodev1alpha1.TranscodeJobConditionFailed); c != nil {
		return c.Reason
	}
	return ""
}

// ensureTranscodeJob creates (or, idempotently, re-applies) the TranscodeJob
// for mf against tp at hash. transcodeJobName is deterministic on
// (mf.Name, hash), so a re-reconcile's Apply is a no-op server-side-apply
// against the same object; every TranscodeJobSpec field it sets is CEL
// self==oldSelf, so this never attempts to change one on an existing job.
func (r *Reconciler) ensureTranscodeJob(
	ctx context.Context,
	tp *transcodev1alpha1.TranscodeProfile,
	mf *catalogv1alpha1.MediaFile,
	hash string,
) error {
	name := transcodeJobName(mf.Name, hash)

	ownerRef, err := k8s.OwnerReferenceAC(mf, r.Scheme)
	if err != nil {
		return fmt.Errorf("transcodeprofile: owner reference for MediaFile %s/%s: %w", mf.Namespace, mf.Name, err)
	}

	spec := transcodeac.TranscodeJobSpec().
		WithMediaFileRef(mf.Name).
		WithProfileRef(tp.Name).
		WithSourcePath(mf.Spec.Path).
		WithSourceProbeHash(mf.Status.ProbeHash).
		WithPriority(tp.Spec.Priority)

	job := transcodeac.TranscodeJob(name, mf.Namespace).
		WithOwnerReferences(ownerRef).
		WithSpec(spec)

	if _, err := k8s.Apply(ctx, r.Client, k8s.ManagerSquasharr, job); err != nil {
		return fmt.Errorf("transcodeprofile: create TranscodeJob %s/%s: %w", mf.Namespace, name, err)
	}
	return nil
}

// extractProbeHash is the §10-documented predicate function from
// pkg/k8s/predicates.go's own StatusFieldChanged doc comment ("squasharr's
// transcodeprofile watch, from §10") -- restated here, not imported, because
// it is a closure over this package's concrete MediaFile type.
func extractProbeHash(o client.Object) string {
	mf, ok := o.(*catalogv1alpha1.MediaFile)
	if !ok {
		return ""
	}
	return mf.Status.ProbeHash
}

// extractJobPhase is this controller's TranscodeJob-watch predicate: a
// pending/running count is only ever stale after a phase transition, so that
// is the one field worth waking every profile's reconcile for.
func extractJobPhase(o client.Object) string {
	tj, ok := o.(*transcodev1alpha1.TranscodeJob)
	if !ok {
		return ""
	}
	return string(tj.Status.Phase)
}

// mapAllProfiles fires whenever ANY TranscodeProfile is created, generation-
// changed or deleted, and enqueues every profile (including the one that
// changed). This is what makes the cross-profile rulings in profile.go
// (duplicate spec.default, selector overlap) converge: a second profile
// turning on spec.default has to re-validate the first one too, and neither
// profile's own For-watch would ever see the other's change.
func (r *Reconciler) mapAllProfiles(ctx context.Context, _ client.Object) []reconcile.Request {
	var list transcodev1alpha1.TranscodeProfileList
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
// Ineligible-kind files (see eligibleKind) never reach here at all, so a
// book or comic MediaFile never wakes a transcode profile.
func (r *Reconciler) mapMediaFileToProfiles(ctx context.Context, o client.Object) []reconcile.Request {
	mf, ok := o.(*catalogv1alpha1.MediaFile)
	if !ok || !eligibleKind(mf.Spec.MediaRef.Kind) {
		return nil
	}
	var list transcodev1alpha1.TranscodeProfileList
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

// mapTranscodeJobToProfile enqueues the one profile a TranscodeJob names in
// spec.profileRef -- that reference is immutable (CEL self==oldSelf), so
// there is never more than one.
func mapTranscodeJobToProfile(_ context.Context, o client.Object) []reconcile.Request {
	tj, ok := o.(*transcodev1alpha1.TranscodeJob)
	if !ok || tj.Spec.ProfileRef == "" {
		return nil
	}
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: tj.Spec.ProfileRef}}}
}

// SetupWithManager registers the TranscodeProfile controller; squasharr's
// run.go setupControllers calls it.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("transcodeprofile").
		For(&transcodev1alpha1.TranscodeProfile{}, builder.WithPredicates(k8s.GenerationChanged())).
		Watches(&transcodev1alpha1.TranscodeProfile{}, handler.EnqueueRequestsFromMapFunc(r.mapAllProfiles),
			builder.WithPredicates(k8s.GenerationChanged())).
		Watches(&catalogv1alpha1.MediaFile{}, handler.EnqueueRequestsFromMapFunc(r.mapMediaFileToProfiles),
			builder.WithPredicates(k8s.StatusFieldChanged(extractProbeHash))).
		Watches(&transcodev1alpha1.TranscodeJob{}, handler.EnqueueRequestsFromMapFunc(mapTranscodeJobToProfile),
			builder.WithPredicates(k8s.StatusFieldChanged(extractJobPhase))).
		// A Movie or Episode appearing or going away changes which files are
		// candidates (managedFiles). Metadata only, matching catalogItems'
		// list, so no full-object informer is ever started for them.
		Watches(&catalogv1alpha1.Movie{}, handler.EnqueueRequestsFromMapFunc(r.mapAllProfiles),
			builder.OnlyMetadata, builder.WithPredicates(createdOrDeleted())).
		Watches(&catalogv1alpha1.Episode{}, handler.EnqueueRequestsFromMapFunc(r.mapAllProfiles),
			builder.OnlyMetadata, builder.WithPredicates(createdOrDeleted())).
		WithOptions(controller.Options{
			RecoverPanic:          ptr.To(true),
			ReconciliationTimeout: 5 * time.Minute,
		}).
		Complete(r)
}
