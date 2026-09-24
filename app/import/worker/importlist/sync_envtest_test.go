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
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	worker "github.com/mediactl/clustarr/app/import/worker/importlist"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	pkgimportlist "github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// seriesCSVFixture is one tvSeries row (Breaking Bad), IMDb id only.
const seriesCSVFixture = `Const,Your Rating,Date Rated,Title,URL,Title Type,IMDb Rating,Runtime (mins),Year,Genres,Num Votes,Release Date,Directors
tt0903747,,,Breaking Bad,https://www.imdb.com/title/tt0903747/,tvSeries,9.5,49,2008,"Crime, Drama",2000000,2008-01-20,
`

func lastResult(t *testing.T, ctx context.Context, bus events.Bus, uid types.UID) worker.Result {
	t.Helper()
	entry, err := bus.KV(events.BucketProgress).Get(ctx, worker.ResultKey(string(uid)))
	require.NoError(t, err)
	res, err := worker.DecodeResult(entry.Value)
	require.NoError(t, err)
	return res
}

func getList(t *testing.T, ctx context.Context, c client.Client, ns, name string) *catalogv1alpha1.ImportList {
	t.Helper()
	var il catalogv1alpha1.ImportList
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &il))
	return &il
}

// mediaDir returns a fresh directory under /data/media (RootFolderSpec.path's
// CEL requires that prefix), skipping when it cannot be made -- the same
// named skip importarr/worker/fileimport's dataDir uses.
func mediaDir(t *testing.T) string {
	t.Helper()
	prefix := "/data/media"
	if v := os.Getenv("CLUSTARR_TEST_MEDIA_ROOT"); v != "" {
		prefix = v
	}
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		t.Skipf("%s is not creatable (%v); run `make test` or set CLUSTARR_TEST_MEDIA_ROOT", prefix, err)
	}
	dir, err := os.MkdirTemp(prefix, "clustarr-importlist-")
	if err != nil {
		t.Skipf("%s is not writable (%v)", prefix, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func writeFile(t *testing.T, path string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("x"), 0o600))
}

// newLibrary creates a RootFolder named name whose path and recycle bin are
// fresh directories, and returns both paths.
func newLibrary(t *testing.T, ctx context.Context, c client.Client, ns, name string, kind catalogv1alpha1.RootFolderKind) (root, bin string) {
	t.Helper()
	root, bin = mediaDir(t), mediaDir(t)
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: catalogv1alpha1.RootFolderSpec{
			Path: root, Kind: kind, RecycleBin: catalogv1alpha1.RecycleBin{Path: bin},
		},
	}
	require.NoError(t, c.Create(ctx, rf))
	return root, bin
}

// newMediaFile creates a MediaFile for ref at path and gives it the sidecars
// catalogarr's MediaFile reconciler would have found, under its manager.
func newMediaFile(t *testing.T, ctx context.Context, c client.Client, ns, name string, ref commonv1.MediaRef, path string, sidecars ...string) {
	t.Helper()
	mf := &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       catalogv1alpha1.MediaFileSpec{MediaRef: ref, Path: path},
	}
	require.NoError(t, c.Create(ctx, mf))
	if len(sidecars) == 0 {
		return
	}
	st := catalogac.MediaFileStatus()
	for _, p := range sidecars {
		st = st.WithSidecars(catalogac.Sidecar().WithPath(p))
	}
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, catalogac.MediaFile(name, ns).WithStatus(st))
	require.NoError(t, err)
}

// recycled returns the path fsops.Recycle moved base to under bin today.
func recycled(bin, base string) string {
	return filepath.Join(bin, time.Now().UTC().Format("2006-01-02"), base)
}

func TestHandleStampsTheListWhenASyncFinishes(t *testing.T) {
	c := requireEnvtest(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "il-stamp")
	bus := newBus(t, ctx)
	serveTmdbResolve(t, bus, "tmdb", "603")

	newConfigMap(t, ctx, c, ns, "watchlist-csv", csvFixture)
	il := newImdbCSVList(ns, "watchlist", "watchlist-csv", catalogv1alpha1.SyncLevelLogOnly)
	require.NoError(t, c.Create(ctx, il))

	w := worker.NewWorker(c, bus)
	require.NoError(t, w.Handle(ctx, newTaskMessage(t, ns, il.Name, string(il.UID))))

	res := lastResult(t, ctx, bus, il.UID)
	got := getList(t, ctx, c, ns, il.Name)
	require.Equal(t, res.SyncedAt.UTC().Format(time.RFC3339Nano), got.Annotations[worker.AnnotationSyncedAt],
		"the stamp carries the checkpoint's SyncedAt, so each finished sync changes it")

	var stampOwner string
	for _, e := range got.ManagedFields {
		if e.Subresource == "" && e.FieldsV1 != nil &&
			strings.Contains(e.FieldsV1.GetRawString(), `"f:`+worker.AnnotationSyncedAt+`"`) {
			stampOwner = e.Manager
		}
	}
	require.Equal(t, "importarr-worker", stampOwner, "the stamp is the worker's field, never the controller's")
	require.Empty(t, got.Status.Conditions, "the worker never writes ImportList.status")
}

func TestHandleSendsPlexAndTraktToTheirBaseURLOverrides(t *testing.T) {
	c := requireEnvtest(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "il-baseurl")
	bus := newBus(t, ctx)
	serveTmdbResolve(t, bus, "tmdb", "329865")

	var plexHits, traktHits atomic.Int32
	plexSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		plexHits.Add(1)
		b, err := os.ReadFile("../../../../test/data/importlist/plex/watchlist_page1.json")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(plexSrv.Close)
	traktSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traktHits.Add(1)
		if r.URL.Path != "/users/nbatkins/watchlist/movies" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		b, err := os.ReadFile("../../../../test/data/importlist/trakt/watchlist_movies.json")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(traktSrv.Close)

	require.NoError(t, c.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "creds"},
		Data: map[string][]byte{
			"token": []byte("plex-tok"), "clientID": []byte("cid"), "clientSecret": []byte("secret"),
		},
	}))
	defaults := catalogv1alpha1.ListDefaults{QualityProfileRef: "hd-1080p", RootFolderRef: "movies"}
	plexList := &catalogv1alpha1.ImportList{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "plex"},
		Spec: catalogv1alpha1.ImportListSpec{
			Kinds: []string{"movie"}, Plex: &catalogv1alpha1.PlexWatchlist{},
			SecretRef: &corev1.LocalObjectReference{Name: "creds"}, Defaults: defaults,
		},
	}
	traktList := &catalogv1alpha1.ImportList{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "trakt"},
		Spec: catalogv1alpha1.ImportListSpec{
			Kinds:     []string{"movie"},
			Trakt:     &catalogv1alpha1.TraktList{ListType: catalogv1alpha1.TraktListTypeWatchlist, Username: "nbatkins"},
			SecretRef: &corev1.LocalObjectReference{Name: "creds"}, Defaults: defaults,
		},
	}
	require.NoError(t, c.Create(ctx, plexList))
	require.NoError(t, c.Create(ctx, traktList))
	require.NoError(t, worker.NewSecretTokenStore(c, traktList).Save(ctx,
		pkgimportlist.Token{AccessToken: "access-tok", ExpiresAt: time.Now().Add(24 * time.Hour)}))

	w := worker.NewWorker(c, bus)
	w.PlexBaseURL, w.TraktBaseURL = plexSrv.URL, traktSrv.URL
	require.NoError(t, w.Handle(ctx, newTaskMessage(t, ns, plexList.Name, string(plexList.UID))))
	require.NoError(t, w.Handle(ctx, newTaskMessage(t, ns, traktList.Name, string(traktList.UID))))

	plexRes := lastResult(t, ctx, bus, plexList.UID)
	require.Empty(t, plexRes.Error)
	require.Equal(t, int32(2), plexRes.Added)
	require.Positive(t, plexHits.Load(), "the Plex fetch must reach the override")

	traktRes := lastResult(t, ctx, bus, traktList.UID)
	require.Empty(t, traktRes.Error)
	require.Equal(t, int32(1), traktRes.Added)
	require.Positive(t, traktHits.Load(), "the Trakt fetch must reach the override")

	for _, name := range []string{
		k8s.ChildName("Dune", "movie", "438631"),
		k8s.ChildName("Arrival", "movie", "329865"),
		k8s.ChildName("Deadpool", "movie", "293660"),
	} {
		var m catalogv1alpha1.Movie
		require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &m), name)
	}
}

func TestHandleFailsAKindTheProviderCannotYieldInsteadOfSkippingIt(t *testing.T) {
	c := requireEnvtest(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "il-unyieldable")
	bus := newBus(t, ctx)
	serveTmdbResolve(t, bus, "tmdb", "603")

	newConfigMap(t, ctx, c, ns, "watchlist-csv", csvFixture)
	il := newImdbCSVList(ns, "watchlist", "watchlist-csv", catalogv1alpha1.SyncLevelLogOnly)
	il.Spec.Kinds = []string{"movie"}
	require.NoError(t, c.Create(ctx, il))
	// An imdbCSV list asking for albums is refused at admission since X14
	// (R-10's CEL rules), so the one the worker must still defend against
	// is a stale task for a list admitted before them: the worker reads it
	// with "album" added, through a client whose Get stands in for it.
	stale := staleKinds{Client: c, name: il.Name, kinds: []string{"movie", "album"}}

	arr := &catalogv1alpha1.ImportList{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "lidarr"},
		Spec: catalogv1alpha1.ImportListSpec{
			Kinds: []string{"album"},
			Arr:   &catalogv1alpha1.ArrList{BaseURL: "http://lidarr.invalid", Kind: catalogv1alpha1.ArrKindLidarr},
			Defaults: catalogv1alpha1.ListDefaults{
				QualityProfileRef: "lossless", RootFolderRef: "music",
			},
		},
	}
	require.NoError(t, c.Create(ctx, arr))

	w := worker.NewWorker(stale, bus)
	require.NoError(t, w.Handle(ctx, newTaskMessage(t, ns, il.Name, string(il.UID))))
	require.NoError(t, w.Handle(ctx, newTaskMessage(t, ns, arr.Name, string(arr.UID))))

	res := lastResult(t, ctx, bus, il.UID)
	require.Contains(t, res.Error, worker.ErrKindNotYieldable.Error(), "an imdbCSV album is a failure on status, not a skip")
	require.Contains(t, res.Error, "album")
	require.Equal(t, int32(1), res.Added, "the movie kind still syncs")

	// Lidarr can yield albums, so the kind goes to the provider rather than
	// being skipped; the arr provider is spec-deferred, and says so.
	arrRes := lastResult(t, ctx, bus, arr.UID)
	require.Contains(t, arrRes.Error, "arr not implemented yet", "the provider's own deferral reaches status")
	require.NotContains(t, arrRes.Error, worker.ErrKindNotYieldable.Error())
}

func TestHandleRemoveAndDeleteRecyclesTheFilesAndRemoveAndKeepLeavesThem(t *testing.T) {
	c := requireEnvtest(t)
	for _, tc := range []struct {
		level       catalogv1alpha1.SyncLevel
		filesDelete bool
	}{
		{catalogv1alpha1.SyncLevelRemoveAndDelete, true},
		{catalogv1alpha1.SyncLevelRemoveAndKeep, false},
	} {
		t.Run(string(tc.level), func(t *testing.T) {
			ctx := context.Background()
			ns := createNamespace(t, ctx, c, "il-"+map[bool]string{true: "rmdel", false: "rmkeep"}[tc.filesDelete])
			bus := newBus(t, ctx)
			serveTmdbResolve(t, bus, "tmdb", "603")
			root, bin := newLibrary(t, ctx, c, ns, "movies", catalogv1alpha1.RootFolderKindMovie)

			newConfigMap(t, ctx, c, ns, "watchlist-csv", csvFixture)
			il := newImdbCSVList(ns, "watchlist", "watchlist-csv", tc.level)
			require.NoError(t, c.Create(ctx, il))
			w := worker.NewWorker(c, bus)
			require.NoError(t, w.Handle(ctx, newTaskMessage(t, ns, il.Name, string(il.UID))))

			// Steady state: the movie was added and has a file and a
			// subtitle, and an unrelated file sits beside them.
			movie := movieName(t)
			folder := filepath.Join(root, "The Matrix (1999)")
			video := filepath.Join(folder, "The Matrix (1999).mkv")
			sub := filepath.Join(folder, "The Matrix (1999).en.srt")
			writeFile(t, video)
			writeFile(t, sub)
			newMediaFile(t, ctx, c, ns, "matrix-file", commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie}, video, sub)

			updateConfigMap(t, ctx, c, ns, "watchlist-csv", csvFixtureEmpty)
			require.NoError(t, w.Handle(ctx, newTaskMessage(t, ns, il.Name, string(il.UID))))

			res := lastResult(t, ctx, bus, il.UID)
			require.Empty(t, res.Error)
			require.Equal(t, int32(1), res.Removed)

			var m catalogv1alpha1.Movie
			require.True(t, apierrors.IsNotFound(c.Get(ctx, types.NamespacedName{Namespace: ns, Name: movie}, &m)),
				"both levels remove the Movie")

			var mf catalogv1alpha1.MediaFile
			mfErr := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "matrix-file"}, &mf)
			if !tc.filesDelete {
				require.NoError(t, mfErr, "removeAndKeep leaves the MediaFile")
				require.FileExists(t, video)
				require.FileExists(t, sub)
				return
			}
			require.True(t, apierrors.IsNotFound(mfErr), "removeAndDelete deletes the MediaFile once its files are recycled")
			require.NoFileExists(t, video)
			require.NoFileExists(t, sub)
			require.FileExists(t, recycled(bin, filepath.Base(video)), "the video goes to the recycle bin, not unlinked")
			require.FileExists(t, recycled(bin, filepath.Base(sub)), "and so does its sidecar")
			require.NoDirExists(t, folder, "the emptied movie folder is pruned")
			require.DirExists(t, root, "the root folder never is")
		})
	}
}

func TestHandleRemoveAndDeleteTakesASeriesEpisodeFiles(t *testing.T) {
	c := requireEnvtest(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "il-rmdel-series")
	bus := newBus(t, ctx)
	serveTmdbResolve(t, bus, "tvdb", "81189")
	root, bin := newLibrary(t, ctx, c, ns, "tv", catalogv1alpha1.RootFolderKindSeries)

	newConfigMap(t, ctx, c, ns, "shows-csv", seriesCSVFixture)
	il := newImdbCSVList(ns, "shows", "shows-csv", catalogv1alpha1.SyncLevelRemoveAndDelete)
	il.Spec.Kinds = []string{"series"}
	il.Spec.Defaults.RootFolderRef = "tv"
	require.NoError(t, c.Create(ctx, il))
	w := worker.NewWorker(c, bus)
	require.NoError(t, w.Handle(ctx, newTaskMessage(t, ns, il.Name, string(il.UID))))

	series := k8s.ChildName("Breaking Bad", "series", "81189")
	ep := &catalogv1alpha1.Episode{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: series + "-s01e01"},
		Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: series, SeasonNumber: 1, EpisodeNumber: 1},
	}
	other := &catalogv1alpha1.Episode{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "someone-else-s01e01"},
		Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "someone-else", SeasonNumber: 1, EpisodeNumber: 1},
	}
	require.NoError(t, c.Create(ctx, ep))
	require.NoError(t, c.Create(ctx, other))
	video := filepath.Join(root, "Breaking Bad", "Season 01", "Breaking Bad - S01E01.mkv")
	otherVideo := filepath.Join(root, "Someone Else", "Season 01", "Someone Else - S01E01.mkv")
	writeFile(t, video)
	writeFile(t, otherVideo)
	newMediaFile(t, ctx, c, ns, "bb-s01e01", commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: ep.Name}, video)
	newMediaFile(t, ctx, c, ns, "other-s01e01", commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: other.Name}, otherVideo)

	updateConfigMap(t, ctx, c, ns, "shows-csv", csvFixtureEmpty)
	require.NoError(t, w.Handle(ctx, newTaskMessage(t, ns, il.Name, string(il.UID))))
	require.Empty(t, lastResult(t, ctx, bus, il.UID).Error)

	require.NoFileExists(t, video)
	require.FileExists(t, recycled(bin, filepath.Base(video)))
	require.NoDirExists(t, filepath.Join(root, "Breaking Bad"))
	require.FileExists(t, otherVideo, "another series' episode file is not this series'")
	var mf catalogv1alpha1.MediaFile
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: "other-s01e01"}, &mf))
	var s catalogv1alpha1.Series
	require.True(t, apierrors.IsNotFound(c.Get(ctx, types.NamespacedName{Namespace: ns, Name: series}, &s)))
}

func TestHandleLeavesAnItemAnotherEnabledListStillHas(t *testing.T) {
	c := requireEnvtest(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "il-two-lists")
	bus := newBus(t, ctx)
	serveTmdbResolve(t, bus, "tmdb", "603")
	root, _ := newLibrary(t, ctx, c, ns, "movies", catalogv1alpha1.RootFolderKindMovie)

	newConfigMap(t, ctx, c, ns, "a-csv", csvFixture)
	newConfigMap(t, ctx, c, ns, "b-csv", csvFixture)
	a := newImdbCSVList(ns, "a", "a-csv", catalogv1alpha1.SyncLevelRemoveAndDelete)
	b := newImdbCSVList(ns, "b", "b-csv", catalogv1alpha1.SyncLevelLogOnly)
	require.NoError(t, c.Create(ctx, a))
	require.NoError(t, c.Create(ctx, b))
	w := worker.NewWorker(c, bus)
	require.NoError(t, w.Handle(ctx, newTaskMessage(t, ns, a.Name, string(a.UID))))
	require.NoError(t, w.Handle(ctx, newTaskMessage(t, ns, b.Name, string(b.UID))))

	movie := movieName(t)
	video := filepath.Join(root, "The Matrix (1999)", "The Matrix (1999).mkv")
	writeFile(t, video)
	newMediaFile(t, ctx, c, ns, "matrix-file", commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: movie}, video)

	// List a drops the film; list b still has it.
	updateConfigMap(t, ctx, c, ns, "a-csv", csvFixtureEmpty)
	require.NoError(t, w.Handle(ctx, newTaskMessage(t, ns, a.Name, string(a.UID))))

	var m catalogv1alpha1.Movie
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: ns, Name: movie}, &m),
		"list b still wants the movie (design spec §8.7: absent from every enabled list)")
	require.FileExists(t, video)
	res := lastResult(t, ctx, bus, a.UID)
	require.Empty(t, res.Error)
	require.Zero(t, res.Removed)
}

func TestHandleRemembersAnItemItCouldNotResolveThisCycle(t *testing.T) {
	c := requireEnvtest(t)
	ctx := context.Background()
	ns := createNamespace(t, ctx, c, "il-remember")
	bus := newBus(t, ctx)

	var failResolve atomic.Bool
	require.NoError(t, bus.Serve(events.RPCMetadataResolve, "test", func(_ context.Context, data []byte) ([]byte, error) {
		var req schema.MetadataRequest
		if err := schema.Decode("", data, &req); err != nil {
			return nil, err
		}
		resp := schema.MetadataResponse{Kind: req.Kind, IDs: map[string]string{"tmdb": "603"}}
		if failResolve.Load() {
			resp = schema.MetadataResponse{Kind: req.Kind, Error: "provider down"}
		}
		_, out, err := schema.Encode(resp)
		return out, err
	}))

	newConfigMap(t, ctx, c, ns, "watchlist-csv", csvFixture)
	il := newImdbCSVList(ns, "watchlist", "watchlist-csv", catalogv1alpha1.SyncLevelRemoveAndKeep)
	require.NoError(t, c.Create(ctx, il))
	w := worker.NewWorker(c, bus)
	require.NoError(t, w.Handle(ctx, newTaskMessage(t, ns, il.Name, string(il.UID))))

	// Still on the list, but the gateway cannot re-resolve it this cycle.
	failResolve.Store(true)
	require.NoError(t, w.Handle(ctx, newTaskMessage(t, ns, il.Name, string(il.UID))))

	// Then it falls off: the list must still know it added it.
	failResolve.Store(false)
	updateConfigMap(t, ctx, c, ns, "watchlist-csv", csvFixtureEmpty)
	require.NoError(t, w.Handle(ctx, newTaskMessage(t, ns, il.Name, string(il.UID))))

	var m catalogv1alpha1.Movie
	err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: movieName(t)}, &m)
	require.True(t, apierrors.IsNotFound(err), "removeAndKeep acts on an item a failed cycle carried forward, got %v", err)
	require.Equal(t, int32(1), lastResult(t, ctx, bus, il.UID).Removed)
}

// staleKinds is a client whose Get returns the named ImportList with
// spec.kinds replaced: an ImportList admitted before the CRD carried R-10's
// CEL rules (task X14), which the apiserver would now refuse to store.
type staleKinds struct {
	client.Client
	name  string
	kinds []string
}

func (s staleKinds) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if err := s.Client.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	if il, ok := obj.(*catalogv1alpha1.ImportList); ok && key.Name == s.name {
		il.Spec.Kinds = append([]string(nil), s.kinds...)
	}
	return nil
}
