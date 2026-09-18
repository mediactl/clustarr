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

package search_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	searchctl "github.com/mediactl/clustarr/catalogarr/controller/search"
	"github.com/mediactl/clustarr/catalogarr/worker/search"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/version"
)

// testMessage is the minimal events.Message Handle needs: it only reads the
// envelope and the delivery count, and lets the bus settle the delivery.
type testMessage struct {
	env *events.Envelope
	// attempt is the 1-based delivery count; zero reads as the first
	// delivery, which is what a bus hands a fresh message.
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

// recordingSink captures what the worker hands to the automatic grab path.
type recordingSink struct {
	mu       sync.Mutex
	tasks    []schema.SearchTask
	batches  [][]catalogv1alpha1.ReleaseDecision
	delivery chan struct{}
}

func newRecordingSink() *recordingSink {
	return &recordingSink{delivery: make(chan struct{}, 8)}
}

func (s *recordingSink) Deliver(_ context.Context, task schema.SearchTask, ranked []catalogv1alpha1.ReleaseDecision) error {
	s.mu.Lock()
	s.tasks = append(s.tasks, task)
	s.batches = append(s.batches, ranked)
	s.mu.Unlock()
	select {
	case s.delivery <- struct{}{}:
	default:
	}
	return nil
}

func (s *recordingSink) last() (schema.SearchTask, []catalogv1alpha1.ReleaseDecision, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.tasks) == 0 {
		return schema.SearchTask{}, nil, 0
	}
	return s.tasks[len(s.tasks)-1], s.batches[len(s.batches)-1], len(s.tasks)
}

// approveEverything is the EvaluateFunc seam standing in for pkg/decision, so
// these tests exercise Handle's plumbing rather than the rule table (which has
// its own tests in pkg/decision). It approves each release and gives the
// better-scoring one the better rank key.
func approveEverything(_ context.Context, _ decision.Target, _ quality.Profile, _ *catalogue.Catalogue, rels []commonv1.ReleaseInfo, _ decision.Options) []decision.Decision {
	out := make([]decision.Decision, 0, len(rels))
	for _, rel := range rels {
		out = append(out, decision.Decision{
			Release:  rel,
			Approved: true,
			Score:    int(rel.FormatScore),
			Rank:     decision.RankKey{FormatScore: int(rel.FormatScore)},
		})
	}
	return out
}

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

func rpcRelease(guid, title string, score int32) schema.Release {
	return schema.Release{
		Info: commonv1.ReleaseInfo{
			GUID: guid, IndexerRef: "idx", IndexerName: "Example", Title: title,
			Protocol: commonv1.ProtocolTorrent, SizeBytes: 8 << 30, FormatScore: score,
			DownloadURL: "https://idx.example/dl/" + guid,
		},
		ParsedTitle: title,
		FetchedAt:   time.Date(2026, 9, 18, 11, 0, 0, 0, time.UTC),
	}
}

type workerFixture struct {
	mgr client.Client
	// api is the manager's uncached reader. Assertions use it: the worker
	// writes through the apiserver, so a read through the shared informer
	// cache can legitimately still show the previous version.
	api    client.Reader
	worker *search.Worker
	rpc    *search.FakeSearchRPC
	sink   *recordingSink
	ns     string
}

// waitCached blocks until the manager's cache can see obj, so a test that
// creates a fixture and immediately drives the worker does not race the
// informer.
func waitCached(t *testing.T, ctx context.Context, c client.Client, key client.ObjectKey, obj client.Object) {
	t.Helper()
	eventually(t, 10*time.Second, "the cache to see "+key.String(), func() bool {
		return c.Get(ctx, key, obj) == nil
	})
}

func newWorkerFixture(t *testing.T, ns string) *workerFixture {
	t.Helper()
	ctx := context.Background()
	mgr := newTestManager(t)
	c := mgr.GetClient()
	newNamespace(t, ctx, c, ns)

	qp := testQualityProfile("wf-" + ns)
	if err := c.Create(ctx, qp); err != nil {
		t.Fatalf("create QualityProfile: %v", err)
	}
	require.NoError(t, c.Create(ctx, &catalogv1alpha1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "the-matrix", Namespace: ns},
		Spec: catalogv1alpha1.MovieSpec{
			TmdbID: 603, QualityProfileRef: qp.Name, RootFolderRef: "movies",
		},
	}))
	waitCached(t, ctx, c, client.ObjectKey{Name: qp.Name}, &catalogv1alpha1.QualityProfile{})
	waitCached(t, ctx, c, client.ObjectKey{Namespace: ns, Name: "the-matrix"}, &catalogv1alpha1.Movie{})

	rpc := &search.FakeSearchRPC{Response: schema.SearchResponse{
		Releases: []schema.Release{
			rpcRelease("g-low", "The.Matrix.1999.1080p.WEBDL.x264-LOW", 10),
			rpcRelease("g-high", "The.Matrix.1999.1080p.BluRay.x264-HIGH", 100),
		},
		Outcomes: []schema.SearchOutcome{{
			IndexerRef: schema.Ref{Namespace: ns, Name: "idx"}, IndexerName: "Example",
			Status: schema.SearchOutcomeOK, Releases: 2, ElapsedMillis: 120,
		}},
	}}
	sink := newRecordingSink()

	w := search.NewWorker(c, rpc, catalogue.LoadedCatalogue())
	w.Evaluate = approveEverything
	w.Sink = sink
	w.Clock = clockwork.NewFakeClockAt(time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC))

	return &workerFixture{mgr: c, api: mgr.GetAPIReader(), worker: w, rpc: rpc, sink: sink, ns: ns}
}

func (f *workerFixture) envelope(t *testing.T, task schema.SearchTask) *events.Envelope {
	t.Helper()
	schemaName, data, err := schema.Encode(task)
	require.NoError(t, err)
	return &events.Envelope{
		ID: "test-" + task.MediaRef.Name, Type: "catalog.SearchTask", Schema: schemaName,
		Source: "catalogarr@" + version.String(),
		Key:    f.ns + "/srch", Time: time.Now().UTC(), Data: data,
	}
}

func TestWorkerHandleWritesAnInteractiveSearchesResults(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-interactive")

	srch := &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: "srch", Namespace: f.ns},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef:    &commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
			Limit:       100,
			IndexerRefs: []string{"idx"},
			Categories:  []int32{2040},
			TTL:         metav1.Duration{Duration: time.Hour},
		},
	}
	require.NoError(t, f.mgr.Create(ctx, srch))
	waitCached(t, ctx, f.mgr, client.ObjectKey{Namespace: f.ns, Name: "srch"}, &catalogv1alpha1.Search{})
	// The controller's half of the status, so the worker's apply is shown to
	// leave it alone rather than release it. A blank object could not observe
	// a server-side-apply release at all.
	writeControllerStatus(t, ctx, f.mgr, f.ns, "srch")

	env := f.envelope(t, schema.SearchTask{
		MediaRef:    commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		Reason:      schema.SearchReasonInteractive,
		SearchRef:   &schema.Ref{Namespace: f.ns, Name: "srch"},
		UserInvoked: true,
	})
	require.NoError(t, f.worker.Handle(ctx, testMessage{env: env}))

	reqs := f.rpc.Requests()
	require.Len(t, reqs, 1)
	require.Equal(t, commonv1.MediaKindMovie, reqs[0].Kind)
	require.Equal(t, "603", reqs[0].IDs[commonv1.IDKeyTMDB])
	require.True(t, reqs[0].UserInvoked)
	require.Equal(t, int32(100), reqs[0].Limit, "spec.limit overrides the worker default")
	require.Equal(t, []int32{2040}, reqs[0].Categories, "spec.categories overrides the per-kind default")
	require.Len(t, reqs[0].IndexerRefs, 1)
	require.Equal(t, "idx", reqs[0].IndexerRefs[0].Name)

	got := &catalogv1alpha1.Search{}
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "srch"}, got))
	require.NotNil(t, got.Status.FinishedAt)
	require.Len(t, got.Status.Results, 2)
	require.Equal(t, "g-high", got.Status.Results[0].GUID, "the better format score ranks first")
	require.Equal(t, int32(1), got.Status.Results[0].Rank)
	require.Equal(t, "g-low", got.Status.Results[1].GUID)
	require.Equal(t, int32(2), got.Status.Results[1].Rank)
	require.False(t, got.Status.Results[0].PublishedAt.IsZero(),
		"a release with no publish date is backfilled from fetchedAt; a zero one cannot be persisted")
	require.Len(t, got.Status.IndexerOutcomes, 1)
	require.Equal(t, catalogv1alpha1.IndexerOutcomeOK, got.Status.IndexerOutcomes[0].State)
	require.Equal(t, int32(120), got.Status.IndexerOutcomes[0].DurationMs)

	require.Equal(t, catalogv1alpha1.SearchPhaseRunning, got.Status.Phase,
		"the worker owns a disjoint field set and must not release the controller's phase")
	require.NotNil(t, got.Status.StartedAt)

	_, _, deliveries := f.sink.last()
	require.Zero(t, deliveries, "an interactive search writes status instead of reaching the grab sink")
}

// writeControllerStatus writes the controller-owned half of a Search's status
// under k8s.ManagerCatalogarr, standing in for search.Reconciler in a
// worker-only test.
func writeControllerStatus(t *testing.T, ctx context.Context, c client.Client, ns, name string) {
	t.Helper()
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalogarr,
		searchctl.Search(name, ns).WithStatus(
			searchctl.SearchStatus().
				WithPhase(catalogv1alpha1.SearchPhaseRunning).
				WithObservedGeneration(1).
				WithStartedAt(metav1.Now())))
	require.NoError(t, err)
}

func TestWorkerHandleDeliversNonInteractiveResultsToTheSink(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-sink")

	env := f.envelope(t, schema.SearchTask{
		MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		Reason:   schema.SearchReasonMissing,
	})
	require.NoError(t, f.worker.Handle(ctx, testMessage{env: env}))

	task, ranked, deliveries := f.sink.last()
	require.Equal(t, 1, deliveries)
	require.Equal(t, schema.SearchReasonMissing, task.Reason)
	require.Len(t, ranked, 2)
	require.Equal(t, "g-high", ranked[0].GUID)
	require.False(t, f.rpc.Requests()[0].UserInvoked, "a cron-driven search is not user invoked")
}

func TestWorkerHandleDiscardsWhatItCannotServe(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-discard")

	tests := []struct {
		name string
		env  func() *events.Envelope
	}{
		{
			name: "a non-video kind is M6 scope",
			env: func() *events.Envelope {
				return f.envelope(t, schema.SearchTask{
					MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindArtist, Name: "aphex-twin"},
					Reason:   schema.SearchReasonMissing,
				})
			},
		},
		{
			name: "an undecodable payload",
			env: func() *events.Envelope {
				e := f.envelope(t, schema.SearchTask{
					MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
				})
				e.Data = []byte("{not json")
				return e
			},
		},
		{
			name: "an envelope with no resolvable namespace",
			env: func() *events.Envelope {
				e := f.envelope(t, schema.SearchTask{
					MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
					Reason:   schema.SearchReasonMissing,
				})
				e.Key = "no-slash"
				return e
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := f.worker.Handle(ctx, testMessage{env: tc.env()})
			require.Error(t, err)
			var de *events.DiscardError
			require.ErrorAs(t, err, &de, "a task that can never succeed must be dead-lettered, not retried forever")
		})
	}
}

// TestWorkerHandleRetriesAMissingObjectOnceThenDiscards pins the cache-warm
// race: the producer writes an object through the apiserver and publishes, and
// the worker reads it back through a watch, so a first miss may just mean the
// informer is a beat behind. Dead-lettering there would kill a good search AND
// strand its Search object in Running until the controller times it out.
func TestWorkerHandleRetriesAMissingObjectOnceThenDiscards(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-missing")

	tests := []struct {
		name string
		task schema.SearchTask
	}{
		{
			name: "an interactive task whose Search is already TTL-deleted",
			task: schema.SearchTask{
				MediaRef:  commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
				Reason:    schema.SearchReasonInteractive,
				SearchRef: &schema.Ref{Namespace: f.ns, Name: "already-gone"},
			},
		},
		{
			name: "a target that no longer exists",
			task: schema.SearchTask{
				MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "deleted-movie"},
				Reason:   schema.SearchReasonMissing,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := f.envelope(t, tc.task)

			err := f.worker.Handle(ctx, testMessage{env: env, attempt: 1})
			var re *events.RetryError
			require.ErrorAs(t, err, &re, "the first delivery must nak, not dead-letter")

			err = f.worker.Handle(ctx, testMessage{env: env, attempt: 2})
			var de *events.DiscardError
			require.ErrorAs(t, err, &de, "a redelivery that still cannot find the object is dead-lettered")
		})
	}
}

func TestWorkerSetupWithManagerSubscribesBothSearchConsumers(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-subscribe")

	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(ctx, events.Default()))

	// A second manager: newTestManager already registered the indexes on the
	// first, and SetupWithManager registers them itself.
	mgr := newManagerWithoutIndexes(t)
	require.NoError(t, f.worker.SetupWithManager(mgr, bus))

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = mgr.Start(runCtx)
	}()
	t.Cleanup(func() { cancel(); <-done })
	require.True(t, mgr.GetCache().WaitForCacheSync(runCtx))

	f.worker.Client = mgr.GetClient()

	mediaKey := events.MediaKey(string(commonv1.MediaKindMovie), f.ns, "the-matrix")
	env := f.envelope(t, schema.SearchTask{
		MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		Reason:   schema.SearchReasonMissing,
	})
	_, err := bus.Publish(ctx, events.WorkSearchSubject(events.PriorityHigh, mediaKey), env)
	require.NoError(t, err)

	select {
	case <-f.sink.delivery:
	case <-time.After(20 * time.Second):
		t.Fatal("the high-priority search consumer never delivered the task")
	}

	env2 := f.envelope(t, schema.SearchTask{
		MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		Reason:   schema.SearchReasonAdd,
	})
	env2.ID = "normal-priority"
	_, err = bus.Publish(ctx, events.WorkSearchSubject(events.PriorityNormal, mediaKey), env2)
	require.NoError(t, err)

	select {
	case <-f.sink.delivery:
	case <-time.After(20 * time.Second):
		t.Fatal("the normal-priority search consumer never delivered the task")
	}

	_, _, deliveries := f.sink.last()
	require.GreaterOrEqual(t, deliveries, 2)
}
