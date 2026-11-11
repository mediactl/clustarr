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

// Internal (package author) so it can call the unexported bookRollupChanged.
package author

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/event"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// An unmonitored Book reads Unmonitored from its first pass on, so its
// document arriving -- one without a release date -- moves none of the
// rollup's leaves; the author's folder scan waits for that document (design
// D18), so its arrival alone wakes the author. A refresh of a document
// already in does not.
func TestABooksMetadataWakesItsAuthor(t *testing.T) {
	bare := catalogv1alpha1.Book{
		ObjectMeta: metav1.ObjectMeta{Namespace: "books", Name: "dune", Generation: 1},
		Spec:       catalogv1alpha1.BookSpec{AuthorRef: ptr.To("frank-herbert"), Monitored: ptr.To(true)},
		Status:     catalogv1alpha1.BookStatus{Phase: commonv1.LeafPhaseUnmonitored},
	}
	withDoc := *bare.DeepCopy()
	withDoc.Status.Metadata = &catalogv1alpha1.BookMetadata{Title: "Dune", RefreshedAt: metav1.Now()}
	assert.True(t, bookRollupChanged().Update(event.UpdateEvent{ObjectOld: &bare, ObjectNew: &withDoc}),
		"the document's arrival wakes the author")

	refreshed := *withDoc.DeepCopy()
	refreshed.Status.Metadata.RefreshedAt = metav1.NewTime(time.Now().Add(time.Hour))
	assert.False(t, bookRollupChanged().Update(event.UpdateEvent{ObjectOld: &withDoc, ObjectNew: &refreshed}),
		"a refresh of a document already in does not")
}
