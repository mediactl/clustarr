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

package fileimport_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/import/worker/fileimport"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// testClient is the manager-cached client every envtest in this package
// shares; nil (and every test skips) when KUBEBUILDER_ASSETS is unset. See
// app/import/worker/rescan's TestMain, which this mirrors.
var (
	testClient client.Client
	testAPI    client.Reader
)

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		os.Exit(m.Run())
	}

	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "start envtest: %v\n", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	code := func() int {
		defer cancel()
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:                 k8s.MustNewScheme(),
			Metrics:                metricsserver.Options{BindAddress: "0"},
			HealthProbeBindAddress: "0",
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "build manager: %v\n", err)
			return 1
		}
		if err := fileimport.IndexMediaFileByTarget(ctx, mgr); err != nil {
			fmt.Fprintf(os.Stderr, "index media files: %v\n", err)
			return 1
		}
		go func() { _ = mgr.Start(ctx) }()
		if !mgr.GetCache().WaitForCacheSync(ctx) {
			fmt.Fprintln(os.Stderr, "cache did not sync")
			return 1
		}
		testClient = mgr.GetClient()
		testAPI = mgr.GetAPIReader()
		return m.Run()
	}()

	if err := env.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "stop envtest: %v\n", err)
	}
	os.Exit(code)
}

func requireEnvtest(t *testing.T) client.Client {
	t.Helper()
	if testClient == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	return testClient
}

func createNamespace(t *testing.T, ctx context.Context, c client.Client, name string) string {
	t.Helper()
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}))
	return name
}

// dataDir returns a fresh, writable directory under /data/<sub>, skipping the
// test when /data is not creatable or writable -- the same named skip
// app/import/worker/rescan.mediaTempDir uses, and for the same reason: a
// missing prerequisite and a broken worker must not look the same in the
// output. sub is "media" for a RootFolder (RootFolderSpec.Path's CEL requires
// the /data/media/ prefix) or "scratch" for a Download content root (no CEL,
// but kept on the same /data mount so a hardlink import does not silently
// fall back to a cross-device copy in the test).
func dataDir(t *testing.T, sub string) string {
	t.Helper()
	prefix := "/data/" + sub
	if v := os.Getenv("CLUSTARR_TEST_MEDIA_ROOT"); v != "" && sub == "media" {
		prefix = v
	}
	if err := os.MkdirAll(prefix, 0o755); err != nil {
		t.Skipf("%s is not creatable (%v); run `make test`, `mkdir -p %s` by hand, "+
			"or set CLUSTARR_TEST_MEDIA_ROOT to a writable directory under /data/media/", prefix, err, prefix)
	}
	dir, err := os.MkdirTemp(prefix, "clustarr-fileimport-")
	if err != nil {
		t.Skipf("%s is not writable (%v)", prefix, err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// mustWriteSparseFile creates a sparse file of the given size, so a test can
// plant a "60 MiB" movie without writing 60 MiB. sampleFloor clears
// fsops.DefaultSampleMaxBytes (mirrored from app/import/worker/rescan's
// sampleFloor); under it a video file is a suspected sample, which the
// import rejects rather than imports (sample_envtest_test.go).
const sampleFloor = 60 << 20

func mustWriteSparseFile(t *testing.T, path string, size int64) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	f, err := os.Create(path) //nolint:gosec // a t.TempDir()-rooted path
	require.NoError(t, err)
	require.NoError(t, f.Truncate(size))
	require.NoError(t, f.Close())
}

func newBus(t *testing.T, ctx context.Context) events.Bus {
	t.Helper()
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	return bus
}

// fakeMessage is a minimal events.Message backed by a locally built
// envelope, mirroring app/import/worker/rescan's fakeMessage and
// app/catalog/worker/search's testMessage.
type fakeMessage struct {
	env     *events.Envelope
	attempt uint64
}

func (m *fakeMessage) Envelope() *events.Envelope { return m.env }
func (m *fakeMessage) Subject() string            { return "" }
func (m *fakeMessage) Attempt() uint64 {
	if m.attempt == 0 {
		return 1
	}
	return m.attempt
}
func (m *fakeMessage) Ack(context.Context) error                { return nil }
func (m *fakeMessage) Nak(context.Context, time.Duration) error { return nil }
func (m *fakeMessage) Term(context.Context, string) error       { return nil }
func (m *fakeMessage) InProgress(context.Context) error         { return nil }

func newImportTaskMessage(t *testing.T, ns, name, uid string) *fakeMessage {
	t.Helper()
	task := schema.ImportTask{DownloadRef: schema.Ref{Namespace: ns, Name: name, UID: uid}}
	schemaName, data, err := schema.Encode(task)
	require.NoError(t, err)
	return &fakeMessage{env: &events.Envelope{
		Schema: schemaName, Data: data, Type: "catalog.ImportTask", Key: ns + "/" + name,
	}}
}

// testQualityProfile mirrors app/catalog/worker/search's fixture of the same
// name: a minimal, cluster-scoped, valid QualityProfile.
func testQualityProfile(name string) *catalogv1alpha1.QualityProfile {
	return &catalogv1alpha1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: catalogv1alpha1.QualityProfileSpec{
			MediaKind: catalogv1alpha1.ProfileMediaKindVideo,
			Cutoff:    "hd",
			Tiers:     []catalogv1alpha1.Tier{{Name: "hd", Qualities: []string{"Bluray-1080p", "WEBDL-1080p"}}},
		},
	}
}

// fixture is one namespace's worth of catalog objects a fileimport test
// needs: a QualityProfile, a RootFolder rooted at a real (temp) directory,
// and a Movie with cached metadata.
type fixture struct {
	c          client.Client
	api        client.Reader
	bus        events.Bus
	worker     *fileimport.Worker
	ns         string
	mediaRoot  string
	movieName  string
	profile    *catalogv1alpha1.QualityProfile
	rootFolder *catalogv1alpha1.RootFolder
}

func newFixture(t *testing.T, ns string) *fixture {
	t.Helper()
	ctx := context.Background()
	c := requireEnvtest(t)
	createNamespace(t, ctx, c, ns)

	qp := testQualityProfile("qp-" + ns)
	require.NoError(t, c.Create(ctx, qp))

	mediaRoot := dataDir(t, "media")
	rf := &catalogv1alpha1.RootFolder{
		ObjectMeta: metav1.ObjectMeta{Name: "movies", Namespace: ns},
		Spec:       catalogv1alpha1.RootFolderSpec{Path: mediaRoot, Kind: catalogv1alpha1.RootFolderKindMovie},
	}
	require.NoError(t, c.Create(ctx, rf))

	movie := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "the-matrix", Namespace: ns},
		Spec: catalogv1alpha1.MovieSpec{
			TmdbID: 603, QualityProfileRef: qp.Name, RootFolderRef: rf.Name,
		},
	}
	require.NoError(t, c.Create(ctx, movie))

	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr,
		catalogac.Movie(movie.Name, ns).WithStatus(
			catalogac.MovieStatus().WithMetadata(
				catalogac.MovieMetadata().
					WithTitle("The Matrix").
					WithYear(1999).
					WithOriginalLanguage("en").
					WithExternalIDs(map[string]string{"imdb": "tt0133093"}))))
	require.NoError(t, err)

	waitFor(t, 5*time.Second, func() bool {
		var got catalogv1alpha1.Movie
		return c.Get(ctx, client.ObjectKey{Namespace: ns, Name: movie.Name}, &got) == nil && got.Status.Metadata != nil
	})
	waitFor(t, 5*time.Second, func() bool {
		var got catalogv1alpha1.RootFolder
		return c.Get(ctx, client.ObjectKey{Namespace: ns, Name: rf.Name}, &got) == nil
	})

	bus := newBus(t, ctx)
	worker := fileimport.NewWorker(c, bus)
	worker.APIReader = testAPI

	return &fixture{
		c: c, api: testAPI, bus: bus, worker: worker, ns: ns, mediaRoot: mediaRoot,
		movieName: movie.Name, profile: qp, rootFolder: rf,
	}
}

// createDownload creates a Download targeting the fixture's movie and
// patches its status (Phase/ContentRoot/CanMoveFiles) under
// k8s.ManagerGrabarr, standing in for grabarr's own engine/controller in a
// worker-only test.
func (f *fixture) createDownload(t *testing.T, name, contentRoot string, target commonv1.MediaRef) *downloadv1alpha1.Download {
	t.Helper()
	ctx := context.Background()
	magnet := "magnet:?xt=urn:btih:" + strings.Repeat("a", 40)
	dl := &downloadv1alpha1.Download{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.ns},
		Spec: downloadv1alpha1.DownloadSpec{
			Protocol: commonv1.ProtocolTorrent,
			Source:   downloadv1alpha1.DownloadSource{MagnetURL: &magnet},
			Release: commonv1.ReleaseInfo{
				GUID: "g-" + name, IndexerRef: "idx", IndexerName: "Example",
				Title: "The Matrix 1999 1080p BluRay x264-SPARKS", Protocol: commonv1.ProtocolTorrent,
				// InfoHash must be set even though it is +optional: the
				// CRD's "release identity is immutable" CEL rule compares
				// self.infoHash to oldSelf.infoHash unconditionally (no
				// has() guard), and a status-subresource apply re-evaluates
				// every spec CEL rule against the stored object. Leaving it
				// unset -- the shape a real usenet release's ReleaseInfo
				// would have, since InfoHash is torrent-only -- makes the
				// FIRST status apply on ANY such Download fail with "no
				// such key: infoHash". Discovered empirically against a
				// real envtest apiserver; flagged in this task's report as
				// a concern for api/download/v1alpha1's CEL rule, which is
				// outside D2-7's scope to fix.
				InfoHash: strings.Repeat("a", 40),
			},
			Target:            target,
			QualityProfileRef: f.profile.Name,
		},
	}
	require.NoError(t, f.c.Create(ctx, dl))

	_, err := k8s.PatchStatus(ctx, f.c, k8s.ManagerGrabarr,
		downloadac.Download(dl.Name, f.ns).WithStatus(
			downloadac.DownloadStatus().
				WithPhase(downloadv1alpha1.DownloadPhaseCompleted).
				WithContentRoot(contentRoot).
				WithCanMoveFiles(false)))
	require.NoError(t, err)

	waitFor(t, 5*time.Second, func() bool {
		var got downloadv1alpha1.Download
		return f.c.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: dl.Name}, &got) == nil &&
			got.Status.Phase == downloadv1alpha1.DownloadPhaseCompleted
	})
	return dl
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

// managerFor returns the name of the field manager that owns jsonPath (for
// example "import" inside the status subresource) on entries, or "" when
// nobody owns it. Copied from app/import/worker/rescan's identical helper:
// decoding FieldsV1 is the only place the apiserver records which manager
// owns which leaf, so an ownership-split test has to read it here rather
// than infer it from values.
func managerFor(t *testing.T, entries []metav1.ManagedFieldsEntry, subresource, jsonPath string) string {
	t.Helper()
	parts := strings.Split(jsonPath, ".")
	for _, e := range entries {
		if e.Subresource != subresource || e.FieldsV1 == nil {
			continue
		}
		var fields map[string]any
		require.NoError(t, json.Unmarshal(e.FieldsV1.GetRawBytes(), &fields))
		if ownsPath(fields, parts) {
			return e.Manager
		}
	}
	return ""
}

func ownsPath(fields map[string]any, parts []string) bool {
	if len(parts) == 0 {
		return true
	}
	next, ok := fields["f:"+parts[0]]
	if !ok {
		return false
	}
	if len(parts) == 1 {
		return true
	}
	child, ok := next.(map[string]any)
	if !ok {
		return false
	}
	return ownsPath(child, parts[1:])
}
