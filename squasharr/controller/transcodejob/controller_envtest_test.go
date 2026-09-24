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
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
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
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/squasharr/controller/pool"
	"github.com/mediactl/clustarr/squasharr/controller/transcodejob"
	squasharrstatus "github.com/mediactl/clustarr/squasharr/status"
	"github.com/mediactl/clustarr/squasharr/task"
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

// newRootFolder creates the library root every fixture's source lives
// under: dispatch refuses a source under none (spec §17.5).
func newRootFolder(t *testing.T, c client.Client, ns, path string) {
	t.Helper()
	require.NoError(t, c.Create(context.Background(), &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: ns},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: path, Kind: catalogv1alpha1.RootFolderKindMovie},
	}))
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
			Path:      "/data/media/movies/" + name + ".mkv",
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
			SourcePath:      "/data/media/movies/" + mediaFile + ".mkv",
			SourceProbeHash: probeHash,
		},
	}
	if mutate != nil {
		mutate(tj)
	}
	require.NoError(t, c.Create(context.Background(), tj))
	return tj
}

// newBus is an in-memory bus carrying the shipped topology: the
// CLUSTARR_WORK_SQUASHARR stream dispatch publishes to, and the history
// stream the job events go to.
func newBus(t *testing.T) events.Bus {
	t.Helper()
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(context.Background(), events.Default().ForSingleNode()))
	t.Cleanup(func() { _ = bus.Close() })
	return bus
}

func newReconciler(t *testing.T, c client.Client, slots map[string]int32) *transcodejob.Reconciler {
	t.Helper()
	bus := newBus(t)
	return &transcodejob.Reconciler{
		Client: c,
		Slots:  slots,
		Pool:   pool.Config{Namespace: "default", Image: "transcoder:test", ImageCUDA: "transcoder-cuda:test"},
		Bus:    bus,
		Leases: bus.KV(events.BucketTranscodeLeases),
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

// fakeMsg is one delivery of a status event, straight to the results
// consumer's handler.
type fakeMsg struct{ env *events.Envelope }

func (fakeMsg) Ack(context.Context) error                { return nil }
func (fakeMsg) Nak(context.Context, time.Duration) error { return nil }
func (fakeMsg) Term(context.Context, string) error       { return nil }
func (fakeMsg) InProgress(context.Context) error         { return nil }
func (f fakeMsg) Envelope() *events.Envelope             { return f.env }
func (fakeMsg) Subject() string                          { return "" }
func (fakeMsg) Attempt() uint64                          { return 1 }

// deliver hands ev, as a pool worker reports it for tj, to the results
// consumer's handler, and returns its settlement: nil acks.
func deliver(t *testing.T, r *transcodejob.Reconciler, tj *transcodev1alpha1.TranscodeJob, ev task.StatusEvent) error {
	t.Helper()
	ev.Job = schema.Ref{Namespace: tj.Namespace, Name: tj.Name, UID: string(tj.UID)}
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	sch, data, err := schema.Encode(ev)
	require.NoError(t, err)
	return transcodejob.HandleEventForTest(r, context.Background(), fakeMsg{&events.Envelope{Schema: sch, Data: data}})
}

func claimed(attempt int32, pod string) task.StatusEvent {
	return task.StatusEvent{Kind: task.EventClaimed, Attempt: attempt, Pod: pod, Node: "node-1"}
}

func finished(attempt int32, o task.Outcome, reason task.Reason, msg string) task.StatusEvent {
	return task.StatusEvent{Kind: task.EventFinished, Attempt: attempt, Outcome: o, Reason: reason, Message: msg}
}

// takeTasks pulls every task on the (profile, class) pool's queue, acking
// each as a worker would, and returns them in order. It waits up to within
// for the first one and then only briefly, so an empty queue returns none.
func takeTasks(t *testing.T, bus events.Bus, profileUID types.UID, class string, within time.Duration) []task.Task {
	t.Helper()
	p, err := bus.(events.PullSubscriber).Pull(context.Background(),
		events.TranscodeTaskConsumer(string(profileUID), class).Subscription())
	require.NoError(t, err)
	defer p.Stop()
	var out []task.Task
	for wait := within; ; wait = 200 * time.Millisecond {
		ctx, cancel := context.WithTimeout(context.Background(), wait)
		_, m, err := p.Next(ctx)
		cancel()
		if err != nil {
			return out
		}
		var tk task.Task
		require.NoError(t, schema.Decode(m.Envelope().Schema, m.Envelope().Data, &tk))
		require.NoError(t, m.Ack(context.Background()))
		out = append(out, tk)
	}
}

func attemptsOf(tasks []task.Task) []int32 {
	out := []int32{}
	for _, tk := range tasks {
		out = append(out, tk.Attempt)
	}
	return out
}

// statusOwners is every manager owning a field of obj's status.
func statusOwners(obj metav1.Object) []string {
	var out []string
	for _, e := range obj.GetManagedFields() {
		if e.Subresource == "status" && e.FieldsV1 != nil && strings.Contains(string(e.FieldsV1.GetRawBytes()), `"f:status"`) {
			out = append(out, e.Manager)
		}
	}
	sort.Strings(out)
	return out
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

// dispatchedFixture is one job reconciled to Queued at attempt 1 on the
// cpu pool.
type dispatchedFixture struct {
	c  client.Client
	r  *transcodejob.Reconciler
	ns string
	tp *transcodev1alpha1.TranscodeProfile
	tj *transcodev1alpha1.TranscodeJob
}

func newDispatched(t *testing.T, ns string, slots map[string]int32) dispatchedFixture {
	t.Helper()
	_, c := startEnv(t)
	newNamespace(t, c, ns)
	newRootFolder(t, c, ns, "/data/media/movies")
	tp := newProfile(t, c, "hevc", "hash1", nil)
	newMediaFile(t, c, ns, "heat", "probe1", ptr.To(h264Probe()))
	newTJ(t, c, ns, "heat-hevc", "heat", "hevc", "probe1", nil)
	r := newReconciler(t, c, slots)
	reconcileTJ(t, r, ns, "heat-hevc")
	tj := getTJ(t, c, ns, "heat-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, tj.Status.Phase, "setup: message %s", tj.Status.Message)
	require.EqualValues(t, 1, tj.Status.Attempts)
	return dispatchedFixture{c: c, r: r, ns: ns, tp: tp, tj: tj}
}

func (f dispatchedFixture) get(t *testing.T) *transcodev1alpha1.TranscodeJob {
	t.Helper()
	return getTJ(t, f.c, f.ns, f.tj.Name)
}

// TestDispatchPublishesTheTaskThenQueues is spec §8's dispatch order: the
// task is on its (profile, class) pool's queue, built by worker.BuildTask,
// before the job reads Queued with attempts 1 and jobRef naming the pool.
func TestDispatchPublishesTheTaskThenQueues(t *testing.T) {
	_, c := startEnv(t)
	const ns = "tj-dispatch"
	newNamespace(t, c, ns)
	newRootFolder(t, c, ns, "/data/media/movies")
	tp := newProfile(t, c, "hevc", "hash1", nil)
	newMediaFile(t, c, ns, "heat", "probe1", ptr.To(h264Probe()))
	newTJ(t, c, ns, "heat-hevc", "heat", "hevc", "probe1", nil)
	r := newReconciler(t, c, map[string]int32{"cpu": 1})

	reconcileTJ(t, r, ns, "heat-hevc") // Pending -> Planned -> admitted -> Queued
	got := getTJ(t, c, ns, "heat-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, got.Status.Phase, "message: %s", got.Status.Message)
	assert.EqualValues(t, 1, got.Status.Attempts)
	assert.Equal(t, transcodev1alpha1.HardwareCPU, got.Status.Hardware)
	require.NotNil(t, got.Status.JobRef)
	assert.Equal(t, pool.Name(pool.Key{Profile: tp.Name, ProfileUID: tp.UID, Class: "cpu"}), *got.Status.JobRef)
	require.NotNil(t, got.Status.Plan)
	assert.Equal(t, "libx265", got.Status.Plan.Encoder)
	assert.Len(t, got.Status.Plan.ArgsHash, 64)
	assert.Equal(t, got.Generation, got.Status.ObservedGeneration)
	assert.True(t, k8s.IsConditionTrue(got.Status.Conditions, transcodev1alpha1.TranscodeJobConditionPlanned))
	cond := k8s.FindCondition(got.Status.Conditions, transcodev1alpha1.TranscodeJobConditionJobCreated)
	require.NotNil(t, cond)
	assert.Equal(t, metav1.ConditionTrue, cond.Status)
	assert.Equal(t, transcodejob.ReasonDispatched, cond.Reason)

	tasks := takeTasks(t, r.Bus, tp.UID, "cpu", 5*time.Second)
	require.Len(t, tasks, 1, "Queued means the task is on the queue")
	tk := tasks[0]
	assert.Equal(t, schema.Ref{Namespace: ns, Name: "heat-hevc", UID: string(got.UID)}, tk.Job)
	assert.Equal(t, got.Status.Plan.ArgsHash, tk.ArgsHash)
	assert.EqualValues(t, 1, tk.Attempt)
	assert.Equal(t, "cpu", tk.Class)
	assert.Equal(t, "/data/media/movies/heat.mkv", tk.SourcePath)
	assert.Equal(t, "probe1", tk.SourceProbeHash)
	assert.Equal(t, "/data/media/movies", tk.Root.Path)
	assert.Equal(t, "hash1", tk.Profile.Hash)

	// A second pass dispatches nothing more: the job is Queued.
	reconcileTJ(t, r, ns, "heat-hevc")
	assert.Empty(t, takeTasks(t, r.Bus, tp.UID, "cpu", 300*time.Millisecond))
	assert.EqualValues(t, 1, getTJ(t, c, ns, "heat-hevc").Status.Attempts)
}

// TestSkipAndRejectAreSkipped covers ruling R1: skip records a plan with
// mode=skip; reject leaves status.plan unset. Neither is dispatched.
func TestSkipAndRejectAreSkipped(t *testing.T) {
	_, c := startEnv(t)
	ctx := context.Background()
	const ns = "tj-skip"
	newNamespace(t, c, ns)
	newRootFolder(t, c, ns, "/data/media/movies")
	newProfile(t, c, "hevc", "hash1", nil)
	newProfile(t, c, "strict", "hash2", func(p *transcodev1alpha1.TranscodeProfile) {
		p.Spec.HDR.DolbyVision = transcodev1alpha1.DolbyVisionReject
	})
	newMediaFile(t, c, ns, "compliant", "p1", ptr.To(compliantProbe()))
	newMediaFile(t, c, ns, "dovi", "p2", ptr.To(dolbyVisionProbe()))
	newTJ(t, c, ns, "compliant-hevc", "compliant", "hevc", "p1", nil)
	newTJ(t, c, ns, "dovi-strict", "dovi", "strict", "p2", nil)

	r := newReconciler(t, c, map[string]int32{"cpu": 10})

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

	// A file squasharr already wrote under this profile is not transcoded
	// again, however catalogarr recorded the tag: the probe reads the file's
	// CLUSTARR_PROFILE into status.mediaInfo.transcodeProfile (an earlier
	// install's output, found by a rescan, has only that), and a transcode
	// it incorporates sets status.transcode.profileTag (a replaceSource=false
	// job does so on a source whose own tag may be another profile's). The
	// video is compliant but a kept TrueHD track is not AAC, so only the tag
	// can skip it; a tag from another hash is not this profile's work.
	tagged := func() commonv1.MediaInfo {
		mi := compliantProbe()
		mi.Audio = append(mi.Audio, commonv1.AudioStream{Index: 2, Codec: "truehd", Channels: 8, Language: "eng"})
		return mi
	}
	for _, tc := range []struct {
		name, probeTag, swapTag string
		skipped                 bool
	}{
		{name: "tagged by the probe", probeTag: "hevc@hash1", skipped: true},
		{name: "tagged by a swap", swapTag: "hevc@hash1", skipped: true},
		{name: "tagged by the probe beside another swap tag", probeTag: "hevc@hash1", swapTag: "hevc@old", skipped: true},
		{name: "a kept copy's tag beside the source's own", probeTag: "other@x", swapTag: "hevc@hash1", skipped: true},
		{name: "tagged by another hash", probeTag: "hevc@old", swapTag: "other@x"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := "tag-" + strings.ReplaceAll(strings.ReplaceAll(tc.name, " ", "-"), "'", "")
			name = strings.TrimSuffix(name[:min(len(name), 50)], "-")
			mi := tagged()
			mi.TranscodeProfile = tc.probeTag
			newMediaFile(t, c, ns, name, "", nil)
			st := catalogac.MediaFileStatus().WithProbeHash("p-" + name).WithMediaInfo(mi)
			if tc.swapTag != "" {
				st = st.WithTranscode(catalogac.TranscodeState().WithProfileTag(tc.swapTag))
			}
			_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, catalogac.MediaFile(name, ns).WithStatus(st))
			require.NoError(t, err)
			newTJ(t, c, ns, name, name, "hevc", "p-"+name, nil)
			reconcileTJ(t, r, ns, name)
			tj := getTJ(t, c, ns, name)
			if !tc.skipped {
				assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, tj.Status.Phase, "message: %s", tj.Status.Message)
				require.NotNil(t, tj.Status.Plan)
				assert.Equal(t, transcodev1alpha1.PlanModeRemuxOnly, tj.Status.Plan.Mode)
				return
			}
			assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseSkipped, tj.Status.Phase, "message: %s", tj.Status.Message)
			require.NotNil(t, tj.Status.Plan)
			assert.Equal(t, "tagged with current profile hash", tj.Status.Plan.SkipReason)
		})
	}

	// Gap-fix ruling R-11 (superseding Phase E's R8, which skipped these): a
	// container change is planned to a NEW name beside the source, in both
	// directions and case-insensitively, and the .part the plan renders is
	// beside that name; a same-container source in another case is in place.
	t.Run("container change mp4 to mkv", func(t *testing.T) {
		newMediaFile(t, c, ns, "mp4src", "p3", ptr.To(h264Probe()))
		newTJ(t, c, ns, "mp4src-hevc", "mp4src", "hevc", "p3", func(tj *transcodev1alpha1.TranscodeJob) {
			tj.Spec.SourcePath = "/data/media/movies/Film (2020)/Film.2020.MP4"
		})
		reconcileTJ(t, r, ns, "mp4src-hevc")
		assertContainerChangePlanned(t, getTJ(t, c, ns, "mp4src-hevc"), "/data/media/movies/Film (2020)/Film.2020.mkv")
	})
	t.Run("container change mkv to mp4", func(t *testing.T) {
		newProfile(t, c, "mp4out", "hash3", func(p *transcodev1alpha1.TranscodeProfile) {
			p.Spec.Container = transcodev1alpha1.ContainerMP4
		})
		newMediaFile(t, c, ns, "mkvsrc", "p4", ptr.To(h264Probe()))
		newTJ(t, c, ns, "mkvsrc-mp4out", "mkvsrc", "mp4out", "p4", nil)
		reconcileTJ(t, r, ns, "mkvsrc-mp4out")
		assertContainerChangePlanned(t, getTJ(t, c, ns, "mkvsrc-mp4out"), "/data/media/movies/mkvsrc.mp4")
	})
	t.Run("same container in a different case is not a change", func(t *testing.T) {
		newMediaFile(t, c, ns, "upper", "p5", ptr.To(h264Probe()))
		newTJ(t, c, ns, "upper-hevc", "upper", "hevc", "p5", func(tj *transcodev1alpha1.TranscodeJob) {
			tj.Spec.SourcePath = "/data/media/movies/Film (2020)/Film.2020.MKV"
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
		assert.Contains(t, cond.Message, "/data/media/movies/kept - keep.mkv")
	})
	// An explicit output path the profile's container contradicts cannot be
	// honoured: failed at plan time, without spending a pod.
	t.Run("an output path with the wrong container fails", func(t *testing.T) {
		newMediaFile(t, c, ns, "wrongext", "p7", ptr.To(h264Probe()))
		newTJ(t, c, ns, "wrongext-hevc", "wrongext", "hevc", "p7", func(tj *transcodev1alpha1.TranscodeJob) {
			tj.Spec.OutputPath = ptr.To("/data/media/movies/wrongext.mp4")
		})
		reconcileTJ(t, r, ns, "wrongext-hevc")
		tj := getTJ(t, c, ns, "wrongext-hevc")
		assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseFailed, tj.Status.Phase)
		cond := k8s.FindCondition(tj.Status.Conditions, transcodev1alpha1.TranscodeJobConditionFailed)
		require.NotNil(t, cond)
		assert.Equal(t, transcodejob.ReasonInvalidOutput, cond.Reason)
		assert.Nil(t, tj.Status.JobRef)
	})

	var tjs transcodev1alpha1.TranscodeJobList
	require.NoError(t, c.List(ctx, &tjs, client.InNamespace(ns)))
	var dispatched []string
	for _, j := range tjs.Items {
		if j.Status.Attempts > 0 {
			dispatched = append(dispatched, j.Name)
		}
	}
	assert.ElementsMatch(t, []string{"tag-tagged-by-another-hash", "mp4src-hevc", "mkvsrc-mp4out", "upper-hevc", "kept-keep"}, dispatched,
		"every planned job, container changes included, is dispatched; the skipped and failed ones are not")
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
	r := newReconciler(t, c, map[string]int32{"cpu": 2})

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
// is dispatched first, the other waits Planned, a paused one is never
// dispatched, and the waiting one is dispatched by the pass that sees the
// first one finish.
func TestAdmissionHonoursTheBudget(t *testing.T) {
	_, c := startEnv(t)
	const ns = "tj-admit"
	newNamespace(t, c, ns)
	newRootFolder(t, c, ns, "/data/media/movies")
	newProfile(t, c, "hevc", "hash1", nil)
	for _, name := range []string{"a", "b", "p"} {
		newMediaFile(t, c, ns, name, "p"+name, ptr.To(h264Probe()))
	}
	newTJ(t, c, ns, "low", "a", "hevc", "pa", func(tj *transcodev1alpha1.TranscodeJob) { tj.Spec.Priority = 10 })
	newTJ(t, c, ns, "high", "b", "hevc", "pb", func(tj *transcodev1alpha1.TranscodeJob) { tj.Spec.Priority = 90 })
	newTJ(t, c, ns, "paused", "p", "hevc", "pp", func(tj *transcodev1alpha1.TranscodeJob) {
		tj.Spec.Priority, tj.Spec.Suspend = 100, ptr.To(true)
	})

	// Plan all three with a zero cpu budget, low first, so none is
	// dispatched and reconcile order cannot decide the outcome ...
	queue := newReconciler(t, c, map[string]int32{"cpu": 0})
	for _, name := range []string{"low", "high", "paused"} {
		reconcileTJ(t, queue, ns, name)
		require.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, getTJ(t, c, ns, name).Status.Phase)
	}

	// ... then one pass with one cpu slot (and a legal zero nvidia budget)
	// from the LOW job's reconcile must dispatch the HIGH one.
	r := newReconciler(t, c, map[string]int32{"cpu": 1, "nvidia": 0})
	reconcileTJ(t, r, ns, "low")
	attempts := func(name string) int32 { return getTJ(t, c, ns, name).Status.Attempts }
	assert.EqualValues(t, 1, attempts("high"))
	assert.EqualValues(t, 0, attempts("low"), "one cpu slot: the second job must wait")
	assert.EqualValues(t, 0, attempts("paused"), "spec.suspend keeps a job out of admission")

	reconcileTJ(t, r, ns, "low")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, getTJ(t, c, ns, "low").Status.Phase)

	high := getTJ(t, c, ns, "high")
	require.NoError(t, deliver(t, r, high, claimed(1, "pool-a")))
	require.NoError(t, deliver(t, r, high, finished(1, task.OutcomeFailed, task.ReasonVerifyFailed, "output too large")))
	high = getTJ(t, c, ns, "high")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseFailed, high.Status.Phase)

	reconcileTJ(t, r, ns, "low")
	assert.EqualValues(t, 1, attempts("low"), "the pass that saw the slot free must dispatch the waiting job")
	assert.EqualValues(t, 0, attempts("paused"))
}

// TestAdmissionHonoursProfileMaxConcurrent: a profile's spec.maxConcurrent
// caps its own dispatched transcodes below a hardware budget that would
// admit more, a profile with none (0) is capped only by the budget, and
// raising the cap dispatches the waiting job on the next pass.
func TestAdmissionHonoursProfileMaxConcurrent(t *testing.T) {
	_, c := startEnv(t)
	ctx := context.Background()
	const ns = "tj-maxconc"
	newNamespace(t, c, ns)
	newRootFolder(t, c, ns, "/data/media/movies")
	newProfile(t, c, "capped", "hashc", func(p *transcodev1alpha1.TranscodeProfile) { p.Spec.MaxConcurrent = 1 })
	newProfile(t, c, "open", "hasho", nil)
	for _, name := range []string{"c1", "c2", "o1", "o2"} {
		newMediaFile(t, c, ns, name, "p"+name, ptr.To(h264Probe()))
	}
	newTJ(t, c, ns, "c1", "c1", "capped", "pc1", nil)
	newTJ(t, c, ns, "c2", "c2", "capped", "pc2", nil)
	newTJ(t, c, ns, "o1", "o1", "open", "po1", nil)
	newTJ(t, c, ns, "o2", "o2", "open", "po2", nil)

	queue := newReconciler(t, c, map[string]int32{"cpu": 0})
	for _, name := range []string{"c1", "c2", "o1", "o2"} {
		reconcileTJ(t, queue, ns, name)
	}

	r := newReconciler(t, c, map[string]int32{"cpu": 10})
	reconcileTJ(t, r, ns, "o1")
	dispatched := func(name string) bool {
		return getTJ(t, c, ns, name).Status.Phase == transcodev1alpha1.TranscodeJobPhaseQueued
	}
	assert.True(t, dispatched("o1"), "a profile with no maxConcurrent is capped only by the budget")
	assert.True(t, dispatched("o2"), "a profile with no maxConcurrent is capped only by the budget")
	assert.NotEqual(t, dispatched("c1"), dispatched("c2"),
		"maxConcurrent 1 with ten free cpu slots must dispatch exactly one of the profile's two jobs")

	var capped transcodev1alpha1.TranscodeProfile
	require.NoError(t, c.Get(ctx, types.NamespacedName{Name: "capped"}, &capped))
	capped.Spec.MaxConcurrent = 2
	require.NoError(t, c.Update(ctx, &capped))
	reconcileTJ(t, r, ns, "o1")
	assert.True(t, dispatched("c1"), "raising maxConcurrent must dispatch the waiting job")
	assert.True(t, dispatched("c2"), "raising maxConcurrent must dispatch the waiting job")
}

// TestStatusEventsDriveStatusAndOneManagerOwnsIt is spec §18.2: claimed and
// progress events move a job to Running and set workerPod, startedAt and
// progress; finished sets the outcome, result and stderr tail -- all of it
// written by squasharr, the one manager on TranscodeJob.status.
func TestStatusEventsDriveStatusAndOneManagerOwnsIt(t *testing.T) {
	f := newDispatched(t, "tj-events-drive", map[string]int32{"cpu": 1})

	require.NoError(t, deliver(t, f.r, f.tj, claimed(1, "pool-xyz")))
	got := f.get(t)
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseRunning, got.Status.Phase)
	assert.Equal(t, "pool-xyz", got.Status.WorkerPod)
	assert.NotNil(t, got.Status.StartedAt)
	assert.Contains(t, got.Status.Message, "pool-xyz")

	updated := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	require.NoError(t, deliver(t, f.r, got, task.StatusEvent{
		Kind: task.EventProgress, Attempt: 1, Pod: "pool-xyz",
		Progress: &transcodev1alpha1.Progress{
			Percent: 42, Frame: 1000, FPSMilli: 24000, SpeedMilli: 1500,
			OutTimeMillis: 41000, BitrateKbps: 5000, UpdatedAt: updated,
		},
	}))
	got = f.get(t)
	require.NotNil(t, got.Status.Progress)
	assert.EqualValues(t, 42, got.Status.Progress.Percent)
	assert.EqualValues(t, 5000, got.Status.Progress.BitrateKbps)

	// An event whose status the apiserver refuses is dead-lettered at once
	// (R17): with one event in flight, retrying it would hold every other
	// job's status back behind it.
	before := f.get(t)
	err := deliver(t, f.r, got, task.StatusEvent{
		Kind: task.EventProgress, Attempt: 1, Pod: "pool-xyz",
		Progress: &transcodev1alpha1.Progress{Percent: 150, UpdatedAt: metav1.NewTime(updated.Add(time.Minute))},
	})
	var discard *events.DiscardError
	require.ErrorAs(t, err, &discard, "an invalid status is terminal, not a retry")
	assert.Equal(t, before.ResourceVersion, f.get(t).ResourceVersion)

	// An older progress report, redelivered late, never steps back.
	require.NoError(t, deliver(t, f.r, got, task.StatusEvent{
		Kind: task.EventProgress, Attempt: 1, Pod: "pool-xyz",
		Progress: &transcodev1alpha1.Progress{Percent: 10, UpdatedAt: metav1.NewTime(updated.Add(-time.Minute))},
	}))
	assert.EqualValues(t, 42, f.get(t).Status.Progress.Percent)

	ev := finished(1, task.OutcomeSucceeded, "", "")
	ev.Result = &transcodev1alpha1.Result{OutputPath: "/data/media/movies/heat.mkv", OutputSizeBytes: 10, OutputToSourcePercent: 40}
	ev.StderrTail = "ok"
	require.NoError(t, deliver(t, f.r, got, ev))
	got = f.get(t)
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseSucceeded, got.Status.Phase)
	require.NotNil(t, got.Status.Result)
	assert.EqualValues(t, 10, got.Status.Result.OutputSizeBytes)
	assert.Equal(t, "/data/media/movies/heat.mkv", got.Status.Result.OutputPath)
	assert.Equal(t, "ok", got.Status.StderrTail)
	assert.NotNil(t, got.Status.FinishedAt, "catalogarr's latestUnincorporatedTranscode requires finishedAt")
	assert.True(t, k8s.IsConditionTrue(got.Status.Conditions, transcodev1alpha1.TranscodeJobConditionSucceeded))
	assert.True(t, k8s.IsConditionTrue(got.Status.Conditions, transcodev1alpha1.TranscodeJobConditionVerified))
	assert.EqualValues(t, 100, got.Status.Progress.Percent)
	assert.Equal(t, "pool-xyz", got.Status.WorkerPod, "the pod that ran it stays named, for kubectl logs")

	assert.Equal(t, []string{string(k8s.ManagerSquasharr)}, statusOwners(got), "squasharr is the one status writer")
	owned := statusFieldsOf(t, got, k8s.ManagerSquasharr)
	for _, fld := range []string{
		"f:phase", "f:plan", "f:jobRef", "f:attempts", "f:startedAt", "f:finishedAt", "f:message",
		"f:conditions", "f:observedGeneration", "f:workerPod", "f:hardware", "f:progress", "f:result", "f:stderrTail",
	} {
		assert.Contains(t, owned, fld)
	}
}

// TestRetriableRequeuesWithBackoffThenBlocks is spec §18.3's retry row: each
// Retriable failure goes back to Planned, held until nextAttemptAt (1m, 5m,
// 15m, 30m), and the fifth is blocked as RetriesExhausted.
func TestRetriableRequeuesWithBackoffThenBlocks(t *testing.T) {
	f := newDispatched(t, "tj-retry", map[string]int32{"cpu": 1})
	now := time.Now()
	f.r.Now = func() time.Time { return now }
	require.Equal(t, []int32{1}, attemptsOf(takeTasks(t, f.r.Bus, f.tp.UID, "cpu", 5*time.Second)))

	waits := []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute}
	for attempt := int32(1); attempt < transcodejob.MaxAttempts; attempt++ {
		tj := f.get(t)
		require.NoError(t, deliver(t, f.r, tj, claimed(attempt, "pool-a")))
		require.NoError(t, deliver(t, f.r, tj, finished(attempt, task.OutcomeFailed, task.ReasonRetriable, "ffmpeg exited 1")))
		tj = f.get(t)
		require.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, tj.Status.Phase, "attempt %d", attempt)
		require.NotNil(t, tj.Status.NextAttemptAt)
		wait := waits[attempt-1]
		assert.WithinDuration(t, now.Add(wait), tj.Status.NextAttemptAt.Time, time.Second, "attempt %d", attempt)
		assert.Contains(t, tj.Status.Message, "failed (Retriable)")
		assert.Empty(t, tj.Status.WorkerPod)
		assert.Nil(t, tj.Status.Progress)

		// Before nextAttemptAt: stays Planned, nothing is published, and the
		// reconcile asks to come back when the wait is over.
		res := reconcileTJ(t, f.r, f.ns, tj.Name)
		assert.Positive(t, res.RequeueAfter)
		assert.LessOrEqual(t, res.RequeueAfter, wait)
		assert.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, f.get(t).Status.Phase)
		assert.Empty(t, takeTasks(t, f.r.Bus, f.tp.UID, "cpu", 300*time.Millisecond), "a waiting job is not dispatched")

		now = now.Add(wait + time.Second)
		reconcileTJ(t, f.r, f.ns, tj.Name)
		tj = f.get(t)
		require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, tj.Status.Phase, "attempt %d", attempt+1)
		assert.Equal(t, attempt+1, tj.Status.Attempts)
		assert.Nil(t, tj.Status.NextAttemptAt)
		assert.Equal(t, []int32{attempt + 1}, attemptsOf(takeTasks(t, f.r.Bus, f.tp.UID, "cpu", 5*time.Second)))
	}

	tj := f.get(t)
	require.EqualValues(t, transcodejob.MaxAttempts, tj.Status.Attempts)
	require.NoError(t, deliver(t, f.r, tj, finished(transcodejob.MaxAttempts, task.OutcomeFailed, task.ReasonRetriable, "again")))
	tj = f.get(t)
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseFailed, tj.Status.Phase)
	blocked := k8s.FindCondition(tj.Status.Conditions, transcodev1alpha1.ConditionBlocked)
	require.NotNil(t, blocked)
	assert.Equal(t, metav1.ConditionTrue, blocked.Status)
	assert.Equal(t, string(task.ReasonRetriesExhausted), blocked.Reason)
	assert.Contains(t, blocked.Message, "5 attempts")
	assert.Contains(t, blocked.Message, "delete the TranscodeJob to retry")

	reconcileTJ(t, f.r, f.ns, tj.Name)
	assert.Empty(t, takeTasks(t, f.r.Bus, f.tp.UID, "cpu", 300*time.Millisecond), "a blocked job is never dispatched again")
}

// TestVerifyFailedBlocks: a failure no retry can fix is Failed with
// Blocked=True, and nothing dispatches it again.
func TestVerifyFailedBlocks(t *testing.T) {
	f := newDispatched(t, "tj-verify", map[string]int32{"cpu": 1})
	require.Len(t, takeTasks(t, f.r.Bus, f.tp.UID, "cpu", 5*time.Second), 1)

	require.NoError(t, deliver(t, f.r, f.tj, finished(1, task.OutcomeFailed, task.ReasonVerifyFailed, "duration off by 12s")))
	got := f.get(t)
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseFailed, got.Status.Phase)
	for _, typ := range []string{transcodev1alpha1.TranscodeJobConditionFailed, transcodev1alpha1.ConditionBlocked} {
		c := k8s.FindCondition(got.Status.Conditions, typ)
		require.NotNil(t, c, typ)
		assert.Equal(t, metav1.ConditionTrue, c.Status, typ)
		assert.Equal(t, string(task.ReasonVerifyFailed), c.Reason, typ)
	}
	assert.Contains(t, got.Status.Message, "duration off by 12s")

	reconcileTJ(t, f.r, f.ns, f.tj.Name)
	assert.Empty(t, takeTasks(t, f.r.Bus, f.tp.UID, "cpu", 300*time.Millisecond))
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseFailed, f.get(t).Status.Phase)
}

// TestAStaleEventChangesNothing: an event for an earlier attempt, or for a
// job that is no longer dispatched, is acked and changes nothing (spec
// §18.2).
func TestAStaleEventChangesNothing(t *testing.T) {
	f := newDispatched(t, "tj-stale", map[string]int32{"cpu": 1})
	now := time.Now()
	f.r.Now = func() time.Time { return now }
	require.NoError(t, deliver(t, f.r, f.tj, finished(1, task.OutcomeFailed, task.ReasonRetriable, "blip")))
	now = now.Add(2 * time.Minute)
	reconcileTJ(t, f.r, f.ns, f.tj.Name)
	queued := f.get(t)
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, queued.Status.Phase)
	require.EqualValues(t, 2, queued.Status.Attempts)

	require.NoError(t, deliver(t, f.r, queued, finished(1, task.OutcomeSucceeded, "", "")), "a stale event is acked")
	require.NoError(t, deliver(t, f.r, queued, claimed(1, "pool-old")), "a stale event is acked")
	got := f.get(t)
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, got.Status.Phase)
	assert.EqualValues(t, 2, got.Status.Attempts)
	assert.Empty(t, got.Status.WorkerPod)
	assert.Equal(t, queued.ResourceVersion, got.ResourceVersion, "a stale event writes nothing")

	// Another incarnation of the job (same name, new UID) is stale too.
	other := queued.DeepCopy()
	other.UID = "someone-else"
	require.NoError(t, deliver(t, f.r, other, claimed(2, "pool-x")))
	assert.Equal(t, queued.ResourceVersion, f.get(t).ResourceVersion)

	// A duplicate finished for a terminal job is acked and ignored.
	require.NoError(t, deliver(t, f.r, queued, finished(2, task.OutcomeSucceeded, "", "")))
	done := f.get(t)
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseSucceeded, done.Status.Phase)
	require.NoError(t, deliver(t, f.r, done, finished(2, task.OutcomeFailed, task.ReasonVerifyFailed, "late")))
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseSucceeded, f.get(t).Status.Phase, "the first final result wins")

	// A job that is gone is acked: there is nothing left to describe.
	require.NoError(t, f.c.Delete(context.Background(), done))
	require.NoError(t, deliver(t, f.r, done, claimed(2, "pool-x")))
}

// interleavingReader runs interleave once, right after the first Get of key
// returns -- inside the window between writeStatus's fresh read and its
// compare-and-swap apply -- and counts the Gets of key, so a test sees the
// write being redone from a new read.
type interleavingReader struct {
	client.Reader
	key        types.NamespacedName
	gets       atomic.Int32
	interleave func()
}

func (r *interleavingReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	err := r.Reader.Get(ctx, key, obj, opts...)
	if key == r.key && r.gets.Add(1) == 1 && r.interleave != nil {
		r.interleave()
	}
	return err
}

// TestAResultEventRacingAReconcileIsNotLost is Review Focus 5: when the
// results consumer and a reconcile write one job at once, neither rolls the
// other back. The loser's compare-and-swap conflicts and is redone from a
// fresh read.
func TestAResultEventRacingAReconcileIsNotLost(t *testing.T) {
	f := newDispatched(t, "tj-race", map[string]int32{"cpu": 1})
	ctx := context.Background()

	// A spec edit the reconciler has not observed yet: its next write moves
	// status.observedGeneration.
	live := f.get(t)
	live.Spec.Priority = 50
	require.NoError(t, f.c.Update(ctx, live))
	require.EqualValues(t, 2, f.get(t).Generation)

	// The event's write loses: a reconcile lands inside the results
	// consumer's read-to-apply window. Its apply must conflict, and the
	// retry, from a fresh read, must carry both changes.
	consumer := *f.r
	racing := &interleavingReader{Reader: f.c, key: types.NamespacedName{Namespace: f.ns, Name: f.tj.Name}}
	racing.interleave = func() { reconcileTJ(t, f.r, f.ns, f.tj.Name) }
	consumer.Reader = racing
	require.NoError(t, deliver(t, &consumer, live, claimed(1, "pool-xyz")))
	assert.GreaterOrEqual(t, racing.gets.Load(), int32(2), "the event's write must have been redone from a fresh read")
	got := f.get(t)
	assert.EqualValues(t, 2, got.Status.ObservedGeneration, "the reconcile's write was not rolled back")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseRunning, got.Status.Phase, "the event's write landed on the retry")
	assert.Equal(t, "pool-xyz", got.Status.WorkerPod)
	assert.NotNil(t, got.Status.StartedAt)

	// The reconcile's write loses: seeded from a read the event overtook.
	stale := f.get(t)
	require.NoError(t, deliver(t, f.r, stale, task.StatusEvent{
		Kind: task.EventProgress, Attempt: 1, Pod: "pool-xyz",
		Progress: &transcodev1alpha1.Progress{Percent: 42, UpdatedAt: metav1.Now()},
	}))
	stale.Status.Message = "reconcile error: something transient"
	err := f.r.PatchCASForTest(ctx, stale, &stale.Status)
	require.True(t, apierrors.IsConflict(err), "the stale write must conflict, got %v", err)
	got = f.get(t)
	require.NotNil(t, got.Status.Progress, "the event's write was not rolled back")
	assert.EqualValues(t, 42, got.Status.Progress.Percent)

	// The requeued reconcile reads fresh and keeps the event's fields.
	reconcileTJ(t, f.r, f.ns, f.tj.Name)
	got = f.get(t)
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseRunning, got.Status.Phase)
	assert.Equal(t, "pool-xyz", got.Status.WorkerPod)
	assert.EqualValues(t, 42, got.Status.Progress.Percent)
	assert.EqualValues(t, 2, got.Status.ObservedGeneration)
}

// failQueuedWrite fails the first status apply that records phase Queued,
// standing in for the apiserver blip -- or a rollout cancelling ctx, or
// leadership moving -- between dispatch's publish and its Queued write.
type failQueuedWrite struct {
	client.Client
	remaining *atomic.Int32
}

func (c failQueuedWrite) Status() client.SubResourceWriter {
	return failQueuedStatus{SubResourceWriter: c.Client.Status(), remaining: c.remaining}
}

type failQueuedStatus struct {
	client.SubResourceWriter
	remaining *atomic.Int32
}

func (w failQueuedStatus) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
	if ac, ok := obj.(*transcodeac.TranscodeJobApplyConfiguration); ok && ac.Status != nil && ac.Status.Phase != nil &&
		*ac.Status.Phase == transcodev1alpha1.TranscodeJobPhaseQueued && w.remaining.Add(-1) >= 0 {
		return apierrors.NewServiceUnavailable("injected: the Queued write is lost")
	}
	return w.SubResourceWriter.Apply(ctx, obj, opts...)
}

// TestALostDispatchWriteIsAdoptedFromTheWorkersEvent is ruling R16: dispatch
// published attempt 1 and then lost its Queued write, so the job still reads
// Planned at attempts 0 while a worker runs the task. The worker's first
// event proves the attempt was published and is adopted -- Queued, then its
// own change, in one write -- instead of being dropped as stale, which would
// leave the next dispatch to mark the job Queued with no task behind it.
func TestALostDispatchWriteIsAdoptedFromTheWorkersEvent(t *testing.T) {
	_, c := startEnv(t)
	ctx := context.Background()
	const ns = "tj-adopt"
	newNamespace(t, c, ns)
	newRootFolder(t, c, ns, "/data/media/movies")
	tp := newProfile(t, c, "hevc", "hash1", nil)
	newMediaFile(t, c, ns, "heat", "probe1", ptr.To(h264Probe()))
	newTJ(t, c, ns, "heat-hevc", "heat", "hevc", "probe1", nil)
	r := newReconciler(t, c, map[string]int32{"cpu": 1})
	remaining := &atomic.Int32{}
	remaining.Store(1)
	r.Client = failQueuedWrite{Client: c, remaining: remaining}

	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "heat-hevc"}})
	require.Error(t, err, "the lost Queued write surfaces as a reconcile error")
	lost := getTJ(t, c, ns, "heat-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, lost.Status.Phase)
	require.EqualValues(t, 0, lost.Status.Attempts)
	tasks := takeTasks(t, r.Bus, tp.UID, "cpu", 5*time.Second) // a worker takes it
	require.Equal(t, []int32{1}, attemptsOf(tasks), "the task was published before the write was lost")

	// An event that does not name its class cannot be adopted: acked, and
	// nothing changes.
	require.NoError(t, deliver(t, r, lost, claimed(1, "pool-a")))
	assert.Equal(t, lost.ResourceVersion, getTJ(t, c, ns, "heat-hevc").ResourceVersion)

	ev := claimed(1, "pool-a")
	ev.Class = transcodev1alpha1.HardwareCPU
	require.NoError(t, deliver(t, r, lost, ev))
	got := getTJ(t, c, ns, "heat-hevc")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseRunning, got.Status.Phase)
	assert.EqualValues(t, 1, got.Status.Attempts)
	assert.Equal(t, transcodev1alpha1.HardwareCPU, got.Status.Hardware)
	require.NotNil(t, got.Status.JobRef)
	assert.Equal(t, pool.Name(pool.Key{Profile: tp.Name, ProfileUID: tp.UID, Class: "cpu"}), *got.Status.JobRef)
	assert.Equal(t, "pool-a", got.Status.WorkerPod)
	assert.True(t, k8s.IsConditionTrue(got.Status.Conditions, transcodev1alpha1.TranscodeJobConditionJobCreated))

	fin := finished(1, task.OutcomeSucceeded, "", "")
	fin.Class = transcodev1alpha1.HardwareCPU
	require.NoError(t, deliver(t, r, got, fin))
	got = getTJ(t, c, ns, "heat-hevc")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseSucceeded, got.Status.Phase)
	assert.EqualValues(t, 1, got.Status.Attempts)

	// Admission again: nothing is dispatched a second time, nothing
	// regresses.
	reconcileTJ(t, r, ns, "heat-hevc")
	assert.Empty(t, takeTasks(t, r.Bus, tp.UID, "cpu", 300*time.Millisecond), "no second task for attempt 1")
	again := getTJ(t, c, ns, "heat-hevc")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseSucceeded, again.Status.Phase)
	assert.EqualValues(t, 1, again.Status.Attempts)
	assert.Equal(t, got.Status.JobRef, again.Status.JobRef)
}

// TestAClaimFromAnotherClassCorrectsTheRecord: a re-dispatch to another
// class was absorbed as a duplicate of the attempt's first publish, so the
// task is on the first class's queue. The worker's claim, which names the
// class it took the task from, puts the record right.
func TestAClaimFromAnotherClassCorrectsTheRecord(t *testing.T) {
	f := newDispatched(t, "tj-reclass", map[string]int32{"cpu": 1})
	require.Equal(t, transcodev1alpha1.HardwareCPU, f.tj.Status.Hardware)

	ev := claimed(1, "pool-gpu")
	ev.Class = transcodev1alpha1.HardwareNVIDIA
	require.NoError(t, deliver(t, f.r, f.tj, ev))
	got := f.get(t)
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseRunning, got.Status.Phase)
	assert.Equal(t, transcodev1alpha1.HardwareNVIDIA, got.Status.Hardware)
	require.NotNil(t, got.Status.JobRef)
	assert.Equal(t, pool.Name(pool.Key{Profile: f.tp.Name, ProfileUID: f.tp.UID, Class: "nvidia"}), *got.Status.JobRef)
}

// failingPublisher is a bus whose publishes all fail, as NATS does when the
// work stream is full or the broker is down.
type failingPublisher struct{ events.Bus }

func (failingPublisher) Publish(context.Context, string, *events.Envelope, ...events.PublishOption) (events.Receipt, error) {
	return events.Receipt{}, events.ErrQueueFull
}

// TestNoQueuedWithoutAPublish is spec §8: nothing is marked Queued that was
// not published. A failed publish leaves the job Planned at attempts 0,
// saying why, and the next pass dispatches it once the bus is back.
func TestNoQueuedWithoutAPublish(t *testing.T) {
	_, c := startEnv(t)
	const ns = "tj-nopublish"
	newNamespace(t, c, ns)
	newRootFolder(t, c, ns, "/data/media/movies")
	tp := newProfile(t, c, "hevc", "hash1", nil)
	newMediaFile(t, c, ns, "heat", "probe1", ptr.To(h264Probe()))
	newTJ(t, c, ns, "heat-hevc", "heat", "hevc", "probe1", nil)
	r := newReconciler(t, c, map[string]int32{"cpu": 1})
	bus := r.Bus
	r.Bus = failingPublisher{Bus: bus}

	_, err := r.Reconcile(context.Background(), reconcile.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "heat-hevc"}})
	require.ErrorIs(t, err, events.ErrQueueFull)
	got := getTJ(t, c, ns, "heat-hevc")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, got.Status.Phase)
	assert.EqualValues(t, 0, got.Status.Attempts)
	assert.Nil(t, got.Status.JobRef)
	assert.Contains(t, got.Status.Message, "work queue full")

	r.Bus = bus
	reconcileTJ(t, r, ns, "heat-hevc")
	got = getTJ(t, c, ns, "heat-hevc")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, got.Status.Phase)
	assert.Equal(t, []int32{1}, attemptsOf(takeTasks(t, bus, tp.UID, "cpu", 5*time.Second)))
}

// TestASourceUnderNoRootFolderBlocksAtDispatch is spec §17.5: the worker
// never touches a file outside a RootFolder, so a source under none is
// blocked InvalidSource at dispatch, without publishing a task.
func TestASourceUnderNoRootFolderBlocksAtDispatch(t *testing.T) {
	_, c := startEnv(t)
	const ns = "tj-noroot"
	newNamespace(t, c, ns)
	tp := newProfile(t, c, "hevc", "hash1", nil)
	newMediaFile(t, c, ns, "heat", "probe1", ptr.To(h264Probe()))
	newTJ(t, c, ns, "heat-hevc", "heat", "hevc", "probe1", nil)
	r := newReconciler(t, c, map[string]int32{"cpu": 1})

	reconcileTJ(t, r, ns, "heat-hevc")
	got := getTJ(t, c, ns, "heat-hevc")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseFailed, got.Status.Phase)
	blocked := k8s.FindCondition(got.Status.Conditions, transcodev1alpha1.ConditionBlocked)
	require.NotNil(t, blocked)
	assert.Equal(t, metav1.ConditionTrue, blocked.Status)
	assert.Equal(t, string(task.ReasonInvalidSource), blocked.Reason)
	assert.Contains(t, got.Status.Message, "not under any RootFolder")
	assert.EqualValues(t, 0, got.Status.Attempts)
	assert.Empty(t, takeTasks(t, r.Bus, tp.UID, "cpu", 300*time.Millisecond), "nothing is published")
}

// TestADeadLetteredTaskBlocksTheJob is spec §18.3's last row: a task the
// queue dead-lettered will never be reported on, so the DLQ projector's
// annotation naming it blocks the job, reason DeadLettered.
func TestADeadLetteredTaskBlocksTheJob(t *testing.T) {
	f := newDispatched(t, "tj-dlq-task", map[string]int32{"cpu": 1})
	ctx := context.Background()
	live := f.get(t)
	subject := events.WorkTranscodeTaskSubject(string(f.tp.UID), "cpu", string(live.UID))
	live.Annotations = map[string]string{k8s.AnnotationDeadLettered: subject + "@2026-09-23T10:00:00Z"}
	require.NoError(t, f.c.Update(ctx, live))

	reconcileTJ(t, f.r, f.ns, f.tj.Name)
	got := f.get(t)
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseFailed, got.Status.Phase)
	blocked := k8s.FindCondition(got.Status.Conditions, transcodev1alpha1.ConditionBlocked)
	require.NotNil(t, blocked)
	assert.Equal(t, string(task.ReasonDeadLettered), blocked.Reason)
	assert.True(t, k8s.IsConditionTrue(got.Status.Conditions, k8s.ConditionDeadLettered), "the annotation is folded too")
}

// TestAnAutoGPUFailureFallsBackToCPU is spec §18.5's fallback: an auto job
// whose GPU attempt reports GPUEncodeFailed records why, goes back to
// Planned with no wait, and is dispatched again to the profile's CPU pool.
func TestAnAutoGPUFailureFallsBackToCPU(t *testing.T) {
	_, c := startEnv(t)
	const ns = "tj-fallback"
	newNamespace(t, c, ns)
	newRootFolder(t, c, ns, "/data/media/movies")
	tp := newProfile(t, c, "nvenc", "hash1", func(p *transcodev1alpha1.TranscodeProfile) {
		p.Spec.Hardware = transcodev1alpha1.HardwareNVIDIA
	})
	newMediaFile(t, c, ns, "heat", "probe1", ptr.To(h264Probe()))
	newTJ(t, c, ns, "heat-nvenc", "heat", "nvenc", "probe1", func(tj *transcodev1alpha1.TranscodeJob) {
		tj.Spec.Hardware = ptr.To(transcodev1alpha1.HardwareAuto)
	})
	nvidiaNode(t, c, "gpu-1", "1") // an auto job goes to a GPU only with a GPU node to go to (Task 13)
	r := newReconciler(t, c, map[string]int32{"cpu": 1, "nvidia": 1})

	reconcileTJ(t, r, ns, "heat-nvenc")
	tj := getTJ(t, c, ns, "heat-nvenc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, tj.Status.Phase, "message: %s", tj.Status.Message)
	require.Equal(t, transcodev1alpha1.HardwareNVIDIA, tj.Status.Hardware)
	require.Equal(t, []int32{1}, attemptsOf(takeTasks(t, r.Bus, tp.UID, "nvidia", 5*time.Second)))

	require.NoError(t, deliver(t, r, tj, claimed(1, "pool-gpu")))
	require.NoError(t, deliver(t, r, tj, finished(1, task.OutcomeFailed, task.ReasonGPUEncodeFailed, "nvenc: no capable devices")))
	tj = getTJ(t, c, ns, "heat-nvenc")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, tj.Status.Phase)
	assert.Nil(t, tj.Status.NextAttemptAt, "a fallback does not wait")
	assert.Contains(t, tj.Status.FallbackReason, "GPUEncodeFailed")

	reconcileTJ(t, r, ns, "heat-nvenc")
	tj = getTJ(t, c, ns, "heat-nvenc")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, tj.Status.Phase)
	assert.Equal(t, transcodev1alpha1.HardwareCPU, tj.Status.Hardware)
	assert.EqualValues(t, 2, tj.Status.Attempts)
	assert.Equal(t, pool.Name(pool.Key{Profile: tp.Name, ProfileUID: tp.UID, Class: "cpu"}), *tj.Status.JobRef)
	cpuTasks := takeTasks(t, r.Bus, tp.UID, "cpu", 5*time.Second)
	require.Len(t, cpuTasks, 1)
	assert.EqualValues(t, 2, cpuTasks[0].Attempt)
	assert.Equal(t, "cpu", cpuTasks[0].Class)
}

// TestTransientFailureDoesNotReleaseStatus drives a job, through real
// status events, to a populated steady state -- a requeued attempt with its
// plan, hardware, stderr tail, nextAttemptAt and conditions -- then makes
// the next dispatch fail half way. The error-path write must re-declare
// every leaf (nothing released), and squasharr must stay the only owner.
func TestTransientFailureDoesNotReleaseStatus(t *testing.T) {
	f := newDispatched(t, "tj-release", map[string]int32{"cpu": 2})
	ctx := context.Background()
	now := time.Now()
	f.r.Now = func() time.Time { return now }
	require.NoError(t, deliver(t, f.r, f.tj, claimed(1, "pool-a")))
	require.NoError(t, deliver(t, f.r, f.tj, task.StatusEvent{
		Kind: task.EventProgress, Attempt: 1,
		Progress: &transcodev1alpha1.Progress{Percent: 42, UpdatedAt: metav1.Now()},
	}))
	ev := finished(1, task.OutcomeFailed, task.ReasonRetriable, "ffmpeg exited 1")
	ev.StderrTail = "frame=1000 error"
	require.NoError(t, deliver(t, f.r, f.tj, ev))

	steady := f.get(t)
	require.Equal(t, transcodev1alpha1.TranscodeJobPhasePlanned, steady.Status.Phase)
	require.NotNil(t, steady.Status.NextAttemptAt)

	now = now.Add(2 * time.Minute)
	broken := *f.r
	broken.Bus = failingPublisher{Bus: f.r.Bus}
	_, err := broken.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: f.ns, Name: f.tj.Name}})
	require.Error(t, err)

	got := f.get(t)
	assert.Contains(t, got.Status.Message, "work queue full")
	assert.Equal(t, steady.Status.Phase, got.Status.Phase)
	assert.Equal(t, steady.Status.JobRef, got.Status.JobRef)
	assert.Equal(t, steady.Status.Attempts, got.Status.Attempts)
	assert.Equal(t, steady.Status.StartedAt, got.Status.StartedAt)
	assert.Equal(t, steady.Status.NextAttemptAt, got.Status.NextAttemptAt)
	assert.Equal(t, steady.Status.Hardware, got.Status.Hardware)
	assert.Equal(t, "frame=1000 error", got.Status.StderrTail)
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
	assert.Equal(t, []string{string(k8s.ManagerSquasharr)}, statusOwners(got))
	owned := statusFieldsOf(t, got, k8s.ManagerSquasharr)
	for _, fld := range []string{
		"f:phase", "f:plan", "f:jobRef", "f:attempts", "f:startedAt", "f:message", "f:conditions",
		"f:observedGeneration", "f:hardware", "f:nextAttemptAt", "f:stderrTail",
	} {
		assert.Contains(t, owned, fld)
	}
}

// TestWatchesWakeTheController runs the real manager, with the results
// consumer subscribed on the bus, and never calls Reconcile by hand. Each
// step is only reachable through one wake:
//   - the profile's status.hash landing wakes a Pending job (profile watch),
//     which is planned and dispatched;
//   - a finished event published on the results subject moves that job to
//     Succeeded (the results consumer);
//   - and frees its slot, so the second job -- waiting Planned behind a
//     one-slot budget, its own periodic requeue a minute away -- is
//     dispatched by the consumer's admission wake.
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
	r := newReconciler(t, mgr.GetClient(), map[string]int32{"cpu": 1})
	r.Reader = mgr.GetAPIReader()
	require.NoError(t, r.SetupWithManager(mgr))
	require.NoError(t, mgr.Add(r.ResultsConsumer()))
	done := make(chan struct{})
	go func() { defer close(done); _ = mgr.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })

	const ns = "tj-watch"
	newNamespace(t, c, ns)
	newRootFolder(t, c, ns, "/data/media/movies")
	newProfile(t, c, "hevc", "", nil)
	newMediaFile(t, c, ns, "a", "pa", ptr.To(h264Probe()))
	newMediaFile(t, c, ns, "b", "pb", ptr.To(h264Probe()))
	newTJ(t, c, ns, "a-hevc", "a", "hevc", "pa", nil)

	phase := func(name string) transcodev1alpha1.TranscodeJobPhase { return getTJ(t, c, ns, name).Status.Phase }
	require.Eventually(t, func() bool { return phase("a-hevc") == transcodev1alpha1.TranscodeJobPhasePending },
		20*time.Second, 100*time.Millisecond)

	setProfileHash(t, c, "hevc", "hash1")
	require.Eventually(t, func() bool { return phase("a-hevc") == transcodev1alpha1.TranscodeJobPhaseQueued },
		20*time.Second, 100*time.Millisecond, "the profile hash must wake the job, which is dispatched")

	newTJ(t, c, ns, "b-hevc", "b", "hevc", "pb", nil)
	require.Eventually(t, func() bool { return phase("b-hevc") == transcodev1alpha1.TranscodeJobPhasePlanned },
		20*time.Second, 100*time.Millisecond)
	require.Never(t, func() bool { return phase("b-hevc") != transcodev1alpha1.TranscodeJobPhasePlanned },
		time.Second, 100*time.Millisecond, "one cpu slot, held by the first job")

	// The pool worker's report, on the stream, as Serve publishes it.
	a := getTJ(t, c, ns, "a-hevc")
	ev := task.StatusEvent{
		Job: schema.Ref{Namespace: ns, Name: a.Name, UID: string(a.UID)}, Attempt: 1, Delivery: 1, Seq: 1,
		Kind: task.EventFinished, Outcome: task.OutcomeSucceeded, At: time.Now().UTC(),
		Result: &transcodev1alpha1.Result{OutputPath: "/data/media/movies/a.mkv", OutputSizeBytes: 1, OutputToSourcePercent: 50},
	}
	sch, data, err := schema.Encode(ev)
	require.NoError(t, err)
	id := events.MsgIDForTranscodeEvent(ev.Job.UID, 1, 1, 1)
	_, err = r.Bus.Publish(ctx, events.WorkTranscodeResultSubject(ev.Job.UID),
		&events.Envelope{ID: id, Type: "transcode.StatusEvent", Schema: sch, Key: ns + "/" + a.Name, Time: ev.At, Data: data},
		events.WithMsgID(id), events.WithExpectStream(events.StreamWorkSquasharr))
	require.NoError(t, err)

	require.Eventually(t, func() bool { return phase("a-hevc") == transcodev1alpha1.TranscodeJobPhaseSucceeded },
		20*time.Second, 100*time.Millisecond, "the results consumer must apply the finished event")
	require.Eventually(t, func() bool { return phase("b-hevc") == transcodev1alpha1.TranscodeJobPhaseQueued },
		20*time.Second, 100*time.Millisecond, "the freed slot must wake admission at once, not after a minute")
}

// staleClient serves one TranscodeJob from a fixed snapshot, standing in for
// an informer that has not yet seen the terminal write.
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
// observed exactly once per job, on the write that made it terminal -- not
// again when the finished event is redelivered, nor when a lagging cache
// hands the Running job back to a reconcile.
func TestTerminalMetricsObservedOnce(t *testing.T) {
	f := newDispatched(t, "tj-metrics", map[string]int32{"cpu": 2})
	require.NoError(t, deliver(t, f.r, f.tj, claimed(1, "pool-a")))
	running := f.get(t)
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseRunning, running.Status.Phase)

	durBefore := histogramCount(t, metrics.TranscodeDuration, "cpu", "hd", "succeeded")
	sizeBefore := histogramCount(t, metrics.TranscodeSizeRatio, "cpu", "hd")

	ev := finished(1, task.OutcomeSucceeded, "", "")
	ev.Result = &transcodev1alpha1.Result{OutputPath: running.Spec.SourcePath, OutputSizeBytes: 1 << 30, OutputToSourcePercent: 45}
	require.NoError(t, deliver(t, f.r, running, ev))
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseSucceeded, f.get(t).Status.Phase)

	// The same event again, as a redelivery would bring it.
	require.NoError(t, deliver(t, f.r, running, ev))
	// A reconcile whose cache still says Running: it reads through Reader.
	stale := newReconciler(t, staleClient{Client: f.c, snapshot: running}, map[string]int32{"cpu": 2})
	stale.Reader = f.c
	reconcileTJ(t, stale, f.ns, running.Name)
	// And an ordinary re-reconcile of the terminal object.
	reconcileTJ(t, f.r, f.ns, running.Name)

	assert.Equal(t, durBefore+1, histogramCount(t, metrics.TranscodeDuration, "cpu", "hd", "succeeded"))
	assert.Equal(t, sizeBefore+1, histogramCount(t, metrics.TranscodeSizeRatio, "cpu", "hd"))
}
