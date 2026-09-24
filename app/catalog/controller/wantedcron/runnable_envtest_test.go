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

package wantedcron

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// newTestClient mirrors pkg/k8s/patch_envtest_test.go: a real apiserver with
// the real CRDs, skipped when KUBEBUILDER_ASSETS is unset. This suite is
// envtest rather than a fake client on purpose -- the sweep reads
// status.phase, which only a real status subresource round-trips.
func newTestClient(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = env.Stop() })
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return c
}

func newTestBus(t *testing.T) events.Bus {
	t.Helper()
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(context.Background(), events.Default().ForSingleNode()))
	t.Cleanup(func() { _ = bus.Close() })
	return bus
}

func createWantedMovie(t *testing.T, ctx context.Context, c client.Client, ns, name string) {
	t.Helper()
	require.NoError(t, client.IgnoreAlreadyExists(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})))
	m := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 1, QualityProfileRef: "hd-bluray-web", RootFolderRef: "movies"},
	}
	require.NoError(t, c.Create(ctx, m))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, catalogac.Movie(name, ns).WithStatus(
		catalogac.MovieStatus().WithPhase(catalogv1alpha1.MoviePhaseWanted)))
	require.NoError(t, err)
}

// TestRunOnce_PublishesOneWantedScanPerEligibleNamespace is the sweep end to
// end against a real apiserver and a real bus: a Wanted movie in one
// namespace, an Imported one in another, and exactly one WantedScan.
func TestRunOnce_PublishesOneWantedScanPerEligibleNamespace(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newTestClient(t)
	bus := newTestBus(t)

	createWantedMovie(t, ctx, c, "media", "the-thing-1982")

	require.NoError(t, client.IgnoreAlreadyExists(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "quiet"}})))
	done := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "already-here", Namespace: "quiet"},
		Spec:       catalogv1alpha1.MovieSpec{TmdbID: 2, QualityProfileRef: "hd-bluray-web", RootFolderRef: "movies"},
	}
	require.NoError(t, c.Create(ctx, done))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, catalogac.Movie(done.Name, done.Namespace).WithStatus(
		catalogac.MovieStatus().WithPhase(catalogv1alpha1.MoviePhaseImported)))
	require.NoError(t, err)

	received := make(chan schema.WantedScan, 4)
	spec, ok := events.Default().Consumer(events.ConsumerCatalogSearchNorm)
	require.True(t, ok)
	stop, err := bus.Subscribe(ctx, spec.Subscription(), func(_ context.Context, m events.Message) error {
		var scan schema.WantedScan
		if err := schema.Decode(m.Envelope().Schema, m.Envelope().Data, &scan); err != nil {
			return err
		}
		received <- scan
		return nil
	})
	require.NoError(t, err)
	defer stop()

	at := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	r := &Runnable{Client: c, Bus: bus, Now: func() time.Time { return at }}
	published, err := r.runOnce(ctx, at)
	require.NoError(t, err)
	assert.Equal(t, []string{"media"}, published, "only the namespace with a Wanted item is woken")

	select {
	case scan := <-received:
		assert.Equal(t, "media", scan.Namespace)
		assert.Equal(t, []commonv1.MediaKind{
			commonv1.MediaKindMovie, commonv1.MediaKindEpisode,
			commonv1.MediaKindAlbum, commonv1.MediaKindBook, commonv1.MediaKindAudiobook, commonv1.MediaKindIssue,
		}, scan.Kinds, "every searchable kind, non-video included")
		assert.True(t, scan.CutoffUnmet)
		assert.Equal(t, at.Unix(), scan.Epoch)
	case <-time.After(10 * time.Second):
		t.Fatal("no WantedScan delivered")
	}

	select {
	case scan := <-received:
		t.Fatalf("a second WantedScan was published: %+v", scan)
	case <-time.After(200 * time.Millisecond):
	}
}

// TestRunOnce_RepublishInsideTheDedupWindowIsAbsorbed proves the Msg-Id does
// its job: a leader flapping between replicas re-fires the same epoch and the
// stream stores nothing new.
func TestRunOnce_RepublishInsideTheDedupWindowIsAbsorbed(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	bus := newTestBus(t)
	createWantedMovie(t, ctx, c, "media", "the-thing-1982")

	at := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	r := &Runnable{Client: c, Bus: bus, Now: func() time.Time { return at }}
	published, err := r.runOnce(ctx, at)
	require.NoError(t, err)
	require.Equal(t, []string{"media"}, published)

	// The second sweep at the same instant publishes the same Msg-Id.
	published, err = r.runOnce(ctx, at)
	require.NoError(t, err)
	require.Equal(t, []string{"media"}, published)

	// One consumer, drained with a short deadline: exactly one delivery.
	spec, ok := events.Default().Consumer(events.ConsumerCatalogSearchNorm)
	require.True(t, ok)
	count := make(chan struct{}, 8)
	stop, err := bus.Subscribe(ctx, spec.Subscription(), func(context.Context, events.Message) error {
		count <- struct{}{}
		return nil
	})
	require.NoError(t, err)
	defer stop()

	select {
	case <-count:
	case <-time.After(10 * time.Second):
		t.Fatal("no WantedScan delivered at all")
	}
	select {
	case <-count:
		t.Fatal("the duplicate sweep was NOT absorbed by the stream's deduplication window")
	case <-time.After(300 * time.Millisecond):
	}
}

// TestRunOnce_NamespaceScopedListing checks the --watch-namespaces path: a
// Runnable restricted to one namespace never publishes for another.
func TestRunOnce_NamespaceScopedListing(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	bus := newTestBus(t)
	createWantedMovie(t, ctx, c, "media", "the-thing-1982")
	createWantedMovie(t, ctx, c, "other", "the-thing-1982")

	at := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	r := &Runnable{Client: c, Bus: bus, Now: func() time.Time { return at }, Namespaces: []string{"other"}}
	published, err := r.runOnce(ctx, at)
	require.NoError(t, err)
	assert.Equal(t, []string{"other"}, published)
}

// TestStart_FiresOnTheSchedule drives Start with a schedule that fires
// immediately, proving the timer loop actually calls runOnce and reschedules
// rather than firing once and stopping.
func TestStart_FiresOnTheSchedule(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newTestClient(t)
	bus := newTestBus(t)
	createWantedMovie(t, ctx, c, "media", "the-thing-1982")

	ticks := make(chan []string, 8)
	r := &Runnable{
		Client:   c,
		Bus:      bus,
		Schedule: everyMillisecond{},
		OnTick:   func(published []string, err error) { ticks <- published },
	}
	go func() { _ = r.Start(ctx) }()

	for i := range 2 {
		select {
		case published := <-ticks:
			assert.Equalf(t, []string{"media"}, published, "tick %d", i)
		case <-time.After(10 * time.Second):
			t.Fatalf("no sweep on tick %d", i)
		}
	}
}

type everyMillisecond struct{}

func (everyMillisecond) Next(t time.Time) time.Time { return t.Add(time.Millisecond) }

func TestTwelveHourly(t *testing.T) {
	sched := TwelveHourly()
	loc := time.Local
	cases := []struct {
		from time.Time
		want time.Time
	}{
		{time.Date(2026, 9, 18, 0, 0, 0, 0, loc), time.Date(2026, 9, 18, 12, 0, 0, 0, loc)},
		{time.Date(2026, 9, 18, 0, 0, 1, 0, loc), time.Date(2026, 9, 18, 12, 0, 0, 0, loc)},
		{time.Date(2026, 9, 18, 11, 59, 59, 0, loc), time.Date(2026, 9, 18, 12, 0, 0, 0, loc)},
		{time.Date(2026, 9, 18, 12, 0, 0, 0, loc), time.Date(2026, 9, 19, 0, 0, 0, 0, loc)},
		{time.Date(2026, 9, 18, 23, 59, 59, 0, loc), time.Date(2026, 9, 19, 0, 0, 0, 0, loc)},
		{time.Date(2026, 9, 18, 6, 30, 0, 0, loc), time.Date(2026, 9, 18, 12, 0, 0, 0, loc)},
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, sched.Next(c.from), "Next(%v)", c.from)
	}

	// Next is strictly after its argument, so a timer loop can never spin.
	at := time.Date(2026, 9, 18, 12, 0, 0, 0, loc)
	for range 5 {
		next := sched.Next(at)
		assert.True(t, next.After(at))
		at = next
	}
}

func TestNeedLeaderElection(t *testing.T) {
	assert.True(t, (&Runnable{}).NeedLeaderElection(), "a sweep from every replica would publish N times")
}

// TestRunOnce_WakesANamespaceHoldingOnlyNonVideoItems: before non-video
// search, the sweep listed only movies and episodes, so a namespace of
// nothing but music was never swept at all.
func TestRunOnce_WakesANamespaceHoldingOnlyNonVideoItems(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	bus := newTestBus(t)

	require.NoError(t, client.IgnoreAlreadyExists(c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "music-only"}})))
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "kid-a", Namespace: "music-only"},
		Spec:       catalogv1alpha1.AlbumSpec{ArtistRef: "radiohead", ReleaseGroupID: "b8048f24-c026-3398-b23a-b5e30716ea6f"},
	}))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, catalogac.Album("kid-a", "music-only").WithStatus(
		catalogac.AlbumStatus().WithPhase(catalogv1alpha1.AlbumPhaseWanted)))
	require.NoError(t, err)

	at := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	r := &Runnable{Client: c, Bus: bus, Now: func() time.Time { return at }, Namespaces: []string{"music-only"}}
	published, err := r.runOnce(ctx, at)
	require.NoError(t, err)
	assert.Equal(t, []string{"music-only"}, published)
}
