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

package audiobook_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/audiobook"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/pkg/metadata"
)

// The Audiobook's phase is rollup.LeafPhase over the Audiobook's own facts
// (2026-11-10 design D4): no fresh metadata reads Pending, the publication
// date gates Unavailable, the pending grab reads Delayed and the profile
// decides CutoffUnevaluated -- the two buckets AudiobookPhase used to lack.
func TestPhaseInputsReadTheAudiobook(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	fresh := func(release *metav1.Time) *catalogv1alpha1.AudiobookMetadata {
		return &catalogv1alpha1.AudiobookMetadata{Title: "Guards! Guards!", ReleaseDate: release, RefreshedAt: metav1.NewTime(now.Add(-time.Hour)), SchemaVersion: metadata.SchemaVersion}
	}
	dated := func(d int) *metav1.Time { t := metav1.NewTime(now.AddDate(0, 0, d)); return &t }
	grab := &catalogv1alpha1.PendingGrab{}
	cases := []struct {
		name                                                          string
		status                                                        catalogv1alpha1.AudiobookStatus
		monitored, metadataReady, hasFile, cutoffMet, profileResolved bool
		phase                                                         commonv1.LeafPhase
		reason                                                        string
	}{
		{"unmonitored wins over everything", catalogv1alpha1.AudiobookStatus{PendingGrab: grab}, false, true, true, true, true, catalogv1alpha1.AudiobookPhaseUnmonitored, commonv1.CutoffReasonCutoffMet},
		{"unmonitored still wins: a leftover pending grab is not the user's intent", catalogv1alpha1.AudiobookStatus{PendingGrab: grab}, false, true, false, false, true, catalogv1alpha1.AudiobookPhaseUnmonitored, commonv1.CutoffReasonPending},
		{"no metadata yet is Pending", catalogv1alpha1.AudiobookStatus{}, true, false, false, false, true, catalogv1alpha1.AudiobookPhasePending, commonv1.CutoffReasonPending},
		{"monitored, fresh, no file yet", catalogv1alpha1.AudiobookStatus{Metadata: fresh(nil)}, true, true, false, false, true, catalogv1alpha1.AudiobookPhaseWanted, commonv1.CutoffReasonPending},
		{"published, no file yet", catalogv1alpha1.AudiobookStatus{Metadata: fresh(dated(-30))}, true, true, false, false, true, catalogv1alpha1.AudiobookPhaseWanted, commonv1.CutoffReasonPending},
		{"not yet published: Unavailable", catalogv1alpha1.AudiobookStatus{Metadata: fresh(dated(30))}, true, true, false, false, true, catalogv1alpha1.AudiobookPhaseUnavailable, commonv1.CutoffReasonPending},
		{"files imported and meeting the cutoff", catalogv1alpha1.AudiobookStatus{Metadata: fresh(nil)}, true, true, true, true, true, catalogv1alpha1.AudiobookPhaseImported, commonv1.CutoffReasonCutoffMet},
		{"files below the cutoff", catalogv1alpha1.AudiobookStatus{Metadata: fresh(nil)}, true, true, true, false, true, catalogv1alpha1.AudiobookPhaseCutoffUnmet, commonv1.CutoffReasonCutoffUnmet},
		{"files and no profile to judge them", catalogv1alpha1.AudiobookStatus{Metadata: fresh(nil)}, true, true, true, false, false, catalogv1alpha1.AudiobookPhaseCutoffUnevaluated, commonv1.CutoffReasonProfileUnresolved},
		// A pending grab is the delay-profile window. It is the only way
		// Delayed is reachable before a grab entry exists.
		{"a wanted audiobook with a pending grab is Delayed", catalogv1alpha1.AudiobookStatus{Metadata: fresh(nil), PendingGrab: grab}, true, true, false, false, true, catalogv1alpha1.AudiobookPhaseDelayed, commonv1.CutoffReasonPending},
		{"a pending upgrade outranks CutoffUnmet: the pending grab IS the upgrade story", catalogv1alpha1.AudiobookStatus{Metadata: fresh(nil), PendingGrab: grab}, true, true, true, false, true, catalogv1alpha1.AudiobookPhaseDelayed, commonv1.CutoffReasonCutoffUnmet},
		{"but Imported outranks a delayed upgrade: a pending grab is an internal timer, not something the user can cancel", catalogv1alpha1.AudiobookStatus{Metadata: fresh(nil), PendingGrab: grab}, true, true, true, true, true, catalogv1alpha1.AudiobookPhaseImported, commonv1.CutoffReasonCutoffMet},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := &catalogv1alpha1.Audiobook{Status: c.status}
			phase, reason := rollup.LeafPhase(audiobook.PhaseInputs(m, c.monitored, c.metadataReady, c.hasFile, c.cutoffMet, c.profileResolved, now))
			assert.Equal(t, c.phase, phase)
			assert.Equal(t, c.reason, reason)
		})
	}
}

// MetadataStale is the one staleness rule the pass and the start-time skip
// share, on the Audiobook's flat 30-day cadence.
func TestMetadataStaleIsTheAudiobooksRefreshRule(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	ttl := metadata.RefreshTTL(commonv1.MediaKindAudiobook, metadata.RefreshStateActive, now)
	md := func(age time.Duration, schema int32) *catalogv1alpha1.AudiobookMetadata {
		return &catalogv1alpha1.AudiobookMetadata{RefreshedAt: metav1.NewTime(now.Add(-age)), SchemaVersion: schema}
	}
	cases := []struct {
		name            string
		md              *catalogv1alpha1.AudiobookMetadata
		stale, outdated bool
	}{
		{"no document", nil, true, false},
		{"fresh", md(time.Hour, metadata.SchemaVersion), false, false},
		{"past its TTL", md(ttl+time.Minute, metadata.SchemaVersion), true, false},
		{"recent but written before the gateway learned a field", md(time.Hour, metadata.SchemaVersion-1), false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stale, outdated := audiobook.MetadataStale(&catalogv1alpha1.Audiobook{Status: catalogv1alpha1.AudiobookStatus{Metadata: c.md}}, now)
			assert.Equal(t, c.stale, stale, "stale")
			assert.Equal(t, c.outdated, outdated, "outdated")
		})
	}
}

// The Audiobook watch's SkipInitial reads the Audiobook as the cache lists
// it, a refresh due whenever the pass would ask for one.
func TestIdleInputsReadTheAudiobook(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	m := &catalogv1alpha1.Audiobook{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "guards-guards", Generation: 2},
		Status: catalogv1alpha1.AudiobookStatus{
			ObservedGeneration: 2,
			Phase:              catalogv1alpha1.AudiobookPhaseUnmonitored,
			Metadata:           &catalogv1alpha1.AudiobookMetadata{RefreshedAt: metav1.NewTime(now.Add(-time.Hour)), SchemaVersion: metadata.SchemaVersion},
			Conditions:         []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}, {Type: "BookRefResolved", Status: metav1.ConditionTrue}},
		},
	}
	assert.True(t, rollup.LeafIdleAtStart(audiobook.IdleInputs(m, false, now)), "unmonitored, fresh, still unmonitored")
	assert.False(t, rollup.LeafIdleAtStart(audiobook.IdleInputs(m, true, now)), "monitored now: the pass moves it")

	stale := m.DeepCopy()
	stale.Status.Metadata.RefreshedAt = metav1.NewTime(now.AddDate(0, -3, 0))
	assert.True(t, audiobook.IdleInputs(stale, false, now).RefreshDue)
	assert.False(t, rollup.LeafIdleAtStart(audiobook.IdleInputs(stale, false, now)), "a refresh due is work, unmonitored or not")

	retired := m.DeepCopy()
	retired.Status.Conditions = append(retired.Status.Conditions, metav1.Condition{Type: "CutoffMet", Status: metav1.ConditionFalse})
	assert.False(t, rollup.LeafIdleAtStart(audiobook.IdleInputs(retired, false, now)), "a retired condition still carried: one pass drops it")

	searching := m.DeepCopy()
	searching.Status.SearchDispatch = &catalogv1alpha1.Dispatch{Seq: 2, AnsweredSeq: 1}
	assert.True(t, audiobook.IdleInputs(searching, false, now).SearchInFlight)
}
