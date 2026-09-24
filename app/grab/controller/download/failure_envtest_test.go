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

// Gap fix Y2: an engine's failure report and seed-goal report become the
// controller's verdicts -- failureReason, Failed -> Blocklisted with the
// label and blocklistedUntil, seedGoalMetAt and the SeedGoalMet condition,
// and the failed/blocklisted/seedGoalMet events. Every test here acts on a
// Download already in steady state (assigned, telemetry flowing, conditions
// set), because a release is invisible on a blank object.
package download_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sevents "k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	downloadctl "github.com/mediactl/clustarr/app/grab/controller/download"
	grabarrstatus "github.com/mediactl/clustarr/app/grab/status"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// steadyItem is a full engine observation of a transfer in flight: enough
// engine-owned leaves that a release of any of them by the controller's
// applies would show.
func steadyItem(stage downloadv1alpha1.DownloadStage) download.Item {
	last := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	return download.Item{
		ID:              "0123456789abcdef0123456789abcdef01234567",
		Status:          download.StatusDownloading,
		Stage:           stage,
		TotalBytes:      8 << 30,
		RemainingBytes:  4 << 30,
		DownloadedBytes: 4 << 30,
		DownRate:        1 << 20,
		ProgressPercent: 50,
		Seeders:         3,
		Peers:           5,
		OutputPath:      "/data/torrents/movie/fail",
		ContentRoot:     "/data/torrents/movie/fail",
		Files:           []download.File{{Path: "movie.mkv", SizeBytes: 8 << 30}},
		Message:         "transferring",
		LastProgressAt:  &last,
	}
}

// engineWrites applies item as the engine does: the complete
// k8s.ManagerGrabarrEngine declaration, seeded from a fresh read.
func engineWrites(t *testing.T, ctx context.Context, c client.Client, ns, name string, item download.Item) {
	t.Helper()
	live := getDownload(t, ctx, c, ns, name)
	require.NoError(t, grabarrstatus.Patch(ctx, c, k8s.ManagerGrabarrEngine, live,
		func(ac *downloadac.DownloadStatusApplyConfiguration) { *ac = *download.ApplyStatus(item) }))
}

// ownsField reports whether manager's managedFields entry for subresource
// ("" for the main resource) claims the field at path.
func ownsField(t *testing.T, entries []metav1.ManagedFieldsEntry, manager k8s.FieldManager, subresource string, path ...string) bool {
	t.Helper()
	for _, e := range entries {
		if e.Manager != manager.String() || e.Subresource != subresource || e.FieldsV1 == nil {
			continue
		}
		var node map[string]any
		require.NoError(t, json.Unmarshal(e.FieldsV1.GetRawBytes(), &node))
		found := true
		for _, p := range path {
			next, ok := node["f:"+p].(map[string]any)
			if !ok {
				found = false
				break
			}
			node = next
		}
		if found {
			return true
		}
	}
	return false
}

// steadyDownload builds a Download the controller has assigned and advanced
// to phase over engine telemetry, and returns it with a reconciler wired to
// a bus whose history consumer is captured.
func steadyDownload(t *testing.T, ctx context.Context, c client.Client, clientName, name string, stage downloadv1alpha1.DownloadStage) (
	*downloadctl.Reconciler, func() []capturedEvent,
) {
	t.Helper()
	const ns = "default"
	newTorrentClient(t, ctx, c, ns, clientName, 1, 1)
	markEngineReady(t, ctx, c, ns, clientName, true)

	bus := newTestBus(t)
	captured := subscribeHistory(t, bus)
	r := &downloadctl.Reconciler{Client: c, Recorder: k8sevents.NewFakeRecorder(50), DataDir: t.TempDir(), Bus: bus}

	newTorrentDownload(t, ctx, c, ns, name, "guid-"+name)
	reconcileOK(t, r, ns, name)
	engineWrites(t, ctx, c, ns, name, steadyItem(stage))
	reconcileOK(t, r, ns, name)

	steady := getDownload(t, ctx, c, ns, name)
	require.NotEmpty(t, steady.Status.Engine)
	require.NotNil(t, steady.Status.StartedAt)
	require.NotNil(t, k8s.FindCondition(steady.Status.Conditions, downloadv1alpha1.DownloadConditionFailed))
	return r, captured
}

func countAction(evs []capturedEvent, action string) (n int, reason string) {
	for _, e := range evs {
		if e.payload.Action == action {
			n++
			reason = e.payload.Reason
		}
	}
	return n, reason
}

func assertEngineLeavesIntact(t *testing.T, st downloadv1alpha1.DownloadStatus) {
	t.Helper()
	assert.Equal(t, "0123456789abcdef0123456789abcdef01234567", st.DownloadID, "downloadID released")
	assert.EqualValues(t, 4<<30, st.DownloadedBytes, "downloadedBytes released")
	assert.EqualValues(t, 50, st.ProgressPercent, "progressPercent released")
	assert.Equal(t, "/data/torrents/movie/fail", st.OutputPath, "outputPath released")
	assert.Len(t, st.Files, 1, "files released")
	assert.NotNil(t, st.LastProgressAt, "lastProgressAt released")
}

// TestReleaseFaultBlocklistsASteadyStateDownload: the engine reports a
// stall; the controller records it, labels the Download blocklisted, sets
// blocklistedUntil and the Failed condition, and announces failed and
// blocklisted once -- without releasing a single engine leaf, and without
// claiming the engine's report. The verdict then outlives the report.
func TestReleaseFaultBlocklistsASteadyStateDownload(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns = "default"
	r, captured := steadyDownload(t, ctx, c, "qbit-stall", "stall-dl", downloadv1alpha1.DownloadStageTransferring)
	require.Equal(t, downloadv1alpha1.DownloadPhaseDownloading, getDownload(t, ctx, c, ns, "stall-dl").Status.Phase)

	stalled := steadyItem(downloadv1alpha1.DownloadStageTransferring)
	stalled.Status = download.StatusFailed
	stalled.FailureReason = downloadv1alpha1.DownloadFailureStalled
	stalled.Message = "stalled: no download progress for 24h0m0s"
	engineWrites(t, ctx, c, ns, "stall-dl", stalled)

	before := time.Now()
	reconcileOK(t, r, ns, "stall-dl")
	reconcileOK(t, r, ns, "stall-dl") // level re-run: nothing new

	got := getDownload(t, ctx, c, ns, "stall-dl")
	assert.Equal(t, downloadv1alpha1.DownloadPhaseBlocklisted, got.Status.Phase)
	assert.Equal(t, downloadv1alpha1.DownloadFailureStalled, got.Status.FailureReason)
	assert.Equal(t, downloadv1alpha1.LabelBlocklistedValue, got.Labels[downloadv1alpha1.LabelBlocklisted])
	assert.Equal(t, "qbit-stall", got.Labels[downloadv1alpha1.LabelClient], "the blocklist apply released the client label")
	assert.Equal(t, got.Status.Engine, got.Labels[downloadv1alpha1.LabelEngine], "the blocklist apply released the engine label")
	assert.Equal(t, "qbit-stall", got.Spec.ClientRef, "the blocklist apply released spec.clientRef")
	require.NotNil(t, got.Status.BlocklistedUntil)
	assert.WithinDuration(t, before.Add(downloadv1alpha1.DefaultBlocklistTTL), got.Status.BlocklistedUntil.Time, time.Minute)

	failedCond := k8s.FindCondition(got.Status.Conditions, downloadv1alpha1.DownloadConditionFailed)
	require.NotNil(t, failedCond)
	assert.Equal(t, metav1.ConditionTrue, failedCond.Status)
	assert.Equal(t, "Stalled", failedCond.Reason)
	assert.Contains(t, failedCond.Message, "no download progress")

	assertEngineLeavesIntact(t, got.Status)
	assert.Equal(t, downloadv1alpha1.DownloadFailureStalled, got.Status.EngineFailureReason)

	// Ownership, where an over-claim is visible at all.
	assert.True(t, ownsField(t, got.ManagedFields, k8s.ManagerGrabarr, "", "metadata", "labels", downloadv1alpha1.LabelBlocklisted))
	assert.True(t, ownsField(t, got.ManagedFields, k8s.ManagerGrabarr, "status", "status", "failureReason"))
	assert.True(t, ownsField(t, got.ManagedFields, k8s.ManagerGrabarr, "status", "status", "blocklistedUntil"))
	assert.False(t, ownsField(t, got.ManagedFields, k8s.ManagerGrabarr, "status", "status", "engineFailureReason"),
		"the controller claimed the engine's report")
	assert.True(t, ownsField(t, got.ManagedFields, k8s.ManagerGrabarrEngine, "status", "status", "engineFailureReason"))
	assert.False(t, ownsField(t, got.ManagedFields, k8s.ManagerGrabarrEngine, "status", "status", "failureReason"),
		"the engine owns the controller's verdict")

	require.Eventually(t, func() bool {
		n, _ := countAction(captured(), events.ActionBlocklisted)
		return n == 1
	}, 5*time.Second, 10*time.Millisecond, "have %v", actionsOf(captured()))
	require.Never(t, func() bool {
		f, _ := countAction(captured(), events.ActionFailed)
		b, _ := countAction(captured(), events.ActionBlocklisted)
		return f > 1 || b > 1
	}, 300*time.Millisecond, 20*time.Millisecond, "an edge is announced once: %v", actionsOf(captured()))
	n, reason := countAction(captured(), events.ActionFailed)
	assert.Equal(t, 1, n, "failed must be announced even though the phase never read Failed")
	assert.Equal(t, string(downloadv1alpha1.DownloadFailureStalled), reason)
	_, reason = countAction(captured(), events.ActionBlocklisted)
	assert.Equal(t, string(downloadv1alpha1.DownloadFailureStalled), reason)

	// The engine restarts and forgets the failure: its next telemetry carries
	// none. The verdict, the label and the deadline all stay.
	until := got.Status.BlocklistedUntil.DeepCopy()
	engineWrites(t, ctx, c, ns, "stall-dl", steadyItem(downloadv1alpha1.DownloadStageTransferring))
	reconcileOK(t, r, ns, "stall-dl")
	after := getDownload(t, ctx, c, ns, "stall-dl")
	assert.Empty(t, after.Status.EngineFailureReason)
	assert.Equal(t, downloadv1alpha1.DownloadPhaseBlocklisted, after.Status.Phase)
	assert.Equal(t, downloadv1alpha1.DownloadFailureStalled, after.Status.FailureReason)
	assert.True(t, until.Equal(after.Status.BlocklistedUntil), "blocklistedUntil moved on a re-run")
}

// TestLocalFaultFailsWithoutBlocklisting: diskFull fails the Download and
// announces failed, but the release is not blocklisted.
func TestLocalFaultFailsWithoutBlocklisting(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns = "default"
	r, captured := steadyDownload(t, ctx, c, "qbit-full", "full-dl", downloadv1alpha1.DownloadStageTransferring)

	full := steadyItem(downloadv1alpha1.DownloadStageTransferring)
	full.Status = download.StatusFailed
	full.FailureReason = downloadv1alpha1.DownloadFailureDiskFull
	engineWrites(t, ctx, c, ns, "full-dl", full)
	reconcileOK(t, r, ns, "full-dl")

	got := getDownload(t, ctx, c, ns, "full-dl")
	assert.Equal(t, downloadv1alpha1.DownloadPhaseFailed, got.Status.Phase)
	assert.Equal(t, downloadv1alpha1.DownloadFailureDiskFull, got.Status.FailureReason)
	assert.NotContains(t, got.Labels, downloadv1alpha1.LabelBlocklisted)
	assert.Nil(t, got.Status.BlocklistedUntil)
	assert.Equal(t, "DiskFull", k8s.FindCondition(got.Status.Conditions, downloadv1alpha1.DownloadConditionFailed).Reason)
	assertEngineLeavesIntact(t, got.Status)

	require.Eventually(t, func() bool {
		n, _ := countAction(captured(), events.ActionFailed)
		return n == 1
	}, 5*time.Second, 10*time.Millisecond)
	_, reason := countAction(captured(), events.ActionFailed)
	assert.Equal(t, string(downloadv1alpha1.DownloadFailureDiskFull), reason)
	n, _ := countAction(captured(), events.ActionBlocklisted)
	assert.Zero(t, n)

	// Terminal: the engine forgetting the failure does not revive it.
	engineWrites(t, ctx, c, ns, "full-dl", steadyItem(downloadv1alpha1.DownloadStageTransferring))
	reconcileOK(t, r, ns, "full-dl")
	assert.Equal(t, downloadv1alpha1.DownloadPhaseFailed, getDownload(t, ctx, c, ns, "full-dl").Status.Phase)
}

// TestLiftedBlocklistIsNotReapplied: an operator removing the label from a
// Download grabarr blocklisted leaves it Failed, and grabarr does not put
// the label back.
func TestLiftedBlocklistIsNotReapplied(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns = "default"
	r, _ := steadyDownload(t, ctx, c, "qbit-lift", "lift-dl", downloadv1alpha1.DownloadStageTransferring)

	missing := steadyItem(downloadv1alpha1.DownloadStageTransferring)
	missing.Status = download.StatusFailed
	missing.FailureReason = downloadv1alpha1.DownloadFailureStalled
	engineWrites(t, ctx, c, ns, "lift-dl", missing)
	reconcileOK(t, r, ns, "lift-dl")
	require.Equal(t, downloadv1alpha1.DownloadPhaseBlocklisted, getDownload(t, ctx, c, ns, "lift-dl").Status.Phase)

	live := getDownload(t, ctx, c, ns, "lift-dl")
	delete(live.Labels, downloadv1alpha1.LabelBlocklisted)
	require.NoError(t, c.Update(ctx, live))
	reconcileOK(t, r, ns, "lift-dl")
	reconcileOK(t, r, ns, "lift-dl")

	got := getDownload(t, ctx, c, ns, "lift-dl")
	assert.NotContains(t, got.Labels, downloadv1alpha1.LabelBlocklisted, "grabarr re-applied a lifted blocklist")
	assert.Equal(t, downloadv1alpha1.DownloadPhaseFailed, got.Status.Phase)
	assert.Equal(t, downloadv1alpha1.DownloadFailureStalled, got.Status.FailureReason)
}

// TestImportRejectedBlocklists: importarr refusing every file is read from
// status.import -- never written -- and blocklists the release.
func TestImportRejectedBlocklists(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns = "default"
	r, captured := steadyDownload(t, ctx, c, "qbit-rej", "rej-dl", downloadv1alpha1.DownloadStageDone)
	require.Equal(t, downloadv1alpha1.DownloadPhaseCompleted, getDownload(t, ctx, c, ns, "rej-dl").Status.Phase)

	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerImportarr, downloadac.Download("rej-dl", ns).WithStatus(
		downloadac.DownloadStatus().WithImport(downloadac.ImportState().
			WithState(downloadv1alpha1.ImportPhaseBlocked).
			WithMessage(downloadv1alpha1.ImportMessageEveryFileRejected).
			WithRejections("movie.mkv: quality SDTV is not in the profile"))))
	require.NoError(t, err)
	reconcileOK(t, r, ns, "rej-dl")

	got := getDownload(t, ctx, c, ns, "rej-dl")
	assert.Equal(t, downloadv1alpha1.DownloadPhaseBlocklisted, got.Status.Phase)
	assert.Equal(t, downloadv1alpha1.DownloadFailureImportRejected, got.Status.FailureReason)
	assert.Equal(t, downloadv1alpha1.LabelBlocklistedValue, got.Labels[downloadv1alpha1.LabelBlocklisted])
	require.NotNil(t, got.Status.Import, "the controller released importarr's status.import")
	assert.Equal(t, downloadv1alpha1.ImportPhaseBlocked, got.Status.Import.State)
	assert.False(t, ownsField(t, got.ManagedFields, k8s.ManagerGrabarr, "status", "status", "import"),
		"the controller claimed importarr's status.import")

	require.Eventually(t, func() bool {
		n, _ := countAction(captured(), events.ActionFailed)
		return n == 1
	}, 5*time.Second, 10*time.Millisecond)
	_, reason := countAction(captured(), events.ActionFailed)
	assert.Equal(t, string(downloadv1alpha1.DownloadFailureImportRejected), reason)
}

// TestHandSetBlocklistLabelIsAManualFailure: the one user action for failing
// a Download is labelling it blocklisted; grabarr records manual, gives it
// the default deadline, and announces both edges.
func TestHandSetBlocklistLabelIsAManualFailure(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns = "default"
	r, captured := steadyDownload(t, ctx, c, "qbit-man", "man-dl", downloadv1alpha1.DownloadStageTransferring)

	live := getDownload(t, ctx, c, ns, "man-dl")
	live.Labels[downloadv1alpha1.LabelBlocklisted] = downloadv1alpha1.LabelBlocklistedValue
	require.NoError(t, c.Update(ctx, live))
	reconcileOK(t, r, ns, "man-dl")

	got := getDownload(t, ctx, c, ns, "man-dl")
	assert.Equal(t, downloadv1alpha1.DownloadPhaseBlocklisted, got.Status.Phase)
	assert.Equal(t, downloadv1alpha1.DownloadFailureManual, got.Status.FailureReason)
	assert.NotNil(t, got.Status.BlocklistedUntil)
	assertEngineLeavesIntact(t, got.Status)

	require.Eventually(t, func() bool {
		f, _ := countAction(captured(), events.ActionFailed)
		b, _ := countAction(captured(), events.ActionBlocklisted)
		return f == 1 && b == 1
	}, 5*time.Second, 10*time.Millisecond, "have %v", actionsOf(captured()))
}

// TestSeedGoalMetIsRecordedOnceAndSticks: the engine reports the seed goal
// met; the controller records seedGoalMetAt and the SeedGoalMet condition
// and announces seedGoalMet once. An engine restart that forgets the goal
// does not un-record it.
func TestSeedGoalMetIsRecordedOnceAndSticks(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns = "default"
	r, captured := steadyDownload(t, ctx, c, "qbit-seed", "seed-dl", downloadv1alpha1.DownloadStageSeeding)

	steady := getDownload(t, ctx, c, ns, "seed-dl")
	require.Equal(t, downloadv1alpha1.DownloadPhaseSeeding, steady.Status.Phase)
	cond := k8s.FindCondition(steady.Status.Conditions, downloadv1alpha1.DownloadConditionSeedGoalMet)
	require.NotNil(t, cond, "a seeding torrent reports SeedGoalMet=False, not nothing")
	assert.Equal(t, metav1.ConditionFalse, cond.Status)

	met := steadyItem(downloadv1alpha1.DownloadStageDone)
	met.Status = download.StatusCompleted
	met.SeedGoalMet = true
	engineWrites(t, ctx, c, ns, "seed-dl", met)
	reconcileOK(t, r, ns, "seed-dl")
	reconcileOK(t, r, ns, "seed-dl")

	got := getDownload(t, ctx, c, ns, "seed-dl")
	require.NotNil(t, got.Status.SeedGoalMetAt)
	assert.True(t, k8s.IsConditionTrue(got.Status.Conditions, downloadv1alpha1.DownloadConditionSeedGoalMet))
	assert.True(t, got.Status.SeedGoalReached)
	assertEngineLeavesIntact(t, got.Status)
	assert.True(t, ownsField(t, got.ManagedFields, k8s.ManagerGrabarr, "status", "status", "seedGoalMetAt"))
	assert.False(t, ownsField(t, got.ManagedFields, k8s.ManagerGrabarr, "status", "status", "seedGoalReached"))

	require.Eventually(t, func() bool {
		n, _ := countAction(captured(), events.ActionSeedGoalMet)
		return n == 1
	}, 5*time.Second, 10*time.Millisecond, "have %v", actionsOf(captured()))

	at := got.Status.SeedGoalMetAt.DeepCopy()
	engineWrites(t, ctx, c, ns, "seed-dl", steadyItem(downloadv1alpha1.DownloadStageSeeding))
	reconcileOK(t, r, ns, "seed-dl")
	after := getDownload(t, ctx, c, ns, "seed-dl")
	assert.False(t, after.Status.SeedGoalReached)
	require.NotNil(t, after.Status.SeedGoalMetAt, "an engine restart un-recorded the seed goal")
	assert.True(t, at.Equal(after.Status.SeedGoalMetAt))
	assert.True(t, k8s.IsConditionTrue(after.Status.Conditions, downloadv1alpha1.DownloadConditionSeedGoalMet))
	require.Never(t, func() bool {
		n, _ := countAction(captured(), events.ActionSeedGoalMet)
		return n > 1
	}, 300*time.Millisecond, 20*time.Millisecond)
}

// A usenet download never seeds, so it carries no SeedGoalMet condition.
func TestUsenetDownloadHasNoSeedGoalCondition(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	const ns = "default"
	dc := newUsenetClient(t, ctx, c, ns, "nzb-seed")
	markEngineReady(t, ctx, c, ns, dc.Name, true)
	r := &downloadctl.Reconciler{Client: c, Recorder: k8sevents.NewFakeRecorder(10), DataDir: t.TempDir(), Bus: newTestBus(t)}

	newUsenetDownload(t, ctx, c, ns, "nzb-seed-dl", "guid-nzb-seed")
	reconcileOK(t, r, ns, "nzb-seed-dl")
	engineWrites(t, ctx, c, ns, "nzb-seed-dl", steadyItem(downloadv1alpha1.DownloadStageDone))
	reconcileOK(t, r, ns, "nzb-seed-dl")

	got := getDownload(t, ctx, c, ns, "nzb-seed-dl")
	assert.Equal(t, downloadv1alpha1.DownloadPhaseCompleted, got.Status.Phase)
	assert.Nil(t, k8s.FindCondition(got.Status.Conditions, downloadv1alpha1.DownloadConditionSeedGoalMet))
}
