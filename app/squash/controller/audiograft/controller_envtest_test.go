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

package audiograft_test

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	batchac "k8s.io/client-go/applyconfigurations/batch/v1"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	transcodeac "github.com/mediactl/clustarr/api/applyconfiguration/transcode/transcode/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/app/squash/controller/audiograft"
	"github.com/mediactl/clustarr/app/squash/controller/pool"
	"github.com/mediactl/clustarr/app/squash/grafttask"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func newClient(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return c
}

type fixture struct {
	t   *testing.T
	ctx context.Context
	c   client.Client
	ns  string
	r   *audiograft.Reconciler
}

func newFixture(t *testing.T, ns string) *fixture {
	c := newClient(t)
	ctx := context.Background()
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "tv", Namespace: ns},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: "/data/media/tv", Kind: catalogv1alpha1.RootFolderKindSeries},
	}))
	return &fixture{t: t, ctx: ctx, c: c, ns: ns, r: &audiograft.Reconciler{
		Client: c, APIReader: c, Pool: pool.Config{Namespace: ns, Image: "transcoder:test", DataClaimName: "data"},
		Clock: func() time.Time { return time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC) },
	}}
}

// episode creates an Episode whose file is probed with audio langs, and
// returns the file.
func (f *fixture) episode(name string, langs ...string) *catalogv1alpha1.MediaFile {
	t := f.t
	require.NoError(t, f.c.Create(f.ctx, &catalogv1alpha1.Episode{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.ns},
		Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "monster", SeasonNumber: 1, EpisodeNumber: 2},
	}))
	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: name + "-file", Namespace: f.ns},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: name},
			Path:     "/data/media/tv/Monster/Season 01/" + name + ".mkv",
		},
	}
	require.NoError(t, f.c.Create(f.ctx, mf))
	var audio []commonv1.AudioStream
	for _, l := range langs {
		audio = append(audio, commonv1.AudioStream{Language: l, Codec: "aac", Channels: 2})
	}
	_, err := k8s.PatchStatus(f.ctx, f.c, k8s.ManagerCatalogarr, catalogac.MediaFile(mf.Name, f.ns).WithStatus(
		catalogac.MediaFileStatus().WithProbeHash("probe-"+name).WithMediaInfo(commonv1.MediaInfo{Audio: audio})))
	require.NoError(t, err)
	_, err = k8s.PatchStatus(f.ctx, f.c, k8s.ManagerCatalogarr, catalogac.Episode(name, f.ns).WithStatus(
		catalogac.EpisodeStatus().WithFileRef(mf.Name)))
	require.NoError(t, err)
	require.NoError(t, f.c.Get(f.ctx, client.ObjectKeyFromObject(mf), mf))
	return mf
}

func (f *fixture) graft(item string) *transcodev1alpha1.AudioGraft {
	g := &transcodev1alpha1.AudioGraft{
		ObjectMeta: metav1.ObjectMeta{Name: item + "-audiograft", Namespace: f.ns},
		Spec: transcodev1alpha1.AudioGraftSpec{
			ItemRef:   commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: item},
			DonorPath: "/data/media/tv/.clustarr/donors/uid/" + item + ".mkv",
			Languages: []string{"en"}, Anchor: "ja", Default: "en",
			Release: "Monster.s01e02.Downfall.DVDRip.480p.x264.AAC.DL-BoB",
		},
	}
	require.NoError(f.t, f.c.Create(f.ctx, g))
	return g
}

func (f *fixture) reconcile(g *transcodev1alpha1.AudioGraft) *transcodev1alpha1.AudioGraft {
	_, err := f.r.Reconcile(f.ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(g)})
	require.NoError(f.t, err)
	var got transcodev1alpha1.AudioGraft
	require.NoError(f.t, f.c.Get(f.ctx, client.ObjectKeyFromObject(g), &got))
	return &got
}

func (f *fixture) jobs() []batchv1.Job {
	var l batchv1.JobList
	require.NoError(f.t, f.c.List(f.ctx, &l, client.InNamespace(f.ns), client.HasLabels{audiograft.LabelGraft}))
	return l.Items
}

// finish plays the Job controller: the Job's pod ends with message as its
// termination message, and the Job reads Complete (or Failed).
func (f *fixture) finish(job *batchv1.Job, res grafttask.Result) {
	t := f.t
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: job.Name + "-pod", Namespace: f.ns, Labels: map[string]string{"batch.kubernetes.io/job-name": job.Name}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "worker", Image: "transcoder:test"}}},
	}
	require.NoError(t, f.c.Create(f.ctx, pod))
	exit := int32(0)
	if res.Phase != grafttask.PhaseSucceeded {
		exit = 1
	}
	_, err := k8s.PatchStatus(f.ctx, f.c, k8s.ManagerSquasharr, corev1ac.Pod(pod.Name, f.ns).WithStatus(corev1ac.PodStatus().
		WithPhase(corev1.PodSucceeded).WithContainerStatuses(corev1ac.ContainerStatus().
		WithName("worker").WithImage("transcoder:test").WithImageID("sha256:0").WithReady(false).WithRestartCount(0).
		WithState(corev1ac.ContainerState().WithTerminated(corev1ac.ContainerStateTerminated().
			WithExitCode(exit).WithMessage(string(res.Encode())).
			WithStartedAt(metav1.Now()).WithFinishedAt(metav1.Now()))))))
	require.NoError(t, err)
	now := metav1.Now()
	st := batchac.JobStatus().WithStartTime(now)
	if exit == 0 {
		st = st.WithSucceeded(1).WithCompletionTime(now).WithConditions(
			batchac.JobCondition().WithType(batchv1.JobSuccessCriteriaMet).WithStatus(corev1.ConditionTrue).WithLastTransitionTime(now),
			batchac.JobCondition().WithType(batchv1.JobComplete).WithStatus(corev1.ConditionTrue).WithLastTransitionTime(now))
	} else {
		st = st.WithFailed(1).WithConditions(
			batchac.JobCondition().WithType(batchv1.JobFailureTarget).WithStatus(corev1.ConditionTrue).WithLastTransitionTime(now),
			batchac.JobCondition().WithType(batchv1.JobFailed).WithStatus(corev1.ConditionTrue).WithLastTransitionTime(now))
	}
	_, err = k8s.PatchStatus(f.ctx, f.c, k8s.ManagerSquasharr, batchac.Job(job.Name, f.ns).WithStatus(st))
	require.NoError(t, err)
}

func taskOf(t *testing.T, job batchv1.Job) grafttask.Task {
	args := job.Spec.Template.Spec.Containers[0].Args
	i := slices.Index(args, "--graft-task")
	require.GreaterOrEqual(t, i, 0, "%v", args)
	var task grafttask.Task
	require.NoError(t, json.Unmarshal([]byte(args[i+1]), &task))
	return task
}

func TestAFileMissingTheLanguageGetsAGraftJob(t *testing.T) {
	f := newFixture(t, "graft-starts")
	mf := f.episode("monster-s01e02", "jpn")
	g := f.reconcile(f.graft("monster-s01e02"))
	assert.Equal(t, transcodev1alpha1.AudioGraftPending, g.Status.Phase)
	assert.Equal(t, mf.Name, g.Status.MediaFileRef)
	assert.Equal(t, mf.Status.ProbeHash, g.Status.TargetProbeHash)

	jobs := f.jobs()
	require.Len(t, jobs, 1)
	j := jobs[0]
	assert.Equal(t, g.Status.JobName, j.Name)
	assert.Equal(t, int32(0), *j.Spec.BackoffLimit)
	require.Len(t, j.OwnerReferences, 1)
	assert.Equal(t, g.UID, j.OwnerReferences[0].UID)
	assert.Equal(t, pool.ManagedByValue, j.Labels[pool.LabelManagedBy], "the Job is in squasharr's Job cache")
	assert.NotContains(t, j.Labels, pool.LabelProfile, "and is no pool")
	pod := j.Spec.Template.Spec
	assert.Equal(t, "transcoder:test", pod.Containers[0].Image)
	assert.NotNil(t, pod.SecurityContext, "the pool's pod security")
	assert.False(t, *pod.AutomountServiceAccountToken)
	for _, e := range pod.Containers[0].Env {
		assert.NotEqual(t, "NATS_URL", e.Name, "a graft pod needs no bus")
	}
	task := taskOf(t, j)
	assert.Equal(t, mf.Spec.Path, task.Target)
	assert.Equal(t, mf.Status.ProbeHash, task.TargetProbeHash)
	assert.Equal(t, "/data/media/tv", task.Root)
	assert.Equal(t, "en", task.Language)
	assert.Equal(t, "ja", task.Anchor)
	assert.True(t, task.Default)
	assert.Equal(t, "/data/.recycle", task.RecycleBin)

	f.reconcile(g)
	assert.Len(t, f.jobs(), 1, "a second pass makes no second Job")
}

func TestAGraftsResultLandsInStatus(t *testing.T) {
	f := newFixture(t, "graft-result")
	f.episode("monster-s01e02", "jpn")
	g := f.reconcile(f.graft("monster-s01e02"))
	job := f.jobs()[0]
	f.finish(&job, grafttask.Result{
		Phase: grafttask.PhaseSucceeded, Reason: grafttask.ReasonGrafted, RateName: "1", RateMicros: 1000000,
		RateMarginMilli: 7310, CoveragePercent: 96, ResidualMillis: 0, Within80Percent: 100, GraftTag: "61d29fba5193",
		Segments: []grafttask.Segment{{DonorStartMillis: 0, TargetStartMillis: 1000, LengthMillis: 1434580}},
	})
	g = f.reconcile(g)
	assert.Equal(t, transcodev1alpha1.AudioGraftSucceeded, g.Status.Phase)
	assert.Equal(t, grafttask.ReasonGrafted, g.Status.Reason)
	assert.Equal(t, "1", g.Status.RateName)
	assert.EqualValues(t, 96, g.Status.CoveragePercent)
	assert.Equal(t, "61d29fba5193", g.Status.GraftTag)
	require.Len(t, g.Status.Segments, 1)
	assert.EqualValues(t, 1000, g.Status.Segments[0].TargetStartMillis)
	assert.NotNil(t, g.Status.CompletedAt)

	var gone batchv1.Job
	err := f.c.Get(f.ctx, client.ObjectKeyFromObject(&job), &gone)
	assert.True(t, apierrors.IsNotFound(err) || gone.DeletionTimestamp != nil, "the finished Job is deleted")

	g = f.reconcile(g)
	assert.Equal(t, transcodev1alpha1.AudioGraftSucceeded, g.Status.Phase, "it waits for the file's re-probe")
	assert.Len(t, f.jobs(), 0+boolToInt(gone.DeletionTimestamp != nil), "and starts nothing meanwhile")
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestAFailedGraftRejectsTheDonorAndStaysFailed(t *testing.T) {
	f := newFixture(t, "graft-failed")
	f.episode("monster-s01e02", "jpn")
	g := f.reconcile(f.graft("monster-s01e02"))
	job := f.jobs()[0]
	f.finish(&job, grafttask.Failed(grafttask.ReasonAlignmentRejected, "audioalign: confident windows cover 12%% of the target"))
	g = f.reconcile(g)
	assert.Equal(t, transcodev1alpha1.AudioGraftFailed, g.Status.Phase)
	assert.Equal(t, grafttask.ReasonAlignmentRejected, g.Status.Reason)
	assert.Equal(t, []string{g.Spec.Release}, g.Status.RejectedReleases)

	// Delete-propagation is the garbage collector's, which envtest lacks:
	// clear the Job so only a new one would show.
	require.NoError(t, client.IgnoreNotFound(f.c.Delete(f.ctx, &job)))
	g = f.reconcile(g)
	assert.Equal(t, transcodev1alpha1.AudioGraftFailed, g.Status.Phase, "the same donor and file are not retried")
	assert.Empty(t, f.jobs())

	// A new donor (a new generation) is tried.
	g.Spec.DonorPath = "/data/media/tv/.clustarr/donors/uid/monster-s01e02-2.mkv"
	g.Spec.Release = "Monster.S01E02.DVD.Dual.Audio"
	require.NoError(t, f.c.Update(f.ctx, g))
	g = f.reconcile(g)
	assert.Equal(t, transcodev1alpha1.AudioGraftPending, g.Status.Phase)
	assert.Len(t, f.jobs(), 1)
	assert.Equal(t, []string{"Monster.s01e02.Downfall.DVDRip.480p.x264.AAC.DL-BoB"}, g.Status.RejectedReleases, "the rejection is kept")
}

func TestAPodThatLeftNoResultFailsTheGraft(t *testing.T) {
	f := newFixture(t, "graft-noresult")
	f.episode("monster-s01e02", "jpn")
	g := f.reconcile(f.graft("monster-s01e02"))
	job := f.jobs()[0]
	f.finish(&job, grafttask.Result{Phase: "garbage"})
	g = f.reconcile(g)
	assert.Equal(t, transcodev1alpha1.AudioGraftFailed, g.Status.Phase)
	assert.Equal(t, audiograft.ReasonJobFailed, g.Status.Reason)
}

func TestAnOpenTranscodeHoldsTheGraft(t *testing.T) {
	f := newFixture(t, "graft-transcoding")
	mf := f.episode("monster-s01e02", "jpn")
	require.NoError(t, f.c.Create(f.ctx, &transcodev1alpha1.TranscodeJob{
		ObjectMeta: metav1.ObjectMeta{Name: "tj", Namespace: f.ns},
		Spec: transcodev1alpha1.TranscodeJobSpec{
			MediaFileRef: mf.Name, ProfileRef: "default",
			SourcePath: mf.Spec.Path, SourceProbeHash: mf.Status.ProbeHash,
		},
	}))
	g := f.reconcile(f.graft("monster-s01e02"))
	assert.Equal(t, transcodev1alpha1.AudioGraftWaiting, g.Status.Phase)
	assert.Equal(t, audiograft.ReasonTranscodeRunning, g.Status.Reason)
	assert.Empty(t, f.jobs())
}

func TestGraftsWaitForASlot(t *testing.T) {
	f := newFixture(t, "graft-slots")
	f.r.Concurrency = 1
	f.episode("monster-s01e01", "jpn")
	f.episode("monster-s01e02", "jpn")
	first := f.reconcile(f.graft("monster-s01e01"))
	assert.Equal(t, transcodev1alpha1.AudioGraftPending, first.Status.Phase)
	second := f.reconcile(f.graft("monster-s01e02"))
	assert.Equal(t, transcodev1alpha1.AudioGraftWaiting, second.Status.Phase)
	assert.Equal(t, audiograft.ReasonWaitingForSlot, second.Status.Reason)
	assert.Len(t, f.jobs(), 1)
}

func TestAFileWithTheLanguageIsPresent(t *testing.T) {
	f := newFixture(t, "graft-present")
	f.episode("monster-s01e02", "jpn", "eng")
	g := f.reconcile(f.graft("monster-s01e02"))
	assert.Equal(t, transcodev1alpha1.AudioGraftSucceeded, g.Status.Phase)
	assert.Equal(t, grafttask.ReasonPresent, g.Status.Reason)
	assert.Empty(t, f.jobs())
}

func TestGraftingNamesTheFilesUnderAGraft(t *testing.T) {
	f := newFixture(t, "graft-grafting")
	mf := f.episode("monster-s01e02", "jpn")
	f.reconcile(f.graft("monster-s01e02"))
	set, err := audiograft.Grafting(f.ctx, f.c)
	require.NoError(t, err)
	assert.True(t, set[types.NamespacedName{Namespace: f.ns, Name: mf.Name}])

	// Only squasharr writes AudioGraft status.
	var g transcodev1alpha1.AudioGraft
	require.NoError(t, f.c.Get(f.ctx, types.NamespacedName{Namespace: f.ns, Name: "monster-s01e02-audiograft"}, &g))
	for _, m := range g.ManagedFields {
		if m.Subresource == "status" {
			assert.Equal(t, string(k8s.ManagerSquasharr), m.Manager)
		}
	}
	_ = transcodeac.AudioGraft
	_ = ptr.To[int]
}

// TestAFailureThatIsNotTheDonorsKeepsTheDonor: an evicted pod, a target that
// changed under the graft or an I/O error says nothing about the donor (spec
// §9 names alignment, mux and verify), so the donor is not rejected and the
// graft is tried again after a while.
func TestAFailureThatIsNotTheDonorsKeepsTheDonor(t *testing.T) {
	f := newFixture(t, "graft-notdonor")
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	f.r.Clock = func() time.Time { return now }
	f.episode("monster-s01e02", "jpn")
	g := f.reconcile(f.graft("monster-s01e02"))
	job := f.jobs()[0]
	f.finish(&job, grafttask.Failed(grafttask.ReasonTargetChanged, "the target changed during the graft"))
	g = f.reconcile(g)
	assert.Equal(t, transcodev1alpha1.AudioGraftFailed, g.Status.Phase)
	assert.Empty(t, g.Status.RejectedReleases, "the donor is not at fault")
	require.NoError(t, client.IgnoreNotFound(f.c.Delete(f.ctx, &job)))

	g = f.reconcile(g)
	assert.Empty(t, f.jobs(), "not retried at once")
	now = now.Add(20 * time.Minute)
	g = f.reconcile(g)
	assert.Equal(t, transcodev1alpha1.AudioGraftPending, g.Status.Phase, "retried after the backoff")
	assert.Len(t, f.jobs(), 1)
}

// hidingTranscodeJobs is a cache that has not seen a TranscodeJob yet.
type hidingTranscodeJobs struct{ client.Client }

func (h hidingTranscodeJobs) List(ctx context.Context, l client.ObjectList, opts ...client.ListOption) error {
	if _, ok := l.(*transcodev1alpha1.TranscodeJobList); ok {
		return nil
	}
	return h.Client.List(ctx, l, opts...)
}

// TestATranscodeTheCacheHasNotSeenHoldsTheGraft: the TranscodeProfile and
// the graft wake on one MediaFile event; the graft checks for a transcode
// once more, live, just before it starts (final review, Important 4).
func TestATranscodeTheCacheHasNotSeenHoldsTheGraft(t *testing.T) {
	f := newFixture(t, "graft-race")
	mf := f.episode("monster-s01e02", "jpn")
	require.NoError(t, f.c.Create(f.ctx, &transcodev1alpha1.TranscodeJob{
		ObjectMeta: metav1.ObjectMeta{Name: "tj", Namespace: f.ns},
		Spec: transcodev1alpha1.TranscodeJobSpec{
			MediaFileRef: mf.Name, ProfileRef: "default",
			SourcePath: mf.Spec.Path, SourceProbeHash: mf.Status.ProbeHash,
		},
	}))
	f.r.Client = hidingTranscodeJobs{f.c}
	g := f.reconcile(f.graft("monster-s01e02"))
	assert.Equal(t, audiograft.ReasonTranscodeRunning, g.Status.Reason)
	assert.Empty(t, f.jobs())
}

// staleGraft is a cache whose AudioGraft is from before the result was
// recorded.
type staleGraft struct {
	client.Client
	stale *transcodev1alpha1.AudioGraft
}

func (s staleGraft) Get(ctx context.Context, key client.ObjectKey, o client.Object, opts ...client.GetOption) error {
	if g, ok := o.(*transcodev1alpha1.AudioGraft); ok && key.Name == s.stale.Name {
		s.stale.DeepCopyInto(g)
		return nil
	}
	return s.Client.Get(ctx, key, o, opts...)
}

// TestAFinishedGraftIsNotRunAgainFromAStaleCache: the Job is deleted once
// its result is written; a reconcile woken by the delete may still read the
// AudioGraft from before. It must not take the Job for lost and run the
// graft again (final review, Important 5).
func TestAFinishedGraftIsNotRunAgainFromAStaleCache(t *testing.T) {
	f := newFixture(t, "graft-stale")
	f.episode("monster-s01e02", "jpn")
	g := f.reconcile(f.graft("monster-s01e02"))
	before := g.DeepCopy() // Pending, with the Job's name
	job := f.jobs()[0]
	f.finish(&job, grafttask.Result{Phase: grafttask.PhaseSucceeded, Reason: grafttask.ReasonGrafted, GraftTag: "61d29fba5193"})
	f.reconcile(g)
	require.NoError(t, client.IgnoreNotFound(f.c.Delete(f.ctx, &job)))

	f.r.Client = staleGraft{Client: f.c, stale: before}
	_, err := f.r.Reconcile(f.ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(g)})
	require.NoError(t, err)
	assert.Empty(t, f.jobs(), "no second Job")
	var got transcodev1alpha1.AudioGraft
	require.NoError(t, f.c.Get(f.ctx, client.ObjectKeyFromObject(g), &got))
	assert.Equal(t, transcodev1alpha1.AudioGraftSucceeded, got.Status.Phase)
	assert.Equal(t, "61d29fba5193", got.Status.GraftTag, "the result is not released")
}

// TestAPresentResultDoesNotPoll: a worker that found the language present
// against a probe that says otherwise is not asked again every minute; a
// change of the file wakes it.
func TestAPresentResultDoesNotPoll(t *testing.T) {
	f := newFixture(t, "graft-present-result")
	f.episode("monster-s01e02", "jpn")
	g := f.reconcile(f.graft("monster-s01e02"))
	job := f.jobs()[0]
	f.finish(&job, grafttask.Result{Phase: grafttask.PhaseSucceeded, Reason: grafttask.ReasonPresent})
	f.reconcile(g)
	res, err := f.r.Reconcile(f.ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(g)})
	require.NoError(t, err)
	assert.Zero(t, res.RequeueAfter)
}
