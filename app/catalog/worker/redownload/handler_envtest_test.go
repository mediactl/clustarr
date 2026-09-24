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

package redownload_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/catalog/v1alpha1"
	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/grab"
	"github.com/mediactl/clustarr/app/catalog/worker/grab/downloads"
	"github.com/mediactl/clustarr/app/catalog/worker/redownload"
	"github.com/mediactl/clustarr/app/catalog/worker/search"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
)

// testCfg is the shared envtest control plane, nil when KUBEBUILDER_ASSETS is
// unset -- in which case every envtest here skips, and a skip is not a pass.
var testCfg *rest.Config

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
		panic("start envtest: " + err.Error())
	}
	testCfg = cfg
	code := m.Run()
	if err := env.Stop(); err != nil {
		panic("stop envtest: " + err.Error())
	}
	os.Exit(code)
}

// testMessage is the events.Message Handle reads: the envelope and the
// delivery count (zero reads as the first delivery).
type testMessage struct {
	env     *events.Envelope
	attempt uint64
}

func (m testMessage) Envelope() *events.Envelope { return m.env }
func (m testMessage) Subject() string            { return "" }
func (m testMessage) Attempt() uint64 {
	if m.attempt == 0 {
		return 1
	}
	return m.attempt
}
func (m testMessage) Ack(context.Context) error                { return nil }
func (m testMessage) Nak(context.Context, time.Duration) error { return nil }
func (m testMessage) Term(context.Context, string) error       { return nil }
func (m testMessage) InProgress(context.Context) error         { return nil }

// published is one publish the recording bus passed through.
type published struct {
	subject string
	env     *events.Envelope
	receipt events.Receipt
}

// recordingBus is a real in-memory bus -- real KV, real stream
// deduplication -- that also remembers every publish and its receipt.
type recordingBus struct {
	events.Bus
	mu  sync.Mutex
	log []published
}

func (b *recordingBus) Publish(ctx context.Context, subject string, e *events.Envelope, opts ...events.PublishOption) (events.Receipt, error) {
	r, err := b.Bus.Publish(ctx, subject, e, opts...)
	if err == nil {
		b.mu.Lock()
		b.log = append(b.log, published{subject: subject, env: e, receipt: r})
		b.mu.Unlock()
	}
	return r, err
}

// searches returns the SearchTasks published so far that the stream did not
// deduplicate.
func (b *recordingBus) searches(t *testing.T) []published {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []published
	for _, p := range b.log {
		if p.env.Schema == (schema.SearchTask{}).Schema() && !p.receipt.Duplicate {
			out = append(out, p)
		}
	}
	return out
}

type fixture struct {
	ctx     context.Context
	c       client.Client // the manager's cached client, as run.go hands the handler
	api     client.Reader
	bus     *recordingBus
	handler *redownload.Handler
	ns      string
}

// newFixture starts a manager with the search worker's Download indexes (the
// full-flow test drives the real search worker, whose snapshot lists by
// them), a namespace and an in-memory bus with the production topology.
func newFixture(t *testing.T, ns string) *fixture {
	t.Helper()
	if testCfg == nil {
		t.Skip("KUBEBUILDER_ASSETS is unset; run via `make test`")
	}
	ctx := context.Background()
	mgr, err := ctrl.NewManager(testCfg, ctrl.Options{
		Scheme:                 k8s.MustNewScheme(),
		Metrics:                metricsserver.Options{BindAddress: k8s.DisabledBindAddress},
		HealthProbeBindAddress: k8s.DisabledBindAddress,
	})
	require.NoError(t, err)
	require.NoError(t, search.RegisterDownloadIndexes(ctx, mgr.GetFieldIndexer()))
	mctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := mgr.Start(mctx); err != nil {
			t.Errorf("manager: %v", err)
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
	require.True(t, mgr.GetCache().WaitForCacheSync(mctx))

	c := mgr.GetClient()
	require.NoError(t, c.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}}))

	mem := membus.New(nil)
	require.NoError(t, mem.Ensure(ctx, events.Default().ForSingleNode()))
	t.Cleanup(func() { _ = mem.Close() })
	bus := &recordingBus{Bus: mem}

	topo := events.Default().ForSingleNode()
	h := redownload.NewHandler(c, bus)
	h.Topology = &topo
	return &fixture{ctx: ctx, c: c, api: mgr.GetAPIReader(), bus: bus, handler: h, ns: ns}
}

// eventually polls cond until it holds or the deadline passes.
func eventually(t *testing.T, msg string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", msg)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// waitCached blocks until the cached client sees obj.
func (f *fixture) waitCached(t *testing.T, obj client.Object) {
	t.Helper()
	key := client.ObjectKeyFromObject(obj)
	eventually(t, "the cache to see "+key.String(), func() bool {
		return f.c.Get(f.ctx, key, obj.DeepCopyObject().(client.Object)) == nil
	})
}

// newMovie creates a monitored Movie in its steady state: a phase and an
// activeDownloadRef under the reconciler's manager, and a search history
// under the grab path's, as a Movie whose Download just failed carries them.
func (f *fixture) newMovie(t *testing.T, name, profile string, monitored bool, activeDownload string) *catalogv1alpha1.Movie {
	t.Helper()
	m := &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: f.ns},
		Spec: catalogv1alpha1.MovieSpec{
			TmdbID: 603, QualityProfileRef: profile, RootFolderRef: "movies", Monitored: ptr.To(monitored),
		},
	}
	require.NoError(t, f.c.Create(f.ctx, m))
	_, err := k8s.PatchStatus(f.ctx, f.c, k8s.ManagerCatalogarr, catalogac.Movie(name, f.ns).WithStatus(
		catalogac.MovieStatus().WithPhase(catalogv1alpha1.MoviePhaseDownloading).WithActiveDownloadRef(activeDownload)))
	require.NoError(t, err)
	_, err = k8s.PatchStatus(f.ctx, f.c, k8s.ManagerCatalogarrGrab, catalogac.Movie(name, f.ns).WithStatus(
		catalogac.MovieStatus().
			WithLastSearchedAt(metav1.NewTime(time.Now().Add(-time.Hour).Truncate(time.Second))).
			WithSearchAttempts(commonv1.Attempts{Count: 1})))
	require.NoError(t, err)
	f.waitCached(t, m)
	return m
}

// failedDownload applies a Download the grab path made for target, then
// drives its status to where grabarr leaves a failed one: phase, reason and,
// when blocklisted, the label, Blocklisted and blocklistedUntil.
func (f *fixture) failedDownload(t *testing.T, target commonv1.MediaRef, guid string,
	reason downloadv1alpha1.DownloadFailureReason, blocklisted bool,
) *downloadv1alpha1.Download {
	t.Helper()
	rel := commonv1.ReleaseInfo{
		GUID: guid, IndexerRef: "idx", IndexerName: "idx", Title: "Some.Release.1080p.BluRay-GRP",
		Protocol:  commonv1.ProtocolTorrent,
		MagnetURL: "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567",
		InfoHash:  "0123456789abcdef0123456789abcdef01234567",
	}
	src, err := downloads.ResolveSource(rel)
	require.NoError(t, err)
	name := k8s.ChildName(target.Name, guid)
	dl := downloadac.Download(name, f.ns).WithSpec(downloadac.DownloadSpec().
		WithProtocol(rel.Protocol).
		WithSource(downloads.SourceApplyConfiguration(src)).
		WithRelease(rel).
		WithTarget(target).
		WithGrabbedBy(downloadv1alpha1.GrabSourceSearch))
	if blocklisted {
		dl = dl.WithLabels(map[string]string{downloadv1alpha1.LabelBlocklisted: downloadv1alpha1.LabelBlocklistedValue})
	}
	_, err = k8s.Apply(f.ctx, f.c, k8s.ManagerCatalogarrGrab, dl)
	require.NoError(t, err)

	st := downloadac.DownloadStatus().WithPhase(downloadv1alpha1.DownloadPhaseFailed).WithFailureReason(reason)
	if blocklisted {
		st = downloadac.DownloadStatus().WithPhase(downloadv1alpha1.DownloadPhaseBlocklisted).
			WithBlocklistedUntil(metav1.NewTime(time.Now().Add(90 * 24 * time.Hour).Truncate(time.Second)))
	}
	_, err = k8s.PatchStatus(f.ctx, f.c, k8s.ManagerGrabarr, downloadac.Download(name, f.ns).WithStatus(st))
	require.NoError(t, err)

	// The object the test goes on to use is read through the API reader: the
	// writes above returned, so the apiserver has it, but the cache may not
	// have seen even the create yet (a cached Get here once read NotFound).
	// The handler reads through the cache, so the test then waits for that.
	var got downloadv1alpha1.Download
	require.NoError(t, f.api.Get(f.ctx, client.ObjectKey{Namespace: f.ns, Name: name}, &got))
	eventually(t, "the cache to see "+name+"'s status", func() bool {
		var c downloadv1alpha1.Download
		return f.c.Get(f.ctx, client.ObjectKeyFromObject(&got), &c) == nil && c.Status.Phase != ""
	})
	return &got
}

// holdLease writes the clustarr-leases key for item as the grab path leaves
// it: valued with the name of the Download that took it.
func (f *fixture) holdLease(t *testing.T, item commonv1.MediaRef, holder string) string {
	t.Helper()
	key := events.LeaseKey(grab.MediaKey(f.ns, item))
	_, err := f.bus.KV(events.BucketLeases).Create(f.ctx, key, []byte(holder))
	require.NoError(t, err)
	return key
}

func (f *fixture) leaseHolder(t *testing.T, key string) string {
	t.Helper()
	e, err := f.bus.KV(events.BucketLeases).Get(f.ctx, key)
	if errors.Is(err, events.ErrKeyNotFound) {
		return ""
	}
	require.NoError(t, err)
	return string(e.Value)
}

// event is the envelope grabarr publishes for dl's action, built the way
// app/grab/controller/download's publishDownloadEvent builds it.
func (f *fixture) event(t *testing.T, dl *downloadv1alpha1.Download, action string,
	reason downloadv1alpha1.DownloadFailureReason, at time.Time,
) *events.Envelope {
	t.Helper()
	evt := schema.DownloadEvent{
		DownloadRef: schema.Ref{Namespace: dl.Namespace, Name: dl.Name, UID: string(dl.UID)},
		Media:       dl.Spec.Target,
		Action:      action,
		Protocol:    dl.Spec.Protocol,
		Title:       dl.Spec.Release.Title,
		InfoHash:    dl.Spec.Release.InfoHash,
		Reason:      string(reason),
		At:          at,
	}
	name, data, err := schema.Encode(evt)
	require.NoError(t, err)
	return &events.Envelope{
		ID: string(dl.UID) + ":" + action, Type: "download.DownloadEvent", Schema: name,
		Source: "grabarr-controller@test", Key: dl.Namespace + "/" + dl.Name, Time: at, Data: data,
	}
}

func approveEverything(_ context.Context, _ decision.Target, _ quality.Profile, _ *catalogue.Catalogue,
	rels []commonv1.ReleaseInfo, _ decision.Options,
) []decision.Decision {
	out := make([]decision.Decision, 0, len(rels))
	for _, rel := range rels {
		out = append(out, decision.Decision{Release: rel, Approved: true, Score: int(rel.FormatScore)})
	}
	return out
}

// TestAFailedDownloadIsRedownloadedAndTheGrabSaysSo is spec §8.3 end to end
// on the catalogarr side, against objects in their steady state: grabarr's
// failed event for a blocklisted Download frees the Movie's lease and
// publishes one redownload search; the REAL search worker, handed that
// message, ranks a new release and hands it to the REAL grab sink; and the
// Download that grab creates records grabbedBy=redownload -- the enum value
// DownloadSpec had with no producer. The search-history fields the Movie
// already carried survive (the search worker's RecordSearchAttempt bumps
// them; nothing here writes anyone else's).
func TestAFailedDownloadIsRedownloadedAndTheGrabSaysSo(t *testing.T) {
	f := newFixture(t, "redownload-flow")
	ctx := f.ctx

	qp := &catalogv1alpha1.QualityProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "redownload-flow-hd"},
		Spec: catalogv1alpha1.QualityProfileSpec{
			MediaKind: catalogv1alpha1.ProfileMediaKindVideo,
			Cutoff:    "hd",
			Tiers:     []catalogv1alpha1.Tier{{Name: "hd", Qualities: []string{"Bluray-1080p", "WEBDL-1080p"}}},
		},
	}
	require.NoError(t, f.c.Create(ctx, qp))
	f.waitCached(t, qp)
	dc := &downloadv1alpha1.DownloadClient{
		ObjectMeta: metav1.ObjectMeta{Name: "torrents", Namespace: f.ns},
		Spec: downloadv1alpha1.DownloadClientSpec{
			Protocol: commonv1.ProtocolTorrent,
			Torrent:  &downloadv1alpha1.TorrentSpec{EnableDHT: ptr.To(false)},
		},
	}
	require.NoError(t, f.c.Create(ctx, dc))
	f.waitCached(t, dc)

	movie := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"}
	failed := f.failedDownload(t, movie, "g-failed", downloadv1alpha1.DownloadFailureMissingArticles, true)
	f.newMovie(t, movie.Name, qp.Name, true, failed.Name)
	lease := f.holdLease(t, movie, failed.Name)

	// 1. grabarr's failed event.
	env := f.event(t, failed, events.ActionFailed, downloadv1alpha1.DownloadFailureMissingArticles, time.Now())
	require.NoError(t, f.handler.Handle(ctx, testMessage{env: env}))

	assert.Empty(t, f.leaseHolder(t, lease), "the failed Download's lease is freed")
	searches := f.bus.searches(t)
	require.Len(t, searches, 1, "one redownload search")
	s := searches[0]
	assert.Equal(t, events.WorkSearchSubject(events.PriorityNormal, grab.MediaKey(f.ns, movie)), s.subject,
		"§8.2: a redownload searches on the normal lane, keyed by the item's media key")
	assert.Equal(t, redownload.MsgID(schema.Ref{Namespace: f.ns, Name: failed.Name, UID: string(failed.UID)}, movie), s.env.ID)
	assert.Equal(t, f.ns+"/"+movie.Name, s.env.Key)
	var task schema.SearchTask
	require.NoError(t, schema.Decode(s.env.Schema, s.env.Data, &task))
	assert.Equal(t, schema.SearchReasonRedownload, task.Reason)
	assert.Equal(t, movie, task.MediaRef)
	assert.False(t, task.UserInvoked)

	// A redelivery of the failure, and the blocklisted event that follows
	// it, publish nothing new: same Msg-Id, deduplicated by the stream.
	require.NoError(t, f.handler.Handle(ctx, testMessage{env: env, attempt: 2}))
	require.NoError(t, f.handler.Handle(ctx, testMessage{
		env: f.event(t, failed, events.ActionBlocklisted, "", time.Now()),
	}))
	assert.Len(t, f.bus.searches(t), 1, "a redelivered failure never searches twice")

	// 2. The real search worker takes the published task.
	rpc := &search.FakeSearchRPC{Response: schema.SearchResponse{
		Releases: []schema.Release{{
			Info: commonv1.ReleaseInfo{
				GUID: "g-new", IndexerRef: "idx", IndexerName: "idx", Title: "The.Matrix.1999.1080p.BluRay.x264-NEW",
				Protocol:  commonv1.ProtocolTorrent,
				MagnetURL: "magnet:?xt=urn:btih:89abcdef0123456789abcdef0123456789abcdef",
			},
			ParsedTitle: "The.Matrix.1999.1080p.BluRay.x264-NEW",
			FetchedAt:   time.Now(),
		}},
		Outcomes: []schema.SearchOutcome{{
			IndexerRef: schema.Ref{Namespace: f.ns, Name: "idx"}, IndexerName: "idx",
			Status: schema.SearchOutcomeOK, Releases: 1,
		}},
	}}
	cat := catalogue.LoadedCatalogue()
	w := search.NewWorker(f.c, rpc, cat)
	w.Evaluate = approveEverything
	w.Sink = grab.Sink{
		Deps: grab.Deps{Client: f.c, Reader: f.api, Bus: f.bus},
		ResolveProfile: func(ctx context.Context, name string) (quality.Profile, error) {
			var p catalogv1alpha1.QualityProfile
			if err := f.c.Get(ctx, client.ObjectKey{Name: name}, &p); err != nil {
				return quality.Profile{}, err
			}
			prof, errs := quality.FromCRD(&p, cat)
			return prof, errors.Join(errs...)
		},
		ResolveDelay: func(context.Context, string, *string, []string) (catalogv1alpha1.DelayProfileSpec, error) {
			return catalogv1alpha1.DelayProfileSpec{}, nil
		},
	}
	require.NoError(t, w.Handle(ctx, testMessage{env: s.env}))

	// 3. The grab that followed records why it happened.
	var list downloadv1alpha1.DownloadList
	require.NoError(t, f.api.List(ctx, &list, client.InNamespace(f.ns)))
	var grabbed *downloadv1alpha1.Download
	for i := range list.Items {
		if list.Items[i].Spec.Release.GUID == "g-new" {
			grabbed = &list.Items[i]
		}
	}
	require.NotNil(t, grabbed, "the redownload search grabbed the new release; downloads: %d", len(list.Items))
	assert.Equal(t, downloadv1alpha1.GrabSourceRedownload, grabbed.Spec.GrabbedBy,
		"the grab a redownload search leads to records grabbedBy=redownload")
	assert.Equal(t, grabbed.Name, f.leaseHolder(t, lease), "the new grab took the freed lease")

	var m catalogv1alpha1.Movie
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: movie.Name}, &m))
	assert.Equal(t, catalogv1alpha1.MoviePhaseDownloading, m.Status.Phase, "the reconciler's phase is not this path's to touch")
	assert.Equal(t, failed.Name, ptr.Deref(m.Status.ActiveDownloadRef, ""),
		"activeDownloadRef is the reconciler's alone (R-5); nothing on this path writes it")
	assert.Equal(t, int32(2), m.Status.SearchAttempts.Count, "the redownload search is counted, on top of the first")
	assert.NotNil(t, m.Status.LastSearchedAt)
}

// TestALocalFaultFreesTheLeaseButDoesNotSearch is the Radarr ruling: a full
// disk or a failed write is not the release's fault, so no search.
func TestALocalFaultFreesTheLeaseButDoesNotSearch(t *testing.T) {
	for _, reason := range []downloadv1alpha1.DownloadFailureReason{
		downloadv1alpha1.DownloadFailureDiskFull, downloadv1alpha1.DownloadFailureWriteError,
	} {
		t.Run(string(reason), func(t *testing.T) {
			f := newFixture(t, "redownload-local-"+strings.ToLower(string(reason)))
			movie := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"}
			failed := f.failedDownload(t, movie, "g-failed", reason, false)
			f.newMovie(t, movie.Name, "hd-bluray-web", true, failed.Name)
			lease := f.holdLease(t, movie, failed.Name)

			require.NoError(t, f.handler.Handle(f.ctx, testMessage{
				env: f.event(t, failed, events.ActionFailed, reason, time.Now()),
			}))
			assert.Empty(t, f.leaseHolder(t, lease), "the lease is freed either way")
			assert.Empty(t, f.bus.searches(t), "a local fault is not searched again")
		})
	}
}

// TestAFailureWaitsForGrabarrsBlocklist: a failed event that arrives before
// grabarr has labelled the Download is retried rather than searched, so the
// search cannot rank the failed release again -- and after the settle
// attempts it searches anyway.
func TestAFailureWaitsForGrabarrsBlocklist(t *testing.T) {
	f := newFixture(t, "redownload-settle")
	movie := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"}
	failed := f.failedDownload(t, movie, "g-failed", downloadv1alpha1.DownloadFailureStalled, false)
	f.newMovie(t, movie.Name, "hd-bluray-web", true, failed.Name)
	lease := f.holdLease(t, movie, failed.Name)
	env := f.event(t, failed, events.ActionFailed, downloadv1alpha1.DownloadFailureStalled, time.Now())

	err := f.handler.Handle(f.ctx, testMessage{env: env, attempt: 1})
	var retry *events.RetryError
	require.ErrorAs(t, err, &retry, "not yet blocklisted: wait")
	assert.Empty(t, f.leaseHolder(t, lease), "the lease is freed before the wait")
	assert.Empty(t, f.bus.searches(t))

	require.ErrorAs(t, f.handler.Handle(f.ctx, testMessage{env: env, attempt: 2}), &retry)
	assert.Empty(t, f.bus.searches(t))

	require.NoError(t, f.handler.Handle(f.ctx, testMessage{env: env, attempt: 3}),
		"a blocklist that never comes does not cost the redownload")
	assert.Len(t, f.bus.searches(t), 1)
}

// TestAPackFailureSearchesEachMonitoredEpisode is Sonarr's multi-episode
// branch: every episode lease the pack held is freed, and each monitored
// episode is searched on its own; an unmonitored one and a deleted one are
// not.
func TestAPackFailureSearchesEachMonitoredEpisode(t *testing.T) {
	f := newFixture(t, "redownload-pack")
	ctx := f.ctx
	series := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "the-wire", Namespace: f.ns},
		Spec:       catalogv1alpha1.SeriesSpec{TvdbID: 79126, QualityProfileRef: "hd-bluray-web", RootFolderRef: "tv"},
	}
	require.NoError(t, f.c.Create(ctx, series))
	for n, monitored := range map[int32]bool{1: true, 2: false} {
		ep := &catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: episodeName(n), Namespace: f.ns},
			Spec: catalogv1alpha1.EpisodeSpec{
				SeriesRef: series.Name, SeasonNumber: 1, EpisodeNumber: n, Monitored: ptr.To(monitored),
			},
		}
		require.NoError(t, f.c.Create(ctx, ep))
		f.waitCached(t, ep)
	}
	// Episode 3 is in the pack but no longer exists.
	keys := []string{episodeName(1), episodeName(2), episodeName(3)}
	pack := commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: series.Name, Keys: keys}
	failed := f.failedDownload(t, pack, "g-pack", downloadv1alpha1.DownloadFailureEncrypted, true)
	var leases []string
	for _, k := range keys {
		leases = append(leases, f.holdLease(t, commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: k}, failed.Name))
	}

	require.NoError(t, f.handler.Handle(ctx, testMessage{
		env: f.event(t, failed, events.ActionFailed, downloadv1alpha1.DownloadFailureEncrypted, time.Now()),
	}))
	for _, l := range leases {
		assert.Empty(t, f.leaseHolder(t, l), "every episode lease the pack held is freed")
	}
	searches := f.bus.searches(t)
	require.Len(t, searches, 1, "only the monitored, existing episode is searched")
	var task schema.SearchTask
	require.NoError(t, schema.Decode(searches[0].env.Schema, searches[0].env.Data, &task))
	assert.Equal(t, commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: episodeName(1)}, task.MediaRef)
	assert.Equal(t, schema.SearchReasonRedownload, task.Reason)
}

func episodeName(n int32) string { return fmt.Sprintf("the-wire-s01e%02d", n) }

// TestAnOldFailureIsLeftToTheWantedSweep: the durable replays the EVENTS
// stream from its start when it is first created, and a failure older than
// MaxEventAge is not searched -- but its lease is still freed.
func TestAnOldFailureIsLeftToTheWantedSweep(t *testing.T) {
	f := newFixture(t, "redownload-stale")
	movie := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"}
	failed := f.failedDownload(t, movie, "g-failed", downloadv1alpha1.DownloadFailureTimeout, true)
	f.newMovie(t, movie.Name, "hd-bluray-web", true, failed.Name)
	lease := f.holdLease(t, movie, failed.Name)

	at := time.Now()
	f.handler.Now = func() time.Time { return at.Add(redownload.MaxEventAge + time.Minute) }
	require.NoError(t, f.handler.Handle(f.ctx, testMessage{
		env: f.event(t, failed, events.ActionFailed, downloadv1alpha1.DownloadFailureTimeout, at),
	}))
	assert.Empty(t, f.leaseHolder(t, lease))
	assert.Empty(t, f.bus.searches(t))
}

// TestALeaseAnotherGrabHoldsIsNotFreed: by the time the failure is handled a
// later grab may already have reclaimed the lease; it is not the failed
// Download's to free, and an unmonitored item is not searched.
func TestALeaseAnotherGrabHoldsIsNotFreed(t *testing.T) {
	f := newFixture(t, "redownload-reclaimed")
	movie := commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"}
	failed := f.failedDownload(t, movie, "g-failed", downloadv1alpha1.DownloadFailureMissingArticles, true)
	f.newMovie(t, movie.Name, "hd-bluray-web", false, failed.Name)
	lease := f.holdLease(t, movie, "the-matrix-later")

	require.NoError(t, f.handler.Handle(f.ctx, testMessage{
		env: f.event(t, failed, events.ActionFailed, downloadv1alpha1.DownloadFailureMissingArticles, time.Now()),
	}))
	assert.Equal(t, "the-matrix-later", f.leaseHolder(t, lease))
	assert.Empty(t, f.bus.searches(t), "an unmonitored movie is not searched (Radarr's MoviesSearchCommand filter)")
}
