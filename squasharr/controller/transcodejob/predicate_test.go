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

package transcodejob

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestTranscodeJobPredicateWakesOnADeadLetterAnnotation: the DLQ
// projector's annotation, and an operator removing it, change neither
// generation nor status -- without the predicate the fold in apply would
// never run. The worker's progress (a status write) and an unrelated
// annotation still do not wake the controller.
func TestTranscodeJobPredicateWakesOnADeadLetterAnnotation(t *testing.T) {
	p := transcodeJobPredicate()
	plain := &transcodev1alpha1.TranscodeJob{ObjectMeta: metav1.ObjectMeta{Name: "tj", Generation: 2}}
	annotated := plain.DeepCopy()
	annotated.Annotations = map[string]string{k8s.AnnotationDeadLettered: "subject@2026-09-23T10:00:00Z"}
	other := plain.DeepCopy()
	other.Annotations = map[string]string{"example.com/unrelated": "x"}
	progressed := plain.DeepCopy()
	progressed.Status.Progress = &transcodev1alpha1.Progress{Percent: 40}
	specChanged := plain.DeepCopy()
	specChanged.Generation = 3

	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: plain, ObjectNew: annotated}), "annotation added")
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: annotated, ObjectNew: plain}), "annotation removed")
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: plain, ObjectNew: specChanged}), "a spec change")
	assert.False(t, p.Update(event.UpdateEvent{ObjectOld: plain, ObjectNew: other}), "an unrelated annotation is not a wake")
	assert.False(t, p.Update(event.UpdateEvent{ObjectOld: plain, ObjectNew: progressed}), "the worker's progress is not a wake")
}
