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

package recordsource

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/controller/priorityqueue"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

// topology is the shipped single-node topology pruned to the two records
// buckets these tests watch and the two streams every topology must carry.
func topology(t *testing.T) events.Topology {
	t.Helper()
	def := events.Default().ForSingleNode()
	var top events.Topology
	for _, s := range def.Streams {
		if s.Name == events.StreamDLQ || s.Name == events.StreamAdvisories {
			top.Streams = append(top.Streams, s)
		}
	}
	for _, b := range def.Buckets {
		if b.Name == events.BucketTranscodes || b.Name == events.BucketSearches {
			top.Buckets = append(top.Buckets, b)
		}
	}
	require.Len(t, top.Buckets, 2)
	return top
}

// realBus is natsbus on an embedded JetStream server: the waker's behaviour
// rests on JetStream's UpdatesOnly and ResumeFromRevision, which membus only
// mirrors.
func realBus(t *testing.T) (*natsbus.Bus, jetstream.JetStream) {
	t.Helper()
	dir, err := os.MkdirTemp(t.TempDir(), "jetstream")
	require.NoError(t, err)
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "recordsource", Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: dir, NoLog: true, NoSigs: true,
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(20*time.Second))
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	bus, err := natsbus.New(nc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(t.Context(), topology(t)))
	return bus, bus.JetStream()
}

func put(t *testing.T, kv events.KV, uid, state string) uint64 {
	t.Helper()
	b, err := json.Marshal(struct{ schema.RecordHeader }{schema.RecordHeader{
		MediaFile: schema.Ref{Namespace: "media", Name: uid, UID: uid}, Seq: 1, State: state,
	}})
	require.NoError(t, err)
	rev, err := kv.Put(context.Background(), events.RecordKey(uid), b)
	require.NoError(t, err)
	return rev
}

func name(r schema.Ref) (string, bool) { return r.Namespace + "/" + r.Name, r.Name != "" }

type added struct {
	key      string
	priority int
}

// recordingQueue records every key enqueued and its priority.
type recordingQueue struct {
	workqueue.TypedRateLimitingInterface[string]
	mu  sync.Mutex
	got []added
}

var _ priorityqueue.PriorityQueue[string] = (*recordingQueue)(nil)

func newRecordingQueue(t *testing.T) *recordingQueue {
	q := &recordingQueue{TypedRateLimitingInterface: workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]())}
	t.Cleanup(q.ShutDown)
	return q
}

func (q *recordingQueue) Add(k string) { q.AddWithOpts(priorityqueue.AddOpts{}, k) }

func (q *recordingQueue) AddWithOpts(o priorityqueue.AddOpts, ks ...string) {
	p := 0
	if o.Priority != nil {
		p = *o.Priority
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, k := range ks {
		q.got = append(q.got, added{k, p})
	}
}

func (q *recordingQueue) GetWithPriority() (string, int, bool) { panic("unused") }

func (q *recordingQueue) keys() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]string, 0, len(q.got))
	for _, a := range q.got {
		out = append(out, a.key)
	}
	return out
}

func (q *recordingQueue) priorityOf(k string) (int, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, a := range q.got {
		if a.key == k {
			return a.priority, true
		}
	}
	return 0, false
}

func shorten(t *testing.T) {
	t.Helper()
	oi, om, oc, od := reopenInitial, reopenMax, recreateCheck, openDeadline
	reopenInitial, reopenMax, recreateCheck, openDeadline = 100*time.Millisecond, 200*time.Millisecond, 50*time.Millisecond, 500*time.Millisecond
	t.Cleanup(func() { reopenInitial, reopenMax, recreateCheck, openDeadline = oi, om, oc, od })
}

// The waker enqueues the file of every worker write after it opened, and
// skips the loop's own writes (requested, withdrawn), deletes, garbage, and
// whatever was written before it opened: that is read by the pass the
// informer's initial list enqueues (loop spec §4.9).
func TestSourceSkipsTheLoopsOwnWrites(t *testing.T) {
	bus, _ := realBus(t)
	kv := bus.KV(events.BucketTranscodes)
	put(t, kv, "before", records.StateAnswered)
	q := newRecordingQueue(t)
	require.NoError(t, New(bus, events.BucketTranscodes, name).Start(t.Context(), q))

	put(t, kv, "requested", records.StateRequested)
	put(t, kv, "withdrawn", records.StateWithdrawn)
	put(t, kv, "claimed", records.StateClaimed)
	put(t, kv, "answered", records.StateAnswered)
	put(t, kv, "failed", records.StateFailed)
	_, err := kv.Put(t.Context(), events.RecordKey("garbage"), []byte("{not json"))
	require.NoError(t, err)
	require.NoError(t, kv.Delete(t.Context(), events.RecordKey("before")))

	require.Eventually(t, func() bool { return len(q.keys()) >= 3 }, 10*time.Second, 10*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	assert.ElementsMatch(t, []string{"media/claimed", "media/answered", "media/failed"}, q.keys())
}

// closingBus lets a test close the watch under a live source, as nats.go does
// when its ordered consumer gives up, and records how each watch opened.
type closingBus struct {
	inner   Bus
	mu      sync.Mutex
	cancels []context.CancelFunc
	opened  []events.WatchOptions
}

func (b *closingBus) KV(bucket string) events.KV { return &closingKV{KV: b.inner.KV(bucket), b: b} }

func (b *closingBus) closeLatest() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cancels[len(b.cancels)-1]()
}

func (b *closingBus) opens() []events.WatchOptions {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]events.WatchOptions(nil), b.opened...)
}

type closingKV struct {
	events.KV
	b *closingBus
}

func (k *closingKV) Watch(ctx context.Context, pattern string, opts ...events.WatchOption) (<-chan events.Entry, error) {
	wctx, cancel := context.WithCancel(ctx)
	k.b.mu.Lock()
	k.b.cancels = append(k.b.cancels, cancel)
	k.b.opened = append(k.b.opened, events.ResolveWatchOptions(opts))
	k.b.mu.Unlock()
	return k.KV.Watch(wctx, pattern, opts...)
}

// A watch that closes while the source runs is opened again after a backoff
// from the revision after the last one seen: nothing written in the gap is
// lost, and nothing before it is delivered twice.
func TestSourceResumesFromRevisionAfterAClose(t *testing.T) {
	shorten(t)
	real, _ := realBus(t)
	bus := &closingBus{inner: real}
	kv := real.KV(events.BucketTranscodes)
	q := newRecordingQueue(t)
	require.NoError(t, New(bus, events.BucketTranscodes, name).Start(t.Context(), q))
	revA := put(t, kv, "a", records.StateAnswered)
	require.Eventually(t, func() bool { return len(q.keys()) == 1 }, 10*time.Second, 10*time.Millisecond)

	bus.closeLatest()
	put(t, kv, "b", records.StateAnswered)
	require.Eventually(t, func() bool { return len(q.keys()) == 2 }, 10*time.Second, 10*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, []string{"media/a", "media/b"}, q.keys(), "b once, a not again")
	opens := bus.opens()
	require.GreaterOrEqual(t, len(opens), 2)
	assert.True(t, opens[0].UpdatesOnly, "the first open replays nothing")
	assert.False(t, opens[1].UpdatesOnly)
	assert.Equal(t, revA+1, opens[1].FromRevision, "the reopen resumes after the last revision seen")
}

// A bucket deleted and created again under the source (a lost NATS volume, an
// operator's purge) is replayed at low priority, and every file whose status
// shows an outstanding request is woken, since its record went with the old
// bucket (loop spec §4.9, §5.15).
func TestSourceSurvivesABucketRecreatedUnderIt(t *testing.T) {
	shorten(t)
	bus, js := realBus(t)
	var woke atomic.Int32
	q := newRecordingQueue(t)
	src := New(bus, events.BucketTranscodes, name, OnRecreated[string](func(_ context.Context, enqueue func(string)) {
		woke.Add(1)
		enqueue("media/outstanding-in-status")
	}))
	require.NoError(t, src.Start(t.Context(), q))
	put(t, bus.KV(events.BucketTranscodes), "old", records.StateAnswered)
	require.Eventually(t, func() bool { return len(q.keys()) == 1 }, 10*time.Second, 10*time.Millisecond)

	require.NoError(t, js.DeleteKeyValue(t.Context(), events.BucketTranscodes))
	require.NoError(t, bus.Ensure(t.Context(), topology(t)))
	put(t, bus.KV(events.BucketTranscodes), "new", records.StateAnswered)

	require.Eventually(t, func() bool {
		_, outstanding := q.priorityOf("media/outstanding-in-status")
		_, fresh := q.priorityOf("media/new")
		return outstanding && fresh
	}, 15*time.Second, 20*time.Millisecond)
	assert.EqualValues(t, 1, woke.Load())
	p, _ := q.priorityOf("media/outstanding-in-status")
	assert.Equal(t, handler.LowPriority, p)
}

// Start fails, rather than leaving the loop deaf, when the bucket never
// appears within openDeadline; controller-runtime then fails the controller.
// A bucket that appears inside the window is watched (on the real bus: a
// membus KV handle binds its bucket when it is made, natsbus's on each call).
func TestStartFailsWhenTheBucketNeverAppears(t *testing.T) {
	shorten(t)
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(t.Context(), events.Default().ForSingleNode()))

	began := time.Now()
	err := New(bus, "clustarr-no-such-bucket", name).Start(t.Context(), newRecordingQueue(t))
	require.ErrorIs(t, err, events.ErrBucketNotFound)
	assert.GreaterOrEqual(t, time.Since(began), 250*time.Millisecond, "it retried before giving up")

	real, _ := realBus(t)
	const late = "clustarr-late-bucket"
	withLate := topology(t)
	withLate.Buckets = append(withLate.Buckets, events.BucketSpec{Name: late, History: 1, Storage: events.StorageMemory, Replicas: 1})
	ensured := make(chan error, 1)
	go func() {
		time.Sleep(150 * time.Millisecond)
		ensured <- real.Ensure(context.Background(), withLate)
	}()
	q := newRecordingQueue(t)
	require.NoError(t, New(real, late, name).Start(t.Context(), q), "a bucket that appears inside the window is watched")
	require.NoError(t, <-ensured)
	put(t, real.KV(late), "after", records.StateAnswered)
	require.Eventually(t, func() bool { return len(q.keys()) == 1 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"media/after"}, q.keys())
}

// TestNewItemsEnqueuesEveryKeyAndSkipsDeletes (ADR-0019 §8.1): an item-keyed
// bucket's waker decodes each worker write's item and enqueues every key
// toKeys returns for it -- a search record of an Episode wakes the Episode
// and its Series -- and enqueues nothing for a delete, a requested or
// withdrawn state, a record with no item, garbage, or a write before it
// opened.
func TestNewItemsEnqueuesEveryKeyAndSkipsDeletes(t *testing.T) {
	bus, _ := realBus(t)
	kv := bus.KV(events.BucketSearches)
	w := records.NewWriter(kv, agentrecords.Searches(), "catalog-0", "v-test")
	ref := func(kind, n string) *schema.ItemRef {
		return &schema.ItemRef{Kind: kind, Ref: schema.Ref{Namespace: "media", Name: n, UID: "uid-" + n}}
	}
	search := func(it, container *schema.ItemRef, task, state string) {
		t.Helper()
		v, err := w.Write(t.Context(), agentrecords.SearchKey(task), &schema.SearchRecord{
			RecordHeader: schema.RecordHeader{Item: it, Seq: 1, State: state},
			Task:         task,
			Container:    container,
		})
		require.NoError(t, err)
		require.Equal(t, records.Wrote, v)
	}
	toKeys := func(it schema.ItemRef, value []byte) []string {
		keys := []string{it.Kind + "/" + it.Namespace + "/" + it.Name}
		var r schema.SearchRecord
		if json.Unmarshal(value, &r) == nil && r.Container != nil {
			keys = append(keys, r.Container.Kind+"/"+r.Container.Namespace+"/"+r.Container.Name)
		}
		return keys
	}

	search(ref("Movie", "before"), nil, "task-before", records.StateAnswered)
	q := newRecordingQueue(t)
	require.NoError(t, NewItems(bus, events.BucketSearches, toKeys).Start(t.Context(), q))

	series := ref("Series", "show")
	search(ref("Episode", "s01e01"), series, "task-episode", records.StateAnswered)
	search(ref("Movie", "heat"), nil, "task-movie", records.StateFailed)
	search(ref("Movie", "asked"), nil, "task-requested", records.StateRequested)
	search(ref("Movie", "gone"), nil, "task-withdrawn", records.StateWithdrawn)
	search(nil, nil, "task-no-item", records.StateAnswered)
	_, err := kv.Put(t.Context(), agentrecords.SearchKey("task-garbage"), []byte("{not json"))
	require.NoError(t, err)
	require.NoError(t, kv.Delete(t.Context(), agentrecords.SearchKey("task-episode")))
	require.NoError(t, kv.Delete(t.Context(), agentrecords.SearchKey("task-before")))
	search(ref("Movie", "last"), nil, "task-last", records.StateAnswered)

	want := []string{"Episode/media/s01e01", "Series/media/show", "Movie/media/heat", "Movie/media/last"}
	require.Eventually(t, func() bool { return len(q.keys()) >= len(want) }, 10*time.Second, 10*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	assert.Equal(t, want, q.keys(), "every key of each worker write, in revision order, and nothing else")
}
