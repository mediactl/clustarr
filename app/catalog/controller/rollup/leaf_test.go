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

package rollup_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
)

// A manager's start enqueues every leaf the cache lists. One whose pass
// would change nothing -- unmonitored and still so, or imported and still
// monitored, with its spec observed, no refresh due and nothing in flight --
// is left to its watches (2026-11-10 design D14).
func TestAnIdleLeafIsSkippedAtAManagersStart(t *testing.T) {
	base := func(phase commonv1.LeafPhase, monitored bool) rollup.LeafIdle {
		return rollup.LeafIdle{Generation: 3, ObservedGeneration: 3, Monitored: monitored, Phase: phase}
	}
	with := func(in rollup.LeafIdle, f func(*rollup.LeafIdle)) rollup.LeafIdle { f(&in); return in }
	ref := "entry"
	cases := []struct {
		name string
		in   rollup.LeafIdle
		idle bool
	}{
		{"unmonitored and still unmonitored", base(commonv1.LeafPhaseUnmonitored, false), true},
		{"reads unmonitored but is monitored now", base(commonv1.LeafPhaseUnmonitored, true), false},
		{"imported and monitored", base(commonv1.LeafPhaseImported, true), true},
		{"imported but unmonitored now", base(commonv1.LeafPhaseImported, false), false},
		{"wanted", base(commonv1.LeafPhaseWanted, true), false},
		{"unavailable", base(commonv1.LeafPhaseUnavailable, true), false},
		{"cutoff unmet", base(commonv1.LeafPhaseCutoffUnmet, true), false},
		{"cutoff unevaluated", base(commonv1.LeafPhaseCutoffUnevaluated, true), false},
		{"pending", base(commonv1.LeafPhasePending, true), false},
		{"no phase yet", base("", true), false},
		{"spec not yet observed", with(base(commonv1.LeafPhaseImported, true), func(i *rollup.LeafIdle) { i.Generation = 4 }), false},
		{"a metadata refresh due", with(base(commonv1.LeafPhaseImported, true), func(i *rollup.LeafIdle) { i.RefreshDue = true }), false},
		{"a refresh due on an unmonitored item", with(base(commonv1.LeafPhaseUnmonitored, false), func(i *rollup.LeafIdle) { i.RefreshDue = true }), false},
		{"a grab in flight", with(base(commonv1.LeafPhaseImported, true), func(i *rollup.LeafIdle) {
			i.ActiveDownloadRef, i.DownloadPhase = &ref, commonv1.DownloadPhaseDownloading
		}), false},
		{"a download phase alone", with(base(commonv1.LeafPhaseImported, true), func(i *rollup.LeafIdle) { i.DownloadPhase = commonv1.DownloadPhaseDownloading }), false},
		{"a pending grab", with(base(commonv1.LeafPhaseUnmonitored, false), func(i *rollup.LeafIdle) { i.PendingGrab = true }), false},
		{"a search in flight", with(base(commonv1.LeafPhaseImported, true), func(i *rollup.LeafIdle) { i.SearchInFlight = true }), false},
		{"still carrying a condition retired on 2026-11-10", with(base(commonv1.LeafPhaseImported, true), func(i *rollup.LeafIdle) {
			i.Conditions = []metav1.Condition{{Type: "CutoffMet", Status: metav1.ConditionTrue, Reason: "CutoffMet"}}
		}), false},
		{"carrying Ready alone", with(base(commonv1.LeafPhaseImported, true), func(i *rollup.LeafIdle) {
			i.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}}
		}), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.idle, rollup.LeafIdleAtStart(c.in))
		})
	}
}

// The retired conditions leave on a copy; Ready, DeadLettered and the
// conditions that report work stay, in order.
func TestDropRetiredLeafConditionsKeepsTheRest(t *testing.T) {
	in := []metav1.Condition{
		{Type: "Ready", Status: metav1.ConditionTrue},
		{Type: "HasFile", Status: metav1.ConditionTrue},
		{Type: "MetadataReady", Status: metav1.ConditionTrue},
		{Type: "CutoffMet", Status: metav1.ConditionFalse},
		{Type: "Released", Status: metav1.ConditionTrue},
		{Type: "TracksSynced", Status: metav1.ConditionTrue},
		{Type: "DeadLettered", Status: metav1.ConditionFalse},
	}
	before := len(in)
	got := rollup.DropRetiredLeafConditions(in)
	assert.Equal(t, []string{"Ready", "MetadataReady", "TracksSynced", "DeadLettered"}, types(got))
	assert.Len(t, in, before, "the input is not modified")
	assert.Equal(t, "HasFile", in[1].Type, "the input is not modified")
	assert.Empty(t, rollup.DropRetiredLeafConditions(nil))
}

func types(conds []metav1.Condition) []string {
	out := make([]string, 0, len(conds))
	for _, c := range conds {
		out = append(out, c.Type)
	}
	return out
}

// The shared leaf overlay: a live entry reads Downloading over whatever the
// leaf computed, a settled or absent one leaves the computation alone.
func TestLeafOverlayReadsALiveEntryAsDownloading(t *testing.T) {
	cases := []struct {
		name   string
		entry  *catalogv1alpha1.DownloadEntry
		phase  commonv1.LeafPhase
		active bool
	}{
		{"no entry", nil, "", false},
		{"downloading", &catalogv1alpha1.DownloadEntry{ID: "e", Phase: commonv1.DownloadPhaseDownloading}, commonv1.LeafPhaseDownloading, true},
		{"queued", &catalogv1alpha1.DownloadEntry{ID: "e", Phase: commonv1.DownloadPhaseQueued}, commonv1.LeafPhaseDownloading, true},
		{"imported", &catalogv1alpha1.DownloadEntry{ID: "e", Phase: commonv1.DownloadPhaseImported}, "", false},
		{"failed", &catalogv1alpha1.DownloadEntry{ID: "e", Phase: commonv1.DownloadPhaseFailed}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			phase, active := rollup.LeafOverlay(c.entry)
			assert.Equal(t, c.phase, phase)
			assert.Equal(t, c.active, active)
		})
	}
}
