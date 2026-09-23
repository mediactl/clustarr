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

package k8s_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func movieWith(annotations map[string]string) *catalogv1alpha1.Movie {
	return &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{
		Name: "heat", Namespace: "media", Generation: 3, Annotations: annotations,
	}}
}

func TestMarkDeadLetteredSetsTheConditionFromTheAnnotation(t *testing.T) {
	at := time.Date(2026, 9, 23, 10, 30, 0, 0, time.UTC)
	m := movieWith(map[string]string{
		k8s.AnnotationDeadLettered: "clustarr.work.catalogarr.search.normal.media.movie.heat@" + at.Format(time.RFC3339),
	})
	var conditions []metav1.Condition
	require.True(t, k8s.MarkDeadLettered(m, &conditions))

	c := k8s.FindCondition(conditions, k8s.ConditionDeadLettered)
	require.NotNil(t, c)
	assert.Equal(t, metav1.ConditionTrue, c.Status)
	assert.Equal(t, k8s.ReasonDeadLettered, c.Reason)
	assert.Equal(t, int64(3), c.ObservedGeneration)
	assert.True(t, c.LastTransitionTime.Time.Equal(at), "the condition dates from the dead letter, not the reconcile")
	assert.Contains(t, c.Message, "clustarr.work.catalogarr.search.normal.media.movie.heat")
	assert.Contains(t, c.Message, k8s.AnnotationDeadLettered)

	// Folding the same annotation again is a no-op: no churn per reconcile.
	assert.False(t, k8s.MarkDeadLettered(m, &conditions))
}

func TestMarkDeadLetteredKeepsAnUnparseableValueVerbatim(t *testing.T) {
	m := movieWith(map[string]string{k8s.AnnotationDeadLettered: "not-the-usual-shape"})
	var conditions []metav1.Condition
	require.True(t, k8s.MarkDeadLettered(m, &conditions))
	c := k8s.FindCondition(conditions, k8s.ConditionDeadLettered)
	require.NotNil(t, c)
	assert.Contains(t, c.Message, "not-the-usual-shape")
	assert.False(t, c.LastTransitionTime.IsZero(), "SetStatusCondition stamps now when the value carries no time")
}

// The removal half: a controller seeds conditions from live status, so a
// DeadLettered it set earlier must go once the operator deletes the
// annotation -- otherwise it would be re-declared forever.
func TestMarkDeadLetteredRemovesTheConditionOnceTheAnnotationIsGone(t *testing.T) {
	conditions := []metav1.Condition{
		{Type: k8s.ConditionReady, Status: metav1.ConditionTrue, Reason: k8s.ReasonReconciled},
		{Type: k8s.ConditionDeadLettered, Status: metav1.ConditionTrue, Reason: k8s.ReasonDeadLettered},
	}
	require.True(t, k8s.MarkDeadLettered(movieWith(nil), &conditions))
	assert.Nil(t, k8s.FindCondition(conditions, k8s.ConditionDeadLettered))
	assert.NotNil(t, k8s.FindCondition(conditions, k8s.ConditionReady), "only DeadLettered is touched")

	assert.False(t, k8s.MarkDeadLettered(movieWith(nil), &conditions), "absent and absent is no change")
}

func TestDeadLetteredAnnotationChanged(t *testing.T) {
	p := k8s.DeadLetteredAnnotationChanged()
	plain := movieWith(nil)
	dead := movieWith(map[string]string{k8s.AnnotationDeadLettered: "a@2026-09-23T10:30:00Z"})
	deadAgain := movieWith(map[string]string{k8s.AnnotationDeadLettered: "b@2026-09-23T11:00:00Z"})
	other := movieWith(map[string]string{"unrelated": "x"})

	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: plain, ObjectNew: dead}), "annotated")
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: dead, ObjectNew: plain}), "annotation removed")
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: dead, ObjectNew: deadAgain}), "dead-lettered again")
	assert.False(t, p.Update(event.UpdateEvent{ObjectOld: dead, ObjectNew: dead}), "unchanged")
	assert.False(t, p.Update(event.UpdateEvent{ObjectOld: plain, ObjectNew: other}), "another annotation")
	assert.True(t, p.Create(event.CreateEvent{Object: plain}))
	assert.True(t, p.Delete(event.DeleteEvent{Object: plain}))
}
