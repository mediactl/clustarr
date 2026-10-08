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
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogac "github.com/mediactl/clustarr/api/applyconfiguration/catalog/v1alpha1"
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/worker/search"
	"github.com/mediactl/clustarr/app/catalog/worker/titleindex"
	"github.com/mediactl/clustarr/pkg/decision"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/quality/catalogue"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
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

// withRecords wires w's records writer and reader over a fresh membus
// clustarr-searches bucket and returns the reader: since A4.3 the worker
// answers every search in its record and writes nothing else (ADR-0019
// §7.4), so a test reads what it did from the record.
func withRecords(t *testing.T, w *search.Worker) *records.Reader[*schema.SearchRecord] {
	t.Helper()
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(context.Background(), events.Default().ForSingleNode()))
	kv := bus.KV(events.BucketSearches)
	w.Records = records.NewWriter(kv, agentrecords.Searches(), "test-pod", version.String())
	w.Answers = records.NewReader(kv, agentrecords.Searches())
	return w.Answers
}

// readRecord is the record stored at key; it fails the test when none is.
func readRecord(t *testing.T, r *records.Reader[*schema.SearchRecord], key string) *schema.SearchRecord {
	t.Helper()
	rec, _, ok, err := r.Get(context.Background(), key)
	require.NoError(t, err)
	require.True(t, ok, "no record at %s", key)
	return rec
}

// itemRef is the record key's item for the object c can see at key: its
// kind, namespace, name and UID, as the manager's planner puts it on a task.
func itemRef(t *testing.T, ctx context.Context, c client.Client, kind string, obj client.Object, key client.ObjectKey) schema.ItemRef {
	t.Helper()
	require.NoError(t, c.Get(ctx, key, obj))
	return schema.ItemRef{Kind: kind, Ref: schema.Ref{Namespace: key.Namespace, Name: key.Name, UID: string(obj.GetUID())}}
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
	// api is the manager's uncached reader. Assertions use it: a read
	// through the shared informer cache can legitimately still show the
	// previous version.
	api    client.Reader
	worker *search.Worker
	rpc    *search.FakeSearchRPC
	// answers reads the records the worker writes.
	answers *records.Reader[*schema.SearchRecord]
	ns      string
}

// movie is the record key's item for the fixture's movie, the-matrix.
func (f *workerFixture) movie(t *testing.T) schema.ItemRef {
	t.Helper()
	return itemRef(t, context.Background(), f.mgr, "Movie", &catalogv1alpha1.Movie{}, client.ObjectKey{Namespace: f.ns, Name: "the-matrix"})
}

// record is the record the worker wrote at key.
func (f *workerFixture) record(t *testing.T, key string) *schema.SearchRecord {
	t.Helper()
	return readRecord(t, f.answers, key)
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

// testNow is the instant every fake clock in this package is frozen at, and
// the only instant fixture deadlines may be derived from.
//
// Mixing it with time.Now() is a time bomb rather than a flake: a fixture
// written as "now minus a day" is measured against the real date while the
// worker measures against this frozen one, so the test passes for exactly as
// long as the two stay within a day of each other and then fails every run
// after. TestWorkerBlocklistPredicateHonoursTheExpiryDeadline did precisely
// that and began failing on 2026-09-19 -- long after the gate that certified
// it green, which is why nothing caught it at the time.
var testNow = time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

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
	w := search.NewWorker(c, rpc, catalogue.LoadedCatalogue())
	w.Evaluate = approveEverything
	w.Clock = clockwork.NewFakeClockAt(testNow)
	answers := withRecords(t, w)

	return &workerFixture{mgr: c, api: mgr.GetAPIReader(), worker: w, rpc: rpc, answers: answers, ns: ns}
}

func (f *workerFixture) envelope(t *testing.T, task schema.SearchTask) *events.Envelope {
	t.Helper()
	schemaName, data, err := schema.Encode(task)
	require.NoError(t, err)
	return &events.Envelope{
		ID: "test-" + task.MediaRef.Name, Type: "catalog.SearchTask", Schema: schemaName,
		Source: "catalog@" + version.String(),
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
	// The controller's half of the status, so the worker is shown to leave
	// it alone. A blank object could not observe a release at all.
	writeControllerStatus(t, ctx, f.mgr, f.ns, "srch")

	env := f.envelope(t, schema.SearchTask{
		MediaRef:    commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		Reason:      schema.SearchReasonInteractive,
		SearchRef:   &schema.Ref{Namespace: f.ns, Name: "srch", UID: string(srch.UID)},
		UserInvoked: true,
		Seq:         1,
	})
	require.NoError(t, f.worker.Handle(ctx, testMessage{env: env}))

	reqs := f.rpc.Requests()
	require.Len(t, reqs, 1)
	require.Equal(t, commonv1.MediaKindMovie, reqs[0].Kind)
	require.Equal(t, "603", reqs[0].IDs[commonv1.IDKeyTMDB])
	require.True(t, reqs[0].UserInvoked)
	require.Equal(t, int32(schema.MaxSearchReleases), reqs[0].Limit,
		"the RPC always fetches up to spec §5's cap; spec.limit caps what is KEPT, not what is fetched")
	require.Equal(t, []int32{2040}, reqs[0].Categories, "spec.categories overrides the per-kind default")
	require.Len(t, reqs[0].IndexerRefs, 1)
	require.Equal(t, "idx", reqs[0].IndexerRefs[0].Name)

	// The answer is the Search's record (ADR-0019 §7.4), keyed by its UID;
	// the Search controller incorporates it into status.
	rec := f.record(t, agentrecords.SearchKey(string(srch.UID)))
	require.False(t, rec.FinishedAt.IsZero())
	require.Len(t, rec.Candidates, 2)
	require.Equal(t, "g-high", rec.Candidates[0].Decision.GUID, "the better format score ranks first")
	require.Equal(t, int32(1), rec.Candidates[0].Decision.Rank)
	require.Equal(t, "g-low", rec.Candidates[1].Decision.GUID)
	require.Equal(t, int32(2), rec.Candidates[1].Decision.Rank)
	// A release with no publish date round-trips as absent rather than being
	// rejected or invented (the defect three separate tasks hit when a
	// non-pointer metav1.Time marshalled to null).
	require.Nil(t, rec.Candidates[0].Decision.PublishedAt,
		"an absent publish date must persist as absent rather than being rejected or invented")
	require.Len(t, rec.IndexerOutcomes, 1)
	require.Equal(t, schema.SearchOutcomeOK, rec.IndexerOutcomes[0].Status)
	require.Equal(t, int64(120), rec.IndexerOutcomes[0].ElapsedMillis)

	got := &catalogv1alpha1.Search{}
	require.NoError(t, f.api.Get(ctx, client.ObjectKey{Namespace: f.ns, Name: "srch"}, got))
	require.Equal(t, catalogv1alpha1.SearchPhaseRunning, got.Status.Phase,
		"the worker writes no status: the controller's phase stands")
	require.NotNil(t, got.Status.StartedAt)
	require.Nil(t, got.Status.FinishedAt, "the worker writes no status; the controller incorporates the record")
	require.Empty(t, got.Status.Results)
}

// writeControllerStatus writes the controller-owned half of a Search's status
// under k8s.ManagerCatalog, standing in for search.Reconciler in a
// worker-only test.
func writeControllerStatus(t *testing.T, ctx context.Context, c client.Client, ns, name string) {
	t.Helper()
	_, err := k8s.PatchStatus(ctx, c, k8s.ManagerCatalog,
		catalogac.Search(name, ns).WithStatus(
			catalogac.SearchStatus().
				WithPhase(catalogv1alpha1.SearchPhaseRunning).
				WithObservedGeneration(1).
				WithStartedAt(metav1.Now())))
	require.NoError(t, err)
}

// TestWorkerHandleAnswersAnAutomaticSearchInTheItemsRecord: an automatic
// search's answer is the item's record, keyed by the item's UID, which the
// owner's grab planner reads (ADR-0019 §7.4). Before A4.3 the worker handed
// it to the grab sink.
func TestWorkerHandleAnswersAnAutomaticSearchInTheItemsRecord(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-sink")

	item := f.movie(t)
	env := f.envelope(t, schema.SearchTask{
		MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		Reason:   schema.SearchReasonMissing,
		Item:     item, Seq: 1,
	})
	require.NoError(t, f.worker.Handle(ctx, testMessage{env: env}))

	rec := f.record(t, agentrecords.SearchKey(item.UID))
	require.Equal(t, "the-matrix", rec.Item.Name)
	require.Equal(t, f.ns, rec.Item.Namespace, "the record names the item's namespace")
	require.Len(t, rec.Candidates, 2)
	require.Equal(t, "g-high", rec.Candidates[0].Decision.GUID)
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
			name: "a task naming no record key has nowhere to answer",
			env: func() *events.Envelope {
				return f.envelope(t, schema.SearchTask{
					MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
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
		name          string
		task          schema.SearchTask
		answersFailed bool
	}{
		{
			name: "an interactive task whose Search is already TTL-deleted",
			task: schema.SearchTask{
				MediaRef:  commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
				Reason:    schema.SearchReasonInteractive,
				SearchRef: &schema.Ref{Namespace: f.ns, Name: "already-gone", UID: "already-gone-uid"},
				Seq:       1,
			},
		},
		{
			// Since A4.3 a redelivery that still finds no target answers in
			// the item's record, failed, so the planner closes the dispatch
			// and backs off, rather than dead-lettering.
			name: "a target that no longer exists",
			task: schema.SearchTask{
				MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "deleted-movie"},
				Reason:   schema.SearchReasonMissing,
				Item:     schema.ItemRef{Kind: "Movie", Ref: schema.Ref{Namespace: f.ns, Name: "deleted-movie", UID: "deleted-movie-uid"}},
				Seq:      1,
			},
			answersFailed: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := f.envelope(t, tc.task)

			err := f.worker.Handle(ctx, testMessage{env: env, attempt: 1})
			var re *events.RetryError
			require.ErrorAs(t, err, &re, "the first delivery must nak, not dead-letter")

			err = f.worker.Handle(ctx, testMessage{env: env, attempt: 2})
			if tc.answersFailed {
				require.NoError(t, err)
				rec := f.record(t, agentrecords.SearchKey(tc.task.Item.UID))
				require.Equal(t, records.StateFailed, rec.State)
				require.Contains(t, rec.Failure, "no longer exists")
				return
			}
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
	// first. SetupWithManager registers none itself -- the agent registers
	// the catalog domain's declared indexes (titleindex.SearchIndexes) once --
	// so this test makes the same calls the wiring does.
	mgr := newManagerWithoutIndexes(t)
	for _, ix := range titleindex.SearchIndexes() {
		require.NoError(t, mgr.GetFieldIndexer().IndexField(ctx, ix.Object, ix.Name, ix.Extract))
	}
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

	// Each task answers in the movie's record; the second carries a later
	// seq, so its answer shows as the record's seq moving.
	item := f.movie(t)
	key := agentrecords.SearchKey(item.UID)
	answeredSeq := func() int64 {
		rec, _, ok, err := f.answers.Get(ctx, key)
		if err != nil || !ok {
			return 0
		}
		return rec.Seq
	}
	mediaKey := events.MediaKey(string(commonv1.MediaKindMovie), f.ns, "the-matrix")
	env := f.envelope(t, schema.SearchTask{
		MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		Reason:   schema.SearchReasonMissing,
		Item:     item, Seq: 1,
	})
	_, err := bus.Publish(ctx, events.WorkSearchSubject(events.PriorityHigh, mediaKey), env)
	require.NoError(t, err)
	eventually(t, 20*time.Second, "the high-priority search consumer to answer the task", func() bool { return answeredSeq() == 1 })

	env2 := f.envelope(t, schema.SearchTask{
		MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
		Reason:   schema.SearchReasonAdd,
		Item:     item, Seq: 2,
	})
	env2.ID = "normal-priority"
	_, err = bus.Publish(ctx, events.WorkSearchSubject(events.PriorityNormal, mediaKey), env2)
	require.NoError(t, err)
	eventually(t, 20*time.Second, "the normal-priority search consumer to answer the task", func() bool { return answeredSeq() == 2 })
}

// TestWorkerProtocolFallbackIsGatedOnUserInvoked pins the asymmetry in
// enabledProtocols. decision.Options.ProtocolsEnabled fails closed, and the
// only reason to open it up in a namespace with no DownloadClient is that an
// interactive search's output is a list a human reads. An automatic search's
// output is a grab, so opening up there would approve releases for a protocol
// nothing can download.
func TestWorkerProtocolFallbackIsGatedOnUserInvoked(t *testing.T) {
	ctx := context.Background()
	f := newWorkerFixture(t, "worker-protocols")

	srch := &catalogv1alpha1.Search{
		ObjectMeta: metav1.ObjectMeta{Name: "srch", Namespace: f.ns},
		Spec: catalogv1alpha1.SearchSpec{
			MediaRef: &commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
			TTL:      metav1.Duration{Duration: time.Hour},
		},
	}
	require.NoError(t, f.mgr.Create(ctx, srch))
	waitCached(t, ctx, f.mgr, client.ObjectKey{Namespace: f.ns, Name: "srch"}, &catalogv1alpha1.Search{})
	item := f.movie(t)

	tests := []struct {
		name        string
		task        schema.SearchTask
		wantEnabled bool
	}{
		{
			name: "an interactive search still shows the user what exists",
			task: schema.SearchTask{
				MediaRef:    commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
				Reason:      schema.SearchReasonInteractive,
				SearchRef:   &schema.Ref{Namespace: f.ns, Name: "srch", UID: string(srch.UID)},
				UserInvoked: true,
				Seq:         1,
			},
			wantEnabled: true,
		},
		{
			name: "an automatic search fails closed, because its list is grabbed",
			task: schema.SearchTask{
				MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "the-matrix"},
				Reason:   schema.SearchReasonMissing,
				Item:     item, Seq: 1,
			},
			wantEnabled: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			capture := &targetCapture{}
			f.worker.Evaluate = capture.evaluate
			require.NoError(t, f.worker.Handle(ctx, testMessage{env: f.envelope(t, tc.task)}))

			_, opts, _ := capture.get(t)
			require.Equal(t, tc.wantEnabled, opts.ProtocolsEnabled[string(commonv1.ProtocolTorrent)])
			require.Equal(t, tc.wantEnabled, opts.ProtocolsEnabled[string(commonv1.ProtocolUsenet)])
		})
	}
}
