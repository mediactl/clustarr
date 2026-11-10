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
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sevents "k8s.io/client-go/tools/events"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/rollup"
)

func drain(ch chan string) []string {
	var out []string
	for {
		select {
		case e := <-ch:
			out = append(out, e)
		default:
			return out
		}
	}
}

// The Reporter records Movie's phase edge on any kind, after the apply:
// a first phase is no edge, a steady phase is none, a move is one Normal
// Event under the new phase's reason (2026-11-10 design D14).
func TestReporterRecordsAPhaseEdgeOnce(t *testing.T) {
	rec := k8sevents.NewFakeRecorder(8)
	r := rollup.Reporter{Recorder: rec}
	alb := &catalogv1alpha1.Album{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "ok-computer"}}

	assert.False(t, r.PhaseEdge(alb, "", "Wanted"), "the first phase is announced by the ItemEvent, not an Event")
	assert.False(t, r.PhaseEdge(alb, "Wanted", "Wanted"), "a steady phase is no edge")
	assert.True(t, r.PhaseEdge(alb, "Wanted", "Downloading"))
	got := drain(rec.Events)
	require.Len(t, got, 1)
	assert.Contains(t, got[0], "Normal")
	assert.Contains(t, got[0], "Downloading")
	assert.Contains(t, got[0], "phase Wanted -> Downloading")
}

// A condition's Warning goes out on the edge into the state and never while
// it persists: the second reconcile that still finds the profile unresolved
// records nothing, and the edge back out is the condition's own business.
func TestReporterWarnsOnAConditionEdgeOnly(t *testing.T) {
	rec := k8sevents.NewFakeRecorder(8)
	r := rollup.Reporter{Recorder: rec}
	bk := &catalogv1alpha1.Book{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "dune"}}

	assert.True(t, r.WarnEdge(bk, nil, "Ready", metav1.ConditionFalse, "RootFolderNotFound", "rootFolder %q not found", "books"), "no prior condition is an edge")
	prev := []metav1.Condition{{Type: "Ready", Status: metav1.ConditionFalse, Reason: "RootFolderNotFound"}}
	assert.False(t, r.WarnEdge(bk, prev, "Ready", metav1.ConditionFalse, "RootFolderNotFound", "rootFolder %q not found", "books"), "the same state again is no edge")
	assert.True(t, r.WarnEdge(bk, prev, "Ready", metav1.ConditionFalse, "AuthorNotFound", "author %q not found", "herbert"), "another reason is an edge")
	got := drain(rec.Events)
	require.Len(t, got, 2)
	assert.Contains(t, got[0], "Warning")
	assert.Contains(t, got[0], "RootFolderNotFound")
	assert.Contains(t, got[1], "AuthorNotFound")
}

// A Reporter without a Recorder records nothing and never panics: a test
// that wires no recorder loses only the Events.
func TestReporterWithoutARecorderIsSilent(t *testing.T) {
	var r rollup.Reporter
	iss := &catalogv1alpha1.Issue{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "saga-1"}}
	assert.NotPanics(t, func() {
		r.Normal(iss, "Reason", "note")
		r.Warn(iss, "Reason", "note")
		assert.True(t, r.PhaseEdge(iss, "Wanted", "Imported"), "the edge is still reported")
		assert.True(t, r.WarnEdge(iss, nil, "Ready", metav1.ConditionFalse, "ComicNotFound", "comic not found"))
	})
}
