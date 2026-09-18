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

package grab_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"k8s.io/utils/ptr"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// sharedEnv keeps ONE apiserver for the whole package: envtest takes seconds
// to start, and every test here wants the same CRDs. Each test gets its own
// namespace instead of its own apiserver.
var (
	sharedEnvOnce sync.Once
	sharedClient  client.Client
	sharedEnvErr  error
	sharedEnvStop func()
)

// newTestClient mirrors pkg/k8s/patch_envtest_test.go's helper: a real
// apiserver with the real CRDs, skipped when KUBEBUILDER_ASSETS is unset.
func newTestClient(t *testing.T) client.Client {
	t.Helper()
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	sharedEnvOnce.Do(func() {
		env := &envtest.Environment{
			CRDDirectoryPaths:     []string{"../../../config/crd/bases"},
			ErrorIfCRDPathMissing: true,
		}
		cfg, err := env.Start()
		if err != nil {
			sharedEnvErr = err
			return
		}
		sharedEnvStop = func() { _ = env.Stop() }
		sharedClient, sharedEnvErr = client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	})
	require.NoError(t, sharedEnvErr)
	return sharedClient
}

func TestMain(m *testing.M) {
	code := m.Run()
	if sharedEnvStop != nil {
		sharedEnvStop()
	}
	os.Exit(code)
}

// nsCounter gives every test its own namespace on the shared apiserver, so a
// List in one test never sees another's Downloads.
var nsCounter atomic64

// clockworkClock keeps the helper signature free of a direct clockwork
// import in files that pass nil.
type clockworkClock = clockwork.Clock

type atomic64 struct {
	mu sync.Mutex
	n  int
}

func (a *atomic64) next() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.n++
	return a.n
}

func newNamespace(t *testing.T, ctx context.Context, c client.Client) string {
	t.Helper()
	ns := "media-" + itoa(nsCounter.next())
	err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	require.NoError(t, client.IgnoreAlreadyExists(err))
	return ns
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

// newTestBus brings up the whole default topology single-node: Topology
// validation requires the DLQ stream, and the bucket TTLs and consumer tuning
// should be production's, not a test's invention.
func newTestBus(t *testing.T, clock clockworkClock) events.Bus {
	t.Helper()
	bus := membus.New(clock)
	require.NoError(t, bus.Ensure(context.Background(), events.Default().ForSingleNode()))
	t.Cleanup(func() { _ = bus.Close() })
	return bus
}

func hdBlurayWeb(t *testing.T) quality.Profile {
	t.Helper()
	profiles, errs := quality.BuiltinProfiles(catalogue.LoadedCatalogue())
	require.Empty(t, errs)
	p, ok := profiles["hd-bluray-web"]
	require.True(t, ok)
	require.GreaterOrEqual(t, len(p.Tiers), 3)
	return p
}

func newMovie(t *testing.T, ctx context.Context, c client.Client, ns, name string) *catalogv1alpha1.Movie {
	t.Helper()
	m := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.MovieSpec{
			TmdbID: 1, QualityProfileRef: "hd-bluray-web", RootFolderRef: "movies",
		},
	}
	require.NoError(t, c.Create(ctx, m))
	return m
}

func newIndexer(t *testing.T, ctx context.Context, c client.Client, ns, name string, secret *corev1.LocalObjectReference) *indexv1alpha1.Indexer {
	t.Helper()
	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL:   "https://example.invalid",
			SecretRef: secret,
			// IndexerSpec's CEL rule requires exactly one of definition,
			// definitionRef or generic.
			Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1.ProtocolTorrent},
		},
	}
	require.NoError(t, c.Create(ctx, idx))
	return idx
}

func torrentRelease(guid, indexerRef string, q commonv1.Quality, score int32) commonv1.ReleaseInfo {
	return commonv1.ReleaseInfo{
		GUID:        guid,
		IndexerRef:  indexerRef,
		IndexerName: indexerRef,
		Protocol:    commonv1.ProtocolTorrent,
		// PublishedAt must be non-zero: ReleaseInfo.PublishedAt is a
		// metav1.Time value (not a pointer) whose `omitempty` cannot fire on
		// a struct, so a zero value marshals to `null` and the Download CRD's
		// schema rejects it. See the task report's finding on this.
		PublishedAt: ptr.To(metav1.NewTime(testNow.Add(-time.Hour))),
		MagnetURL:   "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
		Title:       "The.Thing.1982.1080p.BluRay.x264-GROUP",
		Quality:     q,
		FormatScore: score,
	}
}

func fixedNow(t time.Time) func() time.Time { return func() time.Time { return t } }

// seedWorkerStatus drives a Movie to a realistic steady state under the SAME
// field manager the code under test writes with. It matters: a test that
// creates a blank object cannot observe a server-side-apply release, because
// there is nothing on the object to release.
func seedWorkerStatus(t *testing.T, ctx context.Context, c client.Client, m *catalogv1alpha1.Movie, activeDownloadRef string, pg *catalogv1alpha1.PendingGrab) {
	t.Helper()
	status := catalogac.MovieStatus().WithActiveDownloadRef(activeDownloadRef)
	if pg != nil {
		status = status.WithPendingGrab(catalogac.PendingGrab().
			WithReleaseTitle(pg.ReleaseTitle).
			WithProtocol(pg.Protocol).
			WithGrabAt(pg.GrabAt))
	}
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrWorker, catalogac.Movie(m.Name, m.Namespace).WithStatus(status))
	require.NoError(t, err)
}
