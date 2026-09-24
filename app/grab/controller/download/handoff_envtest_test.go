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

// This file is D2-8a's own end-to-end proof of "the phase gate depends on
// it": telemetry lands under k8s.ManagerGrabarrEngine (standing in for a
// real engine, which does not exist in this package's test fixtures --
// D2-5/D2-6 live elsewhere), status.phase advances under k8s.ManagerGrabarr,
// and importarr's real ConsumerImportFile consumer spec receives exactly one
// parseable schema.ImportTask. It is deliberately against the SHIPPED
// topology (events.Default().ForSingleNode().Consumer), not a hand-rolled
// subscription, so a subject or consumer-name typo here would fail the same
// way it would against a real importarr-worker.
package download_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8sevents "k8s.io/client-go/tools/events"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	downloadctl "github.com/mediactl/clustarr/app/grab/controller/download"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// newTestBus brings up the whole default topology single-node, the same
// reason catalogarr/worker/grab's own newTestBus gives: events.Topology.Validate
// requires the DLQ stream, and a hand-rolled partial topology both fails
// that check and drifts from production's real consumer tuning -- which is
// exactly what this file needs to be a faithful proof of the handoff.
func newTestBus(t *testing.T) events.Bus {
	t.Helper()
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(context.Background(), events.Default().ForSingleNode()))
	t.Cleanup(func() { _ = bus.Close() })
	return bus
}

// subscribeImportFile starts the real ConsumerImportFile consumer (not a
// hand-rolled subscription) and returns the envelopes it receives, guarded
// by a mutex since delivery runs on the bus's own goroutine.
func subscribeImportFile(t *testing.T, bus events.Bus) func() []*events.Envelope {
	t.Helper()
	spec, ok := events.Default().ForSingleNode().Consumer(events.ConsumerImportFile)
	require.True(t, ok, "ConsumerImportFile must be in the default topology")

	var mu sync.Mutex
	var got []*events.Envelope
	stop, err := bus.Subscribe(context.Background(), spec.Subscription(), func(_ context.Context, m events.Message) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, m.Envelope().Clone())
		return nil
	})
	require.NoError(t, err)
	t.Cleanup(stop)

	return func() []*events.Envelope {
		mu.Lock()
		defer mu.Unlock()
		return append([]*events.Envelope(nil), got...)
	}
}

func TestPhaseAdvancesFromTelemetryAndPublishesImportTaskExactlyOnce(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := "default"

	newTorrentClient(t, ctx, c, ns, "qbit-handoff", 1, 1)
	markEngineReady(t, ctx, c, ns, "qbit-handoff", true)

	dl := newTorrentDownload(t, ctx, c, ns, "handoff-dl", "guid-handoff-1")

	bus := newTestBus(t)
	captured := subscribeImportFile(t, bus)

	r := &downloadctl.Reconciler{Client: c, Recorder: k8sevents.NewFakeRecorder(10), DataDir: t.TempDir(), Bus: bus}

	// Reconcile 1: pick the client, wait for EngineReady (already true),
	// pin status.engine. This is the pre-existing D2-4 path, unmodified by
	// this task -- phase lands on Assigned via the plain applyStatus call,
	// not derivePhase, because status.engine is still empty when this
	// reconcile begins.
	reconcileOK(t, r, ns, dl.Name)
	pinned := getDownload(t, ctx, c, ns, dl.Name)
	require.NotEmpty(t, pinned.Status.Engine, "engine must be pinned before telemetry-driven advancement can run")
	require.Equal(t, downloadv1alpha1.DownloadPhaseAssigned, pinned.Status.Phase)

	// Simulate the engine reporting DownloadStageDone -- content complete on
	// disk -- under its own field manager, exactly as D2-5/D2-6 would.
	obj := downloadac.Download(dl.Name, ns).WithStatus(downloadac.DownloadStatus().WithStage(downloadv1alpha1.DownloadStageDone))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerGrabarrEngine, obj)
	require.NoError(t, err)

	// Reconcile 2: status.engine is now non-empty, so this goes through
	// advancePhase. derivePhase reads Stage=Done and returns Completed;
	// isContentComplete(Completed) is true and Downloaded was not
	// previously True, so this is the reconcile that publishes.
	reconcileOK(t, r, ns, dl.Name)

	got := getDownload(t, ctx, c, ns, dl.Name)
	assert.Equal(t, downloadv1alpha1.DownloadPhaseCompleted, got.Status.Phase)
	assert.True(t, k8s.IsConditionTrue(got.Status.Conditions, downloadv1alpha1.DownloadConditionDownloaded),
		"Downloaded condition must be True once content is complete")
	require.NotNil(t, got.Status.CompletedAt, "completedAt must be recorded on the same reconcile that first observes completion")

	require.Eventually(t, func() bool { return len(captured()) >= 1 }, 5*time.Second, 10*time.Millisecond,
		"ConsumerImportFile never received the import task")

	msgs := captured()
	require.Len(t, msgs, 1, "exactly one import task must be published per completion")

	env := msgs[0]
	gotNS, gotName, ok := strings.Cut(env.Key, "/")
	require.True(t, ok, "Envelope.Key must be <namespace>/<name>: got %q", env.Key)
	assert.Equal(t, ns, gotNS)
	assert.Equal(t, dl.Name, gotName)

	var task schema.ImportTask
	require.NoError(t, schema.Decode(env.Schema, env.Data, &task))
	assert.Equal(t, ns, task.DownloadRef.Namespace)
	assert.Equal(t, dl.Name, task.DownloadRef.Name)
	assert.Equal(t, string(got.UID), task.DownloadRef.UID)

	// Reconcile 3: telemetry is unchanged (still Stage=Done), so this is the
	// level-driven re-run the plan explicitly calls out -- "a level-driven
	// reconciler re-runs on every event". Downloaded is already True, so the
	// publish gate must not fire again.
	reconcileOK(t, r, ns, dl.Name)
	require.Never(t, func() bool { return len(captured()) > 1 }, 2*time.Second, 20*time.Millisecond,
		"a re-run over unchanged telemetry must not publish a second import task")
}

func TestUsenetHealthDoesNotBlockPhaseAdvancement(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := "default"

	dc := newUsenetClient(t, ctx, c, ns, "sabnzbd-handoff")
	markEngineReady(t, ctx, c, ns, dc.Name, true)

	dl := newUsenetDownload(t, ctx, c, ns, "handoff-usenet-dl", "guid-handoff-usenet-1")

	bus := newTestBus(t)
	captured := subscribeImportFile(t, bus)
	r := &downloadctl.Reconciler{Client: c, Recorder: k8sevents.NewFakeRecorder(10), DataDir: t.TempDir(), Bus: bus}

	reconcileOK(t, r, ns, dl.Name)
	require.NotEmpty(t, getDownload(t, ctx, c, ns, dl.Name).Status.Engine)

	obj := downloadac.Download(dl.Name, ns).WithStatus(downloadac.DownloadStatus().WithStage(downloadv1alpha1.DownloadStageTransferring))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerGrabarrEngine, obj)
	require.NoError(t, err)
	reconcileOK(t, r, ns, dl.Name)
	assert.Equal(t, downloadv1alpha1.DownloadPhaseDownloading, getDownload(t, ctx, c, ns, dl.Name).Status.Phase)
	assert.Empty(t, captured(), "no import task before content is complete")

	obj = downloadac.Download(dl.Name, ns).WithStatus(downloadac.DownloadStatus().WithStage(downloadv1alpha1.DownloadStageDone))
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerGrabarrEngine, obj)
	require.NoError(t, err)
	reconcileOK(t, r, ns, dl.Name)

	got := getDownload(t, ctx, c, ns, dl.Name)
	assert.Equal(t, downloadv1alpha1.DownloadPhaseCompleted, got.Status.Phase,
		"usenet has no seeding stage, so Done must land on Completed, not Seeding")
	require.Eventually(t, func() bool { return len(captured()) >= 1 }, 5*time.Second, 10*time.Millisecond)
	assert.Len(t, captured(), 1)
}

// An encrypted release is the release's fault (gap fix Y2), so it is
// blocklisted rather than merely failed -- and either way it is never
// handed to the importer.
func TestIsEncryptedBlocklistsTheDownloadWithoutPublishing(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := "default"

	newTorrentClient(t, ctx, c, ns, "qbit-enc", 1, 1)
	markEngineReady(t, ctx, c, ns, "qbit-enc", true)
	dl := newTorrentDownload(t, ctx, c, ns, "handoff-encrypted-dl", "guid-handoff-encrypted-1")

	bus := newTestBus(t)
	captured := subscribeImportFile(t, bus)
	r := &downloadctl.Reconciler{Client: c, Recorder: k8sevents.NewFakeRecorder(10), DataDir: t.TempDir(), Bus: bus}

	reconcileOK(t, r, ns, dl.Name)

	obj := downloadac.Download(dl.Name, ns).WithStatus(downloadac.DownloadStatus().
		WithStage(downloadv1alpha1.DownloadStageExtracting).WithIsEncrypted(true))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerGrabarrEngine, obj)
	require.NoError(t, err)
	reconcileOK(t, r, ns, dl.Name)

	got := getDownload(t, ctx, c, ns, dl.Name)
	assert.Equal(t, downloadv1alpha1.DownloadPhaseBlocklisted, got.Status.Phase)
	assert.Equal(t, downloadv1alpha1.DownloadFailureEncrypted, got.Status.FailureReason)
	assert.Equal(t, downloadv1alpha1.LabelBlocklistedValue, got.Labels[downloadv1alpha1.LabelBlocklisted])
	assert.NotNil(t, got.Status.BlocklistedUntil)
	assert.True(t, k8s.IsConditionTrue(got.Status.Conditions, downloadv1alpha1.DownloadConditionFailed))
	assert.False(t, k8s.IsConditionTrue(got.Status.Conditions, downloadv1alpha1.DownloadConditionDownloaded))

	time.Sleep(200 * time.Millisecond)
	assert.Empty(t, captured(), "an encrypted, failed transfer must never be handed to the importer")
}
