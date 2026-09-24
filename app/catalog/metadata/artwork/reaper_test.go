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

package artwork_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/metadata/artwork"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/k8s"
)

const grace = 30 * time.Minute

type reapFixture struct {
	ctx   context.Context
	clock *clockwork.FakeClock
	store events.ObjectStore
}

func newReapFixture(t *testing.T) *reapFixture {
	t.Helper()
	ctx := context.Background()
	clock := clockwork.NewFakeClockAt(fetchNow)
	bus := membus.New(clock)
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	return &reapFixture{ctx: ctx, clock: clock, store: bus.ObjectStore(events.BucketArtwork)}
}

// put stores one object now, by the fixture's clock.
func (fx *reapFixture) put(t *testing.T, kind commonv1.MediaKind, uid types.UID, imageType, variant string) string {
	t.Helper()
	name := events.ArtworkKey(kind, uid, imageType, variant)
	_, err := fx.store.Put(fx.ctx, name, strings.NewReader("img"), map[string]string{"Content-Type": "image/png"})
	require.NoError(t, err)
	return name
}

func (fx *reapFixture) names(t *testing.T) []string {
	t.Helper()
	infos, err := fx.store.List(fx.ctx, "")
	require.NoError(t, err)
	out := make([]string, 0, len(infos))
	for _, i := range infos {
		out = append(out, i.Name)
	}
	return out
}

func liveMovie(uid types.UID) *catalogv1alpha1.Movie {
	return &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Name: "live-" + string(uid), Namespace: "films", UID: uid}}
}

func (fx *reapFixture) reaper(c client.Reader) *artwork.Reaper {
	return &artwork.Reaper{Store: fx.store, Client: c, Interval: time.Hour, Grace: grace, Clock: fx.clock}
}

func TestReaperDeletesOnlyOrphansPastTheGracePeriod(t *testing.T) {
	fx := newReapFixture(t)
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(liveMovie("live")).Build()

	orphanOriginal := fx.put(t, commonv1.MediaKindMovie, "gone", "poster", events.ArtworkVariantOriginal)
	orphanOverlay := fx.put(t, commonv1.MediaKindMovie, "gone", "poster", events.ArtworkVariantOverlay)
	liveOriginal := fx.put(t, commonv1.MediaKindMovie, "live", "poster", events.ArtworkVariantOriginal)
	liveOverlay := fx.put(t, commonv1.MediaKindMovie, "live", "poster", events.ArtworkVariantOverlay)
	fx.clock.Advance(grace + time.Minute)
	young := fx.put(t, commonv1.MediaKindMovie, "just-created", "poster", events.ArtworkVariantOriginal)

	deleted, err := fx.reaper(c).Sweep(fx.ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, deleted)
	assert.ElementsMatch(t, []string{liveOriginal, liveOverlay, young}, fx.names(t),
		"both variants of the orphan go; the live item's objects and the object younger than the grace period stay")
	assert.NotContains(t, fx.names(t), orphanOriginal)
	assert.NotContains(t, fx.names(t), orphanOverlay)

	fx.clock.Advance(grace)
	deleted, err = fx.reaper(c).Sweep(fx.ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, deleted, "level-driven: the young orphan goes on a later sweep, once past the grace period")
	assert.ElementsMatch(t, []string{liveOriginal, liveOverlay}, fx.names(t))
}

func TestReaperMatchesByKindAndUID(t *testing.T) {
	fx := newReapFixture(t)
	// A live Series whose UID a movie-prefixed object also names: the movie
	// object is still an orphan, because the key names a Movie.
	s := &catalogv1alpha1.Series{ObjectMeta: metav1.ObjectMeta{Name: "lost", Namespace: "tv", UID: "shared"}}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(s).Build()

	movieObj := fx.put(t, commonv1.MediaKindMovie, "shared", "poster", events.ArtworkVariantOriginal)
	seriesObj := fx.put(t, commonv1.MediaKindSeries, "shared", "poster", events.ArtworkVariantOriginal)
	fx.clock.Advance(grace + time.Minute)

	_, err := fx.reaper(c).Sweep(fx.ctx)
	require.NoError(t, err)
	assert.Equal(t, []string{seriesObj}, fx.names(t))
	assert.NotContains(t, fx.names(t), movieObj)
}

func TestReaperDeletesNothingOfAKindItCouldNotList(t *testing.T) {
	fx := newReapFixture(t)
	listErr := errors.New("apiserver unavailable")
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithInterceptorFuncs(interceptor.Funcs{
		List: func(ctx context.Context, cl client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			if list.GetObjectKind().GroupVersionKind().Kind == "MovieList" {
				return listErr
			}
			return cl.List(ctx, list, opts...)
		},
	}).Build()

	movieObj := fx.put(t, commonv1.MediaKindMovie, "maybe-live", "poster", events.ArtworkVariantOriginal)
	seriesObj := fx.put(t, commonv1.MediaKindSeries, "gone", "poster", events.ArtworkVariantOriginal)
	fx.clock.Advance(grace + time.Minute)

	deleted, err := fx.reaper(c).Sweep(fx.ctx)
	require.ErrorIs(t, err, listErr, "the failed list is reported")
	assert.Equal(t, 1, deleted)
	assert.Equal(t, []string{movieObj}, fx.names(t),
		"an empty answer from a failed list is not \"no live movies\": nothing of that kind is deleted")
	assert.NotContains(t, fx.names(t), seriesObj, "other kinds are still reaped")
}

func TestReaperLeavesKeysItDoesNotUnderstand(t *testing.T) {
	fx := newReapFixture(t)
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	episode := fx.put(t, commonv1.MediaKindEpisode, "u1", "thumb", events.ArtworkVariantOriginal)
	_, err := fx.store.Put(fx.ctx, "stray", strings.NewReader("x"), nil)
	require.NoError(t, err)
	fx.clock.Advance(grace + time.Minute)

	deleted, err := fx.reaper(c).Sweep(fx.ctx)
	require.NoError(t, err)
	assert.Zero(t, deleted)
	assert.ElementsMatch(t, []string{episode, "stray"}, fx.names(t),
		"a kind with no artwork, or a name that is not <kind>/<uid>/..., is not the reaper's to judge")
}

func TestReaperIsLeaderElectedAndStopsWithItsContext(t *testing.T) {
	fx := newReapFixture(t)
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	r := fx.reaper(c)
	assert.True(t, r.NeedLeaderElection(), "§B.5: one reaper per cluster")

	orphan := fx.put(t, commonv1.MediaKindMovie, "gone", "poster", events.ArtworkVariantOriginal)
	fx.clock.Advance(grace + time.Minute)

	ctx, cancel := context.WithCancel(fx.ctx)
	done := make(chan error, 1)
	go func() { done <- r.Start(ctx) }()
	require.Eventually(t, func() bool {
		for _, n := range fx.names(t) {
			if n == orphan {
				return false
			}
		}
		return true
	}, 2*time.Second, 5*time.Millisecond, "Start sweeps once at once, without waiting a whole interval")
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not return after its context was cancelled")
	}
}
