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

package metadata_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/metadata"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

type published struct {
	subject string
	env     *events.Envelope
}

type recordingBus struct {
	mu   sync.Mutex
	msgs []published
}

func (b *recordingBus) Publish(_ context.Context, subject string, e *events.Envelope, _ ...events.PublishOption) (events.Receipt, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.msgs = append(b.msgs, published{subject: subject, env: e.Clone()})
	return events.Receipt{}, nil
}

func (b *recordingBus) snapshot() []published {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]published(nil), b.msgs...)
}

type fakeRecorder struct {
	mu      sync.Mutex
	reasons []string
}

func (r *fakeRecorder) Eventf(_, _ runtime.Object, _, reason, _, _ string, _ ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reasons = append(r.reasons, reason)
}

func (r *fakeRecorder) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.reasons...)
}

func setAnnotation(t *testing.T, ctx context.Context, c client.Client, obj client.Object, key, value string) {
	t.Helper()
	require.NoError(t, c.Get(ctx, client.ObjectKeyFromObject(obj), obj))
	patch := client.MergeFrom(obj.DeepCopyObject().(client.Object))
	ann := obj.GetAnnotations()
	if ann == nil {
		ann = map[string]string{}
	}
	ann[key] = value
	obj.SetAnnotations(ann)
	require.NoError(t, c.Patch(ctx, obj, patch))
}

// The operator's forced refresh (spec §5's MetadataTask.refreshEpoch,
// design 2026-09-23-library-page-design's "Metadata refresh"): annotating
// an item with clustarr.io/refresh-metadata=<epoch> publishes one
// high-priority MetadataTask carrying that epoch, under a message id no
// scheduled task shares, and consumes the annotation; a value that is not
// a positive integer is refused and consumed, publishing nothing.
func TestRefresherPublishesAForcedTaskAndConsumesTheAnnotation(t *testing.T) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{CRDDirectoryPaths: []string{"../../../config/crd/bases"}, ErrorIfCRDPathMissing: true}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, env.Stop()) })
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
	})
	require.NoError(t, err)
	bus := &recordingBus{}
	rec := &fakeRecorder{}
	require.NoError(t, metadata.NewRefresher(metadata.RefreshDeps{Client: mgr.GetClient(), Bus: bus, Recorder: rec}).SetupWithManager(mgr))
	go func() { _ = mgr.Start(ctx) }()
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)

	const ns = "refresh"
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))
	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: ns},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 949, QualityProfileRef: "hd", RootFolderRef: "movies", Monitored: ptr.To(true)},
	}
	require.NoError(t, c.Create(ctx, movie))
	series := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "andor", Namespace: ns},
		Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 393189, QualityProfileRef: "web", RootFolderRef: "tv"},
	}
	require.NoError(t, c.Create(ctx, series))

	consumed := func(obj client.Object) func() bool {
		return func() bool {
			return c.Get(ctx, client.ObjectKeyFromObject(obj), obj) == nil && obj.GetAnnotations()[metadata.AnnotationRefresh] == ""
		}
	}

	t.Run("a movie", func(t *testing.T) {
		setAnnotation(t, ctx, c, movie, metadata.AnnotationRefresh, "1758665000")
		require.Eventually(t, consumed(movie), 10*time.Second, 50*time.Millisecond, "the annotation must be consumed")
		msgs := bus.snapshot()
		require.Len(t, msgs, 1)
		assert.Equal(t, events.WorkMetadataSubject(events.PriorityHigh, events.MediaKey("movie", ns, "heat")), msgs[0].subject)
		assert.Equal(t, fmt.Sprintf("%s:%d:metadata:refresh:1758665000", movie.UID, movie.Generation), msgs[0].env.ID,
			"an id the scheduled task never uses, or the duplicate window would drop it")
		assert.Equal(t, "catalog.MetadataTask", msgs[0].env.Type)
		assert.Equal(t, ns+"/heat", msgs[0].env.Key)
		var task schema.MetadataTask
		require.NoError(t, schema.Decode(msgs[0].env.Schema, msgs[0].env.Data, &task))
		assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"}, task.MediaRef)
		assert.EqualValues(t, 1758665000, task.RefreshEpoch)
		assert.Contains(t, rec.seen(), "MetadataRefreshRequested")
	})

	t.Run("a second request with a newer epoch publishes again", func(t *testing.T) {
		setAnnotation(t, ctx, c, movie, metadata.AnnotationRefresh, "1758665001")
		require.Eventually(t, consumed(movie), 10*time.Second, 50*time.Millisecond)
		msgs := bus.snapshot()
		require.Len(t, msgs, 2)
		assert.NotEqual(t, msgs[0].env.ID, msgs[1].env.ID)
	})

	t.Run("a series", func(t *testing.T) {
		setAnnotation(t, ctx, c, series, metadata.AnnotationRefresh, "1758665002")
		require.Eventually(t, consumed(series), 10*time.Second, 50*time.Millisecond)
		msgs := bus.snapshot()
		require.Len(t, msgs, 3)
		assert.Equal(t, events.WorkMetadataSubject(events.PriorityHigh, events.MediaKey("series", ns, "andor")), msgs[2].subject)
	})

	t.Run("not an epoch", func(t *testing.T) {
		before := len(bus.snapshot())
		setAnnotation(t, ctx, c, movie, metadata.AnnotationRefresh, "soon")
		require.Eventually(t, consumed(movie), 10*time.Second, 50*time.Millisecond, "a request that can never succeed is consumed, not retried forever")
		assert.Len(t, bus.snapshot(), before, "a refused request publishes nothing")
		assert.Contains(t, rec.seen(), "MetadataRefreshRefused")
	})
}
