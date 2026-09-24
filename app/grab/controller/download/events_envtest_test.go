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

package download_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sevents "k8s.io/client-go/tools/events"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	downloadctl "github.com/mediactl/clustarr/app/grab/controller/download"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

type capturedEvent struct {
	subject string
	env     *events.Envelope
	payload schema.DownloadEvent
}

// subscribeHistory starts the real catalogarr-history consumer from the
// shipped topology -- the sink these events exist for -- and returns what
// it has received.
func subscribeHistory(t *testing.T, bus events.Bus) func() []capturedEvent {
	t.Helper()
	spec, ok := events.Default().ForSingleNode().Consumer(events.ConsumerCatalogHistory)
	require.True(t, ok)

	var mu sync.Mutex
	var got []capturedEvent
	stop, err := bus.Subscribe(context.Background(), spec.Subscription(), func(_ context.Context, m events.Message) error {
		env := m.Envelope().Clone()
		var p schema.DownloadEvent
		if err := schema.Decode(env.Schema, env.Data, &p); err != nil {
			return err
		}
		mu.Lock()
		defer mu.Unlock()
		got = append(got, capturedEvent{subject: m.Subject(), env: env, payload: p})
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(stop)
	return func() []capturedEvent {
		mu.Lock()
		defer mu.Unlock()
		return append([]capturedEvent(nil), got...)
	}
}

func actionsOf(evs []capturedEvent) []string {
	out := make([]string, 0, len(evs))
	for _, e := range evs {
		out = append(out, e.payload.Action)
	}
	return out
}

// TestDownloadEventsFollowTheLifecycle drives one Download from creation to
// deletion and proves the DownloadEventSubject producer: each transition
// reaches the real history consumer exactly once, on the subject §5 names,
// with the Download's identity and release in the payload -- and a
// level-driven re-run over unchanged state publishes nothing new.
func TestDownloadEventsFollowTheLifecycle(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns = "default"
	newTorrentClient(t, ctx, c, ns, "qbit-events", 1, 1)
	markEngineReady(t, ctx, c, ns, "qbit-events", true)

	bus := newTestBus(t)
	captured := subscribeHistory(t, bus)
	r := &downloadctl.Reconciler{Client: c, Recorder: k8sevents.NewFakeRecorder(20), DataDir: t.TempDir(), Bus: bus}

	dl := newTorrentDownload(t, ctx, c, ns, "events-dl", "guid-events")
	engineStage := func(stage downloadv1alpha1.DownloadStage) {
		obj := downloadac.Download(dl.Name, ns).WithStatus(downloadac.DownloadStatus().WithStage(stage).WithDownloadID("abc123"))
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerGrabarrEngine, obj)
		require.NoError(t, err)
	}
	waitFor := func(n int) []capturedEvent {
		require.Eventually(t, func() bool { return len(captured()) >= n }, 5*time.Second, 10*time.Millisecond,
			"want %d events, have %v", n, actionsOf(captured()))
		return captured()
	}

	reconcileOK(t, r, ns, dl.Name) // assignment
	assert.Equal(t, []string{events.ActionQueued}, actionsOf(waitFor(1)))

	engineStage(downloadv1alpha1.DownloadStageTransferring)
	reconcileOK(t, r, ns, dl.Name)
	assert.Equal(t, []string{events.ActionQueued, events.ActionStarted}, actionsOf(waitFor(2)))

	engineStage(downloadv1alpha1.DownloadStageDone)
	reconcileOK(t, r, ns, dl.Name)
	reconcileOK(t, r, ns, dl.Name) // level re-run: nothing new
	assert.Equal(t, []string{events.ActionQueued, events.ActionStarted, events.ActionCompleted}, actionsOf(waitFor(3)))

	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerImportarr, downloadac.Download(dl.Name, ns).WithStatus(
		downloadac.DownloadStatus().WithImport(downloadac.ImportState().
			WithState(downloadv1alpha1.ImportPhaseImported).WithImportedAt(metav1.Now()))))
	require.NoError(t, err)
	reconcileOK(t, r, ns, dl.Name)
	reconcileOK(t, r, ns, dl.Name)
	assert.Equal(t, []string{events.ActionQueued, events.ActionStarted, events.ActionCompleted, events.ActionImported},
		actionsOf(waitFor(4)))

	live := getDownload(t, ctx, c, ns, dl.Name)
	require.NoError(t, c.Delete(ctx, live))
	reconcileOK(t, r, ns, dl.Name)
	got := waitFor(5)
	require.Never(t, func() bool { return len(captured()) > 5 }, 500*time.Millisecond, 20*time.Millisecond,
		"a transition is announced once: %v", actionsOf(captured()))
	assert.Equal(t, events.ActionRemoved, got[4].payload.Action)

	for _, e := range got {
		assert.Equal(t, events.DownloadEventSubject(e.payload.Action, string(live.UID)), e.subject)
		assert.Equal(t, string(live.UID)+":"+e.payload.Action, e.env.ID, "per-action id, so a re-observed edge dedups")
		assert.Equal(t, ns+"/"+dl.Name, e.env.Key)
		assert.Equal(t, live.Name, e.payload.DownloadRef.Name)
		assert.Equal(t, string(live.UID), e.payload.DownloadRef.UID)
		assert.Equal(t, live.Spec.Target, e.payload.Media)
		assert.Equal(t, "Arrival.2016.1080p", e.payload.Title)
		assert.Equal(t, live.Spec.Release.InfoHash, e.payload.InfoHash)
		require.NotNil(t, e.payload.ClientRef)
		assert.Equal(t, "qbit-events", e.payload.ClientRef.Name)
	}
}

// TestFailedEventCarriesTheReason: a Download failing (here, encrypted
// content) announces "failed" with the failure reason.
func TestFailedEventCarriesTheReason(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns = "default"
	newTorrentClient(t, ctx, c, ns, "qbit-fail", 1, 1)
	markEngineReady(t, ctx, c, ns, "qbit-fail", true)

	bus := newTestBus(t)
	captured := subscribeHistory(t, bus)
	r := &downloadctl.Reconciler{Client: c, Recorder: k8sevents.NewFakeRecorder(20), DataDir: t.TempDir(), Bus: bus}

	dl := newTorrentDownload(t, ctx, c, ns, "fail-dl", "guid-fail")
	reconcileOK(t, r, ns, dl.Name)
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerGrabarrEngine, downloadac.Download(dl.Name, ns).WithStatus(
		downloadac.DownloadStatus().WithStage(downloadv1alpha1.DownloadStageExtracting).WithIsEncrypted(true)))
	require.NoError(t, err)
	reconcileOK(t, r, ns, dl.Name)

	require.Eventually(t, func() bool {
		for _, e := range captured() {
			if e.payload.Action == events.ActionFailed {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "have %v", actionsOf(captured()))
	for _, e := range captured() {
		if e.payload.Action == events.ActionFailed {
			assert.Equal(t, string(downloadv1alpha1.DownloadFailureEncrypted), e.payload.Reason)
		}
	}
}

// TestDeadLetteredAnnotationFoldsIntoDownloadStatus is the DLQ fold for
// Download (gap-fix item 13): on an object already in steady state -- every
// controller condition set -- the projector's annotation adds
// DeadLettered=True without disturbing the rest, and removing the
// annotation removes the condition. It covers the pre-assignment apply too,
// which is the other place this manager declares conditions.
func TestDeadLetteredAnnotationFoldsIntoDownloadStatus(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns = "default"
	newTorrentClient(t, ctx, c, ns, "qbit-dlq", 1, 1)
	markEngineReady(t, ctx, c, ns, "qbit-dlq", true)
	r := &downloadctl.Reconciler{Client: c, Recorder: k8sevents.NewFakeRecorder(20), DataDir: t.TempDir(), Bus: newTestBus(t)}

	dl := newTorrentDownload(t, ctx, c, ns, "dlq-dl", "guid-dlq")
	reconcileOK(t, r, ns, dl.Name)
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerGrabarrEngine, downloadac.Download(dl.Name, ns).WithStatus(
		downloadac.DownloadStatus().WithStage(downloadv1alpha1.DownloadStageDone)))
	require.NoError(t, err)
	reconcileOK(t, r, ns, dl.Name)

	steady := getDownload(t, ctx, c, ns, dl.Name)
	for _, cond := range []string{
		downloadv1alpha1.DownloadConditionAssigned, downloadv1alpha1.DownloadConditionDownloaded,
		downloadv1alpha1.DownloadConditionFailed, downloadv1alpha1.DownloadConditionImported,
	} {
		require.NotNil(t, k8s.FindCondition(steady.Status.Conditions, cond), "steady state must carry %s", cond)
	}
	require.Nil(t, k8s.FindCondition(steady.Status.Conditions, k8s.ConditionDeadLettered))

	annotate := func(value *string) {
		live := getDownload(t, ctx, c, ns, dl.Name)
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
	v := "clustarr.evt.download.download.completed.x@2026-09-23T10:00:00Z"
	annotate(&v)
	reconcileOK(t, r, ns, dl.Name)

	marked := getDownload(t, ctx, c, ns, dl.Name)
	dlq := k8s.FindCondition(marked.Status.Conditions, k8s.ConditionDeadLettered)
	require.NotNil(t, dlq, "the annotation must fold into a DeadLettered condition")
	assert.Equal(t, metav1.ConditionTrue, dlq.Status)
	assert.Equal(t, k8s.ReasonDeadLettered, dlq.Reason)
	assert.Equal(t, downloadv1alpha1.DownloadPhaseCompleted, marked.Status.Phase, "the fold must not disturb the phase")
	assert.True(t, k8s.IsConditionTrue(marked.Status.Conditions, downloadv1alpha1.DownloadConditionDownloaded),
		"nor any other condition")

	annotate(nil)
	reconcileOK(t, r, ns, dl.Name)
	cleared := getDownload(t, ctx, c, ns, dl.Name)
	assert.Nil(t, k8s.FindCondition(cleared.Status.Conditions, k8s.ConditionDeadLettered), "removing the annotation clears it")
	assert.True(t, k8s.IsConditionTrue(cleared.Status.Conditions, downloadv1alpha1.DownloadConditionDownloaded))

	// The pre-assignment apply: a Download with no client to pick.
	pending := newUsenetDownload(t, ctx, c, ns, "dlq-pending", "guid-dlq-pending")
	pending.Annotations = map[string]string{k8s.AnnotationDeadLettered: v}
	require.NoError(t, c.Update(ctx, pending))
	reconcileOK(t, r, ns, pending.Name)
	gotPending := getDownload(t, ctx, c, ns, pending.Name)
	assert.Equal(t, downloadv1alpha1.DownloadPhasePending, gotPending.Status.Phase)
	assert.True(t, k8s.IsConditionTrue(gotPending.Status.Conditions, k8s.ConditionDeadLettered))
}
