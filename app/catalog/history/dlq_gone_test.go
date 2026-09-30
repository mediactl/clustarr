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

package history_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/history"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func deadLetter(t *testing.T, ns string, ref commonv1.MediaRef) testMessage {
	t.Helper()
	env := envelopeFor(t, ns+"/"+ref.Name, schema.SearchTask{MediaRef: ref, Reason: schema.SearchReasonMissing})
	env.Headers = map[string]string{
		events.HeaderDLQSubject:  "clustarr.work.catalogarr.search.normal." + ref.Name,
		events.HeaderDLQReason:   "target no longer exists",
		events.HeaderDLQConsumer: "catalogarr-search-normal",
		events.HeaderDLQAttempts: "2",
	}
	return testMessage{env: env, subject: "clustarr.dlq.catalogarr.search." + ref.Name}
}

// A dead letter for an item deleted since its task was queued marks
// nothing: the projector's write never creates the object. A server-side
// apply did -- an empty Book, no spec, every reconcile of it failing
// validation -- 50 of them after an author was deleted and re-added with
// its books' searches still queued (kind-cluster-plex, 2026-09-29).
func TestDLQProjectorNeverRecreatesADeletedItem(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	proj := history.NewDLQProjector(history.DLQDeps{Client: c, Recorder: &fakeRecorder{}})

	ref := commonv1.MediaRef{Kind: commonv1.MediaKindBook, Name: "the-idiot"}
	require.NoError(t, proj.Handle(context.Background(), deadLetter(t, "books", ref)))

	var got catalogv1alpha1.Book
	err := c.Get(context.Background(), client.ObjectKey{Namespace: "books", Name: "the-idiot"}, &got)
	assert.True(t, apierrors.IsNotFound(err), "the Book must stay deleted, got %v", err)
}

// An item that exists is still marked, and its other annotations are kept.
func TestDLQProjectorMarksAnItemThatExists(t *testing.T) {
	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Namespace: "films", Name: "heat", Annotations: map[string]string{"keep": "me"}},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: "hd", RootFolderRef: "movies", Monitored: ptr.To(true)},
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(movie).Build()
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)
	proj := history.NewDLQProjector(history.DLQDeps{Client: c, Recorder: &fakeRecorder{}, Now: func() time.Time { return now }})

	ref := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}
	require.NoError(t, proj.Handle(context.Background(), deadLetter(t, "films", ref)))

	var got catalogv1alpha1.Movie
	require.NoError(t, c.Get(context.Background(), client.ObjectKeyFromObject(movie), &got))
	assert.Equal(t, "clustarr.work.catalogarr.search.normal.heat@2026-09-29T12:00:00Z", got.Annotations[history.AnnotationDeadLettered])
	assert.Equal(t, "me", got.Annotations["keep"])
	assert.NotContains(t, got.Annotations, history.AnnotationDeadLetterSeq)
}
