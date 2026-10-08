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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/import/importliststate"
	"github.com/mediactl/clustarr/app/import/manager/scanapply"
	"github.com/mediactl/clustarr/app/import/scanprogress"
	worker "github.com/mediactl/clustarr/app/import/worker/importlist"
	"github.com/mediactl/clustarr/app/import/worker/rescan"
	"github.com/mediactl/clustarr/app/intake"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

func TestHandleWithAutomaticAddOffListsTheEntriesButAddsNothing(t *testing.T) {
	c := requireEnvtest(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "il-noauto")
	bus := newBus(t, ctx)
	serveTmdbResolve(t, bus, "tmdb", "603")
	movie := movieName(t)

	newConfigMap(t, ctx, c, ns, "watch-csv", csvFixture)
	watch := newImdbCSVList(ns, "watch", "watch-csv", catalogv1alpha1.SyncLevelRemoveAndKeep)
	watch.Spec.AutomaticAdd = new(false)
	require.NoError(t, c.Create(ctx, watch))
	w := worker.NewWorker(c, bus)
	syncList(t, ctx, c, bus, w, watch)

	// It syncs and reports what it found, and adds nothing.
	var m catalogv1alpha1.Movie
	require.True(t, apierrors.IsNotFound(c.Get(ctx, types.NamespacedName{Namespace: ns, Name: movie}, &m)),
		"automaticAdd=false must not create the movie")
	res := lastResult(t, ctx, c, watch)
	require.Empty(t, res.LastError)
	require.Equal(t, int32(1), res.ItemCount)
	require.Zero(t, res.AddedCount)
	items, _, err := importliststate.LoadItems(ctx, bus.KV(events.BucketImportList), ns, watch.Name, "movie")
	require.NoError(t, err)
	require.Len(t, items, 1, "the found entry is recorded, as Radarr's SyncMoviesForList records it")
	require.True(t, items[0].ListedOnly)
	require.Equal(t, movie, items[0].ObjectName)

	// Radarr's CleanLibrary counts a movie on any enabled list, auto or
	// not, as still wanted: another list that added the film and drops it
	// leaves it alone while this one lists it.
	newConfigMap(t, ctx, c, ns, "auto-csv", csvFixture)
	auto := newImdbCSVList(ns, "auto", "auto-csv", catalogv1alpha1.SyncLevelRemoveAndKeep)
	require.NoError(t, c.Create(ctx, auto))
	syncList(t, ctx, c, bus, w, auto)
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: movie}, &m))
	updateConfigMap(t, ctx, c, ns, "auto-csv", csvFixtureEmpty)
	syncList(t, ctx, c, bus, w, auto)
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: movie}, &m),
		"the non-automatic list still lists the movie")

	// And the non-automatic list never removes what it did not add: the
	// film falls off it too, under removeAndKeep, and the movie stays.
	updateConfigMap(t, ctx, c, ns, "watch-csv", csvFixtureEmpty)
	syncList(t, ctx, c, bus, w, watch)
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: movie}, &m),
		"an entry the list only listed is never its to remove")
	require.Zero(t, lastResult(t, ctx, c, watch).RemovedCount)
}

// scanMessage is the ScanTask the LibraryScan controller would publish for
// scan over root.
func scanMessage(t *testing.T, scan *catalogv1alpha1.LibraryScan, rf *catalogv1alpha1.RootFolder) *fakeMessage {
	t.Helper()
	task := schema.ScanTask{
		LibraryScanRef: schema.Ref{Namespace: scan.Namespace, Name: scan.Name, UID: string(scan.UID)},
		RootFolderRef:  schema.Ref{Namespace: rf.Namespace, Name: rf.Name},
		Path:           rf.Spec.Path,
		Mode:           string(scan.Spec.Mode),
	}
	name, data, err := schema.Encode(task)
	require.NoError(t, err)
	return &fakeMessage{env: &events.Envelope{Schema: name, Data: data, Type: "import.ScanTask"}, attempt: 1}
}

// observingBus hands every library-scan observation published on it to the
// manager's import-intake-scan consumer once the publish lands.
type observingBus struct {
	events.Bus
	intake *intake.ScanConsumer
}

func (b observingBus) Publish(ctx context.Context, subject string, e *events.Envelope, opts ...events.PublishOption) (events.Receipt, error) {
	r, err := b.Bus.Publish(ctx, subject, e, opts...)
	if err != nil || e.Schema != (schema.ScanObservation{}).Schema() {
		return r, err
	}
	return r, b.intake.Handle(ctx, &fakeMessage{env: e, attempt: 1})
}

// TestRemoveAndKeepSurvivesALibraryRescan is the ruling on removeAndKeep:
// the item goes, its files and MediaFile records stay, and the real library
// rescan -- which creates the item for any unrecorded file it can attribute
// -- does not bring it back. A list that lists it again does.
func TestRemoveAndKeepSurvivesALibraryRescan(t *testing.T) {
	c := requireEnvtest(t)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ns := createNamespace(t, ctx, c, "il-rmkeep-rescan")
	bus := newBus(t, ctx)
	serveTmdbResolve(t, bus, "tmdb", "603")

	// The rescan reads through a manager's cache, with its path index.
	mgr, err := ctrl.NewManager(testConfig, ctrl.Options{
		Scheme:                 c.Scheme(),
		Metrics:                metricsserver.Options{BindAddress: "0"},
		HealthProbeBindAddress: "0",
		Cache:                  cache.Options{DefaultNamespaces: map[string]cache.Config{ns: {}}},
	})
	require.NoError(t, err)
	for _, fi := range rescan.FieldIndexes() {
		require.NoError(t, mgr.GetFieldIndexer().IndexField(ctx, fi.Object, fi.Name, fi.Extract))
	}
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	t.Cleanup(func() { cancel(); <-done })
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	cached := mgr.GetClient()

	// A root folder whose defaults would let the rescan create a movie.
	root := mediaDir(t)
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "movies"},
		Spec: catalogv1alpha1.RootFolderSpec{
			Path: root, Kind: catalogv1alpha1.RootFolderKindMovie,
			Defaults:   catalogv1alpha1.RootDefaults{QualityProfileRef: "hd-1080p"},
			RecycleBin: catalogv1alpha1.RecycleBin{Path: mediaDir(t)},
		},
	}
	require.NoError(t, c.Create(ctx, rf))
	video := filepath.Join(root, "The Matrix (1999) [tmdbid-603]", "The Matrix (1999) [tmdbid-603] - Bluray-1080p.mkv")
	require.NoError(t, os.MkdirAll(filepath.Dir(video), 0o755))
	f, err := os.Create(video)
	require.NoError(t, err)
	require.NoError(t, f.Truncate(60<<20)) // clear the suspected-sample floor, sparsely
	require.NoError(t, f.Close())

	// The rescan reports observations and the manager's scan applier
	// writes (ADR-0019 §7.6): every observation the walk publishes is
	// applied before its publish returns, as the import-intake-scan
	// consumer would.
	scanBus := observingBus{Bus: bus, intake: &intake.ScanConsumer{
		Bus: bus, Applier: &scanapply.Applier{Client: c, Reader: cached, APIReader: c, Bus: bus},
	}}
	rescanOnce := func(name string) scanprogress.Progress {
		t.Helper()
		scan := &catalogv1alpha1.LibraryScan{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
			Spec:       catalogv1alpha1.LibraryScanSpec{RootFolderRef: rf.Name, Mode: catalogv1alpha1.ScanModeFull},
		}
		require.NoError(t, c.Create(ctx, scan))
		require.Eventually(t, func() bool {
			var got catalogv1alpha1.LibraryScan
			return cached.Get(ctx, client.ObjectKeyFromObject(scan), &got) == nil
		}, 10*time.Second, 20*time.Millisecond)
		require.NoError(t, rescan.NewWorker(cached, scanBus, nil).Handle(ctx, scanMessage(t, scan, rf)))
		entry, err := bus.KV(events.BucketProgress).Get(ctx, scanprogress.ProgressKey(string(scan.UID)))
		require.NoError(t, err)
		p, err := scanprogress.DecodeProgress(entry.Value)
		require.NoError(t, err)
		require.Equal(t, int64(1), p.FilesSeen, "the walk must reach the kept file")
		// The LibraryScan controller adds the applier's tally to the walk's.
		switch entry, err := bus.KV(events.BucketProgress).Get(ctx, scanprogress.AppliedKey(string(scan.UID))); {
		case err == nil:
			applied, err := scanprogress.DecodeProgress(entry.Value)
			require.NoError(t, err)
			p = p.Merge(applied)
		default:
			require.ErrorIs(t, err, events.ErrKeyNotFound)
		}
		return p
	}
	movieKey := types.NamespacedName{Namespace: ns, Name: movieName(t)}
	cachedHas := func(want bool) func() bool {
		return func() bool {
			var m catalogv1alpha1.Movie
			err := cached.Get(ctx, movieKey, &m)
			return (err == nil) == want
		}
	}

	// Steady state: the list added the movie and a rescan recorded its file.
	newConfigMap(t, ctx, c, ns, "watch-csv", csvFixture)
	il := newImdbCSVList(ns, "watch", "watch-csv", catalogv1alpha1.SyncLevelRemoveAndKeep)
	require.NoError(t, c.Create(ctx, il))
	w := worker.NewWorker(c, bus)
	syncList(t, ctx, c, bus, w, il)
	require.Eventually(t, cachedHas(true), 10*time.Second, 20*time.Millisecond)
	rescanOnce("scan-1")
	var files catalogv1alpha1.MediaFileList
	require.NoError(t, c.List(ctx, &files, client.InNamespace(ns)))
	require.Len(t, files.Items, 1)
	require.Equal(t, movieKey.Name, files.Items[0].Spec.MediaRef.Name, "the rescan attributed the file to the list's movie")

	// The film falls off the list: removeAndKeep.
	updateConfigMap(t, ctx, c, ns, "watch-csv", csvFixtureEmpty)
	syncList(t, ctx, c, bus, w, il)
	var m catalogv1alpha1.Movie
	require.True(t, apierrors.IsNotFound(c.Get(ctx, movieKey, &m)))
	require.Eventually(t, cachedHas(false), 10*time.Second, 20*time.Millisecond)

	// A full rescan walks the kept file and does not bring the movie back.
	p := rescanOnce("scan-2")
	require.Zero(t, p.ItemsCreated)
	require.Empty(t, p.Unmatched)
	var movies catalogv1alpha1.MovieList
	require.NoError(t, c.List(ctx, &movies, client.InNamespace(ns)))
	require.Empty(t, movies.Items, "a rescan must not undo removeAndKeep")
	require.FileExists(t, video, "removeAndKeep keeps the file")
	require.NoError(t, c.List(ctx, &files, client.InNamespace(ns)))
	require.Len(t, files.Items, 1, "and its record, which is what keeps the rescan from re-adopting it")

	// The film comes back onto the list: it is added again, and its kept
	// file is its file again (Radarr: no exclusion; a re-added movie finds
	// its file).
	updateConfigMap(t, ctx, c, ns, "watch-csv", csvFixture)
	syncList(t, ctx, c, bus, w, il)
	require.NoError(t, c.Get(ctx, movieKey, &m))
	require.Equal(t, m.Name, files.Items[0].Spec.MediaRef.Name)
}
