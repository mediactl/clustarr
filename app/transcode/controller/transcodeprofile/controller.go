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

// Package transcodeprofile is the slim TranscodeProfile reconciler (loop
// spec §2.13, §5.12): it hashes each profile (jobspec.ProfileHash, the
// function the transcode planner tags with), validates it (a second default
// is Invalid) and surfaces selector overlap, renders the profile's pool Jobs
// from the admission ledger's confirmed dispatches under transcode-pool, and
// writes TranscodeProfile.status under transcode. It never watches or writes
// a MediaFile (D3): it counts files through the
// remediation.mediafile.transcodeProfile index and is woken by the ledger's
// profileWake. Until F6.6 the LegacyReconciler (legacy.go), the per-file
// pass that creates TranscodeJobs, is what the manager registers.
package transcodeprofile

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	k8sevents "k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	transcodeac "github.com/mediactl/clustarr/api/applyconfiguration/transcode/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/admission"
	"github.com/mediactl/clustarr/app/transcode/controller/pool"
	"github.com/mediactl/clustarr/app/transcode/jobspec"
	transcodestatus "github.com/mediactl/clustarr/app/transcode/status"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
	busevents "github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// The slim reconciler renders and deletes pool Jobs, reads their pods for an
// unschedulable GPU pool, records Events on the profile, and reads MediaFiles
// only through its index (counts) and labels (overlap). The blank line keeps
// the markers package-level.
//
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodeprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;patch;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=list
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch

// Reasons the slim reconciler writes beyond k8s.Reason* and the API's.
const (
	// ReasonWaitingForAdmission is Ready=False's reason until the admission
	// ledger is rebuilt: no count is written and no pool suspended before.
	ReasonWaitingForAdmission = "WaitingForAdmission"
	// ReasonPoolFailed is the Warning Event reason on a profile whose pool
	// Job failed and is recreated.
	ReasonPoolFailed = "PoolFailed"
	// ReasonPoolUnschedulable is the Warning Event reason on a profile whose
	// GPU pool cannot get a pod scheduled.
	ReasonPoolUnschedulable = "PoolUnschedulable"
)

// Requeues.
const (
	// waitForRebuild is how soon a profile is looked at again while the
	// ledger is not rebuilt.
	waitForRebuild = 5 * time.Second
	// scheduleRecheck is how soon a profile is looked at again while a GPU
	// pool has a pod not Ready (it may become unschedulable).
	scheduleRecheck = time.Minute
)

// Reconciler is the slim TranscodeProfile reconciler.
type Reconciler struct {
	Client    client.Client
	APIReader client.Reader
	Recorder  k8sevents.EventRecorder
	Ledger    *admission.Ledger
	Pool      pool.Config
	// Progress is the clustarr-progress bucket GPU pool workers publish
	// their measured encoder limits to (status.encoderLimits); nil shows
	// none.
	Progress busevents.KV
	// Now is the clock; nil is time.Now.
	Now func() time.Time
}

// StatusHash is status.hash: jobspec.ProfileHash, the function the planner
// tags with, pure over spec and standard.Version, so no file waits on it.
func StatusHash(spec transcodev1alpha1.TranscodeProfileSpec) string { return jobspec.ProfileHash(spec) }

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Reconcile is §5.12 steps 1-5 for one profile: hash, validity and overlap;
// its pools from the ledger's confirmed dispatches (nothing suspended or
// deleted before the ledger is rebuilt); then its status, skipped when
// unchanged.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "transcodeprofile.Reconcile")
	defer span.End()
	var tp transcodev1alpha1.TranscodeProfile
	if err := r.Client.Get(ctx, req.NamespacedName, &tp); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if k8s.IsDeleting(&tp) {
		return ctrl.Result{}, nil
	}
	var profiles transcodev1alpha1.TranscodeProfileList
	if err := r.Client.List(ctx, &profiles); err != nil {
		return ctrl.Result{}, fmt.Errorf("transcodeprofile: list TranscodeProfiles: %w", err)
	}
	invalid, invalidReason, invalidMessage := transcodeplan.ValidateProfile(&tp, profiles.Items)
	overlapped, err := r.overlap(ctx, &tp, profiles.Items)
	if err != nil {
		return ctrl.Result{}, err
	}
	requeue, err := r.pools(ctx, &tp)
	if err != nil {
		return ctrl.Result{}, err
	}
	_, rebuilt := r.Ledger.DispatchedPerPool(pool.Key{Profile: tp.Name, ProfileUID: tp.UID, Class: transcodev1alpha1.HardwareCPU})
	var c counts
	if rebuilt {
		if c, err = r.counts(ctx, tp.Name); err != nil {
			return ctrl.Result{}, err
		}
	} else {
		requeue = shorter(requeue, waitForRebuild)
	}
	if err := r.writeStatus(ctx, &tp, rebuilt, c, invalid, invalidReason, invalidMessage, overlapped); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// shorter is the sooner of two requeues, zero being none.
func shorter(a, b time.Duration) time.Duration {
	switch {
	case a == 0:
		return b
	case b == 0 || a < b:
		return a
	}
	return b
}

// overlap reports whether tp's selector matches a video file another
// profile wins (§2.13's one selection rule), over the cached files' labels.
func (r *Reconciler) overlap(ctx context.Context, tp *transcodev1alpha1.TranscodeProfile,
	profiles []transcodev1alpha1.TranscodeProfile,
) (bool, error) {
	if tp.Spec.Selector == nil {
		return false, nil
	}
	var files catalogv1alpha1.MediaFileList
	if err := r.Client.List(ctx, &files, client.UnsafeDisableDeepCopy); err != nil {
		return false, fmt.Errorf("transcodeprofile: list MediaFiles: %w", err)
	}
	def := transcodeplan.DefaultWinner(profiles)
	for i := range files.Items {
		mf := &files.Items[i]
		if !transcodeplan.EligibleKind(mf.Spec.MediaRef.Kind) || !transcodeplan.SelectorMatches(tp, mf.Labels) {
			continue
		}
		if w := transcodeplan.WinningProfile(mf.Labels, profiles, def); w != nil && w.Name != tp.Name {
			return true, nil
		}
	}
	return false, nil
}

// writeStatus renders the profile's status from a fresh read and applies
// it, unless it is what is stored. Before the ledger is rebuilt the counts
// are left as they stand and Ready reads WaitingForAdmission.
func (r *Reconciler) writeStatus(ctx context.Context, tp *transcodev1alpha1.TranscodeProfile, rebuilt bool, c counts,
	invalid bool, invalidReason, invalidMessage string, overlapped bool,
) error {
	var fresh transcodev1alpha1.TranscodeProfile
	if err := r.APIReader.Get(ctx, client.ObjectKeyFromObject(tp), &fresh); err != nil {
		return client.IgnoreNotFound(err)
	}
	st := fresh.Status.DeepCopy()
	st.ObservedGeneration, st.Hash = fresh.Generation, StatusHash(fresh.Spec)
	if rebuilt {
		st.PendingJobs, st.RunningJobs, st.MatchingFiles, st.WaitingFiles = c.pending, c.running, c.matching, c.waiting
	}
	st.EncoderLimits = encoderLimits(ctx, r.Progress, r.now())
	switch {
	case invalid:
		k8s.MarkTrue(&fresh, &st.Conditions, transcodev1alpha1.TranscodeProfileConditionInvalid, invalidReason, "%s", invalidMessage)
		k8s.MarkReady(&fresh, &st.Conditions, false, invalidReason, "%s", invalidMessage)
	case !rebuilt:
		k8s.MarkFalse(&fresh, &st.Conditions, transcodev1alpha1.TranscodeProfileConditionInvalid, k8s.ReasonReconciled, "profile is valid")
		k8s.MarkReady(&fresh, &st.Conditions, false, ReasonWaitingForAdmission, "waiting for transcode admission to rebuild its ledger")
	default:
		k8s.MarkFalse(&fresh, &st.Conditions, transcodev1alpha1.TranscodeProfileConditionInvalid, k8s.ReasonReconciled, "profile is valid")
		k8s.MarkReady(&fresh, &st.Conditions, true, k8s.ReasonReconciled, "%d file(s) in the window, %d waiting",
			c.pending+c.running, c.waiting)
	}
	if overlapped {
		k8s.MarkTrue(&fresh, &st.Conditions, transcodeplan.ConditionOverlap, transcodeplan.ReasonSelectorOverlap,
			"this profile's selector also matches file(s) a higher-priority profile claims; it transcodes none of them")
	} else {
		k8s.MarkFalse(&fresh, &st.Conditions, transcodeplan.ConditionOverlap, transcodeplan.ReasonNoOverlap, "no selector overlap with another profile")
	}
	if equality.Semantic.DeepEqual(*st, fresh.Status) {
		return nil
	}
	if invalid && r.Recorder != nil {
		r.Recorder.Eventf(&fresh, nil, "Warning", invalidReason, "Reconcile", "%s", invalidMessage)
	}
	conditions := st.Conditions
	fresh.Status = *st
	return transcodestatus.PatchProfile(ctx, r.Client, k8s.ManagerTranscode, &fresh,
		func(ac *transcodeac.TranscodeProfileStatusApplyConfiguration) {
			ac.WithConditions(k8s.ConditionACs(conditions)...)
		})
}

// SetupWithManager registers the slim reconciler as "transcodeprofile": its
// profile's generation, its pool Jobs, and the ledger's profileWake. It
// never watches a MediaFile.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).Named("transcodeprofile").
		For(&transcodev1alpha1.TranscodeProfile{}, builder.WithPredicates(k8s.GenerationChanged())).
		Owns(&batchv1.Job{}, builder.WithPredicates(pool.JobPredicate())).
		WatchesRawSource(source.Channel(r.Ledger.ProfileWake(), &handler.EnqueueRequestForObject{})).
		WithOptions(controller.Options{MaxConcurrentReconciles: 1, RecoverPanic: new(true)}).
		Complete(r)
}
