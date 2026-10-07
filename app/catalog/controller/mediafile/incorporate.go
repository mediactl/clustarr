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
	"errors"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/probestore"
)

// incorporation is what a reconcile knows about the file when it folds a
// probe's summary into status.
type incorporation struct {
	path       string // the file: spec.path, or a transcode swap's target
	ps         probeState
	swap, kept *transcodev1alpha1.TranscodeJob
	graft      *transcodev1alpha1.AudioGraft // an audio graft's swap (graft.go): new bytes, never a transcode
	transcoded bool                          // spec.original was false before this reconcile
	now        metav1.Time
}

// incorporated is what folding a summary in did, for Reconcile's Events.
type incorporated struct {
	probed     bool
	changed    bool
	keptOutput string
}

// incorporate folds mi -- a summary of in.path's current bytes by probe
// version version, from the probe record or, for a kept job, from status --
// into known and conditions, and applies catalogarr's labels and, from the
// first swap on, the spec fields it owns, in one k8s.Apply. It is the block
// Reconcile ran after its own probe before the probe queue (spec 2026-10-06
// §6.5.3, "today's success block, unchanged").
func (r *Reconciler) incorporate(ctx context.Context, mf *catalogv1alpha1.MediaFile, conditions *[]metav1.Condition,
	known *knownStatus, in incorporation, mi *commonv1.MediaInfo, version int32,
) (incorporated, error) {
	log := logging.FromContext(ctx)
	out := incorporated{probed: true}
	original := mf.Spec.Original == nil || *mf.Spec.Original
	if in.swap != nil {
		original = false
	}
	if in.graft != nil {
		// An audio graft rewrote the file in place (anime dual-audio spec
		// §7.2): not a transcode -- spec.original stays -- but its bytes are
		// new, and this summary is of them.
		known.GraftTag, known.GraftedAt = in.graft.Status.GraftTag, &in.now
	}

	// MirrorLabels is applied on every probe, not only alongside a
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
	// means catalogarr re-asserts path/sizeBytes/modTime/original with the
	// current probe's fresh values on every Apply from here on, not
	// only the reconcile that first incorporates a swap -- otherwise a
	// later labels-only Apply (triggered by, say, a stale re-probe with
	// no new TranscodeJob) would erase fields catalogarr already owns.
	labels := MirrorLabels(mf.Spec.MediaRef.Kind, mf.Spec.Quality, mi, original)
	mainAC := catalogac.MediaFile(mf.Name, mf.Namespace).WithLabels(labels)
	if original && known.GraftTag != "" {
		// A grafted original: the size, mtime and path are catalogarr's
		// from its first graft on, re-sent on every apply as after a
		// transcode -- and spec.original is never sent, so importarr
		// keeps it (a graft is not a transcode).
		mainAC = mainAC.WithSpec(catalogac.MediaFileSpec().
			WithPath(in.path).
			WithSizeBytes(in.ps.SizeBytes).
			WithModTime(in.ps.ModTime))
	}
	if !original {
		// spec.path is catalogarr's from the first swap on, beside the
		// three fields it already took (gap-fix R-11): a container
		// change, or an explicit spec.outputPath, puts the encode under
		// a new name and retires the source, so the MediaFile must
		// follow the file or it names a path that no longer exists. It
		// is sent on every apply, including an in-place swap where it
		// equals importarr's value (co-owned, harmless), because a
		// manager that stops sending a field releases it.
		mainAC = mainAC.WithSpec(catalogac.MediaFileSpec().
			WithPath(in.path).
			WithSizeBytes(in.ps.SizeBytes).
			WithModTime(in.ps.ModTime).
			WithOriginal(false))
	}
	if _, err := k8s.Apply(ctx, r.Client, k8s.ManagerCatalogarr, mainAC); err != nil {
		return out, err
	}
	if in.swap != nil {
		log.Info("incorporated transcode swap", "transcodeJob", in.swap.Name)
	}
	if in.graft != nil {
		log.Info("incorporated audio graft", "audioGraft", in.graft.Name, "graftTag", in.graft.Status.GraftTag)
	}
	k8s.MarkTrue(mf, conditions, catalogv1alpha1.MediaFileConditionProbed, "Probed", "probed at %s", in.now.Time)
	k8s.MarkTrue(mf, conditions, catalogv1alpha1.MediaFileConditionReady, "Ready", "file present and probed")
	known.ProbeHash = in.ps.Hash
	known.ProbedAt = &in.now
	known.ProbeVersion = version
	known.MediaInfo = mi
	if in.swap == nil && in.kept == nil && in.graft == nil && in.transcoded && bytesChanged(mf, in.ps) && mf.Status.ProbeHash != "" {
		// A rename (naming.renameTranscoded) moves the same bytes:
		// the probe is stale by path alone, and the verdict stands.
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
		out.changed = true
	}
	if in.swap != nil {
		profileTag, err := r.transcodeProfileTag(ctx, in.swap)
		if err != nil {
			return out, err
		}
		// A whole new TranscodeState, not an edit of the one already
		// there: the previous state described the previous encode, and
		// its jobRef names a TranscodeJob that is no longer the latest.
		known.Transcode = &catalogv1alpha1.TranscodeState{
			Compliant:  true,
			ProfileTag: profileTag,
			LastResult: catalogv1alpha1.TranscodeResultSucceeded,
		}
		if in.path != mf.Spec.Path {
			log.Info("followed a transcode to its new path", "from", mf.Spec.Path, "to", in.path)
		}
	}
	if in.kept != nil {
		profileTag, err := r.transcodeProfileTag(ctx, in.kept)
		if err != nil {
			return out, err
		}
		// replaceSource=false: the encode was written beside the source
		// and the source kept, so THIS MediaFile's file is untouched --
		// not original=false, not compliant. The profile revision is
		// recorded so squasharr's "already transcoded to this revision"
		// check (transcodeprofile.alreadyTranscoded) does not plan the
		// same derived copy again, and the result is history. The
		// derived file itself is not this MediaFile's: see swapTarget.
		known.Transcode = &catalogv1alpha1.TranscodeState{
			ProfileTag: profileTag,
			LastResult: catalogv1alpha1.TranscodeResultSucceeded,
		}
		out.keptOutput = in.kept.Status.Result.OutputPath
	}
	return out, nil
}

// pending reports a queued, unanswered probe of a new file, changed bytes or a
// transcode swap's target: Probed and Ready False, ProbePending, every other
// field re-sent as it stands (the complete declaration), naming held. Labels
// and the spec takeover wait for the incorporation.
func (r *Reconciler) pending(ctx context.Context, mf *catalogv1alpha1.MediaFile, conditions []metav1.Condition,
	known *knownStatus, probeStale, transcodePending bool, rec schema.ProbeRecord, res ctrl.Result,
) (ctrl.Result, error) {
	msg := fmt.Sprintf("probe requested %s (%s)", rec.RequestedAt.UTC().Format(time.RFC3339), rec.Lane)
	k8s.MarkFalse(mf, &conditions, catalogv1alpha1.MediaFileConditionProbed, catalogv1alpha1.MediaFileReasonProbePending, "%s", msg)
	k8s.MarkFalse(mf, &conditions, catalogv1alpha1.MediaFileConditionReady, catalogv1alpha1.MediaFileReasonProbePending, "%s", msg)
	var retryNaming bool
	known.Naming, retryNaming = r.renderNaming(ctx, mf, known, namingInputs{
		specPath: mf.Spec.Path, probeStale: probeStale, transcodePending: transcodePending,
	})
	if err := r.applyStatus(ctx, mf, conditions, known); err != nil {
		return ctrl.Result{}, err
	}
	return withNamingRetry(res, retryNaming), nil
}

// probeFailed reports a failed probe, or one given up on, without releasing
// what the last good probe recorded: the ProbeFailed branch of before the
// queue, with the import domain's failure as its message.
func (r *Reconciler) probeFailed(ctx context.Context, mf *catalogv1alpha1.MediaFile, conditions []metav1.Condition,
	known *knownStatus, probeStale, transcodePending bool, j judgement, now time.Time,
) (ctrl.Result, error) {
	k8s.MarkFalse(mf, &conditions, catalogv1alpha1.MediaFileConditionProbed, "ProbeFailed", "%s", j.failure)
	k8s.MarkFalse(mf, &conditions, catalogv1alpha1.MediaFileConditionReady, "ProbeFailed", "probe failed: %s", j.failure)
	var retryNaming bool
	known.Naming, retryNaming = r.renderNaming(ctx, mf, known, namingInputs{
		specPath: mf.Spec.Path, probeStale: probeStale, transcodePending: transcodePending,
	})
	if err := r.applyStatus(ctx, mf, conditions, known); err != nil {
		return ctrl.Result{}, err
	}
	return withNamingRetry(requeueAt(ctrl.Result{}, j.requeueAt, now), retryNaming), nil
}

// ask runs a request or pending verdict: Request and Publish for a request (a
// lost compare-and-swap is conflict), a same-Msg-Id republish inside the
// window for a pending one. It returns the record the status reports.
func (r *Reconciler) ask(ctx context.Context, want probestore.Want, cur probestore.Current, j judgement) (schema.ProbeRecord, bool, error) {
	if j.verdict == verdictPending {
		if j.republish {
			if err := r.Probes.Publish(ctx, cur.Record); err != nil {
				return cur.Record, false, err
			}
		}
		return cur.Record, false, nil
	}
	want.Lane = j.lane
	rec, err := r.Probes.Request(ctx, want, cur)
	if errors.Is(err, probestore.ErrConflict) {
		return rec, true, nil
	}
	if err != nil {
		return rec, false, err
	}
	if err := r.Probes.Publish(ctx, rec); err != nil {
		return rec, false, err
	}
	metrics.RecordRequestsTotal.WithLabelValues("probe", rec.Lane).Inc()
	return rec, false, nil
}
