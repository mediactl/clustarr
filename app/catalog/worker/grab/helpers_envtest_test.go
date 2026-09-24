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
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/grab/downloads"
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
	sharedCfg     *rest.Config
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
			CRDDirectoryPaths:     []string{"../../../../config/crd/bases"},
			ErrorIfCRDPathMissing: true,
		}
		cfg, err := env.Start()
		if err != nil {
			sharedEnvErr = err
			return
		}
		sharedEnvStop = func() { _ = env.Stop() }
		sharedCfg = cfg
		sharedClient, sharedEnvErr = client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	})
	require.NoError(t, sharedEnvErr)
	return sharedClient
}

// newWatchClient is a second client onto the shared apiserver, of the type
// interceptor.NewClient wraps, for tests that interleave a real writer into
// the middle of a read-modify-write.
func newWatchClient(t *testing.T) client.WithWatch {
	t.Helper()
	newTestClient(t)
	wc, err := client.NewWithWatch(sharedCfg, client.Options{Scheme: k8s.MustNewScheme()})
	require.NoError(t, err)
	return wc
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
		// A realistic publish date: it is what the decision engine's age
		// ranking reads. It is optional on the wire (ReleaseInfo.PublishedAt
		// is a *metav1.Time), so a dateless release persists fine -- this
		// fixture simply is not one.
		PublishedAt: ptr.To(metav1.NewTime(testNow.Add(-time.Hour))),
		MagnetURL:   "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
		Title:       "The.Thing.1982.1080p.BluRay.x264-GROUP",
		Quality:     q,
		FormatScore: score,
	}
}

func fixedNow(t time.Time) func() time.Time { return func() time.Time { return t } }

// seedWorkerStatus drives a Movie to a realistic steady state. It matters: a
// test that creates a blank object cannot observe a server-side-apply
// release, because there is nothing on the object to release.
//
// pendingGrab is written under the grab path's own manager
// (k8s.ManagerCatalogarrGrab), as Decide writes it. The reconciler's share is
// written under k8s.ManagerCatalogarr, as it is in production before any
// search can happen: status.phase (Delayed while a grab is pending, Wanted
// otherwise) and, when non-empty, activeDownloadRef -- since gap-fix ruling
// R-5 the reconciler is that field's only writer. The phase is not decoration:
// a status whose only fields are the grab path's becomes empty when the grab
// clears them, and the Movie CRD rejects an empty status outright, which no
// real object -- one its reconciler has written -- ever hits.
func seedWorkerStatus(t *testing.T, ctx context.Context, c client.Client, m *catalogv1alpha1.Movie, activeDownloadRef string, pg *catalogv1alpha1.PendingGrab) {
	t.Helper()
	if pg != nil {
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrGrab, catalogac.Movie(m.Name, m.Namespace).WithStatus(
			catalogac.MovieStatus().WithPendingGrab(catalogac.PendingGrab().
				WithReleaseTitle(pg.ReleaseTitle).
				WithProtocol(pg.Protocol).
				WithGrabAt(pg.GrabAt))))
		require.NoError(t, err)
	}
	reconciler := catalogac.MovieStatus().WithPhase(catalogv1alpha1.MoviePhaseWanted)
	if pg != nil {
		reconciler = reconciler.WithPhase(catalogv1alpha1.MoviePhaseDelayed)
	}
	if activeDownloadRef != "" {
		reconciler = reconciler.WithActiveDownloadRef(activeDownloadRef)
	}
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr, catalogac.Movie(m.Name, m.Namespace).WithStatus(reconciler))
	require.NoError(t, err)
}

// interactiveDownload applies a Download for rel exactly the way the Search
// controller's spec.grab does (app/catalog/controller/search handleGrabs):
// the deterministic name, the target as owner, grabbedBy=interactive,
// manual=true, under k8s.ManagerCatalogarr. src is passed in rather than
// resolved so a test can reproduce a Download whose source another build
// mapped differently.
func interactiveDownload(t *testing.T, ctx context.Context, c client.Client, owner client.Object, target commonv1.MediaRef, rel commonv1.ReleaseInfo, src downloadv1alpha1.DownloadSource) *downloadv1alpha1.Download {
	t.Helper()
	ownerRef, err := k8s.OwnerReferenceAC(owner, c.Scheme())
	require.NoError(t, err)
	name := k8s.ChildName(target.Name, rel.GUID)
	_, err = k8s.Apply(ctx, c, k8s.ManagerCatalogarr, downloadac.Download(name, owner.GetNamespace()).
		WithOwnerReferences(ownerRef).
		WithSpec(downloadac.DownloadSpec().
			WithProtocol(rel.Protocol).
			WithSource(downloads.SourceApplyConfiguration(src)).
			WithRelease(rel).
			WithTarget(target).
			WithQualityProfileRef("hd-bluray-web").
			WithGrabbedBy(downloadv1alpha1.GrabSourceInteractive).
			WithManual(true)))
	require.NoError(t, err)
	var dl downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: owner.GetNamespace(), Name: name}, &dl))
	return &dl
}

// setDownloadPhase writes status.phase the way grabarr's controller does,
// under k8s.ManagerGrabarr.
func setDownloadPhase(t *testing.T, ctx context.Context, c client.Client, ns, name string, phase downloadv1alpha1.DownloadPhase) {
	t.Helper()
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerGrabarr,
		downloadac.Download(name, ns).WithStatus(downloadac.DownloadStatus().WithPhase(phase)))
	require.NoError(t, err)
}

// managerStatusFields returns the raw fieldsV1 of fm's status entry on obj,
// or "" when fm owns nothing there. An over-claim is invisible to every
// value assertion -- pkg/k8s forces ownership -- so this is where a test that
// means to see one has to look.
func managerStatusFields(obj client.Object, fm k8s.FieldManager) string {
	for _, e := range obj.GetManagedFields() {
		if e.Manager == fm.String() && e.Subresource == "status" && e.FieldsV1 != nil {
			return e.FieldsV1.GetRawString()
		}
	}
	return ""
}

// seedGatewayMetadata writes status.metadata exactly as app/catalog/metadata's
// handler does: MovieStatus().WithMetadata(...) and nothing else, under
// k8s.ManagerCatalogarrMetadata.
func seedGatewayMetadata(t *testing.T, ctx context.Context, c client.Client, m *catalogv1alpha1.Movie) {
	t.Helper()
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata,
		catalogac.Movie(m.Name, m.Namespace).WithStatus(catalogac.MovieStatus().WithMetadata(
			catalogac.MovieMetadata().
				WithTitle("The Thing").
				WithYear(1982).
				WithRuntimeMinutes(109).
				WithStatus(catalogv1alpha1.MovieReleaseStatusReleased).
				WithRefreshedAt(metav1.NewTime(testNow)),
		)))
	require.NoError(t, err)
}
