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

package status_test

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	transcodeac "github.com/mediactl/clustarr/api/applyconfiguration/transcode/transcode/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/squasharr/status"
)

func newTestClient(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })

	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return c
}

func newTranscodeJob(t *testing.T, ctx context.Context, c client.Client, ns, name string) *transcodev1alpha1.TranscodeJob {
	t.Helper()
	tj := &transcodev1alpha1.TranscodeJob{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: transcodev1alpha1.TranscodeJobSpec{
			MediaFileRef: "arrival-2016",
			ProfileRef:   "default",
			SourcePath:   "/data/movies/Arrival (2016)/Arrival.2016.1080p.mkv",
		},
	}
	require.NoError(t, c.Create(ctx, tj))
	return tj
}

func newTranscodeProfile(t *testing.T, ctx context.Context, c client.Client, name string) *transcodev1alpha1.TranscodeProfile {
	t.Helper()
	tp := &transcodev1alpha1.TranscodeProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name},
	}
	require.NoError(t, c.Create(ctx, tp))
	return tp
}

// This is the test the TranscodeJob half of this package exists for.
//
// squasharr owns all of TranscodeJob.status, and server-side apply replaces
// a manager's ownership set on every apply, so [status.ControllerFields]
// must re-declare every field on every write -- the reconciler's AND the
// results consumer's -- or a write that changes one field releases the
// others. progress, result and stderrTail are the fields most at risk: until
// the pools, a separate worker manager owned them, and a renderer that still
// left them out would zero the encode's progress on every reconcile.
//
// The object is driven to a populated steady state FIRST, every field set,
// before any apply that changes only one of them. A test that starts from a
// blank object cannot observe a release, because there is nothing to
// release.
func TestTheJobDeclarationIsComplete(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	const ns = "squasharr-status-split"
	require.NoError(t, client.IgnoreAlreadyExists(
		c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})))

	tj := newTranscodeJob(t, ctx, c, ns, "split")
	now := metav1.NewTime(time.Now().UTC().Truncate(time.Second))
	jobRef := "split-a1b2c3d4"

	// Steady state: every field squasharr owns.
	require.NoError(t, status.Patch(ctx, c, k8s.ManagerSquasharr, tj,
		func(ac *transcodeac.TranscodeJobStatusApplyConfiguration) {
			ac.WithObservedGeneration(1).
				WithPhase(transcodev1alpha1.TranscodeJobPhaseRunning).
				WithPlan(transcodeac.Plan().
					WithEncoder("libx265").
					WithMode(transcodev1alpha1.PlanModeTranscode).
					WithHDRMode("hdr10").
					WithVideoArgs("-c:v", "libx265", "-crf", "22").
					WithArgsHash("deadbeef")).
				WithJobRef(jobRef).
				WithAttempts(1).
				WithStartedAt(now).
				WithMessage("encoding").
				WithWorkerPod("split-worker-0").
				WithHardware(transcodev1alpha1.HardwareNVIDIA).
				WithFallbackReason("gpuBusy: no free nvidia slot").
				WithNextAttemptAt(now).
				WithProgress(transcodeac.Progress().
					WithPercent(42).
					WithFrame(1200).
					WithFPSMilli(23976).
					WithSpeedMilli(1500).
					WithOutTimeMillis(60000).
					WithBitrateKbps(4500).
					WithUpdatedAt(now)).
				WithResult(transcodeac.Result().
					WithOutputPath("/data/movies/Arrival (2016)/Arrival.2016.1080p.mkv").
					WithOutputSizeBytes(4 << 30).
					WithOutputToSourcePercent(45).
					WithVMAFCentis(9542)).
				WithStderrTail("frame=1200 fps=24 speed=1.5x").
				WithConditions(k8s.ConditionAC(metav1.Condition{
					Type: transcodev1alpha1.TranscodeJobConditionJobCreated, Status: metav1.ConditionTrue,
					Reason: "JobCreated", LastTransitionTime: now, ObservedGeneration: 1,
				}))
		}))

	var seeded transcodev1alpha1.TranscodeJob
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(tj), &seeded))
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseRunning, seeded.Status.Phase, "setup: the steady state did not land")
	require.NotNil(t, seeded.Status.Result, "setup: the steady state did not land")
	require.EqualValues(t, 45, seeded.Status.Result.OutputToSourcePercent, "setup: the steady state did not land")

	// Re-apply changing only observedGeneration and a condition's reason --
	// the reconciler's kind of write -- through the CAS path both of
	// squasharr's writers use.
	require.NoError(t, status.PatchCAS(ctx, c, &seeded,
		func(ac *transcodeac.TranscodeJobStatusApplyConfiguration) {
			ac.WithObservedGeneration(2).WithConditions(k8s.ConditionAC(metav1.Condition{
				Type: transcodev1alpha1.TranscodeJobConditionJobCreated, Status: metav1.ConditionTrue,
				Reason: "StillCreated", LastTransitionTime: now, ObservedGeneration: 2,
			}))
		}))
	var afterReconcile transcodev1alpha1.TranscodeJob
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(tj), &afterReconcile))
	assertJobIntact(t, afterReconcile.Status, "a write that changed only observedGeneration")
	assert.EqualValues(t, 2, afterReconcile.Status.ObservedGeneration, "the write's own field did not update")

	// Re-apply changing only stderrTail -- the results consumer's kind of
	// write -- with the conditions sent again, once, as every writer does.
	afterReconcile.Status.StderrTail = "frame=2400 fps=24 speed=1.5x"
	require.NoError(t, status.PatchCAS(ctx, c, &afterReconcile,
		func(ac *transcodeac.TranscodeJobStatusApplyConfiguration) {
			ac.WithConditions(k8s.ConditionACs(afterReconcile.Status.Conditions)...)
		}))
	var afterEvent transcodev1alpha1.TranscodeJob
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(tj), &afterEvent))
	assertJobIntact(t, afterEvent.Status, "a write that changed only stderrTail")
	assert.Equal(t, "frame=2400 fps=24 speed=1.5x", afterEvent.Status.StderrTail, "the write's own field did not update")

	// status.plan, status.progress and status.result are structs, and server-
	// side apply tracks ownership per LEAF inside them rather than for the
	// sub-object as a whole. A renderer that sent only one leaf would pass a
	// NotNil check while silently releasing the others on every write.
	if p := afterEvent.Status.Plan; assert.NotNil(t, p, "released status.plan") {
		assert.Equal(t, "libx265", p.Encoder, "released plan.encoder")
		assert.Equal(t, transcodev1alpha1.PlanModeTranscode, p.Mode, "released plan.mode")
		assert.Equal(t, "hdr10", p.HDRMode, "released plan.hdrMode")
		assert.Equal(t, []string{"-c:v", "libx265", "-crf", "22"}, p.VideoArgs, "released plan.videoArgs")
		assert.Equal(t, "deadbeef", p.ArgsHash, "released plan.argsHash")
	}
	if pr := afterEvent.Status.Progress; assert.NotNil(t, pr, "released status.progress") {
		assert.EqualValues(t, 42, pr.Percent, "released progress.percent")
		assert.EqualValues(t, 1200, pr.Frame, "released progress.frame")
		assert.EqualValues(t, 23976, pr.FPSMilli, "released progress.fpsMilli")
		assert.EqualValues(t, 1500, pr.SpeedMilli, "released progress.speedMilli")
		assert.EqualValues(t, 60000, pr.OutTimeMillis, "released progress.outTimeMillis")
		assert.EqualValues(t, 4500, pr.BitrateKbps, "released progress.bitrateKbps")
		assert.False(t, pr.UpdatedAt.IsZero(), "released progress.updatedAt")
	}
	if r := afterEvent.Status.Result; assert.NotNil(t, r, "released status.result") {
		assert.Equal(t, "/data/movies/Arrival (2016)/Arrival.2016.1080p.mkv", r.OutputPath, "released result.outputPath")
		assert.EqualValues(t, 4<<30, r.OutputSizeBytes, "released result.outputSizeBytes")
		assert.EqualValues(t, 45, r.OutputToSourcePercent, "released result.outputToSourcePercent")
		if assert.NotNil(t, r.VMAFCentis, "released result.vmafCentis") {
			assert.EqualValues(t, 9542, *r.VMAFCentis)
		}
	}

	// Conditions is a listType=map too, and ControllerFields leaves it
	// unseeded so the caller sets it exactly once. Re-applying with a
	// changed reason must update the entry rather than duplicate or drop it.
	if assert.Len(t, afterEvent.Status.Conditions, 1, "the conditions list did not survive") {
		assert.Equal(t, "StillCreated", afterEvent.Status.Conditions[0].Reason)
	}

	// Finally, assert OWNERSHIP rather than values: managedFields is the only
	// place an over-claim or a stray second manager is visible.
	assertJobManagedFields(t, &afterEvent)
}

// assertJobIntact checks every field the steady state set survived a write
// that did not mean to change it.
func assertJobIntact(t *testing.T, st transcodev1alpha1.TranscodeJobStatus, who string) {
	t.Helper()
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseRunning, st.Phase, who+" released phase")
	assert.NotNil(t, st.Plan, who+" released plan")
	if assert.NotNil(t, st.JobRef, who+" released jobRef") {
		assert.Equal(t, "split-a1b2c3d4", *st.JobRef)
	}
	assert.EqualValues(t, 1, st.Attempts, who+" released attempts")
	assert.NotNil(t, st.StartedAt, who+" released startedAt")
	assert.Equal(t, "encoding", st.Message, who+" released message")
	assert.NotEmpty(t, st.Conditions, who+" released conditions")
	assert.Equal(t, "split-worker-0", st.WorkerPod, who+" released workerPod")
	assert.Equal(t, transcodev1alpha1.HardwareNVIDIA, st.Hardware, who+" released hardware")
	assert.Equal(t, "gpuBusy: no free nvidia slot", st.FallbackReason, who+" released fallbackReason")
	assert.NotNil(t, st.NextAttemptAt, who+" released nextAttemptAt")
	if assert.NotNil(t, st.Progress, who+" released status.progress") {
		assert.EqualValues(t, 42, st.Progress.Percent, who+" released progress.percent")
	}
	if assert.NotNil(t, st.Result, who+" released status.result") {
		assert.EqualValues(t, 45, st.Result.OutputToSourcePercent, who+" released result.outputToSourcePercent")
	}
	assert.NotEmpty(t, st.StderrTail, who+" released stderrTail")
}

// assertJobManagedFields reads the apiserver's own record of who owns what
// on TranscodeJob.status: squasharr, and only squasharr, owns exactly the
// set it declares.
func assertJobManagedFields(t *testing.T, tj *transcodev1alpha1.TranscodeJob) {
	t.Helper()

	// finishedAt is absent here because the job in this test never leaves
	// phase=Running -- a finishedAt would be a fabricated value, not
	// something either apply set. That is the field's documented absence
	// (ControllerFields omits it while nil), not a release.
	want := jsonNames(transcodev1alpha1.TranscodeJobStatus{}, without(jobControllerOwned, "FinishedAt"))

	var seen bool
	for _, entry := range tj.ManagedFields {
		if entry.Subresource != "status" || entry.FieldsV1 == nil {
			continue
		}
		require.Equalf(t, string(k8s.ManagerSquasharr), entry.Manager,
			"%q owns fields on TranscodeJob.status; only squasharr may", entry.Manager)
		seen = true

		var fields map[string]any
		require.NoError(t, json.Unmarshal(entry.FieldsV1.GetRawBytes(), &fields))
		st, ok := fields["f:status"].(map[string]any)
		require.True(t, ok, "squasharr has a status managedFields entry with no f:status")

		var got []string
		for key := range st {
			got = append(got, strings.TrimPrefix(key, "f:"))
		}
		sort.Strings(got)
		assert.Equal(t, want, got, "the apiserver records squasharr as owning a different set than it declares")
	}
	assert.True(t, seen, "squasharr owns nothing on TranscodeJob.status; its apply never landed")
}

// PatchCAS is the lost-update guard of spec §18.2: a write seeded from a
// read that another write has since overtaken must fail with a Conflict, not
// roll that other write back.
func TestPatchCASRejectsAStaleResourceVersion(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()
	const ns = "squasharr-status-cas"
	require.NoError(t, client.IgnoreAlreadyExists(
		c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})))
	job := newTranscodeJob(t, ctx, c, ns, "cas")
	stale := job.DeepCopy()

	job.Status.Message = "first"
	require.NoError(t, status.PatchCAS(ctx, c, job, nil))

	stale.Status.Message = "second"
	err := status.PatchCAS(ctx, c, stale, nil)
	require.True(t, apierrors.IsConflict(err), "a stale write must conflict, got %v", err)

	var got transcodev1alpha1.TranscodeJob
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(job), &got))
	assert.Equal(t, "first", got.Status.Message, "the stale write must not have landed")

	// Without a resourceVersion there is nothing to be conditional on, so
	// PatchCAS refuses rather than silently applying unconditionally.
	got.ResourceVersion = ""
	require.ErrorContains(t, status.PatchCAS(ctx, c, &got, nil), "no resourceVersion")
}

// This is the test the TranscodeProfile half of this package exists for.
//
// TranscodeProfile.status has one writer, so there is no sibling to release
// fields out from under -- but PatchProfile still must re-declare every field
// on every apply, or a reconcile that changes only runningJobs would drop
// hash, matchingFiles and pendingJobs right back to zero on the object.
func TestTheProfileManagerDeclarationIsComplete(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	tp := newTranscodeProfile(t, ctx, c, "split-profile")
	now := metav1.NewTime(time.Now().UTC().Truncate(time.Second))

	require.NoError(t, status.PatchProfile(ctx, c, k8s.ManagerSquasharr, tp,
		func(ac *transcodeac.TranscodeProfileStatusApplyConfiguration) {
			ac.WithObservedGeneration(1).
				WithHash("deadbeef").
				WithMatchingFiles(10).
				WithPendingJobs(4).
				WithRunningJobs(2).
				WithConditions(k8s.ConditionAC(metav1.Condition{
					Type: transcodev1alpha1.TranscodeProfileConditionReady, Status: metav1.ConditionTrue,
					Reason: "Ready", LastTransitionTime: now, ObservedGeneration: 1,
				}))
		}))

	var seeded transcodev1alpha1.TranscodeProfile
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(tp), &seeded))
	require.Equal(t, "deadbeef", seeded.Status.Hash, "setup: the steady state did not land")

	// Re-apply, changing only runningJobs. Conditions is set again with the
	// SAME content: a real reconciler recomputes and resends its full
	// condition set on every pass (ProfileFields leaves Conditions unseeded
	// for exactly this reason -- see the package doc), so a caller that
	// dropped the WithConditions call here would be simulating a reconciler
	// bug, not testing this package.
	require.NoError(t, status.PatchProfile(ctx, c, k8s.ManagerSquasharr, &seeded,
		func(ac *transcodeac.TranscodeProfileStatusApplyConfiguration) {
			ac.WithRunningJobs(3).WithConditions(k8s.ConditionAC(metav1.Condition{
				Type: transcodev1alpha1.TranscodeProfileConditionReady, Status: metav1.ConditionTrue,
				Reason: "Ready", LastTransitionTime: now, ObservedGeneration: 1,
			}))
		}))

	var after transcodev1alpha1.TranscodeProfile
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(tp), &after))
	assert.EqualValues(t, 1, after.Status.ObservedGeneration, "the re-apply released observedGeneration")
	assert.Equal(t, "deadbeef", after.Status.Hash, "the re-apply released hash")
	assert.EqualValues(t, 10, after.Status.MatchingFiles, "the re-apply released matchingFiles")
	assert.EqualValues(t, 4, after.Status.PendingJobs, "the re-apply released pendingJobs")
	assert.EqualValues(t, 3, after.Status.RunningJobs, "the re-apply did not update runningJobs")
	if assert.Len(t, after.Status.Conditions, 1, "the re-apply released conditions") {
		assert.Equal(t, "Ready", after.Status.Conditions[0].Reason)
	}

	assertProfileManagedFields(t, &after)
}

// assertProfileManagedFields is assertJobManagedFields' counterpart for
// TranscodeProfile.
func assertProfileManagedFields(t *testing.T, tp *transcodev1alpha1.TranscodeProfile) {
	t.Helper()

	var found bool
	for _, entry := range tp.ManagedFields {
		if entry.Subresource != "status" || entry.FieldsV1 == nil || entry.Manager != string(k8s.ManagerSquasharr) {
			continue
		}
		found = true

		var fields map[string]any
		require.NoError(t, json.Unmarshal(entry.FieldsV1.GetRawBytes(), &fields))
		st, ok := fields["f:status"].(map[string]any)
		require.True(t, ok, "squasharr has a status managedFields entry with no f:status")

		var got []string
		for key := range st {
			got = append(got, strings.TrimPrefix(key, "f:"))
		}
		sort.Strings(got)
		assert.Equal(t, jsonNames(transcodev1alpha1.TranscodeProfileStatus{}, profileOwned), got,
			"the apiserver records squasharr as owning a different set than it declares")
	}
	assert.True(t, found, "squasharr owns nothing on TranscodeProfile.status; its apply never landed")
}

func without(names []string, drop string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n != drop {
			out = append(out, n)
		}
	}
	return out
}

// jsonNames maps Go field names onto the JSON names managedFields uses, for
// whichever status struct typ is.
func jsonNames(typ any, goNames []string) []string {
	t := reflect.TypeOf(typ)
	out := make([]string, 0, len(goNames))
	for _, name := range goNames {
		f, ok := t.FieldByName(name)
		if !ok {
			continue
		}
		tag, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		out = append(out, tag)
	}
	sort.Strings(out)
	return out
}

// A manager other than squasharr must be refused rather than allowed to
// claim fields -- the retired squasharr-worker manager included.
func TestPatchRefusesAManagerOutsideTheSplit(t *testing.T) {
	for _, mgr := range []k8s.FieldManager{
		k8s.ManagerCatalogarr, k8s.ManagerGrabarr, k8s.FieldManager("squasharr-worker"), k8s.FieldManager("nonsense"),
	} {
		err := status.Patch(context.Background(), nil, mgr,
			&transcodev1alpha1.TranscodeJob{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "y"}}, nil)
		require.ErrorContains(t, err, "owns no part of TranscodeJob.status")
	}
}

// PatchProfile must refuse every manager but k8s.ManagerSquasharr.
func TestPatchProfileRefusesAManagerOutsideTheSplit(t *testing.T) {
	for _, mgr := range []k8s.FieldManager{k8s.ManagerSquasharrPool, k8s.ManagerCatalogarr, k8s.FieldManager("nonsense")} {
		err := status.PatchProfile(context.Background(), nil, mgr,
			&transcodev1alpha1.TranscodeProfile{ObjectMeta: metav1.ObjectMeta{Name: "x"}}, nil)
		require.ErrorContains(t, err, "owns no part of TranscodeProfile.status")
	}
}
