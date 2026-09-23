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

package rssmatcher_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/worker/rssmatcher"
	"github.com/mediactl/clustarr/catalogarr/worker/search"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// testCfg is the shared envtest control plane. It is nil when
// KUBEBUILDER_ASSETS is unset, in which case every envtest here SKIPS -- and
// a skip is not a pass.
var testCfg *rest.Config

func TestMain(m *testing.M) {
	if os.Getenv("KUBEBUILDER_ASSETS") == "" {
		os.Exit(m.Run())
	}
	env := &envtest.Environment{
		CRDDirectoryPaths:     []string{"../../../config/crd/bases"},
		ErrorIfCRDPathMissing: true,
	}
	cfg, err := env.Start()
	if err != nil {
		panic("start envtest: " + err.Error())
	}
	testCfg = cfg
	code := m.Run()
	if err := env.Stop(); err != nil {
		panic("stop envtest: " + err.Error())
	}
	os.Exit(code)
}

func requireEnvtest(t *testing.T) {
	t.Helper()
	if testCfg == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
}

// newTestManager starts a manager with this package's six field indexes AND
// the search worker's Download indexes -- the exact pair of registrations
// Task C12 must perform, and the reason a real manager is required: a field
// index is a cache feature, and client.List with MatchingFields fails outright
// against an unindexed client.
func newTestManager(t *testing.T) ctrl.Manager {
	t.Helper()
	requireEnvtest(t)

	mgr, err := ctrl.NewManager(testCfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: k8s.DisabledBindAddress},
		HealthProbeBindAddress: k8s.DisabledBindAddress,
	})
	require.NoError(t, err)
	require.NoError(t, rssmatcher.IndexFields(context.Background(), mgr.GetFieldIndexer()))
	require.NoError(t, search.RegisterDownloadIndexes(context.Background(), mgr.GetFieldIndexer()))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := mgr.Start(ctx); err != nil {
			t.Errorf("manager: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	require.True(t, mgr.GetCache().WaitForCacheSync(ctx))
	return mgr
}

var nsSeq int

func newNamespace(t *testing.T, ctx context.Context, c client.Client) string {
	t.Helper()
	nsSeq++
	ns := "rss-" + itoa(nsSeq)
	err := c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create namespace %s: %v", ns, err)
	}
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

func eventually(t *testing.T, timeout time.Duration, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, msg)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func newTestBus(t *testing.T) events.Bus {
	t.Helper()
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(context.Background(), events.Default().ForSingleNode()))
	t.Cleanup(func() { _ = bus.Close() })
	return bus
}

// createMovie makes a Movie and gives it the metadata the title+year index
// reads, through the same field manager the metadata gateway uses.
func createMovie(t *testing.T, ctx context.Context, c client.Client, ns, name string, tmdbID int64, title string, year int32) *catalogv1alpha1.Movie {
	t.Helper()
	m := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.MovieSpec{
			TmdbID: tmdbID, QualityProfileRef: "hd-bluray-web", RootFolderRef: "movies", Monitored: ptr.To(true),
		},
	}
	require.NoError(t, c.Create(ctx, m))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, catalogac.Movie(name, ns).WithStatus(
		catalogac.MovieStatus().WithAvailable(true).WithMetadata(
			catalogac.MovieMetadata().WithTitle(title).WithYear(year).WithRuntimeMinutes(109).
				// A BCP-47 TAG, which is what
				// MovieMetadata.OriginalLanguage is documented to hold and
				// what the metadata gateway really writes. It read "English"
				// while pkg/decision consumed this field as a display name;
				// storing the consumer's vocabulary in the producer's field
				// is what made the fixture pass where production could not.
				WithOriginalLanguage("en").
				WithStatus(catalogv1alpha1.MovieReleaseStatusReleased).WithRefreshedAt(metav1.Now()),
		)))
	require.NoError(t, err)
	return m
}

func createSeries(t *testing.T, ctx context.Context, c client.Client, ns, name string, tvdbID int64, title string, year int32) *catalogv1alpha1.Series {
	t.Helper()
	s := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.SeriesSpec{
			TvdbID: tvdbID, QualityProfileRef: "hd-bluray-web", RootFolderRef: "tv", Monitored: ptr.To(true),
		},
	}
	require.NoError(t, c.Create(ctx, s))
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrMetadata, catalogac.Series(name, ns).WithStatus(
		catalogac.SeriesStatus().WithMetadata(
			catalogac.SeriesMetadata().WithTitle(title).WithYear(year).WithRuntimeMinutes(60).
				WithRefreshedAt(metav1.Now()),
		)))
	require.NoError(t, err)
	return s
}

func createEpisode(t *testing.T, ctx context.Context, c client.Client, ns, seriesName string, season, number int32, airDate *time.Time) string {
	t.Helper()
	name := seriesName + "-s" + pad(season) + "e" + pad(number)
	ep := &catalogv1alpha1.Episode{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: catalogv1alpha1.EpisodeSpec{
			SeriesRef: seriesName, SeasonNumber: season, EpisodeNumber: number, Monitored: ptr.To(true),
		},
	}
	require.NoError(t, c.Create(ctx, ep))
	if airDate != nil {
		_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarrSeries, catalogac.Episode(name, ns).WithStatus(
			catalogac.EpisodeStatus().WithAirDate(metav1.NewTime(*airDate))))
		require.NoError(t, err)
	}
	return name
}

func pad(n int32) string {
	s := itoa(int(n))
	if len(s) < 2 {
		return "0" + s
	}
	return s
}

// createQualityProfile installs an hd-bluray-web profile as a real CRD, so
// the handler resolves it the way production does rather than from a fixture.
//
// QualityProfile is CLUSTER-scoped (qualityprofile_types.go's
// +kubebuilder:resource:scope=Cluster), so one object serves every test in
// this package and a second create is AlreadyExists, not a fresh object in
// another namespace.
func createQualityProfile(t *testing.T, ctx context.Context, c client.Client) {
	t.Helper()
	qp := &catalogv1alpha1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "hd-bluray-web"},
		Spec: catalogv1alpha1.QualityProfileSpec{
			MediaKind:      catalogv1alpha1.ProfileMediaKindVideo,
			UpgradeAllowed: ptr.To(true),
			Cutoff:         "Bluray-1080p",
			// language is deliberately LEFT OUT so the apiserver defaults it
			// to "original", which is what a user who never touched the field
			// gets. It used to be pinned to "any" to dodge the vocabulary
			// defect (catalogarr handed pkg/decision a BCP-47 tag while both
			// of its consumers spoke Radarr display names, so "original"
			// rejected everything and language-not-original scored -10000 on
			// top); pkg/decision/language.go converts at the boundary now, and
			// this suite is more useful holding the default than opting out of
			// it.
			Tiers: []catalogv1alpha1.Tier{
				{Name: "Bluray-1080p", Qualities: []string{"Bluray-1080p"}},
				{Name: "WEB 1080p", Qualities: []string{"WEBDL-1080p", "WEBRip-1080p"}},
				{Name: "Bluray-720p", Qualities: []string{"Bluray-720p"}},
				{Name: "WEB 720p", Qualities: []string{"WEBDL-720p", "WEBRip-720p"}},
			},
		},
	}
	if err := c.Create(ctx, qp); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create quality profile: %v", err)
	}
}

func createIndexer(t *testing.T, ctx context.Context, c client.Client, ns, name string) {
	t.Helper()
	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL: "https://example.invalid",
			Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1.ProtocolTorrent},
		},
	}
	require.NoError(t, c.Create(ctx, idx))
}

func createDelayProfile(t *testing.T, ctx context.Context, c client.Client, ns string, torrentMinutes int32, bypassTopTier bool) {
	t.Helper()
	dp := &catalogv1alpha1.DelayProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: ns},
		Spec: catalogv1alpha1.DelayProfileSpec{
			TorrentDelayMinutes:    torrentMinutes,
			BypassIfHighestQuality: ptr.To(bypassTopTier),
			Order:                  1000,
		},
	}
	require.NoError(t, c.Create(ctx, dp))
}

func metaTime(t time.Time) *metav1.Time { return &metav1.Time{Time: t} }
