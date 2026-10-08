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

package remediation_test

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/remediationtest"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

func naming(path string) remediationtest.PlanFunc {
	return func(_ *remediation.View, _ int, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
		out.Naming = &catalogv1alpha1.NamingStatus{ExpectedPath: path}
		return remediation.Result{}, nil
	}
}

// passes reads clustarr_remediation_passes_total for file keys through
// client_model, not prometheus/testutil, whose godebug dependency go.mod
// does not declare. No test in this package runs in parallel, so a delta
// around a pass is that pass's.
func passes(outcome string) float64 {
	var m dto.Metric
	if err := metrics.RemediationPassesTotal.WithLabelValues(string(remediation.KindMediaFile), outcome).Write(&m); err != nil {
		panic(err)
	}
	return m.GetCounter().GetValue()
}

// A pass that changes nothing sends no status request at all (§3.7): about
// 95% of the old controller's status applies were no-ops.
func TestAnUnchangedPassAppliesNothing(t *testing.T) {
	e := remediationtest.Start(t)
	nn := e.File(t, "unchanged", "heat-abc", "/data/movies/Heat (1995)/heat.mkv")
	r := e.Reconciler(t, remediation.Bind[int](remediationtest.NamingFake(naming("/data/movies/Heat (1995)/Heat (1995).mkv"))))
	e.Pass(t, r, nn)
	require.NotNil(t, e.Get(t, nn).Status.Naming, "setup: the first pass applied")
	before := e.StatusPatches()
	unchanged := passes("unchanged")
	for range 3 {
		e.Pass(t, r, nn)
	}
	assert.Equal(t, before, e.StatusPatches(), "an unchanged pass sent a status request")
	assert.Equal(t, unchanged+3, passes("unchanged"))
}

// A planner that panics keeps its last block; every other planner's fields
// still move in the same apply; its PlannerError condition reads
// PlannerPanic; and catalog alone owns the status (§3.6).
func TestAPanickingPlannerKeepsItsBlock(t *testing.T) {
	e := remediationtest.Start(t)
	nn := e.File(t, "panic", "heat-abc", "/data/movies/heat.mkv")
	var n atomic.Int32
	nam := remediationtest.NamingFake(func(v *remediation.View, in int, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
		if n.Add(1) > 1 {
			out.Naming = &catalogv1alpha1.NamingStatus{ExpectedPath: "/data/movies/second.mkv"}
			panic("boom")
		}
		return naming("/data/movies/first.mkv")(v, in, out)
	})
	mk := remediationtest.MarkersFake(func(v *remediation.View, _ int, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
		out.Markers = &catalogv1alpha1.FileMarkers{Result: catalogv1alpha1.MarkersNotFound, ForProbeHash: "h" + v.Now.Format("150405"), FetchedAt: v.Now}
		return remediation.Result{}, nil
	})
	r := e.Reconciler(t, remediation.Bind[int](nam), remediation.Bind[int](mk))
	e.Pass(t, r, nn)
	steady := e.Get(t, nn)
	require.NotNil(t, steady.Status.Naming, "setup")
	time.Sleep(1100 * time.Millisecond) // v.Now has whole seconds
	res := e.Pass(t, r, nn)
	got := e.Get(t, nn)
	assert.Equal(t, steady.Status.Naming, got.Status.Naming, "the panicking planner's block moved")
	assert.NotEqual(t, steady.Status.Markers.ForProbeHash, got.Status.Markers.ForProbeHash, "the other planner's field did not move")
	c := k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MediaFileConditionNamingPlannerError)
	require.NotNil(t, c)
	assert.Equal(t, metav1.ConditionTrue, c.Status)
	assert.Equal(t, catalogv1alpha1.MediaFileReasonPlannerPanic, c.Reason)
	assert.Contains(t, c.Message, "boom")
	assert.NotContains(t, c.Message, "goroutine", "the stack goes to the log, never to status")
	assert.Nil(t, k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MediaFileConditionMarkersPlannerError))
	assert.Equal(t, 10*time.Minute, res.RequeueAfter, "plannerFailedRetry")
	assert.ElementsMatch(t, []string{"catalog/status", "import-worker/"}, remediationtest.FieldManagerNames(got.ManagedFields))

	// A plan that errors reads PlannerError; a clean plan removes it.
	nam.PlanFn = func(*remediation.View, int, *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
		return remediation.Result{}, errors.New("no such RootFolder")
	}
	e.Pass(t, r, nn)
	c = k8s.FindCondition(e.Get(t, nn).Status.Conditions, catalogv1alpha1.MediaFileConditionNamingPlannerError)
	require.NotNil(t, c)
	assert.Equal(t, catalogv1alpha1.MediaFileReasonPlannerError, c.Reason)
	assert.Equal(t, "no such RootFolder", c.Message)
	nam.PlanFn = naming("/data/movies/first.mkv")
	e.Pass(t, r, nn)
	assert.Nil(t, k8s.FindCondition(e.Get(t, nn).Status.Conditions, catalogv1alpha1.MediaFileConditionNamingPlannerError))
}

// A transient fault (bus, KV or apiserver unavailable, a deadline, EIO)
// writes nothing at all -- no condition -- and backs off 30 s, 1 m, 2 m,
// then 5 m; a clean pass resets the backoff (§3.6).
func TestATransientErrorWritesNothing(t *testing.T) {
	e := remediationtest.Start(t)
	nn := e.File(t, "transient", "heat-abc", "/data/movies/heat.mkv")
	var fail atomic.Bool
	var path atomic.Value
	path.Store("/data/movies/x.mkv")
	nam := remediationtest.NamingFake(func(v *remediation.View, in int, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
		return naming(path.Load().(string))(v, in, out)
	})
	nam.GatherFn = func(context.Context, *remediation.Env, *remediation.View) (int, error) {
		if fail.Load() {
			return 0, remediation.Transient(errors.New("nats: no responders available for request"))
		}
		return 0, nil
	}
	r := e.Reconciler(t, remediation.Bind[int](nam))
	e.Pass(t, r, nn)
	steady := e.Get(t, nn)
	fail.Store(true)
	path.Store("/data/movies/y.mkv")
	before := e.StatusPatches()
	var waits []time.Duration
	for range 5 {
		waits = append(waits, e.Pass(t, r, nn).RequeueAfter)
	}
	assert.Equal(t, before, e.StatusPatches(), "a transient fault wrote status")
	assert.Equal(t, []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 5 * time.Minute}, waits)
	got := e.Get(t, nn)
	assert.Nil(t, k8s.FindCondition(got.Status.Conditions, catalogv1alpha1.MediaFileConditionNamingPlannerError))
	assert.Equal(t, steady.ResourceVersion, got.ResourceVersion, "the object was written")
	fail.Store(false)
	e.Pass(t, r, nn)
	assert.Equal(t, "/data/movies/y.mkv", e.Get(t, nn).Status.Naming.ExpectedPath)
	fail.Store(true)
	assert.Equal(t, 30*time.Second, e.Pass(t, r, nn).RequeueAfter, "a clean pass reset the backoff")
}

// A real writer between Gather and the apply (a user's annotation) makes the
// one apply conflict: the pass runs no effects, requeues in at most a
// second, and the next pass plans from the newer object; nothing the writer
// set is rolled back (§3.7, the lost-update rule).
func TestAWriterBetweenGatherAndApplyIsNotRolledBack(t *testing.T) {
	e := remediationtest.Start(t)
	nn := e.File(t, "interleave", "heat-abc", "/data/movies/heat.mkv")
	var once atomic.Bool
	var effects atomic.Int32
	nam := remediationtest.NamingFake(func(v *remediation.View, in int, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
		res, err := naming("/data/movies/x.mkv")(v, in, out)
		res.Effects = []remediation.Effect{remediationtest.CountingEffect(&effects)}
		return res, err
	})
	nam.GatherFn = func(ctx context.Context, _ *remediation.Env, v *remediation.View) (int, error) {
		if once.CompareAndSwap(false, true) {
			patch := client.RawPatch(types.MergePatchType, []byte(`{"metadata":{"annotations":{"example.com/note":"kept"}}}`))
			if err := e.Admin.Patch(ctx, v.File.DeepCopy(), patch); err != nil {
				return 0, err
			}
		}
		return 0, nil
	}
	r := e.Reconciler(t, remediation.Bind[int](nam))
	conflicts := passes("conflict")
	res := e.Pass(t, r, nn)
	assert.Equal(t, conflicts+1, passes("conflict"))
	assert.LessOrEqual(t, res.RequeueAfter, time.Second)
	assert.Positive(t, res.RequeueAfter)
	assert.Nil(t, e.Get(t, nn).Status.Naming, "the conflicting apply landed")
	assert.Zero(t, effects.Load(), "effects ran after a conflict")
	e.Pass(t, r, nn)
	got := e.Get(t, nn)
	assert.Equal(t, "kept", got.Annotations["example.com/note"])
	require.NotNil(t, got.Status.Naming)
	assert.EqualValues(t, 1, effects.Load())
}

// U+0096 in a planner's output never reaches the apply (F2.2's Render), so
// one bad string cannot block every block: in text it is replaced; in a
// path, which a rewrite would turn into a file that does not exist, the
// field is dropped with a Warning Event; the rest of the status lands.
func TestC1ControlsNeverReachTheApply(t *testing.T) {
	e := remediationtest.Start(t)
	nn := e.File(t, "c1", "heat-abc", "/data/movies/heat.mkv")
	mk := remediationtest.MarkersFake(func(v *remediation.View, _ int, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
		out.Markers = &catalogv1alpha1.FileMarkers{Result: catalogv1alpha1.MarkersNotFound, FetchedAt: v.Now, Message: "no match for Heat\u0096 (1995)"}
		return remediation.Result{}, nil
	})
	r := e.Reconciler(t, remediation.Bind[int](remediationtest.NamingFake(naming("/data/movies/Heat\u0096 (1995).mkv"))), remediation.Bind[int](mk))
	e.Pass(t, r, nn)
	got := e.Get(t, nn).Status
	require.NotNil(t, got.Markers, "the apply was refused")
	assert.NotContains(t, got.Markers.Message, "\u0096")
	assert.Equal(t, "no match for Heat\ufffd (1995)", got.Markers.Message, "text is sanitised")
	require.NotNil(t, got.Naming)
	assert.Empty(t, got.Naming.ExpectedPath, "a path is dropped, never rewritten")
	assert.Contains(t, strings.Join(remediationtest.Drain(e.Recorder), "\n"), "Warning StatusEntryDropped naming.expectedPath dropped")
	assert.Equal(t, 1, e.StatusPatches(), "the first apply landed; nothing was refused and retried")
	e.Pass(t, r, nn)
	assert.Equal(t, 1, e.StatusPatches(), "a rendered string compares equal to the stored one: no re-apply")
	assert.Empty(t, remediationtest.Drain(e.Recorder), "an unchanged pass records no second drop")
}

// A stale view (the cache has not caught up with the loop's own last apply)
// never ends a pass as a no-op: it requeues on the 1 s backoff with outcome
// stale, and a pass from the caught-up cache is quiet again (§3.7, the
// applied-status memo).
func TestAStaleViewRequeuesInsteadOfEndingAsANoOp(t *testing.T) {
	e := remediationtest.Start(t)
	nn := e.File(t, "stale", "heat-abc", "/data/movies/heat.mkv")
	var want atomic.Bool
	nam := remediationtest.NamingFake(func(v *remediation.View, in int, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
		if want.Load() {
			return naming("/data/movies/x.mkv")(v, in, out)
		}
		out.Naming = v.Prev.Naming.DeepCopy()
		return remediation.Result{}, nil
	})
	r := e.Reconciler(t, remediation.Bind[int](nam))
	e.Pass(t, r, nn) // the loop's own fields land: the steady state the cache will lag at
	before := e.Get(t, nn)
	require.Nil(t, before.Status.Naming, "setup")
	want.Store(true)
	e.Pass(t, r, nn)
	require.NotNil(t, e.Get(t, nn).Status.Naming, "setup: the naming apply landed")
	want.Store(false)
	e.Freeze(nn, before) // the cache still shows the object from before that apply
	stale := passes("stale")
	patches := e.StatusPatches()
	res := e.Pass(t, r, nn)
	assert.Equal(t, stale+1, passes("stale"))
	assert.Equal(t, patches, e.StatusPatches(), "a stale view applied")
	assert.Positive(t, res.RequeueAfter)
	assert.LessOrEqual(t, res.RequeueAfter, time.Second)
	e.Thaw(nn)
	res = e.Pass(t, r, nn)
	assert.Zero(t, res.RequeueAfter, "the cache caught up: the memo is dropped and the pass is quiet")
	assert.Equal(t, stale+1, passes("stale"))
	assert.Equal(t, patches, e.StatusPatches())
	assert.Equal(t, "/data/movies/x.mkv", e.Get(t, nn).Status.Naming.ExpectedPath, "the stale view rolled nothing back")
}

// A panicking effect stops only its own planner's remaining effects; the
// other planners' effects still run; the next pass sets the planner's
// PlannerError with an "effect:" message (§3.6).
func TestAPanickingEffectDoesNotStopOtherPlannersEffects(t *testing.T) {
	e := remediationtest.Start(t)
	nn := e.File(t, "effectpanic", "heat-abc", "/data/movies/heat.mkv")
	var namingAfter, markersRan atomic.Int32
	nam := remediationtest.NamingFake(func(v *remediation.View, in int, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
		res, err := naming("/data/movies/x.mkv")(v, in, out)
		res.Effects = []remediation.Effect{remediationtest.PanickingEffect(), remediationtest.CountingEffect(&namingAfter)}
		return res, err
	})
	mk := remediationtest.MarkersFake(func(*remediation.View, int, *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
		return remediation.Result{Effects: []remediation.Effect{remediationtest.CountingEffect(&markersRan)}}, nil
	})
	r := e.Reconciler(t, remediation.Bind[int](nam), remediation.Bind[int](mk))
	res := e.Pass(t, r, nn)
	assert.Zero(t, namingAfter.Load(), "the panicking planner's later effect ran")
	assert.EqualValues(t, 1, markersRan.Load(), "another planner's effect did not run")
	assert.Equal(t, 10*time.Minute, res.RequeueAfter)
	e.Pass(t, r, nn)
	c := k8s.FindCondition(e.Get(t, nn).Status.Conditions, catalogv1alpha1.MediaFileConditionNamingPlannerError)
	require.NotNil(t, c)
	assert.Equal(t, catalogv1alpha1.MediaFileReasonPlannerPanic, c.Reason)
	assert.True(t, strings.HasPrefix(c.Message, "effect: "), c.Message)
	assert.Nil(t, k8s.FindCondition(e.Get(t, nn).Status.Conditions, catalogv1alpha1.MediaFileConditionMarkersPlannerError))
}

// Bulk status writes are paced at the limiter's rate while an exempt pass (a
// worker answer, a grant, a nonce, a first probe) applies at once (§3.7).
func TestBulkWritesArePaced(t *testing.T) {
	e := remediationtest.Start(t)
	var files []types.NamespacedName
	for i := range 20 {
		files = append(files, e.File(t, "paced", "film-"+string(rune('a'+i)), "/data/movies/"+string(rune('a'+i))+".mkv"))
	}
	r := e.ReconcilerWith(t, remediation.Config{BulkWritesPerSecond: 10, BulkBurst: 1},
		remediation.Bind[int](remediationtest.NamingFake(naming("/data/movies/renamed.mkv"))))
	before := e.StatusPatches()
	paced := passes("paced")
	var waited int
	for _, nn := range files {
		if res := e.Pass(t, r, nn); res.RequeueAfter > 0 {
			waited++
			assert.LessOrEqual(t, res.RequeueAfter, 2*time.Second+100*time.Millisecond)
			require.NotNil(t, res.Priority)
			assert.Equal(t, remediation.PriorityTimed, *res.Priority)
		}
	}
	assert.Equal(t, 1, e.StatusPatches()-before, "only the burst applied at once")
	assert.Equal(t, 19, waited)
	assert.Equal(t, paced+19, passes("paced"))

	exempt := e.File(t, "paced", "answered", "/data/movies/answered.mkv")
	urgent := remediationtest.NamingFake(func(v *remediation.View, in int, out *catalogv1alpha1.MediaFileStatus) (remediation.Result, error) {
		res, err := naming("/data/movies/answered.mkv")(v, in, out)
		res.Unpaced = true
		return res, err
	})
	r2 := e.ReconcilerSharing(t, r, remediation.Bind[int](urgent))
	before = e.StatusPatches()
	assert.Zero(t, e.Pass(t, r2, exempt).RequeueAfter)
	assert.Equal(t, 1, e.StatusPatches()-before, "an exempt pass waited for a token")
	r3 := e.ReconcilerSharing(t, r, remediation.Bind[int](remediationtest.NamingFake(naming("/data/movies/third.mkv"))))
	assert.Positive(t, e.Pass(t, r3, exempt).RequeueAfter, "the shared bucket is still spent")
}

// A hung /data stat pins at most the pool: a file whose planners do no I/O
// still reconciles at once, an I/O call on a healthy path is transient at
// once rather than queued, and the goroutine count stays bounded (§3.17).
func TestAHungMountNeverStallsOtherFiles(t *testing.T) {
	e := remediationtest.Start(t)
	release := e.HangStats(t, "/data/hung/", 2, 50*time.Millisecond)
	defer release()
	hung := []types.NamespacedName{
		e.File(t, "hung", "a", "/data/hung/a.mkv"), e.File(t, "hung", "b", "/data/hung/b.mkv"), e.File(t, "hung", "c", "/data/hung/c.mkv"),
	}
	quiet := e.File(t, "hung", "q", "/data/fine/q.mkv")
	statter := remediationtest.NamingFake(naming("/data/x.mkv"))
	statter.GatherFn = func(ctx context.Context, env *remediation.Env, v *remediation.View) (int, error) {
		_, err := env.IO.Stat(ctx, v.File.Spec.Path)
		return 0, err
	}
	statter.AppliesFn = func(mf *catalogv1alpha1.MediaFile) bool { return strings.Contains(mf.Spec.Path, "/hung/") }
	mk := remediationtest.MarkersFake(nil)
	r := e.Reconciler(t, remediation.Bind[int](statter), remediation.Bind[int](mk))
	base := remediationtest.Goroutines()
	for _, nn := range hung {
		start := time.Now()
		res := e.Pass(t, r, nn)
		assert.Less(t, time.Since(start), 2*time.Second)
		assert.Equal(t, 30*time.Second, res.RequeueAfter, "a hung stat is transient")
		assert.Nil(t, e.Get(t, nn).Status.Naming, "a transient fault wrote status")
	}
	start := time.Now()
	e.Pass(t, r, quiet)
	assert.Less(t, time.Since(start), 2*time.Second, "a file with no I/O waited on the mount")
	assert.LessOrEqual(t, remediationtest.Goroutines()-base, 2)
}
