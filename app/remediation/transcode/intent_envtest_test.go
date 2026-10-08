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

package transcode_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/transcode/transcodeplan"
)

// TestSuspendKeepsAWindowPlace (§5.17): a suspended Planned file keeps its
// window place and is never admitted; unsuspended, it is.
func TestSuspendKeepsAWindowPlace(t *testing.T) {
	h := newHarness(t, harnessOptions{slots: map[string]int32{"cpu": 0}})
	mf := h.file(t, "heat-1995")
	waitFile(t, h.c, mf, "Planned", phase(catalogv1alpha1.TranscodePhasePlanned))
	h.annotate(t, mf, catalogv1alpha1.AnnotationTranscodeSuspend, "true")
	got := waitFile(t, h.c, mf, "suspended", func(m *catalogv1alpha1.MediaFile) bool { return m.Status.Transcode.Suspended })
	assert.Equal(t, catalogv1alpha1.TranscodePhasePlanned, got.Status.Transcode.Phase)
	assert.Equal(t, transcodeplan.ReasonSuspended, got.Status.Transcode.Reason)
	assert.Equal(t, 1, h.ledger.Open("hevc"), "the window place is kept")
	h.ledger.SetSlots(map[string]int32{"cpu": 1})
	assert.Never(t, func() bool {
		var m catalogv1alpha1.MediaFile
		return h.c.Get(context.Background(), client.ObjectKeyFromObject(mf), &m) == nil &&
			m.Status.Transcode.Phase != catalogv1alpha1.TranscodePhasePlanned
	}, 5*time.Second, 200*time.Millisecond, "a suspended file is never admitted")
	h.annotate(t, mf, catalogv1alpha1.AnnotationTranscodeSuspend, "false")
	waitFile(t, h.c, mf, "dispatched", phase(catalogv1alpha1.TranscodePhaseQueued))
}

// TestCancelRecordsAVerdictAndRetryClearsIt (§5.17, D14): cancel makes a
// Planned file a Skipped/Cancelled verdict and records its nonce; retry
// clears the verdict, and the window plans the file again with attempts 0.
func TestCancelRecordsAVerdictAndRetryClearsIt(t *testing.T) {
	h := newHarness(t, harnessOptions{slots: map[string]int32{"cpu": 0}})
	mf := h.file(t, "heat-1995")
	waitFile(t, h.c, mf, "Planned", phase(catalogv1alpha1.TranscodePhasePlanned))
	h.annotate(t, mf, catalogv1alpha1.AnnotationTranscodeCancel, "1759752000")
	got := waitFile(t, h.c, mf, "Cancelled", func(m *catalogv1alpha1.MediaFile) bool {
		return m.Status.Transcode.Phase == catalogv1alpha1.TranscodePhaseSkipped && m.Status.Transcode.Reason == transcodeplan.ReasonCancelled
	})
	require.NotNil(t, got.Status.HandledNonces)
	assert.Equal(t, "1759752000", got.Status.HandledNonces.TranscodeCancel)
	assert.Never(t, func() bool {
		var m catalogv1alpha1.MediaFile
		return h.c.Get(context.Background(), client.ObjectKeyFromObject(mf), &m) == nil &&
			m.Status.Transcode.Phase != catalogv1alpha1.TranscodePhaseSkipped
	}, 2*time.Second, 200*time.Millisecond, "the verdict holds")

	h.annotate(t, mf, catalogv1alpha1.AnnotationTranscodeRetry, "1759752001")
	got = waitFile(t, h.c, mf, "back in the window", phase(catalogv1alpha1.TranscodePhasePlanned))
	assert.Equal(t, "1759752001", got.Status.HandledNonces.TranscodeRetry)
	assert.Equal(t, "1759752000", got.Status.HandledNonces.TranscodeCancel, "the cancel stays handled")
	assert.Zero(t, got.Status.Transcode.Attempts)
}

// TestHardwareIntentAppliesAtTheNextDispatch (§5.17): a hardware intent is
// recorded on a Running file's block at once, and never moves the dispatch
// already out.
func TestHardwareIntentAppliesAtTheNextDispatch(t *testing.T) {
	var mf *catalogv1alpha1.MediaFile
	h := newHarness(t, harnessOptions{slots: map[string]int32{"cpu": 1, "nvidia": 0}, seed: func(h *harness) {
		mf = h.seeded(t, "heat-1995", catalogv1alpha1.TranscodePhaseRunning, 9)
	}})
	h.annotate(t, mf, catalogv1alpha1.AnnotationTranscodeHardware, "nvidia")
	got := waitFile(t, h.c, mf, "recorded", func(m *catalogv1alpha1.MediaFile) bool { return m.Status.Transcode.Hardware == "nvidia" })
	assert.Equal(t, catalogv1alpha1.TranscodePhaseRunning, got.Status.Transcode.Phase, "a running dispatch is not moved")
	assert.Equal(t, catalogv1alpha1.TranscodeClass("cpu"), got.Status.Transcode.Class)
	assert.Equal(t, int64(9), got.Status.Transcode.Dispatch.Seq)
	assert.Zero(t, got.Status.Transcode.Dispatch.AnsweredSeq)
}

// TestPriorityIntentOrdersWindowAndAdmission (§5.17): with a window of one,
// the file whose priority intent is highest takes the place, though name
// order alone would give it to the other.
func TestPriorityIntentOrdersWindowAndAdmission(t *testing.T) {
	var first, urgent *catalogv1alpha1.MediaFile
	h := newHarness(t, harnessOptions{slots: map[string]int32{"cpu": 0}, window: 1, seed: func(h *harness) {
		first = h.seeded(t, "aaa-first-by-name", "", 0)
		urgent = h.seeded(t, "zzz-urgent", "", 0)
		h.annotate(t, urgent, catalogv1alpha1.AnnotationTranscodePriority, "100")
	}})
	got := waitFile(t, h.c, urgent, "the urgent file takes the one window place", phase(catalogv1alpha1.TranscodePhasePlanned))
	assert.Equal(t, int32(100), got.Status.Transcode.Priority)
	assert.Never(t, func() bool {
		var other catalogv1alpha1.MediaFile
		return h.c.Get(context.Background(), client.ObjectKeyFromObject(first), &other) == nil && other.Status.Transcode != nil
	}, 3*time.Second, 200*time.Millisecond, "name order alone would have taken aaa")
	assert.Equal(t, 1, h.ledger.Open("hevc"))
}
