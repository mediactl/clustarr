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

// Package audiograft reconciles AudioGraft (anime dual-audio spec §7.2):
// when the item's probed file still lacks the donor's language, it runs one
// graft Job -- transcode --graft-task on the pool's pod template,
// class cpu -- and records the Job's result, which its pod leaves in its
// termination message, as the AudioGraft's status. It never runs a graft
// beside an open TranscodeJob of the file, and the TranscodeProfile
// controller never plans one beside a graft (Grafting).
package audiograft

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	ctrl "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	controllerruntime "sigs.k8s.io/controller-runtime"

	transcodeac "github.com/mediactl/clustarr/api/applyconfiguration/transcode/transcode/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/squash/controller/pool"
	"github.com/mediactl/clustarr/app/squash/controller/transcodeprofile"
	"github.com/mediactl/clustarr/app/squash/graftstate"
	"github.com/mediactl/clustarr/app/squash/grafttask"
	"github.com/mediactl/clustarr/app/squash/jobspec"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/lang"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=audiografts,verbs=get;list;watch
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=audiografts/status,verbs=get;patch;update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;delete
// +kubebuilder:rbac:groups="",resources=pods,verbs=list
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=episodes;movies;mediafiles,verbs=get;list;watch
// +kubebuilder:rbac:groups=catalog.clustarr.io,resources=rootfolders,verbs=list
// +kubebuilder:rbac:groups=transcode.clustarr.io,resources=transcodejobs,verbs=get;list;watch

// LabelGraft names the AudioGraft a graft Job (and its pod) runs for.
const LabelGraft = "squasharr.clustarr.io/audiograft"

// DefaultConcurrency is --graft-concurrency's default: grafts running at
// once in the namespace. Each is a CPU pod of the pool template's size.
const DefaultConcurrency = 2

// Reasons on Waiting.
const (
	ReasonItemMissing      = "ItemMissing"
	ReasonFileUnprobed     = "FileUnprobed"
	ReasonTranscodeRunning = "TranscodeRunning"
	ReasonWaitingForSlot   = "WaitingForSlot"
	ReasonJobLost          = "JobLost"
	// The graft waits for a transcode its file is due, to ride along with
	// it (phase 4 addendum), at most joinWait.
	ReasonWaitingForTranscode = "WaitingForTranscode"
	ReasonJoinedTranscode     = "JoinedTranscode"
	ReasonReduced             = "Reduced"
	ReasonJobFailed           = "JobFailed"
)

// jobDeadline bounds one graft Job: a two-hour film's graft is minutes.
const jobDeadline = 2 * time.Hour

// joinWait is how long a graft waits for a transcode its file is due before
// it grafts alone: a full transcode window can keep a file waiting days.
const joinWait = 6 * time.Hour

// Reconciler reconciles AudioGraft.
type Reconciler struct {
	ctrl.Client
	// APIReader reads the graft Jobs' pods, which the cache does not hold.
	APIReader ctrl.Reader
	// Pool renders the graft Job's pod (image, data claim, securityContext,
	// UMASK), as for a cpu pool.
	Pool pool.Config
	// Concurrency is the most grafts running at once (DefaultConcurrency
	// when 0).
	Concurrency int
	Clock       func() time.Time
}

func (r *Reconciler) now() time.Time {
	if r.Clock != nil {
		return r.Clock()
	}
	return time.Now()
}

// SetupWithManager registers the controller: AudioGraft, its Jobs, and the
// item's MediaFile (by the AudioGraft's status.mediaFileRef).
func (r *Reconciler) SetupWithManager(mgr controllerruntime.Manager) error {
	if err := RegisterIndexes(context.Background(), mgr.GetFieldIndexer()); err != nil {
		return err
	}
	return controllerruntime.NewControllerManagedBy(mgr).
		Named("audiograft").
		For(&transcodev1alpha1.AudioGraft{}).
		Owns(&batchv1.Job{}, builder.WithPredicates(predicate.NewPredicateFuncs(func(o ctrl.Object) bool {
			return o.GetLabels()[LabelGraft] != ""
		}))).
		Watches(&catalogv1alpha1.MediaFile{}, handler.EnqueueRequestsFromMapFunc(r.graftsOfFile)).
		Watches(&transcodev1alpha1.TranscodeJob{}, handler.EnqueueRequestsFromMapFunc(r.graftsOfTranscode)).
		Complete(r)
}

// graftsOfFile maps a MediaFile to the AudioGrafts judged against it, or
// for its item (a new file of an item the graft has not seen yet).
func (r *Reconciler) graftsOfFile(ctx context.Context, o ctrl.Object) []reconcile.Request {
	mf, ok := o.(*catalogv1alpha1.MediaFile)
	if !ok {
		return nil
	}
	// Through field indexes, never a namespace List per event: at start
	// every MediaFile is an event (CLAUDE.md, captionarr 2026-09-29).
	seen := map[string]bool{}
	var out []reconcile.Request
	for _, sel := range []ctrl.MatchingFields{{IndexMediaFileRef: mf.Name}, {IndexItemRef: mf.Spec.MediaRef.Name}} {
		var l transcodev1alpha1.AudioGraftList
		if err := r.List(ctx, &l, ctrl.InNamespace(mf.Namespace), sel); err != nil {
			return nil
		}
		for _, g := range l.Items {
			if !seen[g.Name] {
				seen[g.Name] = true
				out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: g.Namespace, Name: g.Name}})
			}
		}
	}
	return out
}

// graftsOfTranscode maps a TranscodeJob to the AudioGraft that joined it and
// to those waiting on its file.
func (r *Reconciler) graftsOfTranscode(ctx context.Context, o ctrl.Object) []reconcile.Request {
	tj, ok := o.(*transcodev1alpha1.TranscodeJob)
	if !ok {
		return nil
	}
	var out []reconcile.Request
	if g := tj.Status.Graft; g != nil && g.AudioGraft != "" {
		out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: tj.Namespace, Name: g.AudioGraft}})
	}
	var l transcodev1alpha1.AudioGraftList
	if err := r.List(ctx, &l, ctrl.InNamespace(tj.Namespace), ctrl.MatchingFields{IndexMediaFileRef: tj.Spec.MediaFileRef}); err == nil {
		for _, g := range l.Items {
			out = append(out, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: g.Namespace, Name: g.Name}})
		}
	}
	return out
}

// The AudioGraft field indexes graftsOfFile reads. They carry the
// squasharr.audiograft. prefix (spec §5.7) because the one manager also runs
// the MediaFile reconciler, which registers its own AudioGraft index named
// "status.mediaFileRef": a second IndexField on the same (kind, name) in one
// cache is an indexer conflict, and the manager would fail at start.
const (
	IndexMediaFileRef = "squasharr.audiograft.status.mediaFileRef"
	IndexItemRef      = "squasharr.audiograft.spec.itemRef.name"
)

// RegisterIndexes registers them on idx.
func RegisterIndexes(ctx context.Context, idx ctrl.FieldIndexer) error {
	if err := idx.IndexField(ctx, &transcodev1alpha1.AudioGraft{}, IndexMediaFileRef, func(o ctrl.Object) []string {
		if g, ok := o.(*transcodev1alpha1.AudioGraft); ok && g.Status.MediaFileRef != "" {
			return []string{g.Status.MediaFileRef}
		}
		return nil
	}); err != nil {
		return err
	}
	return idx.IndexField(ctx, &transcodev1alpha1.AudioGraft{}, IndexItemRef, func(o ctrl.Object) []string {
		if g, ok := o.(*transcodev1alpha1.AudioGraft); ok {
			return []string{g.Spec.ItemRef.Name}
		}
		return nil
	})
}

// Reconcile implements reconcile.Reconciler.
func (r *Reconciler) Reconcile(ctx context.Context, req reconcile.Request) (reconcile.Result, error) {
	ctx, span := tracing.Start(ctx, "audiograft.Reconcile")
	defer span.End()
	ctx = logging.With(ctx, "audiograft", req.Name, "namespace", req.Namespace)

	var g transcodev1alpha1.AudioGraft
	if err := r.Get(ctx, req.NamespacedName, &g); err != nil {
		return reconcile.Result{}, ctrl.IgnoreNotFound(err)
	}
	if k8s.IsDeleting(&g) {
		return reconcile.Result{}, nil
	}
	st := g.Status
	st.ObservedGeneration = g.Generation

	// A Job of this graft decides first: it is running, or it has a result.
	switch {
	case graftstate.Joined(&g) != "":
		res, done, err := r.followJoined(ctx, &g, &st)
		if err != nil || !done {
			return res, err
		}
	case st.JobName != "":
		res, done, err := r.followJob(ctx, &g, &st)
		if err != nil || !done {
			return res, err
		}
	}

	// The donor is cut down to its audio at once, whatever the target's
	// state (phase 4 addendum): its video leaves the disk however long the
	// graft waits.
	if res, done, err := r.reduce(ctx, &g, &st); err != nil || done {
		return res, err
	}

	mf, reason, err := r.file(ctx, &g)
	if err != nil {
		return reconcile.Result{}, err
	}
	if mf == nil {
		return r.wait(ctx, &g, st, reason, time.Minute)
	}
	if missing(&g, mf) == "" {
		if st.Phase != transcodev1alpha1.AudioGraftSucceeded {
			st.Phase, st.Reason, st.Message = transcodev1alpha1.AudioGraftSucceeded, grafttask.ReasonPresent, "the file carries "+strings.Join(g.Spec.Languages, ", ")
		}
		st.MediaFileRef, st.TargetProbeHash = mf.Name, mf.Status.ProbeHash
		return reconcile.Result{}, r.apply(ctx, &g, st)
	}
	// A graft that swapped this file waits for the file's re-probe, which
	// catalogarr runs on the AudioGraft's success and whose MediaFile event
	// wakes this one; a worker that found the language present against
	// this probe is not asked again until the file changes.
	if st.Phase == transcodev1alpha1.AudioGraftSucceeded && st.TargetProbeHash == mf.Status.ProbeHash {
		return reconcile.Result{}, nil
	}
	if st.Phase == transcodev1alpha1.AudioGraftFailed && st.TargetProbeHash == mf.Status.ProbeHash &&
		failedGeneration(&g) == g.Generation {
		// The donor's fault is final for this donor and this file (spec
		// §9); anything else -- an evicted pod, a target changed under the
		// graft, an I/O error -- is tried again after failureBackoff.
		if DonorFault(st.Reason) {
			return reconcile.Result{}, nil
		}
		if st.CompletedAt != nil {
			if wait := st.CompletedAt.Add(failureBackoff).Sub(r.now()); wait > 0 {
				return reconcile.Result{RequeueAfter: wait}, nil
			}
		}
	}
	if tj, err := r.openTranscode(ctx, r.Client, mf); err != nil {
		return reconcile.Result{}, err
	} else if tj != nil {
		if jg := tj.Status.Graft; jg != nil && jg.AudioGraft == g.Name && jg.Phase == transcodev1alpha1.GraftJoined {
			return r.joined(ctx, &g, st, tj, mf)
		}
		// Not dispatched yet: the dispatcher attaches this graft when it
		// dispatches. Dispatched without it: wait it out, then graft.
		reason := ReasonTranscodeRunning
		if !dispatched(tj) {
			reason = ReasonWaitingForTranscode
		}
		return r.wait(ctx, &g, st, reason, 5*time.Minute)
	}
	// A transcode the file is due rewrites it anyway: wait for it, a while,
	// rather than rewrite the file twice.
	if due, err := transcodeprofile.WouldTranscode(ctx, r.Client, mf); err != nil {
		return reconcile.Result{}, err
	} else if due {
		if st.Reason != ReasonWaitingForTranscode || st.StartedAt == nil {
			now := metav1.NewTime(r.now())
			st.StartedAt = &now
		}
		if left := st.StartedAt.Add(joinWait).Sub(r.now()); left > 0 {
			st.Phase, st.Reason, st.Message = transcodev1alpha1.AudioGraftWaiting, ReasonWaitingForTranscode,
				"waiting for the file's transcode, to graft in the same pass"
			st.MediaFileRef, st.TargetProbeHash = mf.Name, mf.Status.ProbeHash
			return reconcile.Result{RequeueAfter: min(left, 30*time.Minute)}, r.apply(ctx, &g, st)
		}
	}
	running, err := r.running(ctx, g.Namespace)
	if err != nil {
		return reconcile.Result{}, err
	}
	if running >= cmp.Or(r.Concurrency, DefaultConcurrency) {
		return r.wait(ctx, &g, st, ReasonWaitingForSlot, time.Minute)
	}
	return r.start(ctx, &g, st, mf)
}

// failedGeneration is the generation a Failed status was written for: its
// observedGeneration, which a later spec (a new donor) moves past.
func failedGeneration(g *transcodev1alpha1.AudioGraft) int64 { return g.Status.ObservedGeneration }

// file is the item's probed MediaFile, or nil and why there is none.
func (r *Reconciler) file(ctx context.Context, g *transcodev1alpha1.AudioGraft) (*catalogv1alpha1.MediaFile, string, error) {
	key := types.NamespacedName{Namespace: g.Namespace, Name: g.Spec.ItemRef.Name}
	var fileRef string
	switch g.Spec.ItemRef.Kind {
	case commonv1.MediaKindEpisode:
		var e catalogv1alpha1.Episode
		if err := r.Get(ctx, key, &e); err != nil {
			return nil, ReasonItemMissing, ctrl.IgnoreNotFound(err)
		}
		fileRef = ptr.Deref(e.Status.FileRef, "")
	case commonv1.MediaKindMovie:
		var m catalogv1alpha1.Movie
		if err := r.Get(ctx, key, &m); err != nil {
			return nil, ReasonItemMissing, ctrl.IgnoreNotFound(err)
		}
		fileRef = ptr.Deref(m.Status.FileRef, "")
	default:
		return nil, ReasonItemMissing, nil
	}
	if fileRef == "" {
		return nil, ReasonFileUnprobed, nil
	}
	var mf catalogv1alpha1.MediaFile
	if err := r.Get(ctx, types.NamespacedName{Namespace: g.Namespace, Name: fileRef}, &mf); err != nil {
		return nil, ReasonFileUnprobed, ctrl.IgnoreNotFound(err)
	}
	if mf.Status.ProbeHash == "" || mf.Status.MediaInfo == nil {
		return nil, ReasonFileUnprobed, nil
	}
	return &mf, "", nil
}

// missing is the first of the graft's languages the file's probe lacks,
// "" when it carries them all.
func missing(g *transcodev1alpha1.AudioGraft, mf *catalogv1alpha1.MediaFile) string {
	have := map[string]bool{}
	for _, a := range mf.Status.MediaInfo.Audio {
		if t, ok := lang.Normalize(a.Language); ok {
			have[base(string(t))] = true
		}
	}
	for _, l := range g.Spec.Languages {
		if t, ok := lang.Normalize(l); ok && !have[base(string(t))] {
			return string(t)
		}
	}
	return ""
}

func base(t string) string { b, _, _ := strings.Cut(t, "-"); return strings.ToLower(b) }

// transcodingLive reports an open TranscodeJob of the file, read from the
// apiserver.
func (r *Reconciler) transcodingLive(ctx context.Context, mf *catalogv1alpha1.MediaFile) (bool, error) {
	tj, err := r.openTranscode(ctx, r.APIReader, mf)
	return tj != nil, err
}

// openTranscode is the file's open TranscodeJob, nil when none.
func (r *Reconciler) openTranscode(ctx context.Context, c ctrl.Reader, mf *catalogv1alpha1.MediaFile) (*transcodev1alpha1.TranscodeJob, error) {
	var l transcodev1alpha1.TranscodeJobList
	if err := c.List(ctx, &l, ctrl.InNamespace(mf.Namespace)); err != nil {
		return nil, fmt.Errorf("audiograft: list TranscodeJobs: %w", err)
	}
	for i := range l.Items {
		if j := &l.Items[i]; j.Spec.MediaFileRef == mf.Name && !transcodeDone(j) {
			return j, nil
		}
	}
	return nil, nil
}

func transcodeDone(j *transcodev1alpha1.TranscodeJob) bool {
	switch j.Status.Phase {
	case transcodev1alpha1.TranscodeJobPhaseSucceeded, transcodev1alpha1.TranscodeJobPhaseFailed, transcodev1alpha1.TranscodeJobPhaseSkipped:
		return true
	}
	return false
}

// dispatched reports whether a TranscodeJob's task is out to a worker.
func dispatched(j *transcodev1alpha1.TranscodeJob) bool {
	switch j.Status.Phase {
	case transcodev1alpha1.TranscodeJobPhaseQueued, transcodev1alpha1.TranscodeJobPhaseRunning, transcodev1alpha1.TranscodeJobPhaseVerifying:
		return true
	}
	return false
}

// running counts the namespace's graft Jobs that have not finished.
func (r *Reconciler) running(ctx context.Context, ns string) (int, error) {
	var l batchv1.JobList
	if err := r.List(ctx, &l, ctrl.InNamespace(ns), ctrl.HasLabels{LabelGraft}); err != nil {
		return 0, fmt.Errorf("audiograft: list graft Jobs: %w", err)
	}
	n := 0
	for i := range l.Items {
		if !finished(&l.Items[i]) {
			n++
		}
	}
	return n, nil
}

func finished(j *batchv1.Job) bool {
	for _, c := range j.Status.Conditions {
		if (c.Type == batchv1.JobComplete || c.Type == batchv1.JobFailed) && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func (r *Reconciler) wait(ctx context.Context, g *transcodev1alpha1.AudioGraft, st transcodev1alpha1.AudioGraftStatus, reason string, after time.Duration) (reconcile.Result, error) {
	if st.Phase != transcodev1alpha1.AudioGraftFailed || reason != ReasonFileUnprobed {
		st.Phase, st.Reason, st.Message = transcodev1alpha1.AudioGraftWaiting, reason, ""
	}
	return reconcile.Result{RequeueAfter: after}, r.apply(ctx, g, st)
}

// start creates the graft Job for mf.
func (r *Reconciler) start(ctx context.Context, g *transcodev1alpha1.AudioGraft, st transcodev1alpha1.AudioGraftStatus, mf *catalogv1alpha1.MediaFile) (reconcile.Result, error) {
	var folders catalogv1alpha1.RootFolderList
	if err := r.List(ctx, &folders, ctrl.InNamespace(g.Namespace)); err != nil {
		return reconcile.Result{}, fmt.Errorf("audiograft: list RootFolders: %w", err)
	}
	rf := jobspec.RootFolderFor(folders.Items, mf.Spec.Path)
	if rf == nil {
		st.Phase, st.Reason, st.Message = transcodev1alpha1.AudioGraftFailed, grafttask.ReasonInvalidTask, "the file "+mf.Spec.Path+" is under no RootFolder"
		st.MediaFileRef, st.TargetProbeHash = mf.Name, mf.Status.ProbeHash
		return reconcile.Result{}, r.apply(ctx, g, st)
	}
	l := missing(g, mf)
	task := grafttask.Task{
		Graft: g.Namespace + "/" + g.Name, Target: mf.Spec.Path, TargetProbeHash: mf.Status.ProbeHash,
		Root: rf.Spec.Path, Donor: donorPath(g), Language: l, Anchor: g.Spec.Anchor,
		Default:    g.Spec.Default != "" && base(g.Spec.Default) == base(l),
		RecycleBin: jobspec.RecycleBinOf(rf),
	}
	task.Languages = g.Spec.Languages
	// The TranscodeProfile and this controller wake on one MediaFile event
	// and each checked the other through its cache: look for a transcode
	// once more, live, before the graft starts.
	if busy, err := r.transcodingLive(ctx, mf); err != nil {
		return reconcile.Result{}, err
	} else if busy {
		return r.wait(ctx, g, st, ReasonTranscodeRunning, 5*time.Minute)
	}
	return r.create(ctx, g, st, task, mf, "")
}

// create starts a graft or reduce Job for task.
func (r *Reconciler) create(ctx context.Context, g *transcodev1alpha1.AudioGraft, st transcodev1alpha1.AudioGraftStatus,
	task grafttask.Task, mf *catalogv1alpha1.MediaFile, reason string,
) (reconcile.Result, error) {
	l := task.Language
	job, err := r.job(g, task)
	if err != nil {
		return reconcile.Result{}, err
	}
	if err := r.Create(ctx, job); err != nil && !apierrors.IsAlreadyExists(err) {
		return reconcile.Result{}, fmt.Errorf("audiograft: create Job: %w", err)
	}
	now := metav1.NewTime(r.now())
	st.Phase, st.Reason, st.Message = transcodev1alpha1.AudioGraftPending, reason, ""
	st.JobName, st.StartedAt, st.CompletedAt = job.Name, &now, nil
	if mf != nil {
		st.MediaFileRef, st.TargetProbeHash = mf.Name, mf.Status.ProbeHash
	}
	logging.FromContext(ctx).Info("audiograft: started", "job", job.Name, "mode", cmp.Or(task.Mode, "graft"), "language", l)
	return reconcile.Result{}, r.apply(ctx, g, st)
}

// reducedPath is where a donor's audio lives once reduced: the donor itself
// when it is Matroska audio already.
func reducedPath(donor string) string {
	if strings.EqualFold(filepath.Ext(donor), ".mka") {
		return donor
	}
	return strings.TrimSuffix(donor, filepath.Ext(donor)) + ".mka"
}

// reduce runs the reduce Job a donor not yet reduced gets (done: this
// reconcile is over). A donor already Matroska audio needs none.
func (r *Reconciler) reduce(ctx context.Context, g *transcodev1alpha1.AudioGraft, st *transcodev1alpha1.AudioGraftStatus) (reconcile.Result, bool, error) {
	want := reducedPath(g.Spec.DonorPath)
	if st.DonorAudioPath == want {
		return reconcile.Result{}, false, nil
	}
	if want == g.Spec.DonorPath {
		st.DonorAudioPath = want
		return reconcile.Result{}, false, nil
	}
	// A reduce that failed for this donor: the donor's fault is final, and
	// anything else waits failureBackoff.
	if st.Phase == transcodev1alpha1.AudioGraftFailed && failedGeneration(g) == g.Generation && st.TargetProbeHash == "" {
		if DonorFault(st.Reason) {
			return reconcile.Result{}, true, nil
		}
		if st.CompletedAt != nil {
			if wait := st.CompletedAt.Add(failureBackoff).Sub(r.now()); wait > 0 {
				return reconcile.Result{RequeueAfter: wait}, true, nil
			}
		}
	}
	running, err := r.running(ctx, g.Namespace)
	if err != nil {
		return reconcile.Result{}, true, err
	}
	if running >= cmp.Or(r.Concurrency, DefaultConcurrency) {
		res, err := r.wait(ctx, g, *st, ReasonWaitingForSlot, time.Minute)
		return res, true, err
	}
	var folders catalogv1alpha1.RootFolderList
	if err := r.List(ctx, &folders, ctrl.InNamespace(g.Namespace)); err != nil {
		return reconcile.Result{}, true, fmt.Errorf("audiograft: list RootFolders: %w", err)
	}
	rf := jobspec.RootFolderFor(folders.Items, g.Spec.DonorPath)
	if rf == nil {
		st.Phase, st.Reason, st.Message = transcodev1alpha1.AudioGraftFailed, grafttask.ReasonInvalidTask, "the donor "+g.Spec.DonorPath+" is under no RootFolder"
		return reconcile.Result{}, true, r.apply(ctx, g, *st)
	}
	st.TargetProbeHash = "" // a reduce's failure is the donor's, of no file
	task := grafttask.Task{
		Graft: g.Namespace + "/" + g.Name, Mode: grafttask.ModeReduce, Root: rf.Spec.Path, Donor: g.Spec.DonorPath,
		Language: g.Spec.Languages[0], Languages: g.Spec.Languages, Anchor: g.Spec.Anchor,
	}
	res, err := r.create(ctx, g, *st, task, nil, graftstate.ReasonReducing)
	return res, true, err
}

// joined records g riding along with tj, which the dispatcher attached it
// to.
func (r *Reconciler) joined(ctx context.Context, g *transcodev1alpha1.AudioGraft, st transcodev1alpha1.AudioGraftStatus,
	tj *transcodev1alpha1.TranscodeJob, mf *catalogv1alpha1.MediaFile,
) (reconcile.Result, error) {
	now := metav1.NewTime(r.now())
	st.Phase, st.Reason, st.Message = transcodev1alpha1.AudioGraftRunning, ReasonJoinedTranscode, "grafting in the same pass as TranscodeJob "+tj.Name
	st.JobName, st.MediaFileRef, st.TargetProbeHash = graftstate.JoinedPrefix+tj.Name, mf.Name, mf.Status.ProbeHash
	st.StartedAt, st.CompletedAt = &now, nil
	logging.FromContext(ctx).Info("audiograft: joined a transcode", "transcodeJob", tj.Name)
	return reconcile.Result{}, r.apply(ctx, g, st)
}

// followJoined reads the TranscodeJob g rides along with: done is false
// while it runs, true once its result is in st -- or once the transcode
// ended without one (it ran without the graft, or failed), which falls back
// to a graft of g's own.
func (r *Reconciler) followJoined(ctx context.Context, g *transcodev1alpha1.AudioGraft, st *transcodev1alpha1.AudioGraftStatus) (reconcile.Result, bool, error) {
	if st.Phase.Terminal() {
		return reconcile.Result{}, true, nil
	}
	name := graftstate.Joined(g)
	unjoin := func(why string) (reconcile.Result, bool, error) {
		logging.FromContext(ctx).Info("audiograft: the joined transcode carried no graft; grafting alone", "transcodeJob", name, "why", why)
		st.JobName, st.Phase, st.Reason, st.Message = "", transcodev1alpha1.AudioGraftWaiting, ReasonJobLost, why
		return reconcile.Result{}, true, nil
	}
	var tj transcodev1alpha1.TranscodeJob
	if err := r.Get(ctx, types.NamespacedName{Namespace: g.Namespace, Name: name}, &tj); err != nil {
		if apierrors.IsNotFound(err) {
			return unjoin("the TranscodeJob is gone")
		}
		return reconcile.Result{}, false, err
	}
	jg := tj.Status.Graft
	if jg == nil || jg.AudioGraft != g.Name {
		return unjoin("the TranscodeJob ran without this graft")
	}
	switch jg.Phase {
	case transcodev1alpha1.GraftFailed:
	case transcodev1alpha1.GraftSucceeded:
		if tj.Status.Phase != transcodev1alpha1.TranscodeJobPhaseSucceeded {
			if transcodeDone(&tj) {
				return unjoin("the transcode did not swap")
			}
			return reconcile.Result{}, false, nil // the swap is reported with the transcode's success
		}
	default: // Joined
		if transcodeDone(&tj) {
			return unjoin("the transcode ended before its graft reported")
		}
		return reconcile.Result{}, false, nil
	}
	record(st, resultOf(jg), g.Spec.Release, r.now())
	if err := r.apply(ctx, g, *st); err != nil {
		return reconcile.Result{}, false, err
	}
	logging.FromContext(ctx).Info("audiograft: the joined graft finished", "transcodeJob", name, "phase", jg.Phase, "reason", jg.Reason)
	return reconcile.Result{}, false, nil
}

// resultOf is a TranscodeJob's status.graft as a graft result.
func resultOf(g *transcodev1alpha1.GraftResult) grafttask.Result {
	res := grafttask.Result{
		Phase: grafttask.PhaseFailed, Reason: g.Reason, Message: g.Message, RateName: g.RateName, RateMicros: g.RateMicros,
		RateMarginMilli: g.RateMarginMilli, CoveragePercent: g.CoveragePercent, ResidualMillis: g.ResidualMillis,
		Within80Percent: g.Within80Percent, GraftTag: g.GraftTag,
	}
	if g.Phase == transcodev1alpha1.GraftSucceeded {
		res.Phase = grafttask.PhaseSucceeded
	}
	for _, s := range g.Segments {
		res.Segments = append(res.Segments, grafttask.Segment(s))
	}
	return res
}

// JoinTask is the graft a TranscodeJob of mf carries for g, the dispatcher
// asks at dispatch (phase 4 addendum): when g's donor is reduced, mf still
// lacks one of its languages, no graft Job of g's own runs, and no
// donor-fault failure stands for this donor and this file.
func JoinTask(g *transcodev1alpha1.AudioGraft, mf *catalogv1alpha1.MediaFile, folders []catalogv1alpha1.RootFolder) (grafttask.Task, bool) {
	if g.Status.DonorAudioPath == "" || graftstate.Standalone(g) || mf.Status.MediaInfo == nil {
		return grafttask.Task{}, false
	}
	if g.Status.Phase == transcodev1alpha1.AudioGraftFailed && DonorFault(g.Status.Reason) &&
		g.Status.TargetProbeHash == mf.Status.ProbeHash && g.Status.ObservedGeneration == g.Generation {
		return grafttask.Task{}, false
	}
	l := missing(g, mf)
	rf := jobspec.RootFolderFor(folders, mf.Spec.Path)
	if l == "" || rf == nil {
		return grafttask.Task{}, false
	}
	return grafttask.Task{
		Graft: g.Namespace + "/" + g.Name, Target: mf.Spec.Path, TargetProbeHash: mf.Status.ProbeHash,
		Root: rf.Spec.Path, Donor: g.Status.DonorAudioPath, Language: l, Languages: g.Spec.Languages, Anchor: g.Spec.Anchor,
		Default:    g.Spec.Default != "" && base(g.Spec.Default) == base(l),
		RecycleBin: jobspec.RecycleBinOf(rf),
	}, true
}

// donorPath is the donor's audio as a graft reads it: the .mka the reduce
// left, else the donor as importarr placed it.
func donorPath(g *transcodev1alpha1.AudioGraft) string {
	return cmp.Or(g.Status.DonorAudioPath, g.Spec.DonorPath)
}

// job renders the graft Job: the cpu pool's pod running --graft-task.
func (r *Reconciler) job(g *transcodev1alpha1.AudioGraft, t grafttask.Task) (*batchv1.Job, error) {
	b, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	tmpl := pool.Template(&transcodev1alpha1.TranscodeProfile{}, transcodev1alpha1.HardwareCPU, r.Pool)
	c := &tmpl.Spec.Containers[0]
	c.Args = append([]string{"--data-dir", cmp.Or(r.Pool.DataDir, pool.DefaultDataDir), "--graft-task", string(b)}, r.Pool.ExtraArgs...)
	c.Env = slices.DeleteFunc(c.Env, func(e corev1.EnvVar) bool {
		return e.Name == pool.EnvProfileUID || e.Name == pool.EnvClass || e.Name == "NATS_URL"
	})
	c.TerminationMessagePolicy = corev1.TerminationMessageReadFile
	tmpl.Labels[LabelGraft] = g.Name
	tmpl.Labels["app.kubernetes.io/component"] = "squasharr-graft"
	name := k8s.ChildName(g.Name, t.Mode, t.TargetProbeHash, fmt.Sprint(g.Generation))
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: g.Namespace,
			Labels: map[string]string{
				pool.LabelManagedBy: pool.ManagedByValue, LabelGraft: g.Name,
				"app.kubernetes.io/name": "clustarr", "app.kubernetes.io/component": "squasharr-graft",
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr.To[int32](0),
			ActiveDeadlineSeconds:   ptr.To(int64(jobDeadline.Seconds())),
			TTLSecondsAfterFinished: ptr.To[int32](int32((24 * time.Hour).Seconds())),
			Template:                tmpl,
		},
	}
	if err := controllerutil.SetControllerReference(g, job, r.Scheme()); err != nil {
		return nil, err
	}
	return job, nil
}

// followJob reads the graft's Job: done is false while it runs (the status
// says Pending or Running), true once its result is in st -- or once the
// Job is gone without one, which is retried.
func (r *Reconciler) followJob(ctx context.Context, g *transcodev1alpha1.AudioGraft, st *transcodev1alpha1.AudioGraftStatus) (reconcile.Result, bool, error) {
	if st.Phase.Terminal() {
		return reconcile.Result{}, true, nil
	}
	var job batchv1.Job
	err := r.Get(ctx, types.NamespacedName{Namespace: g.Namespace, Name: st.JobName}, &job)
	if apierrors.IsNotFound(err) {
		// The Job is deleted once its result is written, and the delete's
		// event can arrive before the cache has the write: the live object
		// decides whether the result is in.
		var live transcodev1alpha1.AudioGraft
		if err := r.APIReader.Get(ctx, ctrl.ObjectKeyFromObject(g), &live); err != nil {
			return reconcile.Result{}, false, ctrl.IgnoreNotFound(err)
		}
		if live.Status.JobName != st.JobName || live.Status.Phase.Terminal() {
			return reconcile.Result{}, false, nil // a later reconcile reads the recorded result
		}
		logging.FromContext(ctx).Info("audiograft: the graft Job is gone without a result; starting again", "job", st.JobName)
		st.JobName, st.Phase, st.Reason = "", transcodev1alpha1.AudioGraftWaiting, ReasonJobLost
		return reconcile.Result{}, true, nil
	}
	if err != nil {
		return reconcile.Result{}, false, err
	}
	if !finished(&job) {
		phase := transcodev1alpha1.AudioGraftPending
		if job.Status.Active > 0 && job.Status.Ready != nil && *job.Status.Ready > 0 {
			phase = transcodev1alpha1.AudioGraftRunning
		}
		if st.Phase != phase {
			st.Phase = phase
			return reconcile.Result{RequeueAfter: time.Minute}, false, r.apply(ctx, g, *st)
		}
		return reconcile.Result{RequeueAfter: time.Minute}, false, nil
	}
	res, err := r.result(ctx, &job)
	if err != nil {
		return reconcile.Result{}, false, err
	}
	if res.Phase == grafttask.PhaseSucceeded && res.Reason == grafttask.ReasonReduced {
		st.DonorAudioPath, st.JobName = res.DonorAudio, ""
		st.Phase, st.Reason, st.Message = transcodev1alpha1.AudioGraftWaiting, ReasonReduced, "the donor is reduced to its audio"
	} else {
		record(st, res, g.Spec.Release, r.now())
	}
	if err := r.apply(ctx, g, *st); err != nil {
		return reconcile.Result{}, false, err
	}
	if err := r.Delete(ctx, &job, ctrl.PropagationPolicy(metav1.DeletePropagationBackground)); ctrl.IgnoreNotFound(err) != nil {
		return reconcile.Result{}, false, fmt.Errorf("audiograft: delete Job %s: %w", job.Name, err)
	}
	logging.FromContext(ctx).Info("audiograft: finished", "job", job.Name, "phase", res.Phase, "reason", res.Reason, "message", res.Message)
	return reconcile.Result{}, false, nil
}

// result is the Job's pod's termination message, or a failure naming the
// pod's own end when it left none (killed, out of memory, never started).
func (r *Reconciler) result(ctx context.Context, job *batchv1.Job) (grafttask.Result, error) {
	var pods corev1.PodList
	if err := r.APIReader.List(ctx, &pods, ctrl.InNamespace(job.Namespace), ctrl.MatchingLabels{"batch.kubernetes.io/job-name": job.Name}); err != nil {
		return grafttask.Result{}, fmt.Errorf("audiograft: list the graft Job's pods: %w", err)
	}
	why := "the graft Job failed and left no result"
	for _, p := range pods.Items {
		for _, cs := range p.Status.ContainerStatuses {
			term := cs.State.Terminated
			if term == nil {
				continue
			}
			if term.Message != "" {
				if res, err := grafttask.Decode([]byte(term.Message)); err == nil {
					return res, nil
				}
			}
			why = fmt.Sprintf("the graft pod ended %s (exit %d) and left no result", cmp.Or(term.Reason, "abnormally"), term.ExitCode)
		}
	}
	for _, c := range job.Status.Conditions {
		if c.Type == batchv1.JobFailed && c.Status == corev1.ConditionTrue && c.Message != "" {
			why += ": " + c.Message
		}
	}
	return grafttask.Failed(ReasonJobFailed, "%s", why), nil
}

// failureBackoff is how long a graft that failed through no fault of its
// donor waits before it is tried again.
const failureBackoff = 15 * time.Minute

// DonorFault reports whether a failed graft's reason is the donor's: it
// would not align, mux or verify, or lacks the language. Only such a
// failure rejects the donor for the item.
func DonorFault(reason string) bool {
	switch reason {
	case grafttask.ReasonAlignmentRejected, grafttask.ReasonVerifyFailed, grafttask.ReasonMuxFailed, grafttask.ReasonDonorLacksLanguage:
		return true
	}
	return false
}

// record folds a graft's result into st. A failure that is the donor's
// fault rejects its release for this item (spec §9).
func record(st *transcodev1alpha1.AudioGraftStatus, res grafttask.Result, release string, at time.Time) {
	now := metav1.NewTime(at)
	st.CompletedAt = &now
	st.Phase = transcodev1alpha1.AudioGraftSucceeded
	if res.Phase != grafttask.PhaseSucceeded {
		st.Phase = transcodev1alpha1.AudioGraftFailed
		if release != "" && DonorFault(res.Reason) && !slices.Contains(st.RejectedReleases, release) {
			st.RejectedReleases = append(st.RejectedReleases, release)
			if n := len(st.RejectedReleases); n > 16 {
				st.RejectedReleases = st.RejectedReleases[n-16:]
			}
		}
	}
	st.Reason, st.Message = res.Reason, grafttask.Clamp(res.Message)
	st.RateName, st.RateMicros, st.RateMarginMilli, st.CoveragePercent = res.RateName, res.RateMicros, res.RateMarginMilli, res.CoveragePercent
	st.ResidualMillis, st.Within80Percent = res.ResidualMillis, res.Within80Percent
	if res.GraftTag != "" {
		st.GraftTag = res.GraftTag
	}
	st.Segments = nil
	for i, s := range res.Segments {
		if i == 16 {
			break
		}
		st.Segments = append(st.Segments, transcodev1alpha1.AudioGraftSegment(s))
	}
}

// apply declares the whole of squasharr's AudioGraft status.
func (r *Reconciler) apply(ctx context.Context, g *transcodev1alpha1.AudioGraft, st transcodev1alpha1.AudioGraftStatus) error {
	ac := transcodeac.AudioGraftStatus().WithObservedGeneration(st.ObservedGeneration)
	if st.Phase != "" {
		ac = ac.WithPhase(st.Phase)
	}
	for _, s := range []struct {
		v   string
		set func(string) *transcodeac.AudioGraftStatusApplyConfiguration
	}{
		{st.Reason, ac.WithReason},
		{st.Message, ac.WithMessage},
		{st.MediaFileRef, ac.WithMediaFileRef},
		{st.TargetProbeHash, ac.WithTargetProbeHash},
		{st.JobName, ac.WithJobName},
		{st.DonorAudioPath, ac.WithDonorAudioPath},
		{st.RateName, ac.WithRateName},
		{st.GraftTag, ac.WithGraftTag},
	} {
		if s.v != "" {
			s.set(s.v)
		}
	}
	if st.RateMicros != 0 {
		ac = ac.WithRateMicros(st.RateMicros)
	}
	if st.RateMarginMilli != 0 {
		ac = ac.WithRateMarginMilli(st.RateMarginMilli)
	}
	if st.CoveragePercent != 0 {
		ac = ac.WithCoveragePercent(st.CoveragePercent)
	}
	if st.ResidualMillis != 0 {
		ac = ac.WithResidualMillis(st.ResidualMillis)
	}
	if st.Within80Percent != 0 {
		ac = ac.WithWithin80Percent(st.Within80Percent)
	}
	for _, s := range st.Segments {
		ac = ac.WithSegments(transcodeac.AudioGraftSegment().WithDonorStartMillis(s.DonorStartMillis).
			WithTargetStartMillis(s.TargetStartMillis).WithLengthMillis(s.LengthMillis))
	}
	if st.StartedAt != nil {
		ac = ac.WithStartedAt(*st.StartedAt)
	}
	if st.CompletedAt != nil {
		ac = ac.WithCompletedAt(*st.CompletedAt)
	}
	if len(st.RejectedReleases) > 0 {
		ac = ac.WithRejectedReleases(st.RejectedReleases...)
	}
	_, err := k8s.PatchStatus(ctx, r.Client, k8s.ManagerSquasharr, transcodeac.AudioGraft(g.Name, g.Namespace).WithStatus(ac))
	return err
}
