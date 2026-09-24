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

package importlist_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	worker "github.com/mediactl/clustarr/app/import/worker/importlist"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// csvFixture is imdbcsv's own test fixture (imdbcsv/parse_test.go), reused
// here so this suite exercises the real parser rather than a hand-rolled
// substitute. It carries one movie (The Matrix, tt0133093) and one series
// (Breaking Bad), each with an IMDb id only -- no TMDB/TVDB id -- so every
// test in this file also exercises resolveRequiredID's metadata-gateway
// fallback.
const csvFixture = `Const,Your Rating,Date Rated,Title,URL,Title Type,IMDb Rating,Runtime (mins),Year,Genres,Num Votes,Release Date,Directors
tt0133093,,,The Matrix,https://www.imdb.com/title/tt0133093/,movie,8.7,136,1999,"Action, Sci-Fi",2000000,1999-03-31,"Lana Wachowski, Lilly Wachowski"
`

// csvFixtureEmpty has the header only: the movie has fallen off the list.
const csvFixtureEmpty = `Const,Your Rating,Date Rated,Title,URL,Title Type,IMDb Rating,Runtime (mins),Year,Genres,Num Votes,Release Date,Directors
`

// movieName reproduces catalogitem.go's movieName("The Matrix", 603) via
// the exported k8s.ChildName, so this test asserts against the real naming
// convention rather than a hand-copied one that could silently drift from
// it.
func movieName(t *testing.T) string {
	t.Helper()
	return k8s.ChildName("The Matrix", "movie", "603")
}

func newConfigMap(t *testing.T, ctx context.Context, c client.Client, ns, name, content string) {
	t.Helper()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Data:       map[string]string{"list.csv": content},
	}
	require.NoError(t, c.Create(ctx, cm))
}

func updateConfigMap(t *testing.T, ctx context.Context, c client.Client, ns, name, content string) {
	t.Helper()
	var cm corev1.ConfigMap
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &cm))
	cm.Data = map[string]string{"list.csv": content}
	require.NoError(t, c.Update(ctx, &cm))
}

func newImdbCSVList(ns, name, configMap string, syncLevel catalogv1alpha1.SyncLevel) *catalogv1alpha1.ImportList {
	return &catalogv1alpha1.ImportList{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: catalogv1alpha1.ImportListSpec{
			Kinds:   []string{"movie"},
			ImdbCSV: &catalogv1alpha1.CSVList{ConfigMapRef: corev1.LocalObjectReference{Name: configMap}},
			Defaults: catalogv1alpha1.ListDefaults{
				QualityProfileRef: "hd-1080p", RootFolderRef: "movies",
			},
			SyncLevel: syncLevel,
		},
	}
}

// serveTmdbResolve makes the gateway's resolve verb answer every request
// with tmdbID for whatever kind is asked, so a worker under test can turn
// an IMDb-only fetched item into a Movie or Series without a real metadata
// provider.
func serveTmdbResolve(t *testing.T, bus events.Bus, key, id string) {
	t.Helper()
	require.NoError(t, bus.Serve(events.RPCMetadataResolve, "test", func(_ context.Context, data []byte) ([]byte, error) {
		var req schema.MetadataRequest
		require.NoError(t, schema.Decode("", data, &req))
		resp := schema.MetadataResponse{Kind: req.Kind, IDs: map[string]string{key: id}}
		_, out, err := schema.Encode(resp)
		return out, err
	}))
}

func newTaskMessage(t *testing.T, ns, name, uid string) *fakeMessage {
	t.Helper()
	task := schema.ListTask{ListRef: schema.Ref{Namespace: ns, Name: name, UID: uid}}
	schemaName, data, err := schema.Encode(task)
	require.NoError(t, err)
	return &fakeMessage{
		env:     &events.Envelope{Schema: schemaName, Data: data, Key: ns + "/" + name, Type: "importarr.ListTask"},
		attempt: 1,
	}
}

// fakeMessage is a minimal events.Message over a locally built envelope, so
// a test can call Handle directly without a live subscription.
type fakeMessage struct {
	env     *events.Envelope
	attempt uint64
}

func (m *fakeMessage) Envelope() *events.Envelope               { return m.env }
func (m *fakeMessage) Subject() string                          { return "test" }
func (m *fakeMessage) Attempt() uint64                          { return m.attempt }
func (m *fakeMessage) Ack(context.Context) error                { return nil }
func (m *fakeMessage) Nak(context.Context, time.Duration) error { return nil }
func (m *fakeMessage) Term(context.Context, string) error       { return nil }
func (m *fakeMessage) InProgress(context.Context) error         { return nil }

func TestHandleCreatesAMovieFromAnImdbCSVList(t *testing.T) {
	c := requireEnvtest(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "il-happy")
	bus := newBus(t, ctx)
	serveTmdbResolve(t, bus, "tmdb", "603")

	newConfigMap(t, ctx, c, ns, "watchlist-csv", csvFixture)
	il := newImdbCSVList(ns, "watchlist", "watchlist-csv", catalogv1alpha1.SyncLevelLogOnly)
	require.NoError(t, c.Create(ctx, il))

	w := worker.NewWorker(c, bus)
	require.NoError(t, w.Handle(ctx, newTaskMessage(t, ns, il.Name, string(il.UID))))

	name := movieName(t)
	var m catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &m))
	require.Equal(t, int64(603), m.Spec.TmdbID)
	require.NotNil(t, m.Spec.Source)
	require.Equal(t, il.Name, m.Spec.Source.ImportListRef)
	require.Equal(t, "hd-1080p", m.Spec.QualityProfileRef)
	require.Equal(t, catalogv1alpha1.MovieAddMethodList, m.Spec.AddOptions.AddMethod)

	specManager := managerFor(m.ManagedFields, "")
	require.Equal(t, "importarr-worker", specManager, "MovieSpec must be owned by k8s.ManagerImportarrWorker")

	entry, err := bus.KV(events.BucketProgress).Get(ctx, worker.ResultKey(string(il.UID)))
	require.NoError(t, err, "the worker must checkpoint a result for the controller to poll")
	res, err := worker.DecodeResult(entry.Value)
	require.NoError(t, err)
	require.Equal(t, int32(1), res.Fetched)
	require.Equal(t, int32(1), res.Added)
	require.Empty(t, res.Error)
}

func TestHandleRespectsAnImportExclusion(t *testing.T) {
	c := requireEnvtest(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "il-excluded")
	bus := newBus(t, ctx)
	serveTmdbResolve(t, bus, "tmdb", "603")

	// The exclusion index is normally maintained by
	// importarr/controller/importexclusion; this test writes the KV entry
	// directly, the same lookup contract events.ExclusionEntry documents,
	// so it does not need that controller running to prove this worker
	// consults the bucket.
	entry := events.ExclusionEntry{Namespace: ns, Name: "blocked-matrix", Kind: "movie", Reason: "test"}
	data, err := entry.Encode()
	require.NoError(t, err)
	_, err = bus.KV(events.BucketImportExclusions).Put(ctx, events.ExclusionKey("imdb", "tt0133093"), data)
	require.NoError(t, err)

	newConfigMap(t, ctx, c, ns, "watchlist-csv", csvFixture)
	il := newImdbCSVList(ns, "watchlist", "watchlist-csv", catalogv1alpha1.SyncLevelLogOnly)
	require.NoError(t, c.Create(ctx, il))

	w := worker.NewWorker(c, bus)
	require.NoError(t, w.Handle(ctx, newTaskMessage(t, ns, il.Name, string(il.UID))))

	var m catalogv1alpha1.Movie
	err = c.Get(ctx, types.NamespacedName{Namespace: ns, Name: movieName(t)}, &m)
	require.Error(t, err, "an excluded entry must never become a catalog item")

	resEntry, err := bus.KV(events.BucketProgress).Get(ctx, worker.ResultKey(string(il.UID)))
	require.NoError(t, err)
	res, err := worker.DecodeResult(resEntry.Value)
	require.NoError(t, err)
	require.Equal(t, int32(1), res.Excluded)
	require.Equal(t, int32(0), res.Added)
}

func TestHandleUnmonitorsAnItemThatFellOffTheList(t *testing.T) {
	c := requireEnvtest(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "il-unmonitor")
	bus := newBus(t, ctx)
	serveTmdbResolve(t, bus, "tmdb", "603")

	newConfigMap(t, ctx, c, ns, "watchlist-csv", csvFixture)
	il := newImdbCSVList(ns, "watchlist", "watchlist-csv", catalogv1alpha1.SyncLevelKeepAndUnmonitor)
	require.NoError(t, c.Create(ctx, il))

	w := worker.NewWorker(c, bus)
	require.NoError(t, w.Handle(ctx, newTaskMessage(t, ns, il.Name, string(il.UID))))

	name := movieName(t)
	var m catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &m))
	require.True(t, m.Spec.Monitored == nil || *m.Spec.Monitored, "must start monitored")

	// The item falls off the list.
	updateConfigMap(t, ctx, c, ns, "watchlist-csv", csvFixtureEmpty)
	require.NoError(t, w.Handle(ctx, newTaskMessage(t, ns, il.Name, string(il.UID))))

	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &m))
	require.NotNil(t, m.Spec.Monitored)
	require.False(t, *m.Spec.Monitored, "syncLevel=keepAndUnmonitor must unmonitor an item that fell off the list")

	resEntry, err := bus.KV(events.BucketProgress).Get(ctx, worker.ResultKey(string(il.UID)))
	require.NoError(t, err)
	res, err := worker.DecodeResult(resEntry.Value)
	require.NoError(t, err)
	require.Equal(t, int32(1), res.Removed)
}
