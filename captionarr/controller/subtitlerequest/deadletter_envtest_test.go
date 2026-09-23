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

package subtitlerequest_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	subtitlev1alpha1 "github.com/mediactl/clustarr/api/subtitle/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// annotate sets (value non-empty) or removes the DLQ projector's
// clustarr.io/dead-lettered annotation, as the projector and an operator do.
func (f *fixture) annotate(name, value string) {
	f.t.Helper()
	sr := f.get(name)
	patch := client.MergeFrom(sr.DeepCopy())
	if value == "" {
		delete(sr.Annotations, k8s.AnnotationDeadLettered)
	} else {
		if sr.Annotations == nil {
			sr.Annotations = map[string]string{}
		}
		sr.Annotations[k8s.AnnotationDeadLettered] = value
	}
	require.NoError(f.t, f.c.Patch(f.ctx, sr, patch))
}

// The DLQ projector annotates a SubtitleRequest a dead-lettered fetch task
// or subtitle event names (Phase G ruling R1); this controller, the only
// writer of its conditions, folds the annotation into DeadLettered on the
// planned path and on every Blocked path, and removes it with the
// annotation. Acted on an object already in steady state, with both
// managers' status on it, so a release would show.
func TestDeadLetteredFoldsIntoAConditionOnEveryPath(t *testing.T) {
	f := newFixture(t, "sr-deadlettered")
	before := steadyState(t, f)
	require.Nil(t, k8s.FindCondition(before.Status.Conditions, k8s.ConditionDeadLettered), "setup: no dead letter yet")

	at := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	f.annotate("movie", "clustarr.work.captionarr.fetch.normal.x.de@"+at.Format(time.RFC3339))

	f.reconcile("movie")
	after := f.get("movie")
	dl := cond(after, k8s.ConditionDeadLettered)
	assert.Equal(t, metav1.ConditionTrue, dl.Status)
	assert.Equal(t, k8s.ReasonDeadLettered, dl.Reason)
	assert.Contains(t, dl.Message, "clustarr.work.captionarr.fetch.normal.x.de")
	assert.True(t, dl.LastTransitionTime.Time.Equal(at), "dated from the dead letter, not the reconcile")
	assert.Equal(t, before.Status.Phase, after.Status.Phase)
	assert.Equal(t, before.Status.Existing, after.Status.Existing)
	assert.Equal(t, item(t, before, "de"), item(t, after, "de"), "the fold touched an item")
	for _, ct := range []string{
		subtitlev1alpha1.SubtitleRequestConditionPlanned,
		subtitlev1alpha1.SubtitleRequestConditionSatisfied,
		subtitlev1alpha1.SubtitleRequestConditionCutoffMet,
	} {
		assert.Equal(t, cond(before, ct).Status, cond(after, ct).Status, ct)
	}
	split := func(sr *subtitlev1alpha1.SubtitleRequest) {
		t.Helper()
		assertManagedFieldsSplit(t, sr,
			map[k8s.FieldManager][]string{k8s.ManagerCaptionarr: controllerTop, k8s.ManagerCaptionarrWorker: {"items"}},
			map[k8s.FieldManager]map[string][]string{
				k8s.ManagerCaptionarr:       {"de": controllerItem},
				k8s.ManagerCaptionarrWorker: {"de": workerLeaves},
			})
	}
	split(after)

	// A Blocked apply is a complete declaration too: it must keep the
	// condition, not release it.
	require.NoError(t, os.Remove(filepath.Join(f.dir, "movie.mkv")))
	f.reconcile("movie")
	blocked := f.get("movie")
	assert.Equal(t, subtitlev1alpha1.SubtitleRequestPhaseBlocked, blocked.Status.Phase)
	assert.Equal(t, metav1.ConditionTrue, cond(blocked, k8s.ConditionDeadLettered).Status, "the Blocked path released DeadLettered")
	assert.True(t, cond(blocked, k8s.ConditionDeadLettered).LastTransitionTime.Time.Equal(at))
	split(blocked)

	// The operator clears it by removing the annotation.
	f.annotate("movie", "")
	f.reconcile("movie")
	cleared := f.get("movie")
	assert.Nil(t, k8s.FindCondition(cleared.Status.Conditions, k8s.ConditionDeadLettered),
		"the condition is present only while the annotation is")
	assert.Equal(t, metav1.ConditionFalse, cond(cleared, subtitlev1alpha1.SubtitleRequestConditionPlanned).Status,
		"still blocked: removing the annotation changes nothing else")
}
