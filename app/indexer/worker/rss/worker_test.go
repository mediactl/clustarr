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

package rss_test

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/app/indexer/health"
	"github.com/mediactl/clustarr/app/indexer/rssschedule"
	"github.com/mediactl/clustarr/app/indexer/worker/rss"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
	"github.com/mediactl/clustarr/pkg/release"
	"github.com/mediactl/clustarr/pkg/relindex"
	"github.com/mediactl/clustarr/pkg/torznab"
)

var t0 = time.Date(2026, 9, 19, 0, 0, 0, 0, time.UTC)

// tNow is a real "now", nudged past indexer.StartupGrace and rounded to the
// second (metav1.Time's resolution).
//
// The escalation ladder deliberately refuses to escalate within 15 minutes of
// PROCESS start, so that one restart does not disable every indexer at once,
// and it measures that against a real time.Now() captured at package init. A
// fake clock set in the past is therefore always "inside the grace window"
// and never escalates -- so every failure-path test that asserts on
// escalationLevel must drive the worker with a clock past that window, or it
// asserts nothing at all.
var tNow = time.Now().UTC().Truncate(time.Second).Add(time.Hour)

// ---------------------------------------------------------------- fakes ---

type fakeClock struct {
	mu sync.Mutex
	at time.Time
}

func newFakeClock(at time.Time) *fakeClock { return &fakeClock{at: at} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

// fakeMessage is a events.Message that counts its heartbeats, so a test can
// see whether a long poll extended its ack deadline.
type fakeMessage struct {
	env        *events.Envelope
	heartbeats atomic.Int32
	acks       atomic.Int32
	naks       atomic.Int32
	terms      atomic.Int32
}

func (m *fakeMessage) Envelope() *events.Envelope { return m.env }
func (m *fakeMessage) Subject() string            { return events.WorkRSSSubject("uid") }
func (m *fakeMessage) Attempt() uint64            { return 1 }

func (m *fakeMessage) Ack(context.Context) error                { m.acks.Add(1); return nil }
func (m *fakeMessage) Nak(context.Context, time.Duration) error { m.naks.Add(1); return nil }
func (m *fakeMessage) Term(context.Context, string) error       { m.terms.Add(1); return nil }
func (m *fakeMessage) InProgress(context.Context) error         { m.heartbeats.Add(1); return nil }

func mustEnv(t *testing.T, task schema.RssTask) *events.Envelope {
	t.Helper()
	name, data, err := schema.Encode(task)
	require.NoError(t, err)
	return &events.Envelope{Schema: name, Type: "index.RssTask", Data: data}
}

func rssTaskMessage(t *testing.T, ns, name string) *fakeMessage {
	t.Helper()
	return &fakeMessage{env: mustEnv(t, schema.RssTask{
		IndexerRef: schema.Ref{Namespace: ns, Name: name},
	})}
}

type fakeSearcher struct {
	mu       sync.Mutex
	queries  []torznab.Query
	releases []torznab.Release
	err      error
	onSearch func(torznab.Query) ([]torznab.Release, error)
}

func (s *fakeSearcher) Search(_ context.Context, q torznab.Query) ([]torznab.Release, error) {
	s.mu.Lock()
	s.queries = append(s.queries, q)
	on, rels, err := s.onSearch, s.releases, s.err
	s.mu.Unlock()

	if on != nil {
		return on(q)
	}
	return rels, err
}

func (s *fakeSearcher) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queries)
}

// fakeStore is a relindex.Store that records what it was offered and reports
// a fixed insert count, so a test can pin lastRssNewCount to the INDEX's
// answer rather than the feed's row count.
type fakeStore struct {
	mu       sync.Mutex
	upserted []relindex.Release
	inserted int
	err      error
}

func (s *fakeStore) Upsert(_ context.Context, rels []relindex.Release) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upserted = append(s.upserted, rels...)
	return s.inserted, s.err
}

func (s *fakeStore) Search(context.Context, relindex.Query) ([]relindex.Release, error) {
	return nil, nil
}
func (s *fakeStore) Prune(context.Context, time.Time) (int, error) { return 0, nil }
func (s *fakeStore) Stats(context.Context) (relindex.Stats, error) { return relindex.Stats{}, nil }

// The blocklist methods (ADR-0019 §6.14): this fake holds no block, so the
// firehose marks no release blocked.
func (s *fakeStore) Block(context.Context, relindex.Block) (bool, error)   { return false, nil }
func (s *fakeStore) Unblock(context.Context, relindex.Block) (bool, error) { return false, nil }
func (s *fakeStore) BlockState(context.Context, []relindex.BlockKey, time.Time) (map[int][]relindex.Block, error) {
	return nil, nil
}
func (s *fakeStore) ListBlocks(context.Context, string, int, int) ([]relindex.Block, error) {
	return nil, nil
}
func (s *fakeStore) PruneBlocks(context.Context, time.Time) (int, error) { return 0, nil }

func (s *fakeStore) rows() []relindex.Release {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]relindex.Release(nil), s.upserted...)
}

// countingClient counts reads, so a test can prove the handler Gets exactly
// one Indexer and never Lists them.
type countingClient struct {
	client.Client
	gets  atomic.Int32
	lists atomic.Int32
}

func (c *countingClient) Get(
	ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption,
) error {
	c.gets.Add(1)
	return c.Client.Get(ctx, key, obj, opts...)
}

func (c *countingClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	c.lists.Add(1)
	return c.Client.List(ctx, list, opts...)
}

// ------------------------------------------------------------- builders ---

func testIndexer(ns, name string, mutate ...func(*indexv1alpha1.Indexer)) *indexv1alpha1.Indexer {
	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns, UID: types.UID("uid-" + name), Generation: 1,
		},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL: "http://fixture.invalid",
			Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1.ProtocolTorrent},
		},
		Status: indexv1alpha1.IndexerStatus{Protocol: commonv1.ProtocolTorrent},
	}
	for _, fn := range mutate {
		fn(idx)
	}
	return idx
}

func newTestBus(t *testing.T) events.Bus {
	t.Helper()
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(t.Context(), events.Default().ForSingleNode()))
	t.Cleanup(func() { _ = bus.Close() })
	return bus
}

// healthOn is the agent's health writer over bus, as app/indexer/agent
// builds it (newHealthWriter): the poll's outcome is a record in
// clustarr-indexer-health, never Indexer status (ADR-0019 A6.3).
func healthOn(bus events.Bus) *health.Writer {
	return &health.Writer{
		Records:  records.NewWriter(bus.KV(events.BucketIndexerHealth), agentrecords.IndexerHealth(), "index-test", "test"),
		Suppress: &health.Suppress{},
	}
}

// healthRecord reads uid's clustarr-indexer-health record from bus; ok is
// false when nothing was ever reported.
func healthRecord(t *testing.T, bus events.Bus, uid types.UID) (*schema.IndexerHealthRecord, bool) {
	t.Helper()
	rec, _, ok, err := records.NewReader(bus.KV(events.BucketIndexerHealth), agentrecords.IndexerHealth()).
		Get(t.Context(), agentrecords.IndexerHealthKey(string(uid)))
	require.NoError(t, err)
	return rec, ok
}

// mustHealthRecord is healthRecord for a poll that must have reported.
func mustHealthRecord(t *testing.T, bus events.Bus, uid types.UID) *schema.IndexerHealthRecord {
	t.Helper()
	rec, ok := healthRecord(t, bus, uid)
	require.True(t, ok, "the poll reported nothing in clustarr-indexer-health")
	return rec
}

func newFakeClient(objs ...client.Object) client.Client {
	b := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme())
	if len(objs) > 0 {
		b = b.WithObjects(objs...).WithStatusSubresource(objs...)
	}
	return b.Build()
}

// pageOf builds n distinct, parsable torrent releases.
func pageOf(n int) []torznab.Release {
	out := make([]torznab.Release, n)
	for i := range out {
		out[i] = torznab.Release{
			Title:   "Some.Movie." + strconv.Itoa(2000+i) + ".1080p.BluRay.x264-GRP",
			GUID:    "guid-" + strconv.Itoa(i),
			Size:    int64(i+1) * 1024,
			PubDate: t0.Add(-time.Duration(i) * time.Minute),
		}
	}
	return out
}

// ---------------------------------------------------------------- tests ---

func TestSubscriptionFitsThePodsGracePeriod(t *testing.T) {
	sub := (&rss.Worker{}).Subscription()
	require.Equal(t, events.StreamWorkIndex, sub.Stream)
	require.Equal(t, events.ConsumerIndexRSS, sub.Durable)
	require.LessOrEqual(t, sub.AckWait, 60*time.Second,
		"terminationGracePeriodSeconds is 60; work that can outlast AckWait heartbeats instead")
	require.Equal(t, 30*time.Second, sub.Heartbeat)
	require.NoError(t, sub.Validate())
}

func TestHandleDiscardsUnusableTasks(t *testing.T) {
	tests := []struct {
		name string
		env  *events.Envelope
	}{
		{"no envelope", nil},
		{"undecodable", &events.Envelope{Schema: "index.RssTask.v1", Data: []byte("{[")}},
		{"wrong schema", &events.Envelope{Schema: "index.SearchRequest.v1", Data: []byte("{}")}},
		{"no indexer name", mustEnv(t, schema.RssTask{IndexerRef: schema.Ref{Namespace: "media"}})},
		{"no namespace", mustEnv(t, schema.RssTask{IndexerRef: schema.Ref{Name: "idx"}})},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := (&rss.Worker{}).Handle(t.Context(), &fakeMessage{env: tt.env})
			var d *events.DiscardError
			require.ErrorAs(t, err, &d, "an unusable task must be terminated, not retried four times")
		})
	}
}

// TestHandleAcknowledgesADeletedIndexer: the object went away between the
// schedule and the delivery. There is nothing to poll, nothing to record and
// nothing to reschedule.
func TestHandleAcknowledgesADeletedIndexer(t *testing.T) {
	w := &rss.Worker{Deps: rss.Deps{Client: newFakeClient(), Bus: newTestBus(t)}}
	require.NoError(t, w.Handle(t.Context(), rssTaskMessage(t, "media", "gone")))
}

func TestPollHeartbeatsWhileItPages(t *testing.T) {
	clock := newFakeClock(t0)
	msg := rssTaskMessage(t, "media", "idx")

	// Four full pages, each "taking" 25 seconds of fake time.
	searcher := &fakeSearcher{onSearch: func(torznab.Query) ([]torznab.Release, error) {
		clock.Advance(25 * time.Second)
		return pageOf(100), nil
	}}
	w := newTestWorker(t, clock, searcher, testIndexer("media", "idx"), &fakeStore{})

	require.NoError(t, w.Handle(t.Context(), msg))
	require.GreaterOrEqual(t, msg.heartbeats.Load(), int32(4),
		"a 100s poll with a 60s AckWait must extend its deadline or the broker redelivers it and two workers poll one indexer")
	require.Equal(t, 4, searcher.calls(), "the poll is bounded at maxPages")
}

func TestPollStopsPagingOnAShortPage(t *testing.T) {
	clock := newFakeClock(t0)
	searcher := &fakeSearcher{releases: pageOf(3)}
	w := newTestWorker(t, clock, searcher, testIndexer("media", "idx"), &fakeStore{})

	require.NoError(t, w.Handle(t.Context(), rssTaskMessage(t, "media", "idx")))
	require.Equal(t, 1, searcher.calls(), "a page shorter than the limit is the end of the feed")
}

func TestPollAtSigtermRetriesWithoutEscalatingTheIndexer(t *testing.T) {
	// tNow, not t0: inside the ladder's startup grace a failure cannot
	// escalate anyway, so the assertion below would hold whether or not the
	// shutdown path recorded one.
	clock := newFakeClock(tNow)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	searcher := &fakeSearcher{onSearch: func(torznab.Query) ([]torznab.Release, error) {
		cancel()
		return nil, ctx.Err()
	}}
	idx := testIndexer("media", "idx")
	c := newFakeClient(idx)
	w := newTestWorkerWithClient(t, clock, searcher, c, &fakeStore{})

	err := w.Handle(ctx, rssTaskMessage(t, "media", "idx"))

	var r *events.RetryError
	require.ErrorAs(t, err, &r, "a cancelled poll is unfinished work, not a failed indexer")

	// Nothing reaches the Indexer controller's ladder: a rollout must not
	// escalate every indexer at once.
	_, reported := healthRecord(t, w.Deps.Bus, idx.UID)
	require.False(t, reported, "a cancelled poll reported a failure")
}

func TestLastRssNewCountComesFromTheIndexNotTheFeed(t *testing.T) {
	clock := newFakeClock(t0)
	// 5 rows on the wire; the store reports 1 was new.
	store := &fakeStore{inserted: 1}
	idx := testIndexer("media", "idx")
	c := newFakeClient(idx)
	w := newTestWorkerWithClient(t, clock, &fakeSearcher{releases: pageOf(5)}, c, store)

	require.NoError(t, w.Handle(t.Context(), rssTaskMessage(t, "media", "idx")))

	got := mustHealthRecord(t, w.Deps.Bus, idx.UID)
	require.Equal(t, int32(1), got.LastRssNewCount, "new means new to the index, not fetched")
	require.Equal(t, int64(1), got.IndexedReleases, "the running total advances by the insert count")
	require.NotNil(t, got.LastRssAt)
	require.Equal(t, t0, got.LastRssAt.UTC())
	require.Len(t, store.rows(), 5, "every fetched row is offered to the index; the store decides")
}

// TestIndexRowsCarryTheFieldsTheIndexSearchesOn pins what the poll hands
// relindex: the FTS5 columns are title_norm and grp, and the caller
// normalises -- a row that stored the raw title in TitleNorm would be
// unsearchable and nothing would say so.
func TestIndexRowsCarryTheFieldsTheIndexSearchesOn(t *testing.T) {
	clock := newFakeClock(t0)
	store := &fakeStore{inserted: 1}
	c := newFakeClient(testIndexer("media", "idx"))
	w := newTestWorkerWithClient(t, clock, &fakeSearcher{releases: pageOf(1)}, c, store)

	require.NoError(t, w.Handle(t.Context(), rssTaskMessage(t, "media", "idx")))

	rows := store.rows()
	require.Len(t, rows, 1)
	row := rows[0]
	require.Equal(t, "idx", row.Indexer)
	require.Equal(t, "guid-0", row.GUID)
	require.Equal(t, "Some.Movie.2000.1080p.BluRay.x264-GRP", row.Title)
	// The FTS5 column and its normaliser. relindex stores TitleNorm verbatim
	// and escapes Query.Text without normalising it, so the search side runs
	// Query.Text through release.TitleNorm too (app/indexer/query) or the index
	// answers nothing -- and no test inside pkg/relindex can catch the
	// mismatch.
	require.Equal(t, release.TitleNorm(row.Title), row.TitleNorm)
	require.Equal(t, "some movie 2000 1080p bluray x264grp", row.TitleNorm)
	require.Equal(t, "GRP", row.Group)
	require.Equal(t, "torrent", row.Protocol)
	require.Equal(t, t0, row.FetchedAt.UTC())
	require.NotNil(t, row.PublishedAt)
	require.NotEmpty(t, row.InfoJSON, "InfoJSON lets a replay skip the re-query")

	var replayed schema.Release
	require.NoError(t, schema.Decode("", row.InfoJSON, &replayed))
	require.Equal(t, t0, replayed.FetchedAt.UTC(), "the publisher stamps FetchedAt on every projection")
}

func TestHandleNeverListsIndexers(t *testing.T) {
	// Gets of ONE object, by the name in the task, and never a List. If this
	// ever becomes a List, the failure of one indexer can abort the loop and
	// starve every indexer after it in the slice.
	rec := &countingClient{Client: newFakeClient(testIndexer("media", "idx"))}
	w := newTestWorkerWithClient(t, newFakeClock(t0), &fakeSearcher{releases: pageOf(2)}, rec, &fakeStore{})

	require.NoError(t, w.Handle(t.Context(), rssTaskMessage(t, "media", "idx")))
	require.Zero(t, rec.lists.Load(), "one message is one indexer")
	// Exactly one, before the poll: the outcome is a health record and the
	// worker applies no status, so there is no re-read before an apply
	// (ADR-0019 A6.3). Pinned rather than left open, because a Get PER
	// RELEASE would also satisfy "never lists" while being a very different
	// thing.
	require.Equal(t, int32(1), rec.gets.Load())
}

func TestHandleSkipsIndexersTheOperatorTurnedOff(t *testing.T) {
	tests := []struct {
		name  string
		tweak func(*indexv1alpha1.Indexer)
	}{
		{"spec.enabled false", func(i *indexv1alpha1.Indexer) { i.Spec.Enabled = new(false) }},
		{"spec.enableRss false", func(i *indexv1alpha1.Indexer) { i.Spec.EnableRss = new(false) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx := testIndexer("media", "idx", tt.tweak)
			idx.Status.LastRssNewCount = 7
			c := newFakeClient(idx)
			searcher := &fakeSearcher{releases: pageOf(5)}
			w := newTestWorkerWithClient(t, newFakeClock(t0), searcher, c, &fakeStore{inserted: 5})

			require.NoError(t, w.Handle(t.Context(), rssTaskMessage(t, "media", "idx")))
			require.Zero(t, searcher.calls(), "a disabled indexer is never queried")

			_, reported := healthRecord(t, w.Deps.Bus, idx.UID)
			require.False(t, reported, "nothing was observed, so nothing is recorded")
		})
	}
}

func TestHandleDoesNotQueryAnIndexerInsideItsBackoffWindow(t *testing.T) {
	idx := testIndexer("media", "idx", func(i *indexv1alpha1.Indexer) {
		i.Status.EscalationLevel = 3
		i.Status.DisabledUntil = new(metav1.NewTime(t0.Add(10 * time.Minute)))
	})
	c := newFakeClient(idx)
	searcher := &fakeSearcher{releases: pageOf(5)}
	w := newTestWorkerWithClient(t, newFakeClock(t0), searcher, c, &fakeStore{})

	require.NoError(t, w.Handle(t.Context(), rssTaskMessage(t, "media", "idx")))
	require.Zero(t, searcher.calls(), "the backoff window exists precisely to stop the query")

	_, reported := healthRecord(t, w.Deps.Bus, idx.UID)
	require.False(t, reported, "not querying is neither a failure nor a poll")
}

// A failed poll is reported in the health record and acknowledged: the
// Indexer controller runs the ladder on the record and holds the next poll
// at its window (ADR-0019 A6.3, P91), so the worker asks for no redelivery
// of its own (it did before A6.3, and this test's name records that).
func TestFailedPollRecordsTheFailureAndAsksForARedelivery(t *testing.T) {
	idx := testIndexer("media", "idx")
	c := newFakeClient(idx)
	w := newTestWorkerWithClient(t, newFakeClock(tNow),
		&fakeSearcher{err: errors.New("indexer returned 503")}, c, &fakeStore{})

	require.NoError(t, w.Handle(t.Context(), rssTaskMessage(t, "media", "idx")),
		"a failed poll is reported, not redelivered: the controller schedules the next one")

	got := mustHealthRecord(t, w.Deps.Bus, idx.UID)
	require.Equal(t, int64(1), got.Failures)
	require.Equal(t, "indexer returned 503", got.LastFailure)
	require.Equal(t, schema.IndexerFailureError, got.LastFailureClass)
	require.NotNil(t, got.LastFailureAt)
	require.Equal(t, tNow, got.LastFailureAt.UTC())
	require.Nil(t, got.LastRssAt, "a failed poll did not read the feed")
}

func TestScheduleNextDedupsPerSlotAndAdvancesBetweenSlots(t *testing.T) {
	bus, nc := newJetStreamBusAndConn(t)
	idx := testIndexer("media", "sched")
	idx.UID = "u1"
	idx.Generation = 3

	slot1, slot2 := t0.Add(15*time.Minute), t0.Add(30*time.Minute)

	require.NoError(t, rssschedule.ScheduleNext(t.Context(), bus, idx, slot1))
	first := pendingSchedule(t, nc, "u1")
	require.Equal(t, "@at "+slot1.UTC().Format(time.RFC3339), first.schedule)

	// The reconciler seeding a slot while a poll schedules the same one must
	// collapse to ONE delivery. The msg-id is what collapses it: the stream
	// dedups for 1h, so the second publish stores nothing at all and the
	// stored sequence does not move.
	require.NoError(t, rssschedule.ScheduleNext(t.Context(), bus, idx, slot1))
	again := pendingSchedule(t, nc, "u1")
	require.Equal(t, first.seq, again.seq, "a second schedule for the same slot must store nothing")

	// A DIFFERENT slot must get through. A constant per-object msg-id would
	// be a duplicate inside the same 1h window, so nothing would be stored,
	// the pending schedule would still be slot1, and once that one fired the
	// indexer would stop polling for good.
	require.NoError(t, rssschedule.ScheduleNext(t.Context(), bus, idx, slot2))
	next := pendingSchedule(t, nc, "u1")
	require.Greater(t, next.seq, first.seq,
		"a constant per-object msg-id would swallow this and polling would stop dead")
	require.Equal(t, "@at "+slot2.UTC().Format(time.RFC3339), next.schedule,
		"the pending poll is the newest slot, not the stale one")

	require.NotEqual(t, rssschedule.TaskMsgID("u1", 3, slot1), rssschedule.TaskMsgID("u1", 3, slot2))
}

func TestScheduleNextRefusesAnIndexerWithNoUID(t *testing.T) {
	idx := testIndexer("media", "idx")
	idx.UID = ""
	require.Error(t, rssschedule.ScheduleNext(t.Context(), newTestBus(t), idx, t0))
}

// pendingSchedule reads the one scheduled poll held for an indexer.
//
// A scheduled publish is stored on the HOLDING subject and republished to the
// target when it fires. The server stamps it Nats-Rollup: sub, so a later
// schedule for the same indexer REPLACES the pending one rather than queuing
// beside it -- there is at most one pending poll per indexer, by
// construction, and the sequence is what says whether a publish stored
// anything at all.
func pendingSchedule(t *testing.T, nc *nats.Conn, uid string) (out struct {
	seq      uint64
	schedule string
},
) {
	t.Helper()
	hold, err := events.ScheduleSubject(events.WorkRSSSubject(uid))
	require.NoError(t, err)

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	st, err := js.Stream(t.Context(), events.StreamWorkIndex)
	require.NoError(t, err)
	msg, err := st.GetLastMsgForSubject(t.Context(), hold)
	require.NoError(t, err)

	require.Equal(t, events.WorkRSSSubject(uid), msg.Header.Get("Nats-Schedule-Target"),
		"the schedule must target the work subject, or it would re-trigger itself")
	out.seq = msg.Sequence
	out.schedule = msg.Header.Get("Nats-Schedule")
	return out
}

// -------------------------------------------------------------- wiring ----

func newTestWorker(
	t *testing.T, clock *fakeClock, s rss.Searcher, idx *indexv1alpha1.Indexer, store relindex.Store,
) *rss.Worker {
	t.Helper()
	return newTestWorkerWithClient(t, clock, s, newFakeClient(idx), store)
}

func newTestWorkerWithClient(
	t *testing.T, clock *fakeClock, s rss.Searcher, c client.Client, store relindex.Store,
) *rss.Worker {
	t.Helper()
	bus := newTestBus(t)
	hw := healthOn(bus)
	hw.Now = clock.Now
	return rss.NewWorker(rss.Deps{
		Client: c,
		Health: hw,
		Bus:    bus,
		Index:  store,
		SearcherFor: func(context.Context, *indexv1alpha1.Indexer) (rss.Searcher, error) {
			return s, nil
		},
		Clock: clock.Now,
	})
}
