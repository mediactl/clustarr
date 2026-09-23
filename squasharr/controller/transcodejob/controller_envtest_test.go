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

package transcodejob_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	batchac "k8s.io/client-go/applyconfigurations/batch/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	transcodeac "github.com/mediactl/clustarr/api/applyconfiguration/transcode/transcode/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/squasharr/controller/transcodejob"
	squasharrstatus "github.com/mediactl/clustarr/squasharr/status"
)

func startEnv(t *testing.T) (*rest.Config, client.Client) {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })

	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return cfg, c
}

func newNamespace(t *testing.T, c client.Client, ns string) {
	t.Helper()
	require.NoError(t, client.IgnoreAlreadyExists(
		c.Create(context.Background(), &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})))
}

// newProfile creates a TranscodeProfile (CRD defaults fill the spec) and,
// when hash is non-empty, stands in for the E-1 controller by setting
// status.hash.
func newProfile(t *testing.T, c client.Client, name, hash string, mutate func(*transcodev1alpha1.TranscodeProfile)) *transcodev1alpha1.TranscodeProfile {
	t.Helper()
	ctx := context.Background()
	p := &transcodev1alpha1.TranscodeProfile{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if mutate != nil {
		mutate(p)
	}
	require.NoError(t, c.Create(ctx, p))
	if hash != "" {
		setProfileHash(t, c, name, hash)
	}
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: name}, p))
	return p
}

func setProfileHash(t *testing.T, c client.Client, name, hash string) {
	t.Helper()
	ctx := context.Background()
	var p transcodev1alpha1.TranscodeProfile
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: name}, &p))
	require.NoError(t, squasharrstatus.PatchProfile(ctx, c, k8s.ManagerSquasharr, &p,
		func(ac *transcodeac.TranscodeProfileStatusApplyConfiguration) { ac.WithHash(hash) }))
}

func h264Probe() commonv1.MediaInfo {
	return commonv1.MediaInfo{
		Container: "matroska", VideoCodec: "h264", VideoProfile: "High", PixelFormat: "yuv420p",
		VideoBitDepth: 8, Width: 1920, Height: 1080, FpsMilli: 23976, Hdr: commonv1.HdrFormatNone,
		RuntimeMillis: 2 * 60 * 60 * 1000,
		Audio:         []commonv1.AudioStream{{Index: 1, Codec: "ac3", Channels: 6, Language: "eng", Default: true}},
	}
}

func compliantProbe() commonv1.MediaInfo {
	mi := h264Probe()
	mi.VideoCodec, mi.VideoProfile, mi.PixelFormat, mi.VideoBitDepth = "hevc", "Main 10", "yuv420p10le", 10
	mi.Audio = []commonv1.AudioStream{{Index: 1, Codec: "aac", Channels: 2, Language: "eng", Default: true}}
	return mi
}

func dolbyVisionProbe() commonv1.MediaInfo {
	mi := compliantProbe()
	mi.Hdr = commonv1.HdrFormatDolbyVision
	mi.DoviProfile = ptr.To(int32(8))
	mi.DoviBLCompatID = ptr.To(int32(1))
	mi.Audio = []commonv1.AudioStream{{Index: 1, Codec: "truehd", Profile: "Dolby TrueHD + Dolby Atmos", Channels: 8}}
	return mi
}

func newMediaFile(t *testing.T, c client.Client, ns, name, probeHash string, mi *commonv1.MediaInfo) *catalogv1alpha1.MediaFile {
	t.Helper()
	ctx := context.Background()
	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.MediaFileSpec{
			MediaRef:  commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: name},
			Path:      "/data/movies/" + name + ".mkv",
			SizeBytes: 4 << 30,
		},
	}
	require.NoError(t, c.Create(ctx, mf))
	if mi != nil {
		setProbe(t, c, ns, name, probeHash, *mi)
	}
	return mf
}

// setProbe stands in for catalogarr's probe write.
func setProbe(t *testing.T, c client.Client, ns, name, probeHash string, mi commonv1.MediaInfo) {
	t.Helper()
	_, err := k8s.PatchStatus(context.Background(), c, k8s.ManagerCatalogarr,
		catalogac.MediaFile(name, ns).WithStatus(catalogac.MediaFileStatus().WithProbeHash(probeHash).WithMediaInfo(mi)))
	require.NoError(t, err)
}

func newTJ(t *testing.T, c client.Client, ns, name, mediaFile, profile, probeHash string, mutate func(*transcodev1alpha1.TranscodeJob)) *transcodev1alpha1.TranscodeJob {
	t.Helper()
	tj := &transcodev1alpha1.TranscodeJob{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: transcodev1alpha1.TranscodeJobSpec{
			MediaFileRef:    mediaFile,
			ProfileRef:      profile,
			SourcePath:      "/data/movies/" + mediaFile + ".mkv",
			SourceProbeHash: probeHash,
		},
	}
	if mutate != nil {
		mutate(tj)
	}
	require.NoError(t, c.Create(context.Background(), tj))
	return tj
}

func newReconciler(c client.Client, slots map[string]int32) *transcodejob.Reconciler {
	return &transcodejob.Reconciler{
		Client: c,
		Slots:  slots,
		Job: transcodejob.JobConfig{
			Image: "ghcr.io/mediactl/clustarr/media:test", ImageCUDA: "ghcr.io/mediactl/clustarr/media-cuda:test",
			NATSURL: "nats://nats.test:4222", ServiceAccountName: "squasharr-worker",
		},
	}
}

func reconcileTJ(t *testing.T, r *transcodejob.Reconciler, ns, name string) reconcile.Result {
	t.Helper()
	res, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: name}})
	require.NoError(t, err)
	return res
}

func getTJ(t *testing.T, c client.Client, ns, name string) *transcodev1alpha1.TranscodeJob {
	t.Helper()
	var tj transcodev1alpha1.TranscodeJob
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &tj))
	return &tj
}

func getJob(t *testing.T, c client.Client, ns, name string) *batchv1.Job {
	t.Helper()
	var j batchv1.Job
	require.NoError(t, c.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &j))
	return &j
}

// completeJob and failJob stand in for the Job controller, which envtest
// does not run. They use Status().Apply under a test-only owner, not
// pkg/k8s.PatchStatus: no Clustarr field manager writes Job status.
func completeJob(t *testing.T, c client.Client, ns, name string) {
	t.Helper()
	now := metav1.Now()
	ac := batchac.Job(name, ns).WithStatus(batchac.JobStatus().
		WithStartTime(metav1.NewTime(now.Add(-time.Minute))).
		WithCompletionTime(now).
		WithSucceeded(1).
		WithConditions(
			batchac.JobCondition().WithType(batchv1.JobSuccessCriteriaMet).WithStatus(corev1.ConditionTrue).
				WithLastTransitionTime(now).WithLastProbeTime(now),
			batchac.JobCondition().WithType(batchv1.JobComplete).WithStatus(corev1.ConditionTrue).
				WithLastTransitionTime(now).WithLastProbeTime(now)))
	require.NoError(t, c.Status().Apply(context.Background(), ac, client.FieldOwner("test-job-controller"), client.ForceOwnership))
}

func failJob(t *testing.T, c client.Client, ns, name string) {
	t.Helper()
	now := metav1.Now()
	msg := "Container transcode for pod x failed with exit code 3 matching FailJob rule at index 1"
	ac := batchac.Job(name, ns).WithStatus(batchac.JobStatus().
		WithStartTime(metav1.NewTime(now.Add(-time.Minute))).
		WithFailed(1).
		WithConditions(
			batchac.JobCondition().WithType(batchv1.JobFailureTarget).WithStatus(corev1.ConditionTrue).
				WithReason(batchv1.JobReasonPodFailurePolicy).WithMessage(msg).
				WithLastTransitionTime(now).WithLastProbeTime(now),
			batchac.JobCondition().WithType(batchv1.JobFailed).WithStatus(corev1.ConditionTrue).
				WithReason(batchv1.JobReasonPodFailurePolicy).WithMessage(msg).
				WithLastTransitionTime(now).WithLastProbeTime(now)))
	require.NoError(t, c.Status().Apply(context.Background(), ac, client.FieldOwner("test-job-controller"), client.ForceOwnership))
}

// TestPlanQueueAdmitRun walks the happy path: one reconcile plans, creates
// the Job suspended and admits it; the next mirrors it into Running.
func TestPlanQueueAdmitRun(t *testing.T) {
	_, c := startEnv(t)
	const ns = "tj-happy"
	newNamespace(t, c, ns)
	// A Go client sends activeDeadline and resources as present-but-zero,
	// so the CRD defaults a kubectl-applied profile gets do not apply here;
	// set them as the defaults would.
	newProfile(t, c, "hevc", "hash1", func(p *transcodev1alpha1.TranscodeProfile) {
		p.Spec.ActiveDeadline = metav1.Duration{Duration: 48 * time.Hour}
		p.Spec.Resources.Limits = corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("8"), corev1.ResourceMemory: resource.MustParse("4Gi"),
		}
	})
	newMediaFile(t, c, ns, "arrival", "probe1", ptr.To(h264Probe()))
	newTJ(t, c, ns, "arrival-hevc", "arrival", "hevc", "probe1", nil)

	r := newReconciler(c, map[string]int32{"cpu": 2, "nvidia": 1, "intel": 1})
	reconcileTJ(t, r, ns, "arrival-hevc")

	tj := getTJ(t, c, ns, "arrival-hevc")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, tj.Status.Phase)
	require.NotNil(t, tj.Status.Plan)
	assert.Equal(t, transcodev1alpha1.PlanModeTranscode, tj.Status.Plan.Mode)
	assert.Equal(t, "libx265", tj.Status.Plan.Encoder)
	assert.NotEmpty(t, tj.Status.Plan.VideoArgs)
	assert.Len(t, tj.Status.Plan.ArgsHash, 64)
	require.Len(t, tj.Status.Plan.AudioTracks, 1)
	assert.Equal(t, transcodev1alpha1.AudioActionEncode, tj.Status.Plan.AudioTracks[0].Action)
	assert.Equal(t, tj.Generation, tj.Status.ObservedGeneration)
	assert.True(t, k8s.IsConditionTrue(tj.Status.Conditions, transcodev1alpha1.TranscodeJobConditionPlanned))
	assert.True(t, k8s.IsConditionTrue(tj.Status.Conditions, transcodev1alpha1.TranscodeJobConditionJobCreated))
	require.NotNil(t, tj.Status.JobRef)

	job := getJob(t, c, ns, *tj.Status.JobRef)
	// Admission ran after the status landed and unsuspended it.
	require.NotNil(t, job.Spec.Suspend)
	assert.False(t, *job.Spec.Suspend, "a free cpu slot must admit the job")
	assert.True(t, metav1.IsControlledBy(job, tj))
	assert.Equal(t, "cpu", job.Labels[transcodejob.LabelHardware])

	// Ruling R4 and §6.4's Job shape.
	assert.Equal(t, ptr.To(int32(2)), job.Spec.BackoffLimit)
	assert.Equal(t, ptr.To(batchv1.Failed), job.Spec.PodReplacementPolicy)
	assert.Equal(t, ptr.To(int64(48*3600)), job.Spec.ActiveDeadlineSeconds)
	assert.Equal(t, ptr.To(int32(86400)), job.Spec.TTLSecondsAfterFinished)
	require.NotNil(t, job.Spec.PodFailurePolicy)
	rules := job.Spec.PodFailurePolicy.Rules
	require.Len(t, rules, 2)
	assert.Equal(t, batchv1.PodFailurePolicyActionIgnore, rules[0].Action)
	require.Len(t, rules[0].OnPodConditions, 1)
	assert.Equal(t, corev1.DisruptionTarget, rules[0].OnPodConditions[0].Type)
	assert.Equal(t, batchv1.PodFailurePolicyActionFailJob, rules[1].Action)
	require.NotNil(t, rules[1].OnExitCodes)
	assert.Equal(t, batchv1.PodFailurePolicyOnExitCodesOpIn, rules[1].OnExitCodes.Operator)
	assert.Equal(t, []int32{3, 4}, rules[1].OnExitCodes.Values)

	pod := job.Spec.Template.Spec
	assert.Equal(t, corev1.RestartPolicyNever, pod.RestartPolicy)
	assert.Equal(t, "squasharr-worker", pod.ServiceAccountName)
	require.Len(t, pod.Containers, 1)
	ctr := pod.Containers[0]
	assert.Equal(t, "ghcr.io/mediactl/clustarr/media:test", ctr.Image)
	assert.Equal(t, []string{"squasharr", "--role", "worker", "--job", "arrival-hevc", "--data-dir", "/data"}, ctr.Args)
	assert.Equal(t, "8", ctr.Resources.Limits.Cpu().String(), "profile resources carried onto the container")
	var dataMounted bool
	for _, m := range ctr.VolumeMounts {
		dataMounted = dataMounted || (m.Name == "data" && m.MountPath == "/data")
	}
	assert.True(t, dataMounted)
	env := map[string]string{}
	for _, e := range ctr.Env {
		env[e.Name] = e.Value
	}
	assert.Equal(t, "nats://nats.test:4222", env["NATS_URL"])
	assert.Contains(t, env, "POD_NAMESPACE")
	assert.Contains(t, env, "CLUSTARR_CPU_LIMIT")

	reconcileTJ(t, r, ns, "arrival-hevc")
	tj = getTJ(t, c, ns, "arrival-hevc")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseRunning, tj.Status.Phase)
	assert.NotNil(t, tj.Status.StartedAt)

	completeJob(t, c, ns, job.Name)
	reconcileTJ(t, r, ns, "arrival-hevc")
	tj = getTJ(t, c, ns, "arrival-hevc")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseSucceeded, tj.Status.Phase)
	assert.NotNil(t, tj.Status.FinishedAt, "catalogarr's latestUnincorporatedTranscode requires finishedAt")
	assert.True(t, k8s.IsConditionTrue(tj.Status.Conditions, transcodev1alpha1.TranscodeJobConditionSucceeded))
	assert.True(t, k8s.IsConditionTrue(tj.Status.Conditions, transcodev1alpha1.TranscodeJobConditionVerified))
	assert.EqualValues(t, 1, tj.Status.Attempts)
}

// A profile created through a typed Go client -- the way every controller
// and test in this tree creates one -- sends activeDeadline "0s",
// resources {} and scratch "0" present, so the apiserver's defaults (48h,
// cpu 8 / 4Gi, 20Gi) never apply to it. The premise is asserted on the
// stored object first; then the Job built from it must carry the defaults
// anyway, rather than no deadline, no limits and an unbounded scratch.
func TestJobFromATypedClientProfileGetsTheCRDDefaults(t *testing.T) {
	_, c := startEnv(t)
	const ns = "tj-typed-defaults"
	newNamespace(t, c, ns)
	p := newProfile(t, c, "typed", "hash1", nil)
	require.Zero(t, p.Spec.ActiveDeadline.Duration, "premise: a typed create sends activeDeadline present and zero")
	require.Empty(t, p.Spec.Resources.Limits, "premise: a typed create sends resources present and empty")
	require.True(t, p.Spec.Scratch.IsZero(), "premise: a typed create sends scratch present and zero")

	newMediaFile(t, c, ns, "arrival", "probe1", ptr.To(h264Probe()))
	newTJ(t, c, ns, "arrival-typed", "arrival", "typed", "probe1", nil)
	r := newReconciler(c, map[string]int32{"cpu": 1})
	reconcileTJ(t, r, ns, "arrival-typed")

	tj := getTJ(t, c, ns, "arrival-typed")
	require.NotNil(t, tj.Status.JobRef)
	job := getJob(t, c, ns, *tj.Status.JobRef)
	require.NotNil(t, job.Spec.ActiveDeadlineSeconds, "a Job with no deadline can run forever")
	assert.Equal(t, int64(48*3600), *job.Spec.ActiveDeadlineSeconds)
	ctr := job.Spec.Template.Spec.Containers[0]
	assert.Equal(t, "8", ctr.Resources.Limits.Cpu().String())
	assert.Equal(t, "4Gi", ctr.Resources.Limits.Memory().String())
	var scratch *resource.Quantity
	for _, v := range job.Spec.Template.Spec.Volumes {
		if v.Name == "scratch" && v.EmptyDir != nil {
			scratch = v.EmptyDir.SizeLimit
		}
	}
	require.NotNil(t, scratch)
	assert.Equal(t, "20Gi", scratch.String())
}

// TestSkipAndRejectAreSkipped covers ruling R1: skip records a plan with
// mode=skip; reject leaves status.plan unset. Neither creates a Job.
func TestSkipAndRejectAreSkipped(t *testing.T) {
	_, c := startEnv(t)
	ctx := context.Background()
	const ns = "tj-skip"
	newNamespace(t, c, ns)
	newProfile(t, c, "hevc", "hash1", nil)
	newProfile(t, c, "strict", "hash2", func(p *transcodev1alpha1.TranscodeProfile) {
		p.Spec.HDR.DolbyVision = transcodev1alpha1.DolbyVisionReject
	})
	newMediaFile(t, c, ns, "compliant", "p1", ptr.To(compliantProbe()))
	newMediaFile(t, c, ns, "dovi", "p2", ptr.To(dolbyVisionProbe()))
	newTJ(t, c, ns, "compliant-hevc", "compliant", "hevc", "p1", nil)
	newTJ(t, c, ns, "dovi-strict", "dovi", "strict", "p2", nil)

	r := newReconciler(c, map[string]int32{"cpu": 2})

	t.Run("skip", func(t *testing.T) {
		reconcileTJ(t, r, ns, "compliant-hevc")
		tj := getTJ(t, c, ns, "compliant-hevc")
		assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseSkipped, tj.Status.Phase)
		require.NotNil(t, tj.Status.Plan)
		assert.Equal(t, transcodev1alpha1.PlanModeSkip, tj.Status.Plan.Mode)
		assert.Equal(t, "already compliant with profile", tj.Status.Plan.SkipReason)
		assert.Nil(t, tj.Status.JobRef)
		assert.NotNil(t, tj.Status.FinishedAt)
		cond := k8s.FindCondition(tj.Status.Conditions, transcodev1alpha1.TranscodeJobConditionPlanned)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionTrue, cond.Status)
		assert.Equal(t, transcodejob.ReasonSkipped, cond.Reason)
	})

	t.Run("reject", func(t *testing.T) {
		reconcileTJ(t, r, ns, "dovi-strict")
		tj := getTJ(t, c, ns, "dovi-strict")
		assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseSkipped, tj.Status.Phase)
		assert.Nil(t, tj.Status.Plan, "R1: a reject leaves status.plan unset")
		assert.Contains(t, tj.Status.Message, "Dolby Vision")
		assert.Nil(t, tj.Status.JobRef)
		assert.Nil(t, k8s.FindCondition(tj.Status.Conditions, transcodev1alpha1.TranscodeJobConditionFailed),
			"R1: a reject is not a failure")
		cond := k8s.FindCondition(tj.Status.Conditions, transcodev1alpha1.TranscodeJobConditionPlanned)
		require.NotNil(t, cond)
		assert.Equal(t, metav1.ConditionFalse, cond.Status)
		assert.Equal(t, transcodejob.ReasonRejected, cond.Reason)
		assert.Contains(t, cond.Message, "reject")
	})

	// Gap-fix ruling R-11 (superseding Phase E's R8, which skipped these): a
	// container change is planned to a NEW name beside the source, in both
	// directions and case-insensitively, and the .part the plan renders is
	// beside that name; a same-container source in another case is in place.
	t.Run("container change mp4 to mkv", func(t *testing.T) {
		newMediaFile(t, c, ns, "mp4src", "p3", ptr.To(h264Probe()))
		newTJ(t, c, ns, "mp4src-hevc", "mp4src", "hevc", "p3", func(tj *transcodev1alpha1.TranscodeJob) {
			tj.Spec.SourcePath = "/data/movies/Film (2020)/Film.2020.MP4"
		})
		reconcileTJ(t, r, ns, "mp4src-hevc")
		assertContainerChangePlanned(t, getTJ(t, c, ns, "mp4src-hevc"), "/data/movies/Film (2020)/Film.2020.mkv")
	})
	t.Run("container change mkv to mp4", func(t *testing.T) {
		newProfile(t, c, "mp4out", "hash3", func(p *transcodev1alpha1.TranscodeProfile) {
			p.Spec.Container = transcodev1alpha1.ContainerMP4
		})
		newMediaFile(t, c, ns, "mkvsrc", "p4", ptr.To(h264Probe()))
		newTJ(t, c, ns, "mkvsrc-mp4out", "mkvsrc", "mp4out", "p4", nil)
		reconcileTJ(t, r, ns, "mkvsrc-mp4out")
		assertContainerChangePlanned(t, getTJ(t, c, ns, "mkvsrc-mp4out"), "/data/movies/mkvsrc.mp4")
	})
	t.Run("same container in a different case is not a change", func(t *testing.T) {
		newMediaFile(t, c, ns, "upper", "p5", ptr.To(h264Probe()))
		newTJ(t, c, ns, "upper-hevc", "upper", "hevc", "p5", func(tj *transcodev1alpha1.TranscodeJob) {
			tj.Spec.SourcePath = "/data/movies/Film (2020)/Film.2020.MKV"
		})
		reconcileTJ(t, r, ns, "upper-hevc")
		tj := getTJ(t, c, ns, "upper-hevc")
		assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, tj.Status.Phase)
		cond := k8s.FindCondition(tj.Status.Conditions, transcodev1alpha1.TranscodeJobConditionPlanned)
		require.NotNil(t, cond)
		assert.Equal(t, transcodejob.ReasonPlanned, cond.Reason, "in place, not a container change")
	})
	// replaceSource=false keeps the source, so the output takes the
	// multiple-version name beside it.
	t.Run("replaceSource false", func(t *testing.T) {
		newProfile(t, c, "keep", "hash4", func(p *transcodev1alpha1.TranscodeProfile) {
			p.Spec.Policy.ReplaceSource = ptr.To(false)
		})
		newMediaFile(t, c, ns, "kept", "p6", ptr.To(h264Probe()))
		newTJ(t, c, ns, "kept-keep", "kept", "keep", "p6", nil)
		reconcileTJ(t, r, ns, "kept-keep")
		tj := getTJ(t, c, ns, "kept-keep")
		assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, tj.Status.Phase)
		cond := k8s.FindCondition(tj.Status.Conditions, transcodev1alpha1.TranscodeJobConditionPlanned)
		require.NotNil(t, cond)
		assert.Contains(t, cond.Message, "/data/movies/kept - keep.mkv")
	})
	// An explicit output path the profile's container contradicts cannot be
	// honoured: failed at plan time, without spending a pod.
	t.Run("an output path with the wrong container fails", func(t *testing.T) {
		newMediaFile(t, c, ns, "wrongext", "p7", ptr.To(h264Probe()))
		newTJ(t, c, ns, "wrongext-hevc", "wrongext", "hevc", "p7", func(tj *transcodev1alpha1.TranscodeJob) {
			tj.Spec.OutputPath = ptr.To("/data/movies/wrongext.mp4")
		})
		reconcileTJ(t, r, ns, "wrongext-hevc")
		tj := getTJ(t, c, ns, "wrongext-hevc")
		assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseFailed, tj.Status.Phase)
		cond := k8s.FindCondition(tj.Status.Conditions, transcodev1alpha1.TranscodeJobConditionFailed)
		require.NotNil(t, cond)
		assert.Equal(t, transcodejob.ReasonInvalidOutput, cond.Reason)
		assert.Nil(t, tj.Status.JobRef)
	})

	var jobs batchv1.JobList
	require.NoError(t, c.List(ctx, &jobs, client.InNamespace(ns)))
	var withJobs []string
	for _, j := range jobs.Items {
		withJobs = append(withJobs, j.Annotations[transcodejob.AnnotationTranscodeJob])
	}
	assert.ElementsMatch(t, []string{"mp4src-hevc", "mkvsrc-mp4out", "upper-hevc", "kept-keep"}, withJobs,
		"every planned job, container changes included, creates a Job; the failed one does not")
}

// assertContainerChangePlanned: the job is planned and queued like any
// other, its Planned condition names the change and the new path, and its
// argv writes the .part beside that path.
func assertContainerChangePlanned(t *testing.T, tj *transcodev1alpha1.TranscodeJob, out string) {
	t.Helper()
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, tj.Status.Phase)
	require.NotNil(t, tj.Status.Plan)
	assert.Equal(t, transcodev1alpha1.PlanModeTranscode, tj.Status.Plan.Mode)
	assert.NotNil(t, tj.Status.JobRef)
	cond := k8s.FindCondition(tj.Status.Conditions, transcodev1alpha1.TranscodeJobConditionPlanned)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, transcodejob.ReasonContainerChange, cond.Reason)
	assert.Contains(t, cond.Message, out)
}

// TestPendingUntilDependenciesAndSourceChange: a job waits (Pending) for the
// probe and the profile hash, and fails without spending a pod when the
// source changed since it was created.
func TestPendingUntilDependenciesAndSourceChange(t *testing.T) {
	_, c := startEnv(t)
	const ns = "tj-pending"
	newNamespace(t, c, ns)
	newProfile(t, c, "hevc", "", nil)
	newMediaFile(t, c, ns, "arrival", "", nil)
	newTJ(t, c, ns, "arrival-hevc", "arrival", "hevc", "probe1", nil)
	r := newReconciler(c, map[string]int32{"cpu": 2})

	res := reconcileTJ(t, r, ns, "arrival-hevc")
	assert.Positive(t, res.RequeueAfter)
	tj := getTJ(t, c, ns, "arrival-hevc")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhasePending, tj.Status.Phase)
	assert.Contains(t, tj.Status.Message, "probed")

	setProbe(t, c, ns, "arrival", "probe1", h264Probe())
	reconcileTJ(t, r, ns, "arrival-hevc")
	tj = getTJ(t, c, ns, "arrival-hevc")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhasePending, tj.Status.Phase)
	assert.Contains(t, tj.Status.Message, "hashed")

	setProfileHash(t, c, "hevc", "hash1")
	setProbe(t, c, ns, "arrival", "probe2-changed", h264Probe())
	reconcileTJ(t, r, ns, "arrival-hevc")
	tj = getTJ(t, c, ns, "arrival-hevc")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseFailed, tj.Status.Phase)
	cond := k8s.FindCondition(tj.Status.Conditions, transcodev1alpha1.TranscodeJobConditionFailed)
	require.NotNil(t, cond)
	assert.Equal(t, transcodejob.ReasonSourceChanged, cond.Reason)
	assert.Nil(t, tj.Status.JobRef)
}

// TestAdmissionHonoursTheBudget: with one cpu slot, the higher-priority job
// runs first, the other waits suspended, and it is admitted by the pass
// that sees the first one finish.
func TestAdmissionHonoursTheBudget(t *testing.T) {
	_, c := startEnv(t)
	const ns = "tj-admit"
	newNamespace(t, c, ns)
	newProfile(t, c, "hevc", "hash1", nil)
	newMediaFile(t, c, ns, "a", "pa", ptr.To(h264Probe()))
	newMediaFile(t, c, ns, "b", "pb", ptr.To(h264Probe()))
	newTJ(t, c, ns, "low", "a", "hevc", "pa", func(tj *transcodev1alpha1.TranscodeJob) { tj.Spec.Priority = 10 })
	newTJ(t, c, ns, "high", "b", "hevc", "pb", func(tj *transcodev1alpha1.TranscodeJob) { tj.Spec.Priority = 90 })

	// Queue both with a zero cpu budget, low first, so neither is admitted
	// and reconcile order cannot decide the outcome ...
	queue := newReconciler(c, map[string]int32{"cpu": 0})
	reconcileTJ(t, queue, ns, "low")
	reconcileTJ(t, queue, ns, "high")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, getTJ(t, c, ns, "low").Status.Phase)

	// ... then one pass with one cpu slot (and a legal zero nvidia budget)
	// from the LOW job's reconcile must still admit the HIGH one.
	r := newReconciler(c, map[string]int32{"cpu": 1, "nvidia": 0})
	reconcileTJ(t, r, ns, "low")

	high := getTJ(t, c, ns, "high")
	low := getTJ(t, c, ns, "low")
	highJob := getJob(t, c, ns, *high.Status.JobRef)
	lowJob := getJob(t, c, ns, *low.Status.JobRef)
	assert.False(t, *highJob.Spec.Suspend)
	assert.True(t, *lowJob.Spec.Suspend, "one cpu slot: the second job must stay suspended")

	reconcileTJ(t, r, ns, "low")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, getTJ(t, c, ns, "low").Status.Phase)
	reconcileTJ(t, r, ns, "high")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseRunning, getTJ(t, c, ns, "high").Status.Phase)

	failJob(t, c, ns, highJob.Name)
	reconcileTJ(t, r, ns, "high")
	high = getTJ(t, c, ns, "high")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseFailed, high.Status.Phase)
	cond := k8s.FindCondition(high.Status.Conditions, transcodev1alpha1.TranscodeJobConditionFailed)
	require.NotNil(t, cond)
	assert.Equal(t, batchv1.JobReasonPodFailurePolicy, cond.Reason)

	lowJob = getJob(t, c, ns, *low.Status.JobRef)
	assert.False(t, *lowJob.Spec.Suspend, "the pass that saw the slot free must admit the waiting job")
}

// TestAdmissionHonoursProfileMaxConcurrent: a profile's spec.maxConcurrent
// caps its own running transcodes below a hardware budget that would admit
// more, a profile with none (0) is capped only by the budget, and raising the
// cap admits the waiting job on the next pass.
func TestAdmissionHonoursProfileMaxConcurrent(t *testing.T) {
	_, c := startEnv(t)
	ctx := context.Background()
	const ns = "tj-maxconc"
	newNamespace(t, c, ns)
	newProfile(t, c, "capped", "hashc", func(p *transcodev1alpha1.TranscodeProfile) { p.Spec.MaxConcurrent = 1 })
	newProfile(t, c, "open", "hasho", nil)
	for _, name := range []string{"c1", "c2", "o1", "o2"} {
		newMediaFile(t, c, ns, name, "p"+name, ptr.To(h264Probe()))
	}
	newTJ(t, c, ns, "c1", "c1", "capped", "pc1", nil)
	newTJ(t, c, ns, "c2", "c2", "capped", "pc2", nil)
	newTJ(t, c, ns, "o1", "o1", "open", "po1", nil)
	newTJ(t, c, ns, "o2", "o2", "open", "po2", nil)

	queue := newReconciler(c, map[string]int32{"cpu": 0})
	for _, name := range []string{"c1", "c2", "o1", "o2"} {
		reconcileTJ(t, queue, ns, name)
	}

	r := newReconciler(c, map[string]int32{"cpu": 10})
	reconcileTJ(t, r, ns, "o1")
	suspended := func(name string) bool {
		return *getJob(t, c, ns, *getTJ(t, c, ns, name).Status.JobRef).Spec.Suspend
	}
	assert.False(t, suspended("o1"), "a profile with no maxConcurrent is capped only by the budget")
	assert.False(t, suspended("o2"), "a profile with no maxConcurrent is capped only by the budget")
	assert.NotEqual(t, suspended("c1"), suspended("c2"),
		"maxConcurrent 1 with ten free cpu slots must admit exactly one of the profile's two jobs")

	var capped transcodev1alpha1.TranscodeProfile
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "capped"}, &capped))
	capped.Spec.MaxConcurrent = 2
	require.NoError(t, c.Update(ctx, &capped))
	reconcileTJ(t, r, ns, "o1")
	assert.False(t, suspended("c1"), "raising maxConcurrent must admit the waiting job")
	assert.False(t, suspended("c2"), "raising maxConcurrent must admit the waiting job")
}

// TestUserSuspendPausesAndResumes: spec.suspend re-suspends a running Job
// and keeps it out of admission until cleared.
func TestUserSuspendPausesAndResumes(t *testing.T) {
	_, c := startEnv(t)
	ctx := context.Background()
	const ns = "tj-suspend"
	newNamespace(t, c, ns)
	newProfile(t, c, "hevc", "hash1", nil)
	newMediaFile(t, c, ns, "a", "pa", ptr.To(h264Probe()))
	newTJ(t, c, ns, "a-hevc", "a", "hevc", "pa", nil)
	r := newReconciler(c, map[string]int32{"cpu": 2})
	reconcileTJ(t, r, ns, "a-hevc")
	reconcileTJ(t, r, ns, "a-hevc")
	tj := getTJ(t, c, ns, "a-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseRunning, tj.Status.Phase)

	tj.Spec.Suspend = ptr.To(true)
	require.NoError(t, c.Update(ctx, tj))
	reconcileTJ(t, r, ns, "a-hevc")
	tj = getTJ(t, c, ns, "a-hevc")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, tj.Status.Phase)
	assert.Equal(t, "paused by spec.suspend", tj.Status.Message)
	assert.True(t, *getJob(t, c, ns, *tj.Status.JobRef).Spec.Suspend)

	tj.Spec.Suspend = ptr.To(false)
	require.NoError(t, c.Update(ctx, tj))
	reconcileTJ(t, r, ns, "a-hevc")
	assert.False(t, *getJob(t, c, ns, *tj.Status.JobRef).Spec.Suspend)
}

// failingReader fails every Job read, standing in for an apiserver blip.
type failingReader struct{ client.Reader }

func (failingReader) Get(context.Context, client.ObjectKey, client.Object, ...client.GetOption) error {
	return errors.New("injected: apiserver unavailable")
}

func (failingReader) List(context.Context, client.ObjectList, ...client.ListOption) error {
	return errors.New("injected: apiserver unavailable")
}

// TestTransientFailureDoesNotReleaseStatus drives a job to a populated
// steady state -- controller fields AND the worker's progress -- then makes
// the next reconcile fail half way. The error-path apply must re-declare
// every controller leaf (nothing released), must not claim the worker's
// fields (managedFields), and must leave the worker's progress standing.
func TestTransientFailureDoesNotReleaseStatus(t *testing.T) {
	_, c := startEnv(t)
	ctx := context.Background()
	const ns = "tj-release"
	newNamespace(t, c, ns)
	newProfile(t, c, "hevc", "hash1", nil)
	newMediaFile(t, c, ns, "a", "pa", ptr.To(h264Probe()))
	newTJ(t, c, ns, "a-hevc", "a", "hevc", "pa", nil)
	r := newReconciler(c, map[string]int32{"cpu": 2})
	reconcileTJ(t, r, ns, "a-hevc")
	reconcileTJ(t, r, ns, "a-hevc")

	steady := getTJ(t, c, ns, "a-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseRunning, steady.Status.Phase)
	require.NoError(t, squasharrstatus.Patch(ctx, c, k8s.ManagerSquasharrWorker, steady,
		func(ac *transcodeac.TranscodeJobStatusApplyConfiguration) {
			ac.WithProgress(transcodeac.Progress().WithPercent(42).WithFrame(1000).WithFPSMilli(24000).
				WithSpeedMilli(1000).WithOutTimeMillis(41000).WithBitrateKbps(5000).WithUpdatedAt(metav1.Now())).
				WithStderrTail("frame=1000")
		}))
	steady = getTJ(t, c, ns, "a-hevc")

	broken := newReconciler(c, map[string]int32{"cpu": 2})
	broken.Reader = failingReader{}
	_, err := broken.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "a-hevc"}})
	require.Error(t, err)

	got := getTJ(t, c, ns, "a-hevc")
	assert.Contains(t, got.Status.Message, "injected")
	assert.Equal(t, steady.Status.Phase, got.Status.Phase)
	assert.Equal(t, steady.Status.JobRef, got.Status.JobRef)
	assert.Equal(t, steady.Status.Attempts, got.Status.Attempts)
	assert.Equal(t, steady.Status.StartedAt, got.Status.StartedAt)
	assert.Equal(t, steady.Status.ObservedGeneration, got.Status.ObservedGeneration)
	// Every leaf of the plan, not only the parent.
	require.NotNil(t, got.Status.Plan)
	assert.Equal(t, *steady.Status.Plan, *got.Status.Plan)
	assert.Len(t, got.Status.Conditions, len(steady.Status.Conditions))
	for _, want := range steady.Status.Conditions {
		gotC := k8s.FindCondition(got.Status.Conditions, want.Type)
		require.NotNil(t, gotC, want.Type)
		assert.Equal(t, want.Status, gotC.Status, want.Type)
		assert.Equal(t, want.Reason, gotC.Reason, want.Type)
	}
	// The worker's fields survive and stay the worker's.
	require.NotNil(t, got.Status.Progress)
	assert.EqualValues(t, 42, got.Status.Progress.Percent)
	assert.Equal(t, "frame=1000", got.Status.StderrTail)

	owned := statusFieldsOf(t, got, k8s.ManagerSquasharr)
	for _, f := range []string{"f:progress", "f:result", "f:stderrTail"} {
		assert.NotContains(t, owned, f, "squasharr must not claim the worker's %s", f)
	}
	for _, f := range []string{"f:phase", "f:plan", "f:jobRef", "f:attempts", "f:startedAt", "f:message", "f:conditions", "f:observedGeneration"} {
		assert.Contains(t, owned, f)
	}
	assert.Contains(t, statusFieldsOf(t, got, k8s.ManagerSquasharrWorker), "f:progress")
}

func statusFieldsOf(t *testing.T, obj metav1.Object, mgr k8s.FieldManager) map[string]any {
	t.Helper()
	for _, e := range obj.GetManagedFields() {
		if e.Manager != mgr.String() || e.Subresource != "status" || e.FieldsV1 == nil {
			continue
		}
		var fields map[string]any
		require.NoError(t, json.Unmarshal(e.FieldsV1.GetRawBytes(), &fields))
		st, _ := fields["f:status"].(map[string]any)
		return st
	}
	t.Fatalf("no status managedFields entry for %s", mgr)
	return nil
}

// TestWatchesWakeTheController runs the real manager and never calls
// Reconcile by hand. Each step is only reachable through one watch:
//   - the profile's status.hash landing wakes a Pending job (profile watch);
//   - admission's unsuspend moves Queued to Running (owned-Job watch on
//     spec.suspend -- a generation change on the Job, but only because
//     this controller patched it);
//   - the Job controller's STATUS write moves Running to Succeeded. Status
//     writes by another manager never bump metadata.generation, so this
//     step fails under a generation-only predicate: the D2-8a trap.
func TestWatchesWakeTheController(t *testing.T) {
	cfg, c := startEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		Controller:             config.Controller{SkipNameValidation: ptr.To(true)},
	})
	require.NoError(t, err)
	r := newReconciler(mgr.GetClient(), map[string]int32{"cpu": 2})
	r.Reader = mgr.GetAPIReader()
	require.NoError(t, r.SetupWithManager(mgr))
	go func() { _ = mgr.Start(ctx) }()

	const ns = "tj-watch"
	newNamespace(t, c, ns)
	newProfile(t, c, "hevc", "", nil)
	newMediaFile(t, c, ns, "a", "pa", ptr.To(h264Probe()))
	newTJ(t, c, ns, "a-hevc", "a", "hevc", "pa", nil)

	phase := func() transcodev1alpha1.TranscodeJobPhase { return getTJ(t, c, ns, "a-hevc").Status.Phase }
	require.Eventually(t, func() bool { return phase() == transcodev1alpha1.TranscodeJobPhasePending },
		20*time.Second, 100*time.Millisecond)

	setProfileHash(t, c, "hevc", "hash1")
	require.Eventually(t, func() bool { return phase() == transcodev1alpha1.TranscodeJobPhaseRunning },
		20*time.Second, 100*time.Millisecond, "profile hash + admission should reach Running without a manual reconcile")

	// Let the events this controller's own writes produced (the Job create
	// and the unsuspend patch, each queued once more behind the reconcile
	// already running) drain. Without this, a trailing reconcile can race
	// the status write below and see the Job complete by accident, which
	// made this step pass even under a generation-only Owns predicate.
	time.Sleep(2 * time.Second)

	completeJob(t, c, ns, *getTJ(t, c, ns, "a-hevc").Status.JobRef)
	require.Eventually(t, func() bool { return phase() == transcodev1alpha1.TranscodeJobPhaseSucceeded },
		20*time.Second, 100*time.Millisecond, "a Job status write must wake the owner")
}

// staleClient serves one TranscodeJob from a fixed snapshot, standing in for
// an informer that has not yet seen this controller's own terminal write.
type staleClient struct {
	client.Client
	snapshot *transcodev1alpha1.TranscodeJob
}

func (s staleClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if tj, ok := obj.(*transcodev1alpha1.TranscodeJob); ok && key == client.ObjectKeyFromObject(s.snapshot) {
		s.snapshot.DeepCopyInto(tj)
		return nil
	}
	return s.Client.Get(ctx, key, obj, opts...)
}

func histogramCount(t *testing.T, vec *prometheus.HistogramVec, labels ...string) uint64 {
	t.Helper()
	var m dto.Metric
	require.NoError(t, vec.WithLabelValues(labels...).(prometheus.Metric).Write(&m))
	return m.GetHistogram().GetSampleCount()
}

// TestTerminalMetricsObservedOnce is ruling R9: duration and size ratio are
// observed exactly once per job, on the transition to a terminal phase --
// not again when a lagging cache hands the same transition back.
func TestTerminalMetricsObservedOnce(t *testing.T) {
	_, c := startEnv(t)
	ctx := context.Background()
	const ns = "tj-metrics"
	newNamespace(t, c, ns)
	newProfile(t, c, "hevc", "hash1", nil)
	newMediaFile(t, c, ns, "a", "pa", ptr.To(h264Probe()))
	newTJ(t, c, ns, "a-hevc", "a", "hevc", "pa", nil)
	r := newReconciler(c, map[string]int32{"cpu": 2})
	reconcileTJ(t, r, ns, "a-hevc")
	reconcileTJ(t, r, ns, "a-hevc")
	running := getTJ(t, c, ns, "a-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseRunning, running.Status.Phase)

	// The worker's result, as it writes it before exiting 0.
	require.NoError(t, squasharrstatus.Patch(ctx, c, k8s.ManagerSquasharrWorker, running,
		func(ac *transcodeac.TranscodeJobStatusApplyConfiguration) {
			ac.WithResult(transcodeac.Result().WithOutputPath(running.Spec.SourcePath).
				WithOutputSizeBytes(1 << 30).WithOutputToSourcePercent(45))
		}))
	running = getTJ(t, c, ns, "a-hevc")

	durBefore := histogramCount(t, metrics.TranscodeDuration, "cpu", "hd", "succeeded")
	sizeBefore := histogramCount(t, metrics.TranscodeSizeRatio, "cpu", "hd")

	completeJob(t, c, ns, *running.Status.JobRef)
	reconcileTJ(t, r, ns, "a-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseSucceeded, getTJ(t, c, ns, "a-hevc").Status.Phase)

	// The same transition again, from a cache that still says Running.
	stale := newReconciler(staleClient{Client: c, snapshot: running}, map[string]int32{"cpu": 2})
	stale.Reader = c
	reconcileTJ(t, stale, ns, "a-hevc")
	// And an ordinary re-reconcile of the terminal object.
	reconcileTJ(t, r, ns, "a-hevc")

	assert.Equal(t, durBefore+1, histogramCount(t, metrics.TranscodeDuration, "cpu", "hd", "succeeded"))
	assert.Equal(t, sizeBefore+1, histogramCount(t, metrics.TranscodeSizeRatio, "cpu", "hd"))
}
