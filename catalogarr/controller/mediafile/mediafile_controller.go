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

package mediafile

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	"sigs.k8s.io/controller-runtime/pkg/handler"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// The MediaFile controller's RBAC. The Events group is events.k8s.io and not
// "" because the Recorder is a k8s.io/client-go/tools/events.EventRecorder,
// handed in by mgr.GetEventRecorder, and that writes events.k8s.io/v1. The
// marker and the recorder type move together or not at all: a mismatch is
// denied only on a real cluster, and no suite can see it, because envtest does
// not enforce RBAC. catalogarr's setupControllers records the occasion this
// repo learned it.
//
// The blank line below is load-bearing: controller-gen only collects
// +kubebuilder:rbac from PACKAGE-level comments, and a marker block touching a
// declaration becomes that declaration's doc comment and is silently dropped.
// cmd/clustarr's TestRBACMarkersArePackageLevel is the guard.
//
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=mediafiles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodejobs,verbs=get;list;watch
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodeprofiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=subtitle.clustarr.io,resources=subtitlerequests,verbs=get;list;watch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// AnnotationObservedFingerprint is how importarr's library rescan tells this
// controller that a post-transcode file changed on disk: "<sizeBytes>@<RFC
// 3339 mtime>" as the walk found it, applied under k8s.ManagerImportarr,
// which owns nothing else on a MediaFile. It is the same key as
// importarr/worker/rescan.AnnotationObservedFingerprint (a test holds the
// two equal; catalogarr does not import importarr to read one constant).
//
// This is the gap-fix X5a/X7a contract for "a transcoded file's bytes
// legitimately changed". Once catalogarr incorporates a transcode swap it
// owns spec.sizeBytes, spec.modTime and spec.original, so the rescan never
// re-applies them -- k8s.Apply forces ownership and would silently take
// them back. The rescan only OBSERVES and hands over through this
// annotation; this controller ACTS: the annotation's change wakes it (a
// metadata change bumps no generation, so the For() predicate has an arm
// for it), and the reconcile re-stats the file, re-probes it when its
// fingerprint moved, re-records size and mtime under its own manager and
// drops the transcode verdict the old bytes earned. The value is never
// parsed: the stat is the authority, the annotation only the doorbell.
const AnnotationObservedFingerprint = "catalog.clustarr.io/observed-fingerprint"

// TranscodedRecheckInterval is how often a transcoded MediaFile
// (spec.original=false) is re-examined on disk when nothing else wakes it:
// the floor under [AnnotationObservedFingerprint], for a library whose root
// folder has no rescan schedule, or a change between two scans. A day is the
// budget: one stat, two cached Lists and one no-op apply per transcoded file
// per day.
const TranscodedRecheckInterval = 24 * time.Hour

// Reconciler owns 100% of MediaFile.status (see this section's "Resolving
// the field-manager split") plus, narrowly, spec.sizeBytes/modTime/original
// after a transcode swap. It writes nothing at all on any other resource.
//
// It deliberately does NOT roll the file up onto the owning Movie or
// Episode, though an earlier revision of this controller did (task C13
// removed it). Two reasons, in order of severity:
//
//  1. It corrupted the owning item. The rollup applied a seven-field
//     Movie/Episode status -- hasFile, fileRef, fileQuality,
//     fileFormatScore, cutoffMet, phase, conditions -- under
//     k8s.ManagerCatalogarr, which is the very field manager the Movie and
//     Episode reconcilers use for those same objects. Server-side apply
//     REPLACES a manager's ownership set on every apply instead of merging
//     it, so each rollup released every other field that manager held:
//     path, available/availableAt, addOptionsApplied and observedGeneration
//     deterministically (nothing else writes them), and activeDownloadRef
//     whenever catalogarr was its sole owner. A movie whose
//     addOptionsApplied is reset has its addOptions applied a second time;
//     one whose activeDownloadRef is cleared orphans the in-flight Download
//     and re-enters the search rotation.
//  2. It was redundant. Both owning controllers already watch MediaFile
//     (Watches + k8s.GenerationChanged, which passes creates, deletes and
//     every spec change -- and §8.4 freezes quality/revision/formatScore/
//     matchedFormats/releaseType in MediaFileSpec, so a generation bump
//     covers every input the rollup read) and recompute the same fields
//     from rollup.PickMediaFile + rollup.FileState on a List. Their version
//     is strictly better: it clears hasFile when the file is deleted, which
//     the rollup could never do, and it feeds the file into the full
//     Phase() ladder instead of unconditionally stamping Imported/
//     CutoffUnmet over Unmonitored, Pending, Delayed or Downloading.
//
// Giving the rollup a field manager of its own was not an option either:
// server-side apply only keeps two managers from colliding when their field
// sets are disjoint, and the rollup's set was a strict subset of what
// movie/episode/reconciler.go's own doc comments claim sole writership of.
type Reconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
	Probe    ProbeFunc
	Clock    func() time.Time
}

// ProbeFunc matches mediainfo.Probe's signature so tests can substitute a
// fake that never shells out to ffprobe.
type ProbeFunc func(ctx context.Context, path string) (*commonv1.MediaInfo, *mediainfo.Raw, error)

// NewReconciler builds a Reconciler with production defaults: the real
// ffprobe-backed Probe and the wall clock.
func NewReconciler(c client.Client, scheme *runtime.Scheme, recorder events.EventRecorder) *Reconciler {
	return &Reconciler{
		Client:   c,
		Scheme:   scheme,
		Recorder: recorder,
		Probe:    mediainfo.Probe,
		Clock:    time.Now,
	}
}

// Reconcile probes the file at spec.path when it is new or stale (or when a
// transcode swap has not yet been incorporated), mirrors the result onto
// status and the catalog.clustarr.io/* labels, and folds in any
// SubtitleRequest sidecars. It is the sole writer of MediaFileStatus (see
// the package doc in "Resolving the field-manager split") and touches no
// other resource -- the owning Movie/Episode rollup is theirs to compute
// from their own MediaFile watch, per the Reconciler doc comment above.
//
// Every path that reports anything at all leaves through exactly ONE
// k8s.PatchStatus, built by knownStatus.statusAC from a baseline seeded with
// the status already on the object. Both halves of that sentence are
// load-bearing, and each one was a shipped defect that a real apiserver
// reproduced:
//
//   - ONE apply. status.sidecars used to be patched by a second, separate
//     PatchStatus running unconditionally at the end of every reconcile,
//     under this same field manager. Server-side apply REPLACES a manager's
//     ownership set on every apply instead of merging it, so that second,
//     narrower apply released probeHash, probedAt, mediaInfo and transcode
//     -- everything the probe had written ten lines earlier. It needed only
//     a matching SubtitleRequest to fire, which after captionarr lands is
//     every video file; and because the For predicate is GenerationChanged,
//     the status-only wipe did not re-trigger, so the object sat gutted
//     until resync, re-probed (running ffprobe again) and was wiped again.
//   - SEEDED FROM THE LIVE STATUS. The FileMissing and ProbeFailed early
//     returns used to apply observedGeneration + conditions only, which
//     released the same fields. Both are transient -- an RWX /data blip, a
//     user moving a file and moving it back -- so a healthy, probed file was
//     gutted by a blip and stayed gutted for the whole 60s requeue. Zeroing
//     status.transcode.compliant in particular makes squasharr re-transcode
//     an already-compliant file once Phase E lands.
//
// The sibling shape is movie/series' reassertKnownStatus, which re-adds the
// fields its manager owns on each early return. This package seeds instead
// of re-asserting, for one reason: MediaFileStatus' Conditions and Sidecars
// are LISTS, and the generated With* for a list appends rather than
// replaces, so a "reassert, then overwrite" helper would double every
// sidecar on the happy path. Seeding a plain struct and rendering the apply
// configuration once, at the end, cannot express that bug.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "mediafile.Reconcile")
	defer span.End()
	ctx = logging.With(ctx, "mediafile", req.Name, "namespace", req.Namespace)
	log := logging.FromContext(ctx)

	var mf catalogv1alpha1.MediaFile
	if err := r.Get(ctx, req.NamespacedName, &mf); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if k8s.IsDeleting(&mf) {
		return ctrl.Result{}, nil
	}

	conditions := append([]metav1.Condition(nil), mf.Status.Conditions...)
	now := metav1.NewTime(r.Clock())
	// Taken before anything below can flip it: a file catalogarr already
	// took over after a transcode swap.
	transcoded := mf.Spec.Original != nil && !*mf.Spec.Original

	// The complete declaration of everything ManagerCatalogarr owns on
	// MediaFileStatus, seeded from what is already on the object. Each
	// branch below overwrites only what it actually recomputes; whatever it
	// does not touch is re-asserted rather than released.
	known := statusOf(&mf)

	info, statErr := os.Stat(mf.Spec.Path)
	if statErr != nil {
		k8s.MarkFalse(&mf, &conditions, catalogv1alpha1.MediaFileConditionReady, "FileMissing", "stat %s: %s", mf.Spec.Path, statErr)
		if err := r.applyStatus(ctx, &mf, conditions, known); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}

	ps := evaluateProbe(mf.Spec.Path, info.Size(), info.ModTime(), mf.Status.ProbeHash)

	// Detect an unincorporated successful transcode: the newest Succeeded
	// TranscodeJob for this MediaFile whose result landed after the last
	// probe. §8.5: "worker swaps file at the same path" -- spec.path never
	// changes here, only sizeBytes/modTime/original.
	swap, err := r.latestUnincorporatedTranscode(ctx, &mf)
	if err != nil {
		return ctrl.Result{}, err
	}

	probed, changed := false, false
	if swap != nil || ps.Stale || mf.Status.ProbeHash == "" {
		mi, _, probeErr := r.Probe(ctx, mf.Spec.Path)
		if probeErr != nil {
			k8s.MarkFalse(&mf, &conditions, catalogv1alpha1.MediaFileConditionProbed, "ProbeFailed", "%s", probeErr)
			k8s.MarkFalse(&mf, &conditions, catalogv1alpha1.MediaFileConditionReady, "ProbeFailed", "probe failed: %s", probeErr)
			if serr := r.applyStatus(ctx, &mf, conditions, known); serr != nil {
				return ctrl.Result{}, serr
			}
			return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
		}

		original := mf.Spec.Original == nil || *mf.Spec.Original
		if swap != nil {
			original = false
		}

		// mirrorLabels is applied on every probe, not only alongside a
		// transcode swap: spec §8.4 says the MediaFile reconciler "probes,
		// sets labels, probeHash, Probed" unconditionally, and
		// metadata.labels is neither spec nor status -- disjoint from
		// everything importarr's MediaFileSpec Apply owns, so there is no
		// two-writer reason to withhold it pre-transcode (ruled on
		// explicitly after the mandatory gate's original, overly broad
		// "no main-resource claim" assertion was narrowed to "no spec.*
		// claim"). original already reflects an incorporated swap by this
		// point, so LabelOriginal flips to "false" in the same reconcile
		// that flips spec.original.
		//
		// Labels and the spec fields both go in ONE k8s.Apply call, not
		// two: server-side apply is not additive across separate Apply
		// calls from the same field manager -- each call fully declares
		// that manager's current field set, so a later, narrower Apply
		// from catalogarr would silently release whatever an earlier one
		// in the same reconcile had just claimed. !original (true from the
		// first swap onward, since mf.Spec.Original was force-set false)
		// means catalogarr re-asserts sizeBytes/modTime/original with the
		// current probe's fresh values on every Apply from here on, not
		// only the reconcile that first incorporates a swap -- otherwise a
		// later labels-only Apply (triggered by, say, a stale re-probe with
		// no new TranscodeJob) would erase fields catalogarr already owns.
		labels := mirrorLabels(mf.Spec.MediaRef.Kind, mf.Spec.Quality, mi, original)
		mainAC := catalogac.MediaFile(mf.Name, mf.Namespace).WithLabels(labels)
		if !original {
			mainAC = mainAC.WithSpec(catalogac.MediaFileSpec().
				WithSizeBytes(ps.SizeBytes).
				WithModTime(ps.ModTime).
				WithOriginal(false))
		}
		if _, err := k8s.Apply(ctx, r.Client, k8s.ManagerCatalogarr, mainAC); err != nil {
			return ctrl.Result{}, err
		}
		if swap != nil {
			log.Info("incorporated transcode swap", "transcodeJob", swap.Name)
		}

		k8s.MarkTrue(&mf, &conditions, catalogv1alpha1.MediaFileConditionProbed, "Probed", "probed at %s", now.Time)
		k8s.MarkTrue(&mf, &conditions, catalogv1alpha1.MediaFileConditionReady, "Ready", "file present and probed")

		known.ProbeHash = ps.Hash
		known.ProbedAt = &now
		known.MediaInfo = mi
		probed = true

		if swap == nil && transcoded && mf.Status.ProbeHash != "" {
			// The bytes of a file catalogarr already took over changed, and
			// no newer Succeeded TranscodeJob explains it (a re-mux, a hand
			// edit, a restore from backup). The size and mtime are
			// re-recorded above, under this manager, which has owned them
			// since the swap; the rescan never re-applies them, it only rings
			// AnnotationObservedFingerprint. What the previous encode claimed
			// about the file no longer describes these bytes, so the
			// compliance verdict and the profile revision it was judged
			// against are dropped. LastResult stays: it is the history of the
			// last transcode, which did happen. squasharr re-judges the file
			// from the fresh probe (its watch keys on probeHash) and decides
			// whether it needs another encode -- this controller never
			// guesses that.
			known.Transcode = staleTranscodeState(known.Transcode)
			changed = true
		}
		if swap != nil {
			profileTag, terr := r.transcodeProfileTag(ctx, swap)
			if terr != nil {
				return ctrl.Result{}, terr
			}
			// A whole new TranscodeState, not an edit of the one already
			// there: the previous state described the previous encode, and
			// its jobRef names a TranscodeJob that is no longer the latest.
			known.Transcode = &catalogv1alpha1.TranscodeState{
				Compliant:  true,
				ProfileTag: profileTag,
				LastResult: catalogv1alpha1.TranscodeResultSucceeded,
			}
		}
	}

	// Sidecars are folded into the SAME apply, not patched separately after
	// it -- see this function's doc comment. A reconcile with no matching
	// SubtitleRequest leaves whatever status already holds: catalogarr owns
	// the field either way, and "no request exists" is not evidence the
	// sidecars are gone.
	if sidecars, matched, serr := r.scanSidecars(ctx, &mf); serr != nil {
		return ctrl.Result{}, serr
	} else if matched {
		known.Sidecars = sidecars
	}

	if err := r.applyStatus(ctx, &mf, conditions, known); err != nil {
		return ctrl.Result{}, err
	}

	if probed && r.Recorder != nil {
		r.Recorder.Eventf(&mf, nil, "Normal", "Probed", "Reconcile", "probed %s", mf.Spec.Path)
	}
	if changed && r.Recorder != nil {
		r.Recorder.Eventf(&mf, nil, "Warning", "TranscodedFileChanged", "Reconcile",
			"the transcoded file at %s changed on disk with no transcode to explain it; re-probed, compliance cleared", mf.Spec.Path)
	}

	if transcoded || swap != nil {
		return ctrl.Result{RequeueAfter: TranscodedRecheckInterval}, nil
	}
	return ctrl.Result{}, nil
}

// staleTranscodeState is t with the compliance verdict dropped: Compliant
// false and no ProfileTag, because the bytes they described are gone.
// LastResult and JobRef are kept -- they are about the last transcode, not
// about the file as it now is. A nil t stays nil.
func staleTranscodeState(t *catalogv1alpha1.TranscodeState) *catalogv1alpha1.TranscodeState {
	if t == nil {
		return nil
	}
	out := *t
	out.Compliant = false
	out.ProfileTag = ""
	return &out
}

// knownStatus is the whole of MediaFileStatus, minus the two fields every
// path recomputes from scratch (observedGeneration and conditions), as one
// reconcile decides it. ManagerCatalogarr is the sole owner of all of it, so
// an apply that omits a field RELEASES it; carrying the lot in one struct
// makes "declare everything this manager owns" the only thing the code can
// express.
type knownStatus struct {
	ProbeHash string
	ProbedAt  *metav1.Time
	MediaInfo *commonv1.MediaInfo
	Sidecars  []catalogv1alpha1.Sidecar
	Transcode *catalogv1alpha1.TranscodeState
}

// statusOf seeds a knownStatus from the live object, so a reconcile that
// recomputes nothing re-asserts everything.
func statusOf(mf *catalogv1alpha1.MediaFile) *knownStatus {
	return &knownStatus{
		ProbeHash: mf.Status.ProbeHash,
		ProbedAt:  mf.Status.ProbedAt,
		MediaInfo: mf.Status.MediaInfo,
		Sidecars:  mf.Status.Sidecars,
		Transcode: mf.Status.Transcode,
	}
}

// statusAC renders the apply configuration. Every field is sent whenever it
// has a value, including the Compliant boolean once it is true -- a boolean
// that stops being sent flips back to false, which is one of the forms of
// the apply-release hazard CLAUDE.md enumerates.
func (k *knownStatus) statusAC(mf *catalogv1alpha1.MediaFile, conditions []metav1.Condition) *catalogac.MediaFileStatusApplyConfiguration {
	ac := catalogac.MediaFileStatus().
		WithObservedGeneration(mf.Generation).
		WithConditions(k8s.ConditionACs(conditions)...)
	if k.ProbeHash != "" {
		ac = ac.WithProbeHash(k.ProbeHash)
	}
	if k.ProbedAt != nil {
		ac = ac.WithProbedAt(*k.ProbedAt)
	}
	if k.MediaInfo != nil {
		ac = ac.WithMediaInfo(*k.MediaInfo)
	}
	for _, s := range k.Sidecars {
		ac = ac.WithSidecars(catalogac.Sidecar().
			WithPath(s.Path).WithLanguage(s.Language).WithForced(s.Forced).WithHI(s.HI))
	}
	if t := k.Transcode; t != nil {
		tac := catalogac.TranscodeState().WithCompliant(t.Compliant)
		if t.ProfileTag != "" {
			tac = tac.WithProfileTag(t.ProfileTag)
		}
		if t.JobRef != nil {
			tac = tac.WithJobRef(*t.JobRef)
		}
		// Guarded because TranscodeResult is a CRD enum: a non-nil pointer
		// to "" serialises as lastResult:"" and the apiserver rejects it,
		// where omitting the field lets the schema default apply.
		if t.LastResult != "" {
			tac = tac.WithLastResult(t.LastResult)
		}
		ac = ac.WithTranscode(tac)
	}
	return ac
}

// applyStatus is the one status write in this package.
func (r *Reconciler) applyStatus(ctx context.Context, mf *catalogv1alpha1.MediaFile, conditions []metav1.Condition, known *knownStatus) error {
	_, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr,
		catalogac.MediaFile(mf.Name, mf.Namespace).WithStatus(known.statusAC(mf, conditions)))
	return err
}

// SetupWithManager registers the field indexes this controller's watches
// need, then wires the For(MediaFile) controller plus the TranscodeJob and
// SubtitleRequest watches §10 lists for it. The eventual catalogarr/run.go
// integration calls it as:
//
//	return mediafile.NewReconciler(mgr.GetClient(), mgr.GetScheme(),
//	    mgr.GetEventRecorder("mediafile-controller")).SetupWithManager(mgr)
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()
	if err := mgr.GetFieldIndexer().IndexField(ctx, &transcodev1alpha1.TranscodeJob{}, transcodeJobMediaFileRefIndex, indexTranscodeJobByMediaFileRef); err != nil {
		return err
	}
	if err := mgr.GetFieldIndexer().IndexField(ctx, &subtitlev1alpha1.SubtitleRequest{}, subtitleRequestMediaFileRefIndex, indexSubtitleRequestByMediaFileRef); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("mediafile").
		For(&catalogv1alpha1.MediaFile{}, builder.WithPredicates(k8s.Or(
			k8s.GenerationChanged(),
			k8s.StatusFieldChanged(observedFingerprint),
		))).
		Watches(&transcodev1alpha1.TranscodeJob{}, handler.EnqueueRequestsFromMapFunc(r.mediaFileForTranscodeJob),
			builder.WithPredicates(k8s.StatusFieldIn(extractTranscodeJobPhase, string(transcodev1alpha1.TranscodeJobPhaseSucceeded)))).
		Watches(&subtitlev1alpha1.SubtitleRequest{}, handler.EnqueueRequestsFromMapFunc(r.mediaFileForSubtitleRequest),
			builder.WithPredicates(k8s.StatusFieldChanged(extractSubtitleItemsSignature))).
		WithOptions(controller.Options{
			RecoverPanic:          ptr.To(true),
			ReconciliationTimeout: 5 * time.Minute,
		}).
		Complete(r)
}

// latestUnincorporatedTranscode lists TranscodeJobs for mf via the field
// index and returns the most recently finished Succeeded one whose
// FinishedAt is after mf.Status.ProbedAt, or nil if none. This is what
// tells Reconcile "a transcode swap happened and I have not folded it in
// yet" without needing to know which watch woke it up.
//
// This filters list.Items in Go rather than sending
// client.MatchingFields{transcodeJobMediaFileRefIndex: mf.Name}: that option
// is only served from a manager-cached client's local FieldIndexer (the one
// SetupWithManager registers). Against a direct, uncached client -- which is
// exactly what this task's mandatory two-writer envtest uses to call
// Reconcile without starting a manager -- the same option is instead sent to
// the apiserver as a fieldSelector, and neither TranscodeJob nor
// SubtitleRequest declares that path as a CRD selectable field, so the
// apiserver rejects it ("field label not supported"). Filtering client-side
// is correct under both a raw and a cached client; the registered index
// still documents and enables the reverse (MediaFile -> its TranscodeJobs)
// lookup direction for any future caller that goes through the manager
// cache.
func (r *Reconciler) latestUnincorporatedTranscode(ctx context.Context, mf *catalogv1alpha1.MediaFile) (*transcodev1alpha1.TranscodeJob, error) {
	var list transcodev1alpha1.TranscodeJobList
	if err := r.List(ctx, &list, client.InNamespace(mf.Namespace)); err != nil {
		return nil, fmt.Errorf("mediafile: list TranscodeJobs: %w", err)
	}
	var latest *transcodev1alpha1.TranscodeJob
	for i := range list.Items {
		tj := &list.Items[i]
		if tj.Spec.MediaFileRef != mf.Name {
			continue
		}
		if tj.Status.Phase != transcodev1alpha1.TranscodeJobPhaseSucceeded || tj.Status.FinishedAt == nil {
			continue
		}
		if mf.Status.ProbedAt != nil && !tj.Status.FinishedAt.After(mf.Status.ProbedAt.Time) {
			continue
		}
		if latest == nil || tj.Status.FinishedAt.After(latest.Status.FinishedAt.Time) {
			latest = tj
		}
	}
	return latest, nil
}

// transcodeProfileTag renders §4.5's "CLUSTARR_PROFILE=<name>@<hash>"
// convention from the TranscodeProfile the job ran against.
func (r *Reconciler) transcodeProfileTag(ctx context.Context, tj *transcodev1alpha1.TranscodeJob) (string, error) {
	var tp transcodev1alpha1.TranscodeProfile
	if err := r.Get(ctx, types.NamespacedName{Name: tj.Spec.ProfileRef}, &tp); err != nil {
		return "", fmt.Errorf("mediafile: get TranscodeProfile %s: %w", tj.Spec.ProfileRef, err)
	}
	return fmt.Sprintf("%s@%s", tj.Spec.ProfileRef, tp.Status.Hash), nil
}

// scanSidecars derives status.sidecars from the SubtitleRequest for mf,
// reporting whether one was found at all. One SubtitleRequest per video
// MediaFile by convention (spec §4.6), but this lists rather than
// Gets-by-name so a missing or renamed request never errors the reconcile.
//
// It COMPUTES and returns rather than patching. It used to patch, under
// ManagerCatalogarr, from a status apply configuration holding only
// observedGeneration and sidecars -- and because it ran unconditionally at
// the end of every reconcile, after the probe block's own apply under the
// same manager, it released probeHash, probedAt, mediaInfo and transcode
// every time a SubtitleRequest existed. See Reconcile's doc comment.
//
// See latestUnincorporatedTranscode's comment for why this filters
// list.Items in Go instead of sending
// client.MatchingFields{subtitleRequestMediaFileRefIndex: mf.Name}: the same
// apiserver-selectable-field gap applies to SubtitleRequest.
func (r *Reconciler) scanSidecars(ctx context.Context, mf *catalogv1alpha1.MediaFile) ([]catalogv1alpha1.Sidecar, bool, error) {
	var list subtitlev1alpha1.SubtitleRequestList
	if err := r.List(ctx, &list, client.InNamespace(mf.Namespace)); err != nil {
		return nil, false, fmt.Errorf("mediafile: list SubtitleRequests: %w", err)
	}
	var match *subtitlev1alpha1.SubtitleRequest
	for i := range list.Items {
		if list.Items[i].Spec.MediaFileRef == mf.Name {
			match = &list.Items[i]
			break
		}
	}
	if match == nil {
		return nil, false, nil
	}
	sidecars, err := sidecarsFromSubtitleRequest(filepath.Dir(mf.Spec.Path), match.Status.Items)
	if err != nil {
		return nil, false, err
	}
	return sidecars, true, nil
}

// observedFingerprint extracts [AnnotationObservedFingerprint], for the For()
// predicate arm that lets importarr's hand-over reach this reconcile.
func observedFingerprint(o client.Object) string {
	if o == nil {
		return ""
	}
	return o.GetAnnotations()[AnnotationObservedFingerprint]
}
