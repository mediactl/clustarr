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
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
)

// Every bucket of the leaf phase, with Movie's precedence (2026-11-10
// design D4), and the cutoffReason each writes.
func TestLeafPhaseHasMoviesPrecedence(t *testing.T) {
	ready := rollup.LeafInputs{Monitored: true, MetadataReady: true, Released: true, CutoffEvaluated: true}
	with := func(f func(*rollup.LeafInputs)) rollup.LeafInputs { in := ready; f(&in); return in }
	cases := []struct {
		name   string
		in     rollup.LeafInputs
		phase  commonv1.LeafPhase
		reason string
	}{
		{"unmonitored outranks everything", with(func(i *rollup.LeafInputs) {
			i.Monitored = false
			i.HasFile = true
			i.CutoffMet = true
			i.PendingGrab = true
		}), commonv1.LeafPhaseUnmonitored, commonv1.CutoffReasonCutoffMet},
		{"no metadata yet is Pending, even with a file", with(func(i *rollup.LeafInputs) { i.MetadataReady = false; i.HasFile = true; i.CutoffMet = true }), commonv1.LeafPhasePending, commonv1.CutoffReasonCutoffMet},
		{"a file at the cutoff is Imported", with(func(i *rollup.LeafInputs) { i.HasFile = true; i.CutoffMet = true }), commonv1.LeafPhaseImported, commonv1.CutoffReasonCutoffMet},
		{"a file at the cutoff outranks a pending grab", with(func(i *rollup.LeafInputs) { i.HasFile = true; i.CutoffMet = true; i.PendingGrab = true }), commonv1.LeafPhaseImported, commonv1.CutoffReasonCutoffMet},
		{"an album with a track missing is CutoffUnmet whatever its files' quality", with(func(i *rollup.LeafInputs) { i.HasFile = true; i.CutoffMet = true; i.TracksMissing = true }), commonv1.LeafPhaseCutoffUnmet, commonv1.CutoffReasonTracksMissing},
		{"a pending grab with no file is Delayed", with(func(i *rollup.LeafInputs) { i.PendingGrab = true }), commonv1.LeafPhaseDelayed, commonv1.CutoffReasonPending},
		{"a pending upgrade of a file below the cutoff is Delayed", with(func(i *rollup.LeafInputs) { i.HasFile = true; i.PendingGrab = true }), commonv1.LeafPhaseDelayed, commonv1.CutoffReasonCutoffUnmet},
		{"a pending grab outranks Unavailable", with(func(i *rollup.LeafInputs) { i.Released = false; i.PendingGrab = true }), commonv1.LeafPhaseDelayed, commonv1.CutoffReasonPending},
		{"a file with no profile to judge it is CutoffUnevaluated", with(func(i *rollup.LeafInputs) { i.HasFile = true; i.CutoffEvaluated = false; i.CutoffMet = true }), commonv1.LeafPhaseCutoffUnevaluated, commonv1.CutoffReasonProfileUnresolved},
		{"a file below the cutoff is CutoffUnmet", with(func(i *rollup.LeafInputs) { i.HasFile = true }), commonv1.LeafPhaseCutoffUnmet, commonv1.CutoffReasonCutoffUnmet},
		{"a file outranks availability", with(func(i *rollup.LeafInputs) { i.HasFile = true; i.Released = false }), commonv1.LeafPhaseCutoffUnmet, commonv1.CutoffReasonCutoffUnmet},
		{"unreleased with nothing is Unavailable", with(func(i *rollup.LeafInputs) { i.Released = false }), commonv1.LeafPhaseUnavailable, commonv1.CutoffReasonPending},
		{"released with nothing is Wanted", ready, commonv1.LeafPhaseWanted, commonv1.CutoffReasonPending},
		{"an unresolved profile with no file is still Wanted", with(func(i *rollup.LeafInputs) { i.CutoffEvaluated = false }), commonv1.LeafPhaseWanted, commonv1.CutoffReasonPending},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			phase, reason := rollup.LeafPhase(c.in)
			assert.Equal(t, c.phase, phase)
			assert.Equal(t, c.reason, reason)
			assert.Equal(t, phase == commonv1.LeafPhaseImported || (c.in.HasFile && c.in.CutoffEvaluated && c.in.CutoffMet && !c.in.TracksMissing), rollup.LeafCutoffMet(c.in), "cutoffMet agrees with the phase")
		})
	}
}

// The container's rollup: its own phase from its metadata and monitor
// flag, the counts from its children's phases, the release dates over
// monitored, dated children only (Sonarr's series statistics).
func TestContainerFoldsItsChildren(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	at := func(d int) *metav1.Time { t := metav1.NewTime(now.AddDate(0, 0, d)); return &t }
	children := []rollup.ContainerChild{
		{Monitored: true, Phase: commonv1.LeafPhaseWanted, ReleaseDate: at(-30)},
		{Monitored: true, Phase: commonv1.LeafPhaseWanted, ReleaseDate: at(-10)},
		{Monitored: true, Phase: commonv1.LeafPhaseDownloading, ReleaseDate: at(-5)},
		{Monitored: true, Phase: commonv1.LeafPhaseDelayed},
		{Monitored: true, Phase: commonv1.LeafPhaseCutoffUnmet, HasFile: true, ReleaseDate: at(-400)},
		{Monitored: true, Phase: commonv1.LeafPhaseImported, HasFile: true, ReleaseDate: at(-200)},
		{Monitored: true, Phase: commonv1.LeafPhaseUnavailable, ReleaseDate: at(20)},
		{Monitored: true, Phase: commonv1.LeafPhaseUnavailable, ReleaseDate: at(45)},
		// An unmonitored child counts toward no date, as Sonarr nulls it out.
		{Monitored: false, Phase: commonv1.LeafPhaseUnmonitored, ReleaseDate: at(3)},
		{Monitored: false, Phase: commonv1.LeafPhaseUnmonitored, ReleaseDate: at(-1)},
	}
	got := rollup.Container(true, true, children, now)
	assert.Equal(t, catalogv1alpha1.ContainerPhaseReady, got.Phase)
	assert.EqualValues(t, 2, got.MissingChildCount)
	assert.EqualValues(t, 2, got.DownloadingChildCount)
	assert.EqualValues(t, 1, got.CutoffUnmetChildCount)
	assert.Equal(t, at(20).Time, got.NextReleaseDate.Time)
	assert.Equal(t, at(-5).Time, got.PreviousReleaseDate.Time)

	assert.Equal(t, catalogv1alpha1.ContainerPhasePending, rollup.Container(true, false, children, now).Phase, "no metadata yet")
	assert.Equal(t, catalogv1alpha1.ContainerPhaseUnmonitored, rollup.Container(false, true, children, now).Phase)
	empty := rollup.Container(true, true, nil, now)
	assert.Nil(t, empty.NextReleaseDate)
	assert.Nil(t, empty.PreviousReleaseDate)
	assert.Zero(t, empty.MissingChildCount)
}
