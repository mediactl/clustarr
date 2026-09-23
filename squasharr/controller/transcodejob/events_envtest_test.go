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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sevents "k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	transcodeac "github.com/mediactl/clustarr/api/applyconfiguration/transcode/transcode/v1alpha1"
	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	squasharrstatus "github.com/mediactl/clustarr/squasharr/status"
	"github.com/mediactl/clustarr/squasharr/worker"
)

type capturedJobEvent struct {
	subject string
	env     *events.Envelope
	payload schema.JobEvent
}

// subscribeHistory starts the real catalogarr-history consumer from the
// shipped topology -- the sink these events exist for -- on an in-memory
// bus, and returns the bus and what the consumer has received.
func subscribeHistory(t *testing.T) (events.Bus, func() []capturedJobEvent) {
	t.Helper()
	bus := membus.New(nil)
	topo := events.Default().ForSingleNode()
	require.NoError(t, bus.Ensure(context.Background(), topo))
	t.Cleanup(func() { _ = bus.Close() })
	spec, ok := topo.Consumer(events.ConsumerCatalogHistory)
	require.True(t, ok)

	var mu sync.Mutex
	var got []capturedJobEvent
	stop, err := bus.Subscribe(context.Background(), spec.Subscription(), func(_ context.Context, m events.Message) error {
		env := m.Envelope().Clone()
		var p schema.JobEvent
		if err := schema.Decode(env.Schema, env.Data, &p); err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		got = append(got, capturedJobEvent{subject: m.Subject(), env: env, payload: p})
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(stop)
	return bus, func() []capturedJobEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]capturedJobEvent(nil), got...)
	}
}

func jobActions(evs []capturedJobEvent) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.payload.Action)
	}
	return out
}

// drainRecorder returns every Event the fake recorder has buffered so far.
func drainRecorder(rec *k8sevents.FakeRecorder) []string {
	var out []string
	for {
		select {
		case e := <-rec.Events:
			out = append(out, e)
		default:
			return out
		}
	}
}

func reasonsOf(recorded []string) []string {
	out := make([]string, 0, len(recorded))
	for _, e := range recorded {
		// FakeRecorder renders "<type> <reason> <note>".
		if f := strings.Fields(e); len(f) >= 2 {
			out = append(out, f[0]+" "+f[1])
		}
	}
	return out
}

// TestJobEventsFollowTheLifecycle drives one TranscodeJob from creation to
// Succeeded and proves the TranscodeJobSubject producer and the Kubernetes
// Events: each edge reaches the real history consumer exactly once, on the
// subject §5 names, with the job's identity and plan in the payload, the
// success carries the worker's output figures, and a level re-run over
// unchanged state -- including after the job is terminal -- publishes and
// records nothing new.
func TestJobEventsFollowTheLifecycle(t *testing.T) {
	_, c := startEnv(t)
	ctx := context.Background()
	const ns = "tj-events"
	newNamespace(t, c, ns)
	newProfile(t, c, "hevc", "hash1", nil)
	newMediaFile(t, c, ns, "arrival", "probe1", ptr.To(h264Probe()))
	newTJ(t, c, ns, "arrival-hevc", "arrival", "hevc", "probe1", nil)

	bus, captured := subscribeHistory(t)
	rec := k8sevents.NewFakeRecorder(50)
	r := newReconciler(c, map[string]int32{"cpu": 2})
	r.Bus, r.Recorder = bus, rec
	waitFor := func(n int) []capturedJobEvent {
		require.Eventually(t, func() bool { return len(captured()) >= n }, 5*time.Second, 10*time.Millisecond,
			"want %d events, have %v", n, jobActions(captured()))
		return captured()
	}

	reconcileTJ(t, r, ns, "arrival-hevc") // plan, create the Job, admit
	assert.Equal(t, []string{events.ActionQueued}, jobActions(waitFor(1)))
	assert.Equal(t, []string{"Normal Planned", "Normal JobCreated"}, reasonsOf(drainRecorder(rec)))

	reconcileTJ(t, r, ns, "arrival-hevc") // mirrors the unsuspended Job: Running
	reconcileTJ(t, r, ns, "arrival-hevc") // level re-run: nothing new
	assert.Equal(t, []string{events.ActionQueued, events.ActionStarted}, jobActions(waitFor(2)))
	assert.Equal(t, []string{"Normal Started"}, reasonsOf(drainRecorder(rec)))

	// The worker's result, as it writes it, then the Job completing.
	tj := getTJ(t, c, ns, "arrival-hevc")
	require.NoError(t, squasharrstatus.Patch(ctx, c, k8s.ManagerSquasharrWorker, tj,
		func(ac *transcodeac.TranscodeJobStatusApplyConfiguration) {
			ac.WithResult(transcodeac.Result().WithOutputPath("/data/movies/arrival.mkv").
				WithOutputSizeBytes(1 << 30).WithOutputToSourcePercent(25))
		}))
	completeJob(t, c, ns, *tj.Status.JobRef)
	reconcileTJ(t, r, ns, "arrival-hevc")
	reconcileTJ(t, r, ns, "arrival-hevc") // terminal: nothing new
	got := waitFor(3)
	require.Never(t, func() bool { return len(captured()) > 3 }, 300*time.Millisecond, 20*time.Millisecond,
		"an edge is announced once: %v", jobActions(captured()))
	assert.Equal(t, []string{events.ActionQueued, events.ActionStarted, events.ActionSucceeded}, jobActions(got))
	assert.Equal(t, []string{"Normal JobSucceeded"}, reasonsOf(drainRecorder(rec)))

	live := getTJ(t, c, ns, "arrival-hevc")
	for _, e := range got {
		assert.Equal(t, events.TranscodeJobSubject(e.payload.Action, string(live.UID)), e.subject)
		assert.Equal(t, string(live.UID)+":"+e.payload.Action, e.env.ID, "per-action id, so a re-observed edge dedups")
		assert.Equal(t, ns+"/arrival-hevc", e.env.Key)
		assert.Equal(t, schema.Ref{Namespace: ns, Name: "arrival-hevc", UID: string(live.UID)}, e.payload.JobRef)
		require.NotNil(t, e.payload.ProfileRef)
		assert.Equal(t, "hevc", e.payload.ProfileRef.Name)
		assert.Equal(t, "/data/movies/arrival.mkv", e.payload.InputPath)
		assert.Equal(t, "transcode", e.payload.Mode)
		assert.Equal(t, "libx265", e.payload.Encoder)
	}
	succeeded := got[2].payload
	assert.Equal(t, "/data/movies/arrival.mkv", succeeded.OutputPath)
	assert.Equal(t, int64(1<<30), succeeded.OutputBytes)
	assert.Equal(t, int32(75), succeeded.SavedPercent)
}

// TestSkippedAndFailedEventsCarryTheReason: a compliant file is announced
// "skipped" and a file that changed since the job was made "failed" (a
// Warning Event), each with the reason the status records.
func TestSkippedAndFailedEventsCarryTheReason(t *testing.T) {
	_, c := startEnv(t)
	const ns = "tj-events-fail"
	newNamespace(t, c, ns)
	newProfile(t, c, "hevc", "hash1", nil)
	newMediaFile(t, c, ns, "done", "pdone", ptr.To(compliantProbe()))
	newMediaFile(t, c, ns, "moved", "pnew", ptr.To(h264Probe()))
	newTJ(t, c, ns, "done-hevc", "done", "hevc", "pdone", nil)
	newTJ(t, c, ns, "moved-hevc", "moved", "hevc", "pold", nil)

	bus, captured := subscribeHistory(t)
	rec := k8sevents.NewFakeRecorder(50)
	r := newReconciler(c, map[string]int32{"cpu": 2})
	r.Bus, r.Recorder = bus, rec

	reconcileTJ(t, r, ns, "done-hevc")
	reconcileTJ(t, r, ns, "moved-hevc")
	require.Eventually(t, func() bool { return len(captured()) >= 2 }, 5*time.Second, 10*time.Millisecond,
		"have %v", jobActions(captured()))

	byAction := map[string]schema.JobEvent{}
	for _, e := range captured() {
		byAction[e.payload.Action] = e.payload
	}
	skipped, ok := byAction[events.ActionSkipped]
	require.True(t, ok, "have %v", jobActions(captured()))
	assert.Equal(t, "done-hevc", skipped.JobRef.Name)
	assert.Equal(t, getTJ(t, c, ns, "done-hevc").Status.Message, skipped.Reason)
	failed, ok := byAction[events.ActionFailed]
	require.True(t, ok, "have %v", jobActions(captured()))
	assert.Equal(t, "moved-hevc", failed.JobRef.Name)
	assert.Contains(t, failed.Reason, "no longer matches spec.sourceProbeHash")

	assert.ElementsMatch(t, []string{"Normal Skipped", "Warning SourceChanged"}, reasonsOf(drainRecorder(rec)))
}

// TestDeadLetteredFoldsIntoTranscodeJobStatus is the DLQ fold for
// TranscodeJob (gap-fix item 13). The projector annotates mostly FINISHED
// jobs -- their succeeded/failed/skipped events are what the history
// consumer dead-letters -- so the fold is proved on a job already at
// Succeeded, in steady state: the annotation adds DeadLettered=True and
// changes nothing else, and removing it removes the condition. A queued
// job folds it on its ordinary pass too.
func TestDeadLetteredFoldsIntoTranscodeJobStatus(t *testing.T) {
	_, c := startEnv(t)
	ctx := context.Background()
	const ns = "tj-dlq"
	newNamespace(t, c, ns)
	newProfile(t, c, "hevc", "hash1", nil)
	newMediaFile(t, c, ns, "arrival", "probe1", ptr.To(h264Probe()))
	newMediaFile(t, c, ns, "waiting", "probe2", ptr.To(h264Probe()))
	newTJ(t, c, ns, "arrival-hevc", "arrival", "hevc", "probe1", nil)
	newTJ(t, c, ns, "waiting-hevc", "waiting", "hevc", "probe2", nil)

	r := newReconciler(c, map[string]int32{"cpu": 1})
	reconcileTJ(t, r, ns, "arrival-hevc")
	reconcileTJ(t, r, ns, "arrival-hevc")
	completeJob(t, c, ns, *getTJ(t, c, ns, "arrival-hevc").Status.JobRef)
	reconcileTJ(t, r, ns, "arrival-hevc")
	steady := getTJ(t, c, ns, "arrival-hevc")
	require.Equal(t, transcodev1alpha1.TranscodeJobPhaseSucceeded, steady.Status.Phase)
	require.Nil(t, k8s.FindCondition(steady.Status.Conditions, k8s.ConditionDeadLettered))

	annotate := func(name string, value *string) {
		live := getTJ(t, c, ns, name)
		if live.Annotations == nil {
			live.Annotations = map[string]string{}
		}
		if value == nil {
			delete(live.Annotations, k8s.AnnotationDeadLettered)
		} else {
			live.Annotations[k8s.AnnotationDeadLettered] = *value
		}
		require.NoError(t, c.Update(ctx, live))
	}
	withoutDLQ := func(st transcodev1alpha1.TranscodeJobStatus) transcodev1alpha1.TranscodeJobStatus {
		st = *st.DeepCopy()
		k8s.RemoveCondition(&st.Conditions, k8s.ConditionDeadLettered)
		return st
	}

	v := "clustarr.evt.transcode.job.succeeded.x@2026-09-23T10:00:00Z"
	annotate("arrival-hevc", &v)
	reconcileTJ(t, r, ns, "arrival-hevc")
	marked := getTJ(t, c, ns, "arrival-hevc")
	dlq := k8s.FindCondition(marked.Status.Conditions, k8s.ConditionDeadLettered)
	require.NotNil(t, dlq, "the annotation must fold into a DeadLettered condition on a finished job")
	assert.Equal(t, metav1.ConditionTrue, dlq.Status)
	assert.Equal(t, k8s.ReasonDeadLettered, dlq.Reason)
	assert.Equal(t, withoutDLQ(steady.Status), withoutDLQ(marked.Status), "the fold must change nothing but the condition")
	assertConditionsOwnedBy(t, marked, "squasharr")

	annotate("arrival-hevc", nil)
	reconcileTJ(t, r, ns, "arrival-hevc")
	cleared := getTJ(t, c, ns, "arrival-hevc")
	assert.Nil(t, k8s.FindCondition(cleared.Status.Conditions, k8s.ConditionDeadLettered), "removing the annotation clears it")
	assert.Equal(t, withoutDLQ(steady.Status), withoutDLQ(cleared.Status))

	// A job still queued (no cpu slot at all) folds it on its own pass.
	queueOnly := newReconciler(c, map[string]int32{"cpu": 0})
	reconcileTJ(t, queueOnly, ns, "waiting-hevc")
	annotate("waiting-hevc", &v)
	reconcileTJ(t, queueOnly, ns, "waiting-hevc")
	waiting := getTJ(t, c, ns, "waiting-hevc")
	assert.Equal(t, transcodev1alpha1.TranscodeJobPhaseQueued, waiting.Status.Phase)
	assert.True(t, k8s.IsConditionTrue(waiting.Status.Conditions, k8s.ConditionDeadLettered))
	assert.True(t, k8s.IsConditionTrue(waiting.Status.Conditions, transcodev1alpha1.TranscodeJobConditionJobCreated))
}

// assertConditionsOwnedBy checks status.conditions is claimed by manager on
// metadata.managedFields -- the controller's, never the worker's.
func assertConditionsOwnedBy(t *testing.T, obj client.Object, manager string) {
	t.Helper()
	for _, mf := range obj.GetManagedFields() {
		if mf.Manager != manager || mf.Subresource != "status" || mf.FieldsV1 == nil {
			continue
		}
		if strings.Contains(string(mf.FieldsV1.GetRawBytes()), `"f:conditions"`) {
			return
		}
	}
	t.Errorf("status.conditions is not owned by %s: %+v", manager, obj.GetManagedFields())
}

// TestJobCarriesTheReconcileTraceParent: with tracing set up as the binary
// sets it up, the Job ensureJob creates carries the reconcile span's W3C
// traceparent, which the worker continues (worker.ContextWithTraceParent) --
// so an ffmpeg run's spans land in the trace of the reconcile that queued it.
func TestJobCarriesTheReconcileTraceParent(t *testing.T) {
	_, c := startEnv(t)
	ctx := context.Background()
	shutdown, err := tracing.Setup(ctx, tracing.Options{ServiceName: "squasharr-test", SampleRatio: 1})
	require.NoError(t, err)
	t.Cleanup(func() { _ = shutdown(ctx) })

	const ns = "tj-trace"
	newNamespace(t, c, ns)
	newProfile(t, c, "hevc", "hash1", nil)
	newMediaFile(t, c, ns, "arrival", "probe1", ptr.To(h264Probe()))
	newTJ(t, c, ns, "arrival-hevc", "arrival", "hevc", "probe1", nil)
	reconcileTJ(t, newReconciler(c, map[string]int32{"cpu": 0}), ns, "arrival-hevc")

	job := getJob(t, c, ns, *getTJ(t, c, ns, "arrival-hevc").Status.JobRef)
	var tp string
	for _, e := range job.Spec.Template.Spec.Containers[0].Env {
		if e.Name == worker.TraceParentEnv {
			tp = e.Value
		}
	}
	assert.Regexp(t, `^00-[0-9a-f]{32}-[0-9a-f]{16}-01$`, tp, "the Job must carry the reconcile's sampled traceparent")
}
