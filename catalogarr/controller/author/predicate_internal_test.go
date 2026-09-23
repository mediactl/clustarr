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

// Internal (package author) so it can call the unexported For() predicate.
package author

import (
	"testing"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestPredicatePassesADeadLetteredAnnotationChange: the DLQ projector's
// annotation bumps no generation and touches no status, so without its own
// arm in authorPredicate the reconcile that folds it would never run.
func TestPredicatePassesADeadLetteredAnnotationChange(t *testing.T) {
	old := &catalogv1alpha1.Author{ObjectMeta: metav1.ObjectMeta{Name: "x", Namespace: "ns", Generation: 3}}
	annotated := old.DeepCopy()
	annotated.Annotations = map[string]string{k8s.AnnotationDeadLettered: "subj@2026-09-23T10:00:00Z"}
	assert.True(t, authorPredicate().Update(event.UpdateEvent{ObjectOld: old, ObjectNew: annotated}), "annotated")
	assert.True(t, authorPredicate().Update(event.UpdateEvent{ObjectOld: annotated, ObjectNew: old}), "annotation removed")

	relabelled := old.DeepCopy()
	relabelled.Labels = map[string]string{"unrelated": "change"}
	assert.False(t, authorPredicate().Update(event.UpdateEvent{ObjectOld: old, ObjectNew: relabelled}), "an unrelated metadata change still does not pass")
}
