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

package download

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestDownloadPredicateWakesOnADeadLetterAnnotation: the DLQ projector's
// annotation, and an operator removing it, change neither generation nor
// any status field -- without its own predicate the fold in the status
// apply would never run. An unrelated annotation still does not wake it.
func TestDownloadPredicateWakesOnADeadLetterAnnotation(t *testing.T) {
	p := downloadPredicate()
	plain := &downloadv1alpha1.Download{ObjectMeta: metav1.ObjectMeta{Name: "d", Generation: 3}}
	annotated := plain.DeepCopy()
	annotated.Annotations = map[string]string{k8s.AnnotationDeadLettered: "subject@2026-09-23T10:00:00Z"}
	other := plain.DeepCopy()
	other.Annotations = map[string]string{"example.com/unrelated": "x"}

	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: plain, ObjectNew: annotated}), "annotation added")
	assert.True(t, p.Update(event.UpdateEvent{ObjectOld: annotated, ObjectNew: plain}), "annotation removed")
	assert.False(t, p.Update(event.UpdateEvent{ObjectOld: plain, ObjectNew: other}), "an unrelated annotation is not a wake")
}
