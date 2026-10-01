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

package projection_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/ui/projection"
)

// fakeInformer records the event handlers registered on it; every other
// cache.Informer method is the nil embedded interface's, so a projection
// that called one would panic the test.
type fakeInformer struct {
	cache.Informer
	mu       sync.Mutex
	handlers []toolscache.ResourceEventHandler
}

func (f *fakeInformer) AddEventHandler(h toolscache.ResourceEventHandler) (toolscache.ResourceEventHandlerRegistration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handlers = append(f.handlers, h)
	return nil, nil
}

// informingReader is a counting reader that is also an informer source,
// the shape ui.NewClusterReader's cache has: a change is announced by
// firing one kind's handlers.
type informingReader struct {
	countingReader
	mu        sync.Mutex
	informers map[string]*fakeInformer
}

func (r *informingReader) GetInformer(_ context.Context, obj client.Object, _ ...cache.InformerGetOption) (cache.Informer, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.informers == nil {
		r.informers = map[string]*fakeInformer{}
	}
	key := fmt.Sprintf("%T", obj)
	inf, ok := r.informers[key]
	if !ok {
		inf = &fakeInformer{}
		r.informers[key] = inf
	}
	return inf, nil
}

func (r *informingReader) changed(obj client.Object) {
	r.mu.Lock()
	inf := r.informers[fmt.Sprintf("%T", obj)]
	r.mu.Unlock()
	inf.mu.Lock()
	defer inf.mu.Unlock()
	for _, h := range inf.handlers {
		h.OnAdd(obj, false)
	}
}

func dirtyFixture(t *testing.T) (*informingReader, client.Client, *atomic.Int64) {
	t.Helper()
	movie := &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "heat", Namespace: "default", UID: "heat-uid"},
		Status:     catalogv1.MovieStatus{Metadata: &catalogv1.MovieMetadata{Title: "Heat"}},
	}
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(movie).Build()
	calls := &atomic.Int64{}
	return &informingReader{countingReader: countingReader{Reader: c, calls: calls}}, c, calls
}

func addMovie(t *testing.T, c client.Client, name string) *catalogv1.Movie {
	t.Helper()
	m := &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", UID: types.UID("uid-" + name)},
		Status:     catalogv1.MovieStatus{Metadata: &catalogv1.MovieMetadata{Title: name}},
	}
	require.NoError(t, c.Create(context.Background(), m))
	return m
}

// TestAnUnchangedLibraryIsNotListedAgain: over the ui's cache, whose
// informers announce every change, a tick with nothing changed lists
// nothing -- the projection used to list (and deep-copy) every kind every
// five seconds -- and a change is projected at the next tick.
func TestAnUnchangedLibraryIsNotListedAgain(t *testing.T) {
	reader, c, calls := dirtyFixture(t)
	proj := projection.New(reader, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = proj.Run(ctx) }()

	ch, unsub := proj.SubscribeLibrary()
	defer unsub()
	require.Eventually(t, proj.Projected, 5*time.Second, 5*time.Millisecond)

	time.Sleep(150 * time.Millisecond) // seven ticks, nothing changed
	require.EqualValues(t, listCallsPerTick, calls.Load(), "a tick over an unchanged library listed it again")

	reader.changed(addMovie(t, c, "ronin"))
	require.Eventually(t, func() bool {
		select {
		case items := <-ch:
			return len(items) == 2
		default:
			return false
		}
	}, 5*time.Second, 5*time.Millisecond, "a change never reached the subscriber")
	require.EqualValues(t, 2*listCallsPerTick, calls.Load(), "one change is one more round")
}

// TestNoSubscriberNoRebuild: with no stream open, ticks build nothing even
// when something changed; the first round still runs, so /readyz turns
// ready (Projected), and a page read past the interval projects the change
// on demand, so no page is staler than one interval, as before.
func TestNoSubscriberNoRebuild(t *testing.T) {
	reader, c, calls := dirtyFixture(t)
	const interval = 20 * time.Millisecond
	proj := projection.New(reader, interval)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = proj.Run(ctx) }()

	require.Eventually(t, proj.Projected, 5*time.Second, 5*time.Millisecond,
		"the first round must run with no subscriber: /readyz waits on it")
	require.Len(t, proj.Library(ctx), 1)

	reader.changed(addMovie(t, c, "ronin"))
	time.Sleep(150 * time.Millisecond)
	require.EqualValues(t, listCallsPerTick, calls.Load(), "a tick rebuilt with nobody subscribed")

	require.Len(t, proj.Library(ctx), 2, "a page read did not see a change older than the interval")
	require.EqualValues(t, 2*listCallsPerTick, calls.Load())
	require.Len(t, proj.Library(ctx), 2)
	require.EqualValues(t, 2*listCallsPerTick, calls.Load(), "an unchanged page read listed again")
}

// TestAReaderWithoutInformersStillRebuildsForSubscribers: a reader that
// announces nothing (a raw or fake client) is always dirty, so a
// subscriber still sees every tick, as before.
func TestAReaderWithoutInformersStillRebuildsForSubscribers(t *testing.T) {
	_, c, _ := dirtyFixture(t)
	calls := &atomic.Int64{}
	proj := projection.New(&countingReader{Reader: c, calls: calls}, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = proj.Run(ctx) }()
	ch, unsub := proj.SubscribeLibrary()
	defer unsub()
	require.Eventually(t, proj.Projected, 5*time.Second, 5*time.Millisecond)

	addMovie(t, c, "ronin")
	require.Eventually(t, func() bool {
		select {
		case items := <-ch:
			return len(items) == 2
		default:
			return false
		}
	}, 5*time.Second, 5*time.Millisecond)
}

// kindRecorder records the kind of every List a round makes.
type kindRecorder struct {
	*informingReader
	mu    sync.Mutex
	kinds map[string]bool
}

func (r *kindRecorder) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	r.mu.Lock()
	r.kinds[strings.TrimSuffix(fmt.Sprintf("%T", list), "List")] = true
	r.mu.Unlock()
	return r.informingReader.List(ctx, list, opts...)
}

// TestEveryListedKindIsWatched: a kind a round lists but no handler
// watches would change without marking the projection dirty, and the page
// would never show it.
func TestEveryListedKindIsWatched(t *testing.T) {
	reader, _, _ := dirtyFixture(t)
	rec := &kindRecorder{informingReader: reader, kinds: map[string]bool{}}
	proj := projection.New(rec, time.Hour)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = proj.Run(ctx) }()
	require.Eventually(t, proj.Projected, 5*time.Second, 5*time.Millisecond)

	rec.mu.Lock()
	defer rec.mu.Unlock()
	reader.mu.Lock()
	defer reader.mu.Unlock()
	require.Len(t, rec.kinds, listCallsPerTick)
	for kind := range rec.kinds {
		inf, ok := reader.informers[kind]
		require.True(t, ok, "%s is listed but not watched", kind)
		require.NotEmpty(t, inf.handlers, "%s has no change handler", kind)
	}
}
