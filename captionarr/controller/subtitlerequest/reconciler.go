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

// The +kubebuilder:rbac markers for this package live in doc.go, at package
// level -- controller-gen ignores a marker attached to a declaration.
package subtitlerequest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	subtitleac "github.com/mediactl/clustarr/api/applyconfiguration/subtitle/subtitle/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/captionarr/datapath"
	"github.com/mediactl/clustarr/captionarr/status"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/subtitles"
	"github.com/mediactl/clustarr/pkg/version"
)

// Condition reasons. The Planned condition carries every Blocked reason, so
// `kubectl get subtitlerequest -o yaml` says why a request is stuck.
const (
	ReasonPlanned            = "Planned"
	ReasonMediaFileNotFound  = "MediaFileNotFound"
	ReasonNotVideo           = "NotVideo"
	ReasonAwaitingProbe      = "AwaitingProbe"
	ReasonProfileNotFound    = "ProfileNotFound"
	ReasonProfileInvalid     = "ProfileInvalid"
	ReasonNoProfile          = "NoProfile"
	ReasonMediaDirUnreadable = "MediaDirUnreadable"
	ReasonNotOnDisk          = "MediaFileNotOnDisk"
	ReasonNotOnDataVolume    = "MediaFileNotOnDataVolume"
	ReasonAllPresent         = "AllPresent"
	ReasonMissing            = "Missing"
	ReasonCutoffMet          = "CutoffMet"
	ReasonCutoffNotMet       = "CutoffNotMet"
	ReasonNotPlanned         = "NotPlanned"
)

// FetchTaskType is the Clustarr-Type header of every fetch task this
// controller publishes.
const FetchTaskType = "subtitle.FetchTask"

const (
	// requeueDisk re-checks a block that no watch will ever clear: the
	// media directory is unreadable or the file is not in it. Both are
	// usually a mount or a rename in flight, not a permanent state.
	requeueDisk = 5 * time.Minute
	// requeueQueueFull backs off a publish the work stream refused.
	requeueQueueFull = time.Minute
	// minRequeue floors every computed requeue. Nothing is ever due again
	// immediately after it was dispatched and stamped, so a requeue this
	// short would mean a bug; the floor keeps such a bug from becoming a hot
	// loop against the bus.
	minRequeue = 30 * time.Second
)

// Reconciler owns SubtitleRequest: it plans which languages a video still
// wants, publishes a fetch task for each one that is due, and schedules the
// next search. It writes only the controller half of status under
// k8s.ManagerCaptionarr, through captionarr/status.PatchRequest, and never
// touches MediaFile (ruling R1). See doc.go.
type Reconciler struct {
	Client client.Client

	// Bus publishes schema.FetchTask. Nil is a configuration error that
	// surfaces as a reconcile error the first time a task is due, not as a
	// silently idle planner.
	Bus events.Publisher

	// DataDir is where the /data volume is mounted in this process
	// (--data-dir). Every MediaFile path is a logical /data path, mapped
	// through captionarr/datapath exactly as the fetch worker maps it. Empty
	// means /data itself.
	DataDir string

	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

func (r *Reconciler) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// blocked is a reconcile that cannot plan: why, and when to look again. A
// zero requeue means a watch will bring the request back.
type blocked struct {
	reason  string
	message string
	requeue time.Duration
}

// inputs is everything a plan is computed from.
type inputs struct {
	mf       *catalogv1alpha1.MediaFile
	profile  *subtitlev1alpha1.SubtitleProfile
	dirNames []string
}

// task is one fetch task this reconcile decided to publish.
type task struct {
	// item indexes the status.items entry the task is for. Every task has
	// one: this controller creates the entry for a newly wanted language in
	// the same apply that records its first dispatch.
	item     int
	langKey  string
	upgrade  bool
	minScore int32
	// due is true when the task goes out because its schedule says so,
	// false when only spec.forceSearch sends it.
	due bool
}

func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (ctrl.Result, error) {
	ctx, span := tracing.Start(ctx, "subtitlerequest.Reconciler.Reconcile")
	defer span.End()

	var sr subtitlev1alpha1.SubtitleRequest
	if err := r.Client.Get(ctx, req.NamespacedName, &sr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if k8s.IsDeleting(&sr) {
		return ctrl.Result{}, nil
	}

	in, blk, err := r.gather(ctx, &sr)
	if err != nil {
		tracing.RecordError(span, err)
		return ctrl.Result{}, err
	}
	if blk != nil {
		return r.block(ctx, &sr, *blk)
	}
	res, err := r.plan(ctx, &sr, in, r.now())
	if err != nil {
		tracing.RecordError(span, err)
	}
	return res, err
}

// gather reads the MediaFile, the profile and the media file's directory. A
// missing or unusable input is a block, not an error; an error is a
// transient API failure, returned without applying anything (no apply
// releases nothing).
func (r *Reconciler) gather(ctx context.Context, sr *subtitlev1alpha1.SubtitleRequest) (*inputs, *blocked, error) {
	var mf catalogv1alpha1.MediaFile
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: sr.Namespace, Name: sr.Spec.MediaFileRef}, &mf); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, &blocked{
				reason:  ReasonMediaFileNotFound,
				message: fmt.Sprintf("MediaFile %q does not exist", sr.Spec.MediaFileRef),
			}, nil
		}
		return nil, nil, fmt.Errorf("subtitlerequest: get MediaFile %s: %w", sr.Spec.MediaFileRef, err)
	}
	switch mf.Spec.MediaRef.Kind {
	case commonv1alpha1.MediaKindMovie, commonv1alpha1.MediaKindEpisode:
	default:
		return nil, &blocked{
			reason:  ReasonNotVideo,
			message: fmt.Sprintf("MediaFile %q is a %s, and only movies and episodes get subtitles", mf.Name, mf.Spec.MediaRef.Kind),
		}, nil
	}
	if mf.Status.ProbeHash == "" || mf.Status.MediaInfo == nil {
		// Planning without the probe would count no embedded subtitle and
		// no audio language, and search for everything. The MediaFile
		// watch fires on status.probeHash, so no requeue is needed.
		return nil, &blocked{
			reason:  ReasonAwaitingProbe,
			message: fmt.Sprintf("MediaFile %q has not been probed yet", mf.Name),
		}, nil
	}

	profile, blk, err := r.resolveProfile(ctx, sr, &mf)
	if err != nil || blk != nil {
		return nil, blk, err
	}

	// The controller role mounts /data (config/manager/captionarr.yaml and
	// the chart's "data" true), so the directory is read, never assumed. A
	// directory that cannot be read is a block: treating it as "no sidecars"
	// would re-download every subtitle a user placed there by hand.
	//
	// spec.path is a logical /data path; --data-dir says where that volume
	// is mounted here, through the same mapping the fetch worker uses. A
	// path off the volume can never be listed, by this controller or by the
	// worker, so it is a block of its own rather than an unreadable
	// directory.
	local, err := datapath.Local(r.DataDir, mf.Spec.Path)
	if err != nil {
		return nil, &blocked{
			reason:  ReasonNotOnDataVolume,
			message: fmt.Sprintf("MediaFile %q: %v", mf.Name, err),
		}, nil
	}
	dir := filepath.Dir(local)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, &blocked{
			reason: ReasonMediaDirUnreadable, requeue: requeueDisk,
			message: fmt.Sprintf("cannot list %s: %v", dir, err),
		}, nil
	}
	base := filepath.Base(mf.Spec.Path)
	names := make([]string, 0, len(entries))
	found := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		names = append(names, e.Name())
		found = found || e.Name() == base
	}
	if !found {
		// An empty or foreign directory is what a missing mount looks like
		// too, so a listing that does not contain the video is not trusted
		// as a statement about its sidecars.
		return nil, &blocked{
			reason: ReasonNotOnDisk, requeue: requeueDisk,
			message: fmt.Sprintf("%s is not in %s", base, dir),
		}, nil
	}
	return &inputs{mf: &mf, profile: profile, dirNames: names}, nil, nil
}

// resolveProfile returns spec.profileRef's SubtitleProfile, or the one
// [selectProfile] picks when it is empty.
func (r *Reconciler) resolveProfile(ctx context.Context, sr *subtitlev1alpha1.SubtitleRequest,
	mf *catalogv1alpha1.MediaFile,
) (*subtitlev1alpha1.SubtitleProfile, *blocked, error) {
	if ref := sr.Spec.ProfileRef; ref != "" {
		var p subtitlev1alpha1.SubtitleProfile
		if err := r.Client.Get(ctx, types.NamespacedName{Name: ref}, &p); err != nil {
			if apierrors.IsNotFound(err) {
				return nil, &blocked{
					reason:  ReasonProfileNotFound,
					message: fmt.Sprintf("SubtitleProfile %q does not exist", ref),
				}, nil
			}
			return nil, nil, fmt.Errorf("subtitlerequest: get SubtitleProfile %s: %w", ref, err)
		}
		if profileInvalid(&p) {
			return nil, &blocked{
				reason:  ReasonProfileInvalid,
				message: fmt.Sprintf("SubtitleProfile %q is marked Invalid", ref),
			}, nil
		}
		return &p, nil, nil
	}

	var list subtitlev1alpha1.SubtitleProfileList
	if err := r.Client.List(ctx, &list); err != nil {
		return nil, nil, fmt.Errorf("subtitlerequest: list SubtitleProfiles: %w", err)
	}
	p, err := selectProfile(list.Items, mf.Labels)
	if err != nil {
		return nil, &blocked{reason: ReasonProfileInvalid, message: err.Error()}, nil
	}
	if p == nil {
		return nil, &blocked{
			reason:  ReasonNoProfile,
			message: "no SubtitleProfile selects this MediaFile and there is no valid default profile",
		}, nil
	}
	return p, nil, nil
}

// plan is the happy path: derive existing, run the planner, dispatch what is
// due, schedule the rest, and apply the complete controller half of status.
func (r *Reconciler) plan(ctx context.Context, sr *subtitlev1alpha1.SubtitleRequest, in *inputs, now time.Time) (ctrl.Result, error) {
	log := logging.FromContext(ctx).With("subtitleRequest", client.ObjectKeyFromObject(sr))
	mf, profile := in.mf, in.profile
	kind := mf.Spec.MediaRef.Kind

	pp, unknownKeys, profileLangs := plannerProfile(profile.Spec, sr.Spec.Languages)
	existing := buildExisting(mf.Status.MediaInfo, profile.Spec.Embedded, filepath.Base(mf.Spec.Path), in.dirNames, profileLangs)
	wantedKeys, cutoffMet := subtitles.Plan(pp, audioLanguages(mf.Status.MediaInfo), existingKeys(existing))

	wanted := make(map[string]bool, len(wantedKeys))
	for _, k := range wantedKeys {
		wanted[string(k)] = true
	}
	profileKeys := make(map[string]bool, len(pp.Languages))
	for _, l := range pp.Languages {
		profileKeys[string(l.Key)] = true
	}

	next := sr.Status.DeepCopy()
	var dropped []string
	next.Items, dropped = planItems(next.Items, profileKeys, wantedKeys)
	if next.ProbeHash != "" && next.ProbeHash != mf.Status.ProbeHash {
		// A different file (a rename, an upgrade, a transcode swap): every
		// language's search history describes a file that is gone, so the
		// new one is searched now rather than at the old file's schedule.
		// Only attempts are reset: every kept item is rescheduled below,
		// so none reaches the apply without a nextSearchAt.
		for i := range next.Items {
			next.Items[i].Attempts = commonv1alpha1.Attempts{}
		}
	}

	cad := cadenceFor(profile.Spec)
	threshold := minScoreFor(kind, profile.Spec, sr.Spec.MinScoreOverride)
	force := sr.Spec.ForceSearch
	// fallback is the check time of a satisfied item that has neither been
	// downloaded nor searched; see upgradeCheckAt.
	fallback := func(it subtitlev1alpha1.SubtitleItem) time.Time {
		if status.IsLive(it) {
			return it.NextSearchAt.Time
		}
		return now.Add(cad.upgradeInterval)
	}

	var tasks []task
	for i, it := range next.Items {
		if wanted[it.LangKey] {
			due := !now.Before(wantedDueAt(now, it.Attempts, cad))
			if due || force {
				tasks = append(tasks, task{item: i, langKey: it.LangKey, minScore: threshold, due: due})
			}
			continue
		}
		if !upgradeCandidate(now, it, cad) {
			continue
		}
		at := upgradeCheckAt(it, cad, fallback(it))
		due := upgradeScheduled(now, at, it, cad) && !now.Before(at)
		if due || force {
			tasks = append(tasks, task{
				item: i, langKey: it.LangKey, upgrade: true,
				minScore: upgradeMinScore(it.Score, threshold), due: due,
			})
		}
	}

	dispatched := 0
	var pubErr error
	for _, t := range tasks {
		prio := events.PriorityNormal
		switch {
		case force:
			prio = events.PriorityHigh
		case t.upgrade:
			prio = events.PriorityLow
		}
		it := &next.Items[t.item]
		msgID := events.MsgIDForSubtitle(string(sr.UID), t.langKey, mf.Status.ProbeHash, it.Attempts.Count+1)
		if force {
			msgID = events.MsgIDForForcedSubtitle(string(sr.UID), t.langKey, mf.Status.ProbeHash, sr.Generation)
		}
		rcpt, err := r.publish(ctx, sr, mf.Status.ProbeHash, msgID, t, prio, now)
		if err != nil {
			pubErr = err
			break
		}
		if !rcpt.Duplicate {
			dispatched++
		} else {
			log.Debug("fetch task absorbed by the dedup window", "langKey", t.langKey, "upgrade", t.upgrade, "msgID", msgID)
		}
		// A duplicate receipt means this very dispatch -- the same attempt
		// number, or the same forced generation -- was accepted inside the
		// window. When the language was due, that dispatch was never
		// recorded (an apply that failed after the publish), so record it
		// now. When only forceSearch sent it, the likelier cause is a reset
		// that failed after an apply that did record it: counting it again
		// would inflate attempts.count and push nextSearchAt out.
		if !rcpt.Duplicate || t.due || it.Attempts.Latest == nil {
			it.Attempts = stamp(it.Attempts, now)
		}
	}

	// nextSearchAt is derived for EVERY kept item on every reconcile, so
	// kubectl shows the real schedule and a profile cadence change applies at
	// once -- and it is never nil or zero on an item this apply sends,
	// because under the liveness protocol (status.IsLive) it is what keeps the
	// item alive: status.RequestControllerFields would silently leave out,
	// and so withdraw, any item without one.
	var wake time.Time
	consider := func(t time.Time) {
		if wake.IsZero() || t.Before(wake) {
			wake = t
		}
	}
	searching := dispatched > 0
	for i := range next.Items {
		it := &next.Items[i]
		var at time.Time
		if wanted[it.LangKey] {
			at = wantedDueAt(now, it.Attempts, cad)
			consider(at)
		} else {
			at = upgradeCheckAt(*it, cad, fallback(*it))
			if upgradeScheduled(now, at, *it, cad) {
				consider(at)
			}
		}
		it.NextSearchAt = &metav1.Time{Time: at}
		switch it.State {
		case "", subtitlev1alpha1.SubtitleItemPending, subtitlev1alpha1.SubtitleItemSearching:
			// Planned and not reported on yet, or reported in flight.
			searching = true
		}
	}
	if len(dropped) > 0 {
		log.Info("releasing items for languages the profile no longer wants", "langKeys", dropped)
	}

	phase := subtitlev1alpha1.SubtitleRequestPhaseSatisfied
	switch {
	case searching:
		phase = subtitlev1alpha1.SubtitleRequestPhaseSearching
	case len(wantedKeys) > 0:
		phase = subtitlev1alpha1.SubtitleRequestPhaseWanted
	}

	next.ObservedGeneration = sr.Generation
	next.Phase = phase
	next.ProfileGeneration = profile.Generation
	next.ProbeHash = mf.Status.ProbeHash
	next.FileFingerprint = fingerprint(mf)
	next.Existing = existing

	conds := carriedConditions(sr.Status.Conditions)
	planned := fmt.Sprintf("planned with SubtitleProfile %s (generation %d)", profile.Name, profile.Generation)
	if len(unknownKeys) > 0 {
		planned += fmt.Sprintf("; spec.languages names keys the profile does not have, ignored: %s", strings.Join(unknownKeys, ", "))
	}
	k8s.MarkTrue(sr, &conds, subtitlev1alpha1.SubtitleRequestConditionPlanned, ReasonPlanned, "%s", planned)
	if len(wantedKeys) == 0 {
		k8s.MarkTrue(sr, &conds, subtitlev1alpha1.SubtitleRequestConditionSatisfied, ReasonAllPresent,
			"every wanted language has a subtitle")
	} else {
		k8s.MarkFalse(sr, &conds, subtitlev1alpha1.SubtitleRequestConditionSatisfied, ReasonMissing,
			"missing: %s", strings.Join(stringKeys(wantedKeys), ", "))
	}
	if cutoffMet {
		k8s.MarkTrue(sr, &conds, subtitlev1alpha1.SubtitleRequestConditionCutoffMet, ReasonCutoffMet, "the profile cutoff is satisfied")
	} else {
		k8s.MarkFalse(sr, &conds, subtitlev1alpha1.SubtitleRequestConditionCutoffMet, ReasonCutoffNotMet, "the profile cutoff is not satisfied")
	}

	if err := r.apply(ctx, sr, *next, conds); err != nil {
		return ctrl.Result{}, err
	}

	if pubErr != nil {
		if errors.Is(pubErr, events.ErrQueueFull) {
			log.Info("captionarr work queue is full; retrying", "err", pubErr)
			return ctrl.Result{RequeueAfter: requeueQueueFull}, nil
		}
		return ctrl.Result{}, fmt.Errorf("subtitlerequest: publish fetch task: %w", pubErr)
	}

	if force {
		if err := r.resetForceSearch(ctx, sr); err != nil {
			return ctrl.Result{}, err
		}
	}
	if len(tasks) > 0 {
		log.Info("planned", "phase", phase, "wanted", stringKeys(wantedKeys), "dispatched", dispatched, "force", force)
	}
	return ctrl.Result{RequeueAfter: requeueAfter(now, wake)}, nil
}

// block applies a Blocked status that re-declares everything this manager
// owns from the live object: the last good existing list, probeHash,
// profileGeneration, fingerprint and every item's schedule. A block is
// usually transient -- a MediaFile mid-probe, a directory mid-rename -- and
// an apply that dropped any of those here would release them, gutting a
// healthy request on a blip (CLAUDE.md, "An early-return path built a partial
// status").
func (r *Reconciler) block(ctx context.Context, sr *subtitlev1alpha1.SubtitleRequest, b blocked) (ctrl.Result, error) {
	logging.FromContext(ctx).Info("subtitle request blocked",
		"subtitleRequest", client.ObjectKeyFromObject(sr), "reason", b.reason, "message", b.message)

	next := sr.Status.DeepCopy()
	next.ObservedGeneration = sr.Generation
	next.Phase = subtitlev1alpha1.SubtitleRequestPhaseBlocked
	// Items are re-declared exactly as they are: status.RequestControllerFields
	// renders only the live ones, so an item this manager already withdrew is
	// not claimed again -- sending even its langKey would keep it alive.

	conds := carriedConditions(sr.Status.Conditions)
	k8s.MarkFalse(sr, &conds, subtitlev1alpha1.SubtitleRequestConditionPlanned, b.reason, "%s", b.message)
	for _, t := range []string{subtitlev1alpha1.SubtitleRequestConditionSatisfied, subtitlev1alpha1.SubtitleRequestConditionCutoffMet} {
		if k8s.FindCondition(conds, t) == nil {
			k8s.MarkUnknown(sr, &conds, t, ReasonNotPlanned, "the request has not been planned")
		}
	}
	if err := r.apply(ctx, sr, *next, conds); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: b.requeue}, nil
}

// apply writes st and conds as the complete k8s.ManagerCaptionarr
// declaration.
//
// No re-Get precedes it, and that is deliberate rather than an oversight of
// the read-work-apply rule: every leaf this manager declares has exactly one
// writer -- this reconciler, leader-elected, serialized per object by the
// workqueue -- so a snapshot taken at the top of the reconcile cannot roll
// back anybody else's value. The worker's leaves are not in the declaration
// at all (captionarr/status renders only langKey, attempts and nextSearchAt
// per item), and the worker never creates or withdraws an item (the
// liveness protocol, status.IsLive), so the snapshot's item set is this
// manager's own.
func (r *Reconciler) apply(ctx context.Context, sr *subtitlev1alpha1.SubtitleRequest,
	st subtitlev1alpha1.SubtitleRequestStatus, conds []metav1.Condition,
) error {
	target := sr.DeepCopy()
	target.Status = st
	return status.PatchRequest(ctx, r.Client, k8s.ManagerCaptionarr, target,
		func(ac *subtitleac.SubtitleRequestStatusApplyConfiguration) {
			// Conditions are unseeded by captionarr/status and WithConditions
			// appends, so this is the one place they are set.
			ac.WithConditions(k8s.ConditionACs(conds)...)
		})
}

// publish sends one schema.FetchTask under msgID -- events.MsgIDForSubtitle
// for a scheduled search, events.MsgIDForForcedSubtitle for a forced one
// (ruling R6, and doc.go's "Dedup and forceSearch"): a level-driven
// reconciler publishes on every eligible pass, and the work stream's
// deduplication window is what turns those repeats into one task.
func (r *Reconciler) publish(ctx context.Context, sr *subtitlev1alpha1.SubtitleRequest, probeHash, msgID string,
	t task, prio events.Priority, now time.Time,
) (events.Receipt, error) {
	ctx, span := tracing.Start(ctx, "subtitlerequest.publishFetchTask")
	defer span.End()
	if r.Bus == nil {
		return events.Receipt{}, errors.New("subtitlerequest: no bus configured")
	}
	schemaName, data, err := schema.Encode(schema.FetchTask{
		RequestRef: schema.Ref{Namespace: sr.Namespace, Name: sr.Name, UID: string(sr.UID)},
		LangKey:    t.langKey,
		MinScore:   t.minScore,
		Upgrade:    t.upgrade,
		ProbeHash:  probeHash,
	})
	if err != nil {
		return events.Receipt{}, err
	}
	env := &events.Envelope{
		ID:     msgID,
		Type:   FetchTaskType,
		Schema: schemaName,
		Source: "captionarr@" + version.String(),
		Key:    sr.Namespace + "/" + sr.Name,
		Time:   now,
		Data:   data,
	}
	tracing.Inject(ctx, env)
	rcpt, err := r.Bus.Publish(ctx, events.WorkFetchSubject(prio, string(sr.UID), t.langKey), env,
		events.WithMsgID(msgID), events.WithExpectStream(events.StreamWorkCaptionarr))
	if err != nil {
		tracing.RecordError(span, err)
	}
	return rcpt, err
}

// resetForceSearch clears the one-shot spec.forceSearch once its searches are
// out (SubtitleRequestSpec.ForceSearch: "the controller resets it to false
// once the search has been dispatched").
//
// It is a JSON merge patch, not a server-side apply, on purpose: the profile
// controller creates SubtitleRequests under the same k8s.ManagerCaptionarr
// name, and an apply of spec.forceSearch alone would share -- and so release
// -- that manager's apply-owned spec fields. An Update-operation patch is
// tracked in its own managedFields entry and cannot.
func (r *Reconciler) resetForceSearch(ctx context.Context, sr *subtitlev1alpha1.SubtitleRequest) error {
	patch := client.RawPatch(types.MergePatchType, []byte(`{"spec":{"forceSearch":false}}`))
	if err := r.Client.Patch(ctx, sr, patch, client.FieldOwner(k8s.ManagerCaptionarr.String())); err != nil {
		return fmt.Errorf("subtitlerequest: reset spec.forceSearch: %w", err)
	}
	return nil
}

// carriedConditions returns the live Planned, Satisfied and CutoffMet
// conditions, in that order, for the caller to update. Only these three are
// carried: they are the complete set this manager declares.
func carriedConditions(live []metav1.Condition) []metav1.Condition {
	var out []metav1.Condition
	for _, t := range []string{
		subtitlev1alpha1.SubtitleRequestConditionPlanned,
		subtitlev1alpha1.SubtitleRequestConditionSatisfied,
		subtitlev1alpha1.SubtitleRequestConditionCutoffMet,
	} {
		if c := k8s.FindCondition(live, t); c != nil {
			out = append(out, *c)
		}
	}
	return out
}

// fingerprint is status.fileFingerprint: the size and mtime the MediaFile's
// probe hashed.
func fingerprint(mf *catalogv1alpha1.MediaFile) *commonv1alpha1.FileFingerprint {
	fp := &commonv1alpha1.FileFingerprint{SizeBytes: mf.Spec.SizeBytes}
	if !mf.Spec.ModTime.IsZero() {
		mt := mf.Spec.ModTime
		fp.ModTime = &mt
	}
	return fp
}

// requeueAfter turns the earliest scheduled search into a RequeueAfter. Worker
// status writes do not bump metadata.generation, and neither does the clock:
// a search coming due and an upgrade window opening are wake-ups only this
// computed requeue delivers.
func requeueAfter(now, wake time.Time) time.Duration {
	if wake.IsZero() {
		return 0
	}
	return max(wake.Sub(now), minRequeue)
}

func stringKeys(ks []subtitles.LangKey) []string {
	out := make([]string, len(ks))
	for i, k := range ks {
		out[i] = string(k)
	}
	return out
}

// SetupWithManager registers the controller.
//
// Three things wake a request, and each has its own predicate:
//
//   - its own spec (metadata.generation), or a change to any leaf the
//     WORKER owns on any item ([workerSignal]). Worker writes never bump
//     metadata.generation, so a generation-only predicate would never see a
//     language get downloaded or come back unavailable; this controller's
//     own writes change none of those leaves, so they do not re-trigger it.
//   - its MediaFile's status.probeHash (§10's row for captionarr). The
//     request shares the MediaFile's name (SubtitleRequestSpec.MediaFileRef).
//   - a SubtitleProfile's generation or Invalid condition, fanned out to the
//     requests that name it or resolve by selector.
//
// Time-based work -- a search or an upgrade coming due -- is none of these,
// and arrives through the RequeueAfter [requeueAfter] computes.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("subtitlerequest").
		For(&subtitlev1alpha1.SubtitleRequest{}, builder.WithPredicates(k8s.Or(
			k8s.GenerationChanged(), k8s.StatusFieldChanged(workerSignal)))).
		Watches(&catalogv1alpha1.MediaFile{}, handler.EnqueueRequestsFromMapFunc(mapMediaFile),
			builder.WithPredicates(k8s.StatusFieldChanged(probeHashOf))).
		Watches(&subtitlev1alpha1.SubtitleProfile{}, handler.EnqueueRequestsFromMapFunc(r.mapProfile),
			builder.WithPredicates(k8s.Or(k8s.GenerationChanged(), k8s.StatusFieldChanged(profileInvalidOf)))).
		Complete(r)
}

// workerSignal projects every captionarr-worker leaf of every item into one
// comparable string: langKey, state, score, scoreOutOf, provider,
// subtitleID, path, lastError and downloadedAt. It deliberately excludes
// attempts and nextSearchAt, which are this controller's own.
func workerSignal(o client.Object) string {
	sr, ok := o.(*subtitlev1alpha1.SubtitleRequest)
	if !ok {
		return ""
	}
	items := slices.Clone(sr.Status.Items)
	slices.SortFunc(items, func(a, b subtitlev1alpha1.SubtitleItem) int { return strings.Compare(a.LangKey, b.LangKey) })
	var sb strings.Builder
	for _, it := range items {
		dl := ""
		if it.DownloadedAt != nil {
			dl = it.DownloadedAt.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(&sb, "%q %q %d %d %q %q %q %q %q;", it.LangKey, it.State, it.Score, it.ScoreOutOf,
			it.Provider, it.SubtitleID, it.Path, it.LastError, dl)
	}
	return sb.String()
}

func probeHashOf(o client.Object) string {
	mf, ok := o.(*catalogv1alpha1.MediaFile)
	if !ok {
		return ""
	}
	return mf.Status.ProbeHash
}

func profileInvalidOf(o client.Object) bool {
	p, ok := o.(*subtitlev1alpha1.SubtitleProfile)
	return ok && profileInvalid(p)
}

func mapMediaFile(_ context.Context, o client.Object) []reconcile.Request {
	return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: o.GetNamespace(), Name: o.GetName()}}}
}

// mapProfile enqueues every request that names the profile, and every request
// that names none -- selector resolution and the default fallback can change
// under any profile edit.
func (r *Reconciler) mapProfile(ctx context.Context, o client.Object) []reconcile.Request {
	var list subtitlev1alpha1.SubtitleRequestList
	if err := r.Client.List(ctx, &list); err != nil {
		logging.FromContext(ctx).Error("list SubtitleRequests for a profile change", "profile", o.GetName(), "err", err)
		return nil
	}
	var out []reconcile.Request
	for _, sr := range list.Items {
		if sr.Spec.ProfileRef == "" || sr.Spec.ProfileRef == o.GetName() {
			out = append(out, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&sr)})
		}
	}
	return out
}
