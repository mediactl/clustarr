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

package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/tvdb"
	"github.com/mediactl/clustarr/pkg/metadata/extended"
)

// The TVDB client is the registry's episode people provider.
var _ pkgmetadata.EpisodePeopleProvider = (*tvdb.Client)(nil)

// recordedEpisodeTVDB is the real TVDB client against
// hack/record-metadata-fixtures' recording of Doctor Who's "Rose" (TVDB
// episode 295294: nine guest stars, a director, a writer); any other
// episode is TheTVDB's 404. It counts the episode requests by id.
func recordedEpisodeTVDB(t *testing.T) (*tvdb.Client, func() map[string]int) {
	t.Helper()
	var mu sync.Mutex
	asked := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		file := ""
		switch {
		case r.URL.Path == "/login":
			file = "login.json"
		case strings.HasPrefix(r.URL.Path, "/episodes/"):
			id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/episodes/"), "/extended")
			mu.Lock()
			asked[id]++
			mu.Unlock()
			if id == "295294" {
				file = "episode-295294-extended.json"
			}
		}
		if file == "" {
			http.NotFound(w, r)
			return
		}
		body, err := os.ReadFile("../../../test/data/metadata/tvdb/" + file)
		require.NoError(t, err)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	c := tvdb.New("test-key", "test-pin", srv.Client(), srv.URL, pkgmetadata.NewLimiter(rate.Inf, 1))
	return c, func() map[string]int {
		mu.Lock()
		defer mu.Unlock()
		out := map[string]int{}
		for k, v := range asked {
			out[k] = v
		}
		return out
	}
}

func episode(name, series string, number int32, hasFile bool, tvdbID int64) *catalogv1alpha1.Episode {
	return &catalogv1alpha1.Episode{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media", UID: types.UID(name + "-uid")},
		Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: series, SeasonNumber: 1, EpisodeNumber: number},
		Status:     catalogv1alpha1.EpisodeStatus{HasFile: hasFile, TvdbID: tvdbID},
	}
}

type episodePeopleFixture struct {
	h      *Handler
	kv     events.KV
	clock  *clockwork.FakeClock
	series *catalogv1alpha1.Series
	asked  func() map[string]int
}

// newEpisodePeopleFixture is Doctor Who with an episode in every case the
// pass tells apart, plus another series' episode the field selector must
// keep out, over the real default topology's extended bucket.
func newEpisodePeopleFixture(t *testing.T, eps ...*catalogv1alpha1.Episode) episodePeopleFixture {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, catalogv1alpha1.AddToScheme(scheme))
	objs := make([]client.Object, len(eps))
	for i, e := range eps {
		objs[i] = e
	}
	reader := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).
		WithIndex(&catalogv1alpha1.Episode{}, episodeSeriesRefField, func(o client.Object) []string {
			return []string{o.(*catalogv1alpha1.Episode).Spec.SeriesRef}
		}).Build()

	clock := clockwork.NewFakeClockAt(time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC))
	bus := membus.New(clock)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(context.Background(), events.Default().ForSingleNode()))

	provider, asked := recordedEpisodeTVDB(t)
	series := &catalogv1alpha1.Series{ObjectMeta: metav1.ObjectMeta{Name: "doctor-who", Namespace: "media"}}
	kv := bus.KV(events.BucketMetadataExtended)
	return episodePeopleFixture{
		h:      &Handler{Reader: reader, Registry: &pkgmetadata.Registry{Series: []pkgmetadata.SeriesProvider{provider}}, Extended: kv},
		kv:     kv,
		clock:  clock,
		series: series,
		asked:  asked,
	}
}

func (f episodePeopleFixture) doc(t *testing.T, name string) (extended.Doc, bool) {
	t.Helper()
	e, err := f.kv.Get(context.Background(), extended.Key(commonv1.MediaKindEpisode, types.UID(name+"-uid")))
	if err != nil {
		require.ErrorIs(t, err, events.ErrKeyNotFound)
		return extended.Doc{}, false
	}
	d, err := extended.Decode(e.Value)
	require.NoError(t, err)
	return d, true
}

// A refresh files the guest cast and crew of every episode of the series
// with a file, from TheTVDB's episode record; an episode without a file,
// one with no TVDB id and another series' episode are never asked for,
// and an episode TheTVDB does not know is filed with no people, so it is
// not asked again.
func TestASeriesRefreshFilesItsEpisodesPeople(t *testing.T) {
	f := newEpisodePeopleFixture(t,
		episode("rose", "doctor-who", 1, true, 295294),
		episode("no-file", "doctor-who", 2, false, 295294),
		episode("no-tvdb-id", "doctor-who", 3, true, 0),
		episode("unknown", "doctor-who", 4, true, 999),
		episode("other-series", "torchwood", 1, true, 295294),
	)
	f.h.refreshEpisodePeople(context.Background(), f.series, f.clock.Now(), nil)

	rose, ok := f.doc(t, "rose")
	require.True(t, ok)
	require.Len(t, rose.Role, 9, "the nine guest stars, as Role")
	require.Len(t, rose.Director, 1)
	require.Len(t, rose.Writer, 1)
	require.NotEmpty(t, rose.Role[0].Character, "a guest star carries the character played")
	require.Equal(t, "Director", rose.Director[0].Job)

	unknown, ok := f.doc(t, "unknown")
	require.True(t, ok, "TheTVDB's 404 is filed, so the episode is not asked again before the TTL")
	require.Empty(t, unknown.Role)

	for _, name := range []string{"no-file", "no-tvdb-id", "other-series"} {
		_, ok := f.doc(t, name)
		require.False(t, ok, name)
	}
	require.Equal(t, map[string]int{"295294": 1, "999": 1}, f.asked(), "one request per filed episode")
}

// A filed episode is not asked again until its document is older than
// EpisodePeopleTTL.
func TestEpisodePeopleAreAskedAgainOnlyAfterTheTTL(t *testing.T) {
	f := newEpisodePeopleFixture(t, episode("rose", "doctor-who", 1, true, 295294))
	ctx := context.Background()
	f.h.refreshEpisodePeople(ctx, f.series, f.clock.Now(), nil)

	f.clock.Advance(EpisodePeopleTTL - time.Hour)
	f.h.refreshEpisodePeople(ctx, f.series, f.clock.Now(), nil)
	require.Equal(t, map[string]int{"295294": 1}, f.asked(), "fresh: not asked again")

	f.clock.Advance(2 * time.Hour)
	f.h.refreshEpisodePeople(ctx, f.series, f.clock.Now(), nil)
	require.Equal(t, map[string]int{"295294": 2}, f.asked(), "stale: asked again")
}

// One pass asks about at most episodePeoplePerPass episodes; the next
// refresh takes the rest.
func TestAPassAsksAboutAtMostItsCap(t *testing.T) {
	defer func(n int) { episodePeoplePerPass = n }(episodePeoplePerPass)
	episodePeoplePerPass = 2
	f := newEpisodePeopleFixture(t,
		episode("e1", "doctor-who", 1, true, 1001),
		episode("e2", "doctor-who", 2, true, 1002),
		episode("e3", "doctor-who", 3, true, 1003),
	)
	ctx := context.Background()
	f.h.refreshEpisodePeople(ctx, f.series, f.clock.Now(), nil)
	require.Len(t, f.asked(), 2)
	f.h.refreshEpisodePeople(ctx, f.series, f.clock.Now(), nil)
	require.Len(t, f.asked(), 3, "the second pass takes the one left")
	for _, n := range []int{1, 2, 3} {
		require.Equal(t, 1, f.asked()["100"+string(rune('0'+n))])
	}
}

// With no episode people provider configured the pass does nothing.
func TestAPassWithoutAProviderDoesNothing(t *testing.T) {
	f := newEpisodePeopleFixture(t, episode("rose", "doctor-who", 1, true, 295294))
	f.h.Registry = &pkgmetadata.Registry{}
	f.h.refreshEpisodePeople(context.Background(), f.series, f.clock.Now(), nil)
	_, ok := f.doc(t, "rose")
	require.False(t, ok)
	require.Empty(t, f.asked())
}
