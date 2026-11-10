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

package issue_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/issue"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
)

// The Issue's phase is rollup.LeafPhase over the Issue's own facts
// (2026-11-10 design D4): the cover date gates Unavailable, the pending grab
// reads Delayed, the Comic's profile decides CutoffUnevaluated, and an Issue
// never reads Pending, since its metadata is its Comic's.
func TestPhaseInputsReadTheIssue(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	dated := func(d int) *metav1.Time { t := metav1.NewTime(now.AddDate(0, 0, d)); return &t }
	cases := []struct {
		name                                           string
		status                                         catalogv1alpha1.IssueStatus
		monitored, hasFile, cutoffMet, profileResolved bool
		phase                                          commonv1.LeafPhase
		reason                                         string
	}{
		{"unmonitored outranks everything", catalogv1alpha1.IssueStatus{PendingGrab: &catalogv1alpha1.PendingGrab{}}, false, true, true, true, catalogv1alpha1.IssuePhaseUnmonitored, commonv1.CutoffReasonCutoffMet},
		{"nothing yet, undated: Wanted (Mylar decides undated ones)", catalogv1alpha1.IssueStatus{}, true, false, false, true, catalogv1alpha1.IssuePhaseWanted, commonv1.CutoffReasonPending},
		{"nothing yet, released", catalogv1alpha1.IssueStatus{Date: dated(-7)}, true, false, false, true, catalogv1alpha1.IssuePhaseWanted, commonv1.CutoffReasonPending},
		{"nothing yet, not released: Unavailable", catalogv1alpha1.IssueStatus{Date: dated(7)}, true, false, false, true, catalogv1alpha1.IssuePhaseUnavailable, commonv1.CutoffReasonPending},
		{"a file arrived early", catalogv1alpha1.IssueStatus{Date: dated(7)}, true, true, true, true, catalogv1alpha1.IssuePhaseImported, commonv1.CutoffReasonCutoffMet},
		{"a file below the cutoff", catalogv1alpha1.IssueStatus{}, true, true, false, true, catalogv1alpha1.IssuePhaseCutoffUnmet, commonv1.CutoffReasonCutoffUnmet},
		{"a file and no profile to judge it", catalogv1alpha1.IssueStatus{}, true, true, false, false, catalogv1alpha1.IssuePhaseCutoffUnevaluated, commonv1.CutoffReasonProfileUnresolved},
		{"no file and no profile is still Wanted", catalogv1alpha1.IssueStatus{}, true, false, false, false, catalogv1alpha1.IssuePhaseWanted, commonv1.CutoffReasonPending},
		{"a delayed grab with no file", catalogv1alpha1.IssueStatus{PendingGrab: &catalogv1alpha1.PendingGrab{}}, true, false, false, true, catalogv1alpha1.IssuePhaseDelayed, commonv1.CutoffReasonPending},
		{"a delayed upgrade of a file below the cutoff", catalogv1alpha1.IssueStatus{PendingGrab: &catalogv1alpha1.PendingGrab{}}, true, true, false, true, catalogv1alpha1.IssuePhaseDelayed, commonv1.CutoffReasonCutoffUnmet},
		{"a file at the cutoff outranks a delayed grab", catalogv1alpha1.IssueStatus{PendingGrab: &catalogv1alpha1.PendingGrab{}}, true, true, true, true, catalogv1alpha1.IssuePhaseImported, commonv1.CutoffReasonCutoffMet},
		{"a delayed grab outranks Unavailable", catalogv1alpha1.IssueStatus{Date: dated(7), PendingGrab: &catalogv1alpha1.PendingGrab{}}, true, false, false, true, catalogv1alpha1.IssuePhaseDelayed, commonv1.CutoffReasonPending},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			iss := &catalogv1alpha1.Issue{Status: c.status}
			in := issue.PhaseInputs(iss, c.monitored, c.hasFile, c.cutoffMet, c.profileResolved, now)
			assert.True(t, in.MetadataReady, "an Issue's metadata is its Comic's: never Pending")
			phase, reason := rollup.LeafPhase(in)
			assert.Equal(t, c.phase, phase)
			assert.Equal(t, c.reason, reason)
		})
	}
}

// The Issue watch's SkipInitial reads the Issue as the cache lists it.
func TestIdleInputsReadTheIssue(t *testing.T) {
	ref := "saga-entry"
	iss := &catalogv1alpha1.Issue{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "saga-001.0", Generation: 3},
		Spec:       catalogv1alpha1.IssueSpec{ComicRef: "saga", Number: "1", Monitored: ptr.To(false)},
		Status: catalogv1alpha1.IssueStatus{
			ObservedGeneration: 3,
			Phase:              catalogv1alpha1.IssuePhaseUnmonitored,
			Conditions:         []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue}},
		},
	}
	assert.True(t, rollup.LeafIdleAtStart(issue.IdleInputs(iss, false)), "unmonitored and still unmonitored")
	assert.False(t, rollup.LeafIdleAtStart(issue.IdleInputs(iss, true)), "its Comic is monitored again")

	live := iss.DeepCopy()
	live.Status.ActiveDownloadRef, live.Status.DownloadPhase = &ref, commonv1.DownloadPhaseDownloading
	in := issue.IdleInputs(live, false)
	assert.Equal(t, &ref, in.ActiveDownloadRef)
	assert.Equal(t, commonv1.DownloadPhaseDownloading, in.DownloadPhase)
	assert.False(t, rollup.LeafIdleAtStart(in), "a grab in flight")

	searching := iss.DeepCopy()
	searching.Status.SearchDispatch = &catalogv1alpha1.Dispatch{Seq: 2, AnsweredSeq: 1}
	assert.True(t, issue.IdleInputs(searching, false).SearchInFlight)
	assert.False(t, issue.IdleInputs(iss, false).RefreshDue, "an Issue has no metadata document to refresh")
}
