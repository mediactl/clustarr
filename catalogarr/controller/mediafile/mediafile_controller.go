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

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
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
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// Reconciler owns 100% of MediaFile.status (see this section's "Resolving
// the field-manager split") plus, narrowly, spec.sizeBytes/modTime/original
// after a transcode swap, plus the HasFile/FileRef/FileQuality/
// FileFormatScore/CutoffMet/Phase rollup on the owning Movie/Episode.
type Reconciler struct {
	client.Client
	Scheme    *runtime.Scheme
	Recorder  record.EventRecorder
	Probe     ProbeFunc
	Catalogue *catalogue.Catalogue
	Clock     func() time.Time
}

// ProbeFunc matches mediainfo.Probe's signature so tests can substitute a
// fake that never shells out to ffprobe.
type ProbeFunc func(ctx context.Context, path string) (*commonv1.MediaInfo, *mediainfo.Raw, error)

// NewReconciler builds a Reconciler with production defaults: the real
// ffprobe-backed Probe, the embedded catalogue and the wall clock.
func NewReconciler(c client.Client, scheme *runtime.Scheme, recorder record.EventRecorder) *Reconciler {
	return &Reconciler{
		Client:    c,
		Scheme:    scheme,
		Recorder:  recorder,
		Probe:     mediainfo.Probe,
		Catalogue: catalogue.LoadedCatalogue(),
		Clock:     time.Now,
	}
}

// Reconcile probes the file at spec.path when it is new or stale (or when a
// transcode swap has not yet been incorporated), mirrors the result onto
// status and the catalog.clustarr.io/* labels, folds in any SubtitleRequest
// sidecars, and rolls the outcome up onto the owning Movie or Episode. It is
// the sole writer of MediaFileStatus (see the package doc in
// "Resolving the field-manager split").
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

	info, statErr := os.Stat(mf.Spec.Path)
	if statErr != nil {
		k8s.MarkFalse(&mf, &conditions, catalogv1alpha1.MediaFileConditionReady, "FileMissing", "stat %s: %s", mf.Spec.Path, statErr)
		if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr,
			catalogac.MediaFile(mf.Name, mf.Namespace).WithStatus(
				catalogac.MediaFileStatus().
					WithObservedGeneration(mf.Generation).
					WithConditions(k8s.ConditionACs(conditions)...),
			)); err != nil {
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

	if swap != nil || ps.Stale || mf.Status.ProbeHash == "" {
		mi, _, probeErr := r.Probe(ctx, mf.Spec.Path)
		if probeErr != nil {
			k8s.MarkFalse(&mf, &conditions, catalogv1alpha1.MediaFileConditionProbed, "ProbeFailed", "%s", probeErr)
			k8s.MarkFalse(&mf, &conditions, catalogv1alpha1.MediaFileConditionReady, "ProbeFailed", "probe failed: %s", probeErr)
			if _, serr := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr,
				catalogac.MediaFile(mf.Name, mf.Namespace).WithStatus(
					catalogac.MediaFileStatus().
						WithObservedGeneration(mf.Generation).
						WithConditions(k8s.ConditionACs(conditions)...),
				)); serr != nil {
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

		statusAC := catalogac.MediaFileStatus().
			WithObservedGeneration(mf.Generation).
			WithProbeHash(ps.Hash).
			WithProbedAt(now).
			WithMediaInfo(*mi).
			WithConditions(k8s.ConditionACs(conditions)...)

		if swap != nil {
			profileTag, terr := r.transcodeProfileTag(ctx, swap)
			if terr != nil {
				return ctrl.Result{}, terr
			}
			statusAC = statusAC.WithTranscode(catalogac.TranscodeState().
				WithCompliant(true).
				WithProfileTag(profileTag).
				WithLastResult(catalogv1alpha1.TranscodeResultSucceeded))
		}

		if _, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr,
			catalogac.MediaFile(mf.Name, mf.Namespace).WithStatus(statusAC)); err != nil {
			return ctrl.Result{}, err
		}

		if r.Recorder != nil {
			r.Recorder.Eventf(&mf, "Normal", "Probed", "probed %s", mf.Spec.Path)
		}
	}

	if err := r.rescanSidecars(ctx, &mf); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.rollupToOwner(ctx, &mf); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{}, nil
}

// SetupWithManager registers the field indexes this controller's watches
// need, then wires the For(MediaFile) controller plus the TranscodeJob and
// SubtitleRequest watches §10 lists for it. The eventual catalogarr/run.go
// integration calls it as:
//
//	return mediafile.NewReconciler(mgr.GetClient(), mgr.GetScheme(),
//	    mgr.GetEventRecorderFor("mediafile-controller")).SetupWithManager(mgr)
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
		For(&catalogv1alpha1.MediaFile{}, builder.WithPredicates(k8s.GenerationChanged())).
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

// rescanSidecars folds every SubtitleRequest for mf into status.sidecars.
// One SubtitleRequest per video MediaFile by convention (spec §4.6), but
// this lists rather than Gets-by-name so a missing or renamed request never
// errors the reconcile.
//
// See latestUnincorporatedTranscode's comment for why this filters
// list.Items in Go instead of sending
// client.MatchingFields{subtitleRequestMediaFileRefIndex: mf.Name}: the same
// apiserver-selectable-field gap applies to SubtitleRequest.
func (r *Reconciler) rescanSidecars(ctx context.Context, mf *catalogv1alpha1.MediaFile) error {
	var list subtitlev1alpha1.SubtitleRequestList
	if err := r.List(ctx, &list, client.InNamespace(mf.Namespace)); err != nil {
		return fmt.Errorf("mediafile: list SubtitleRequests: %w", err)
	}
	var match *subtitlev1alpha1.SubtitleRequest
	for i := range list.Items {
		if list.Items[i].Spec.MediaFileRef == mf.Name {
			match = &list.Items[i]
			break
		}
	}
	if match == nil {
		return nil
	}
	dir := filepath.Dir(mf.Spec.Path)
	sidecars, err := sidecarsFromSubtitleRequest(dir, match.Status.Items)
	if err != nil {
		return err
	}
	acs := make([]*catalogac.SidecarApplyConfiguration, 0, len(sidecars))
	for _, s := range sidecars {
		acs = append(acs, catalogac.Sidecar().WithPath(s.Path).WithLanguage(s.Language).WithForced(s.Forced).WithHI(s.HI))
	}
	_, err = k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr,
		catalogac.MediaFile(mf.Name, mf.Namespace).WithStatus(
			catalogac.MediaFileStatus().WithObservedGeneration(mf.Generation).WithSidecars(acs...),
		))
	return err
}

// rollupToOwner mirrors mf onto its owning Movie or Episode's
// HasFile/FileRef/FileQuality/FileFormatScore/CutoffMet/Phase, scoped to
// those two kinds for this task (see "Scope decision" above). Both are
// catalog.clustarr.io kinds catalogarr's controller manager already owns
// phase/conditions on (pkg/k8s's ManagerCatalogarr doc comment), so this
// reuses the same field manager rather than inventing a third.
func (r *Reconciler) rollupToOwner(ctx context.Context, mf *catalogv1alpha1.MediaFile) error {
	profileRef, err := r.ownerQualityProfileRef(ctx, mf)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	var profile quality.Profile
	hasProfile := false
	if profileRef != "" {
		var qp catalogv1alpha1.QualityProfile
		if gerr := r.Get(ctx, types.NamespacedName{Name: profileRef}, &qp); gerr == nil {
			p, errs := quality.FromCRD(&qp, r.Catalogue)
			if len(errs) == 0 {
				profile, hasProfile = p, true
			}
		}
	}

	res := computeRollup(rollupInput{
		FileRef:     mf.Name,
		Quality:     mf.Spec.Quality,
		FormatScore: mf.Spec.FormatScore,
		Profile:     profile,
		HasProfile:  hasProfile,
	})

	switch mf.Spec.MediaRef.Kind {
	case commonv1.MediaKindMovie:
		return r.applyMovieRollup(ctx, mf.Namespace, mf.Spec.MediaRef.Name, res)
	case commonv1.MediaKindEpisode:
		return r.applyEpisodeRollup(ctx, mf.Namespace, mf.Spec.MediaRef.Name, res)
	default:
		return nil // out of scope for this task -- see "Scope decision"
	}
}

// ownerQualityProfileRef resolves the QualityProfile a MediaFile's owning
// item is ranked against: the Movie's own ref, or an Episode's Series' ref
// (an Episode carries no QualityProfileRef of its own -- confirmed absent
// from api/catalog/v1alpha1/episode_types.go). A NotFound Get is returned
// as-is so rollupToOwner can proceed with HasProfile: false rather than
// failing the reconcile outright; any other error is a hard failure.
func (r *Reconciler) ownerQualityProfileRef(ctx context.Context, mf *catalogv1alpha1.MediaFile) (string, error) {
	switch mf.Spec.MediaRef.Kind {
	case commonv1.MediaKindMovie:
		var m catalogv1alpha1.Movie
		if err := r.Get(ctx, types.NamespacedName{Namespace: mf.Namespace, Name: mf.Spec.MediaRef.Name}, &m); err != nil {
			return "", err
		}
		return m.Spec.QualityProfileRef, nil
	case commonv1.MediaKindEpisode:
		var ep catalogv1alpha1.Episode
		if err := r.Get(ctx, types.NamespacedName{Namespace: mf.Namespace, Name: mf.Spec.MediaRef.Name}, &ep); err != nil {
			return "", err
		}
		var series catalogv1alpha1.Series
		if err := r.Get(ctx, types.NamespacedName{Namespace: mf.Namespace, Name: ep.Spec.SeriesRef}, &series); err != nil {
			return "", err
		}
		return series.Spec.QualityProfileRef, nil
	default:
		return "", nil
	}
}

// applyMovieRollup mirrors res onto the named Movie's status: HasFile,
// FileRef, FileQuality, FileFormatScore, CutoffMet, Phase, and the
// HasFile/CutoffMet conditions. It first Gets the Movie so the conditions it
// sends are the full current set with only HasFile/CutoffMet touched --
// conditions is a listType=map owned in full by k8s.ManagerCatalogarr (the
// Movie controller's own field manager), so an apply carrying only these two
// entries would otherwise release every condition the Movie controller
// itself set (Ready, MetadataReady, Available, ...). A Movie that no longer
// exists is not an error: there is nothing left to roll up onto.
func (r *Reconciler) applyMovieRollup(ctx context.Context, namespace, name string, res rollupResult) error {
	var m catalogv1alpha1.Movie
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &m); err != nil {
		return client.IgnoreNotFound(err)
	}

	conditions := append([]metav1.Condition(nil), m.Status.Conditions...)
	k8s.MarkTrue(&m, &conditions, catalogv1alpha1.MovieConditionHasFile, "HasFile", "backed by MediaFile %s", res.FileRef)
	if res.CutoffMet {
		k8s.MarkTrue(&m, &conditions, catalogv1alpha1.MovieConditionCutoffMet, "CutoffMet", "file meets the profile cutoff")
	} else {
		k8s.MarkFalse(&m, &conditions, catalogv1alpha1.MovieConditionCutoffMet, "CutoffUnmet", "file does not meet the profile cutoff")
	}

	_, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr,
		catalogac.Movie(name, namespace).WithStatus(
			catalogac.MovieStatus().
				WithHasFile(res.HasFile).
				WithFileRef(res.FileRef).
				WithFileQuality(res.FileQuality).
				WithFileFormatScore(res.FileFormatScore).
				WithCutoffMet(res.CutoffMet).
				WithPhase(moviePhaseForFile(res.CutoffMet)).
				WithConditions(k8s.ConditionACs(conditions)...),
		))
	return err
}

// applyEpisodeRollup is applyMovieRollup's Episode counterpart.
func (r *Reconciler) applyEpisodeRollup(ctx context.Context, namespace, name string, res rollupResult) error {
	var ep catalogv1alpha1.Episode
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &ep); err != nil {
		return client.IgnoreNotFound(err)
	}

	conditions := append([]metav1.Condition(nil), ep.Status.Conditions...)
	k8s.MarkTrue(&ep, &conditions, catalogv1alpha1.EpisodeConditionHasFile, "HasFile", "backed by MediaFile %s", res.FileRef)
	if res.CutoffMet {
		k8s.MarkTrue(&ep, &conditions, catalogv1alpha1.EpisodeConditionCutoffMet, "CutoffMet", "file meets the profile cutoff")
	} else {
		k8s.MarkFalse(&ep, &conditions, catalogv1alpha1.EpisodeConditionCutoffMet, "CutoffUnmet", "file does not meet the profile cutoff")
	}

	_, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerCatalogarr,
		catalogac.Episode(name, namespace).WithStatus(
			catalogac.EpisodeStatus().
				WithHasFile(res.HasFile).
				WithFileRef(res.FileRef).
				WithFileQuality(res.FileQuality).
				WithFileFormatScore(res.FileFormatScore).
				WithCutoffMet(res.CutoffMet).
				WithPhase(episodePhaseForFile(res.CutoffMet)).
				WithConditions(k8s.ConditionACs(conditions)...),
		))
	return err
}
