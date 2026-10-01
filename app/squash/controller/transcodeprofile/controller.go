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

// Package transcodeprofile reconciles TranscodeProfile: it hashes each
// profile (worker.ProfileHash, over the standard's inputs) and validates it
// (a second default is Invalid), and is the mapper that makes the rest of
// squasharr do anything -- for every MediaFile a profile wins (its own
// spec.selector, or spec.default when no selector-matching profile claims
// the file) that is not already transcoded and has no open job of the
// profile, it creates the TranscodeJob that names it, deterministic on
// (MediaFile, profile hash) so a re-reconcile creates nothing new.
//
// It never plans a transcode itself (pkg/transcode.Plan needs the MediaFile's
// stored probe and Capabilities, which is task E-2's TranscodeJob
// controller's job, run from inside the same manager process but a distinct
// reconciler) and never touches batch/v1 -- this package only ever writes
// TranscodeJob.spec (via k8s.Apply, main resource, not status) and
// TranscodeProfile.status (via app/squash/status.PatchProfile).
package transcodeprofile

import (
	"context"
	"errors"
	"fmt"
	"sort"
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
	squasharrstatus "github.com/mediactl/clustarr/app/squash/status"
	"github.com/mediactl/clustarr/app/squash/task"
	busevents "github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// This controller's own RBAC, on top of what app/squash/status already grants
// for the /status subresources it writes through. The blank line below is
// load-bearing -- see app/squash/status/doc.go and
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
// app/squash/status.PatchProfile) and is the sole creator of TranscodeJob
// objects (also k8s.ManagerSquasharr, on the main resource). It never writes
// MediaFile or TranscodeJob.status.
type Reconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder

	// Window is the most non-terminal TranscodeJobs a profile keeps; 0 is
	// no limit.
	Window int
	// Retention is how long a Succeeded TranscodeJob is kept once its
	// MediaFile has been probed since; 0 keeps it for good.
	Retention time.Duration
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Progress is the clustarr-progress bucket GPU pool workers publish
	// their measured encoder limits to (task.PublishEncoderLimits), shown
	// as status.encoderLimits; nil shows none.
	Progress busevents.KV
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

	// The window: at most r.Window of this profile's jobs are not terminal,
	// and new ones are taken in name order, so a profile holds the next few
	// files rather than one job per matching file (10-20k objects per
	// re-roll on the owner's library). The rest wait, counted in status.
	sort.Slice(matching, func(i, j int) bool {
		return matching[i].Namespace+"/"+matching[i].Name < matching[j].Namespace+"/"+matching[j].Name
	})
	open := countOpen(jobList.Items, tp.Name)
	busy := openFiles(jobList.Items, tp.Name, mfList.Items)
	created, waiting := 0, 0
	var createErrs []error
	if !invalid {
		for _, mf := range matching {
			if !probed(mf) || alreadyTranscoded(mf, tp.Name) {
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
			if _, exists := jobsByName[key]; exists {
				continue // its spec is immutable: there is nothing to re-apply
			}
			if busy[types.NamespacedName{Namespace: mf.Namespace, Name: mf.Name}] {
				// An open job of this profile under an earlier hash: it runs
				// under the current one (the worker tags with the profile's
				// hash), so a second job would only encode the file twice.
				continue
			}
			if r.Window > 0 && open >= r.Window {
				waiting++
				continue
			}
			if err := r.ensureTranscodeJob(ctx, &tp, mf, hash); err != nil {
				createErrs = append(createErrs, err)
				continue
			}
			created++
			open++
		}
	}
	createErrs = append(createErrs, r.retireSucceeded(ctx, jobList.Items, tp.Name, mfList.Items)...)
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
			"%d matching file(s), %d job(s) created this pass, %d waiting for the window of %d", len(matching), created, waiting, r.Window)
	}
	if overlapped {
		k8s.MarkTrue(&fresh, &conditions, ConditionOverlap, ReasonSelectorOverlap,
			"this profile's selector also matches file(s) a higher-priority profile claims; no job was created for them")
	} else {
		k8s.MarkFalse(&fresh, &conditions, ConditionOverlap, ReasonNoOverlap, "no selector overlap with another profile")
	}

	fresh.Status.EncoderLimits = r.encoderLimits(ctx)
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
	if r.Retention > 0 {
		// Nothing else wakes a profile when a Succeeded job ages past its
		// retention.
		return ctrl.Result{RequeueAfter: min(r.Retention, time.Hour)}, nil
	}
	return ctrl.Result{}, nil
}

// maxEncoderLimits is status.encoderLimits' MaxItems.
const maxEncoderLimits = 16

// encoderLimits is status.encoderLimits: every GPU class's fresh per-node
// entries (task.ReadEncoderLimitsByNode), sorted by class then node and
// capped. An unreadable bucket shows none rather than failing the pass.
func (r *Reconciler) encoderLimits(ctx context.Context) []transcodev1alpha1.EncoderLimit {
	if r.Progress == nil {
		return nil
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	var out []transcodev1alpha1.EncoderLimit
	for _, class := range []transcodev1alpha1.Hardware{transcodev1alpha1.HardwareIntel, transcodev1alpha1.HardwareNVIDIA} {
		nodes, err := task.ReadEncoderLimitsByNode(ctx, r.Progress, string(class), now)
		if err != nil {
			logging.FromContext(ctx).WarnContext(ctx, "transcodeprofile: cannot read the encoder limits", "class", class, "error", err)
			continue
		}
		health, err := task.ReadEncoderHealth(ctx, r.Progress, string(class), now)
		if err != nil {
			logging.FromContext(ctx).WarnContext(ctx, "transcodeprofile: cannot read the devices' health", "class", class, "error", err)
		}
		names := make([]string, 0, len(nodes))
		for n := range nodes {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			l := transcodev1alpha1.EncoderLimit{
				Class: class, Node: n,
				NVDEC: nodes[n].NVDEC.Decodable(),
			}
			if h, ok := health[n]; ok {
				l.Healthy, l.Message = ptr.To(h.Healthy), h.Error
			}
			out = append(out, l)
		}
	}
	if len(out) > maxEncoderLimits {
		out = out[:maxEncoderLimits]
	}
	return out
}

// countOpen counts profile's TranscodeJobs not yet terminal -- no phase,
// Pending, Planned, Queued or Running -- that are not being deleted: the
// jobs the window holds.
func countOpen(jobs []transcodev1alpha1.TranscodeJob, profile string) int {
	n := 0
	for i := range jobs {
		if isOpen(&jobs[i], profile) {
			n++
		}
	}
	return n
}

// openFiles is the MediaFiles a job of profile still holds, whatever hash
// it was named by: an open job, or a Succeeded one whose swap catalogarr
// has not incorporated yet -- the file was not re-probed after the job
// finished, so it still reads untranscoded, and a job under a newer hash
// would be planned against bytes the swap replaced (and fail SourceChanged).
func openFiles(jobs []transcodev1alpha1.TranscodeJob, profile string, files []catalogv1alpha1.MediaFile) map[types.NamespacedName]bool {
	probedAt := make(map[types.NamespacedName]*metav1.Time, len(files))
	for i := range files {
		probedAt[client.ObjectKeyFromObject(&files[i])] = files[i].Status.ProbedAt
	}
	out := map[types.NamespacedName]bool{}
	for i := range jobs {
		tj := &jobs[i]
		key := types.NamespacedName{Namespace: tj.Namespace, Name: tj.Spec.MediaFileRef}
		switch {
		case isOpen(tj, profile):
			out[key] = true
		case tj.Spec.ProfileRef == profile && tj.Status.Phase == transcodev1alpha1.TranscodeJobPhaseSucceeded && !k8s.IsDeleting(tj):
			done, probed := tj.Status.FinishedAt, probedAt[key]
			if done == nil || probed == nil || !probed.After(done.Time) {
				out[key] = true
			}
		}
	}
	return out
}

// isOpen reports whether tj is profile's and not yet terminal or deleting.
func isOpen(tj *transcodev1alpha1.TranscodeJob, profile string) bool {
	if tj.Spec.ProfileRef != profile || k8s.IsDeleting(tj) {
		return false
	}
	switch tj.Status.Phase {
	case transcodev1alpha1.TranscodeJobPhaseSucceeded, transcodev1alpha1.TranscodeJobPhaseFailed,
		transcodev1alpha1.TranscodeJobPhaseSkipped:
		return false
	}
	return true
}

// retireSucceeded deletes profile's Succeeded TranscodeJobs that finished
// more than r.Retention ago and whose MediaFile was probed since. catalogarr
// incorporates a transcode swap by reading the Succeeded job
// (latestUnincorporatedTranscode, probedAt before finishedAt), so a job is
// kept until that probe has happened; afterwards the output's
// CLUSTARR_PROFILE tag keeps the file from being selected again and the
// history sink holds the job's events. Failed and Skipped jobs stay: the
// first for the operator, the second as the record that stops the file
// being planned again.
func (r *Reconciler) retireSucceeded(ctx context.Context, jobs []transcodev1alpha1.TranscodeJob, profile string,
	files []catalogv1alpha1.MediaFile,
) []error {
	if r.Retention <= 0 {
		return nil
	}
	now := time.Now()
	if r.Now != nil {
		now = r.Now()
	}
	probedAt := make(map[types.NamespacedName]*metav1.Time, len(files))
	for i := range files {
		probedAt[client.ObjectKeyFromObject(&files[i])] = files[i].Status.ProbedAt
	}
	var errs []error
	for i := range jobs {
		tj := &jobs[i]
		done := tj.Status.FinishedAt
		if tj.Spec.ProfileRef != profile || tj.Status.Phase != transcodev1alpha1.TranscodeJobPhaseSucceeded ||
			done == nil || now.Sub(done.Time) < r.Retention || k8s.IsDeleting(tj) {
			continue
		}
		probed := probedAt[types.NamespacedName{Namespace: tj.Namespace, Name: tj.Spec.MediaFileRef}]
		if probed == nil || !probed.After(done.Time) {
			continue // catalogarr has not read it yet
		}
		if err := r.Delete(ctx, tj, client.Preconditions{UID: &tj.UID}); client.IgnoreNotFound(err) != nil {
			errs = append(errs, fmt.Errorf("transcodeprofile: retire TranscodeJob %s: %w", client.ObjectKeyFromObject(tj), err))
		}
	}
	return errs
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
