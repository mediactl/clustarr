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

package book_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/book"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
	"github.com/mediactl/clustarr/pkg/metadata"
)

// The Book's phase is rollup.LeafPhase over the Book's own facts (2026-11-10
// design D4): no fresh metadata reads Pending, the work's publication date
// gates Unavailable, the pending grab reads Delayed and the profile decides
// CutoffUnevaluated.
func TestPhaseInputsReadTheBook(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	fresh := func(release *metav1.Time) *catalogv1alpha1.BookMetadata {
		return &catalogv1alpha1.BookMetadata{Title: "Dune", ReleaseDate: release, RefreshedAt: metav1.NewTime(now.Add(-time.Hour)), SchemaVersion: metadata.SchemaVersion}
	}
	dated := func(d int) *metav1.Time { t := metav1.NewTime(now.AddDate(0, 0, d)); return &t }
	cases := []struct {
		name                                                          string
		status                                                        catalogv1alpha1.BookStatus
		monitored, metadataReady, hasFile, cutoffMet, profileResolved bool
		phase                                                         commonv1.LeafPhase
		reason                                                        string
	}{
		{"unmonitored outranks everything", catalogv1alpha1.BookStatus{PendingGrab: &catalogv1alpha1.PendingGrab{}}, false, false, true, true, true, catalogv1alpha1.BookPhaseUnmonitored, commonv1.CutoffReasonCutoffMet},
		{"no metadata yet is Pending", catalogv1alpha1.BookStatus{}, true, false, false, false, true, catalogv1alpha1.BookPhasePending, commonv1.CutoffReasonPending},
		{"no metadata yet is Pending even with a file", catalogv1alpha1.BookStatus{}, true, false, true, true, true, catalogv1alpha1.BookPhasePending, commonv1.CutoffReasonCutoffMet},
		{"fresh metadata, undated, nothing yet: Wanted", catalogv1alpha1.BookStatus{Metadata: fresh(nil)}, true, true, false, false, true, catalogv1alpha1.BookPhaseWanted, commonv1.CutoffReasonPending},
		{"published, nothing yet: Wanted", catalogv1alpha1.BookStatus{Metadata: fresh(dated(-365))}, true, true, false, false, true, catalogv1alpha1.BookPhaseWanted, commonv1.CutoffReasonPending},
		{"not yet published: Unavailable", catalogv1alpha1.BookStatus{Metadata: fresh(dated(30))}, true, true, false, false, true, catalogv1alpha1.BookPhaseUnavailable, commonv1.CutoffReasonPending},
		{"a file arrived before publication", catalogv1alpha1.BookStatus{Metadata: fresh(dated(30))}, true, true, true, true, true, catalogv1alpha1.BookPhaseImported, commonv1.CutoffReasonCutoffMet},
		{"a file below the cutoff", catalogv1alpha1.BookStatus{Metadata: fresh(nil)}, true, true, true, false, true, catalogv1alpha1.BookPhaseCutoffUnmet, commonv1.CutoffReasonCutoffUnmet},
		{"a file and no profile to judge it", catalogv1alpha1.BookStatus{Metadata: fresh(nil)}, true, true, true, false, false, catalogv1alpha1.BookPhaseCutoffUnevaluated, commonv1.CutoffReasonProfileUnresolved},
		{"a delayed grab with no file", catalogv1alpha1.BookStatus{Metadata: fresh(nil), PendingGrab: &catalogv1alpha1.PendingGrab{}}, true, true, false, false, true, catalogv1alpha1.BookPhaseDelayed, commonv1.CutoffReasonPending},
		{"a delayed upgrade of a file below the cutoff", catalogv1alpha1.BookStatus{Metadata: fresh(nil), PendingGrab: &catalogv1alpha1.PendingGrab{}}, true, true, true, false, true, catalogv1alpha1.BookPhaseDelayed, commonv1.CutoffReasonCutoffUnmet},
		{"a file at the cutoff outranks a delayed grab", catalogv1alpha1.BookStatus{Metadata: fresh(nil), PendingGrab: &catalogv1alpha1.PendingGrab{}}, true, true, true, true, true, catalogv1alpha1.BookPhaseImported, commonv1.CutoffReasonCutoffMet},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bk := &catalogv1alpha1.Book{Status: c.status}
			phase, reason := rollup.LeafPhase(book.PhaseInputs(bk, c.monitored, c.metadataReady, c.hasFile, c.cutoffMet, c.profileResolved, now))
			assert.Equal(t, c.phase, phase)
			assert.Equal(t, c.reason, reason)
		})
	}
}

// MetadataStale is the one staleness rule the pass and the start-time skip
// share: missing or past its TTL is stale, a recent document from before the
// gateway learned a field is outdated and nothing else.
func TestMetadataStaleIsTheBooksRefreshRule(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	ttl := metadata.RefreshTTL(commonv1.MediaKindBook, "", now)
	md := func(age time.Duration, schema int32) *catalogv1alpha1.BookMetadata {
		return &catalogv1alpha1.BookMetadata{RefreshedAt: metav1.NewTime(now.Add(-age)), SchemaVersion: schema}
	}
	cases := []struct {
		name            string
		md              *catalogv1alpha1.BookMetadata
		stale, outdated bool
	}{
		{"no document", nil, true, false},
		{"fresh", md(time.Hour, metadata.SchemaVersion), false, false},
		{"past its TTL", md(ttl+time.Minute, metadata.SchemaVersion), true, false},
		{"recent but written before the gateway learned a field", md(time.Hour, metadata.SchemaVersion-1), false, true},
		{"past its TTL and old: stale alone, the refresh covers both", md(ttl+time.Minute, metadata.SchemaVersion-1), true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stale, outdated := book.MetadataStale(&catalogv1alpha1.Book{Status: catalogv1alpha1.BookStatus{Metadata: c.md}}, now)
			assert.Equal(t, c.stale, stale, "stale")
			assert.Equal(t, c.outdated, outdated, "outdated")
		})
	}
}

// The Book watch's SkipInitial reads the Book as the cache lists it, a
// refresh due whenever the pass would ask for one.
func TestIdleInputsReadTheBook(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	bk := &catalogv1alpha1.Book{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "dune", Generation: 2},
		Status: catalogv1alpha1.BookStatus{
			ObservedGeneration: 2,
			Phase:              catalogv1alpha1.BookPhaseImported,
			Metadata:           &catalogv1alpha1.BookMetadata{RefreshedAt: metav1.NewTime(now.Add(-time.Hour)), SchemaVersion: metadata.SchemaVersion},
			Conditions:         []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}},
		},
	}
	assert.True(t, rollup.LeafIdleAtStart(book.IdleInputs(bk, true, now)), "imported, fresh, monitored")
	assert.False(t, rollup.LeafIdleAtStart(book.IdleInputs(bk, false, now)), "unmonitored now: the pass moves it")

	stale := bk.DeepCopy()
	stale.Status.Metadata = nil
	assert.True(t, book.IdleInputs(stale, true, now).RefreshDue)
	assert.False(t, rollup.LeafIdleAtStart(book.IdleInputs(stale, true, now)), "a refresh due is work")

	retired := bk.DeepCopy()
	retired.Status.Conditions = append(retired.Status.Conditions, metav1.Condition{Type: "HasFile", Status: metav1.ConditionTrue})
	assert.False(t, rollup.LeafIdleAtStart(book.IdleInputs(retired, true, now)), "a retired condition still carried: one pass drops it")

	grab := bk.DeepCopy()
	grab.Status.PendingGrab = &catalogv1alpha1.PendingGrab{}
	assert.True(t, book.IdleInputs(grab, true, now).PendingGrab)
}
