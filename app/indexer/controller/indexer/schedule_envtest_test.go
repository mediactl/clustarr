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

package indexer_test

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sevents "k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	indexac "github.com/mediactl/clustarr/api/applyconfiguration/index/index/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/app/indexer/controller/indexer"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/ratelimit"
)

// These tests run against a REAL JetStream server, not membus. What is being
// asserted is a property of the broker: the reconciler's seed is idempotent
// only because CLUSTARR_WORK_INDEXARR deduplicates by msg-id for an hour and
// a scheduled publish rolls up per subject. membus has neither, so it would
// report a pass for a seed that in production stormed the work queue.

// startNATS boots an embedded JetStream server with its store under the
// test's temporary directory. It is pkg/events/natsbus's helper, verbatim.
func startNATS(t *testing.T) *natsserver.Server {
	t.Helper()
	dir, err := os.MkdirTemp(t.TempDir(), "jetstream")
	require.NoError(t, err)
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "clustarr-indexer",
		Host:       "127.0.0.1",
		Port:       -1,
		JetStream:  true,
		StoreDir:   dir,
		NoLog:      true,
		NoSigs:     true,
	})
	require.NoError(t, err)
	go srv.Start()
	if !srv.ReadyForConnections(20 * time.Second) {
		t.Fatal("embedded NATS server did not become ready")
	}
	t.Cleanup(srv.Shutdown)
	return srv
}

func newJetStreamReconciler(t *testing.T, c client.Client) (*indexer.Reconciler, *nats.Conn) {
	t.Helper()
	nc, err := nats.Connect(startNATS(t).ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)

	bus, err := natsbus.New(nc)
	require.NoError(t, err)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(t.Context(), events.Default().ForSingleNode()))

	r := indexer.NewReconciler(c, k8sevents.NewFakeRecorder(10), ratelimit.New(ratelimit.Config{}), bus)
	return r, nc
}

// pollSubjects are the two places one indexer's scheduled poll can be: held
// on the schedule subject, or -- once the schedule fires, which for a slot at
// `now` is within a second -- republished onto the work subject it targets.
// A test that watched only one of them would be a race.
func pollSubjects(t *testing.T, uid string) []string {
	t.Helper()
	hold, err := events.ScheduleSubject(events.WorkRSSSubject(uid))
	require.NoError(t, err)
	return []string{hold, events.WorkRSSSubject(uid)}
}

func stream(t *testing.T, nc *nats.Conn) jetstream.Stream {
	t.Helper()
	js, err := jetstream.New(nc)
	require.NoError(t, err)
	st, err := js.Stream(context.Background(), events.StreamWorkIndexarr)
	require.NoError(t, err)
	return st
}

// scheduledPoll returns the RssTask queued for uid, and whether there was
// one at all.
func scheduledPoll(t *testing.T, nc *nats.Conn, uid string) (schema.RssTask, bool) {
	t.Helper()
	st := stream(t, nc)
	for _, subj := range pollSubjects(t, uid) {
		msg, err := st.GetLastMsgForSubject(context.Background(), subj)
		if err != nil {
			continue
		}
		var task schema.RssTask
		require.NoError(t, schema.Decode(msg.Header.Get(events.HeaderSchema), msg.Data, &task))
		return task, true
	}
	return schema.RssTask{}, false
}

// requireScheduledPoll waits briefly for the poll to settle on one subject or
// the other. The publish itself is synchronous inside Reconcile, so the only
// thing being waited on is the instant the broker moves a fired schedule
// across.
func requireScheduledPoll(t *testing.T, nc *nats.Conn, uid, msgAndArgs string) schema.RssTask {
	t.Helper()
	var task schema.RssTask
	require.Eventually(t, func() bool {
		var ok bool
		task, ok = scheduledPoll(t, nc, uid)
		return ok
	}, 5*time.Second, 50*time.Millisecond, msgAndArgs)
	return task
}

func purgePolls(t *testing.T, nc *nats.Conn, uid string) {
	t.Helper()
	st := stream(t, nc)
	for _, subj := range pollSubjects(t, uid) {
		require.NoError(t, st.Purge(context.Background(), jetstream.WithPurgeSubject(subj)))
	}
	_, still := scheduledPoll(t, nc, uid)
	require.False(t, still, "setup: the purge left a poll behind, so the next assertion would read a stale one")
}

// newSchedulableIndexer creates an Indexer that reconciles all the way to
// Ready: a reachable caps endpoint, a generic source, enabled by default.
func newSchedulableIndexer(t *testing.T, c client.Client, ns, name string) types.NamespacedName {
	t.Helper()
	srv := capsServer(t, readFixture(t, "testdata/caps.xml"), http.StatusOK)
	require.NoError(t, c.Create(context.Background(), &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL: srv.URL,
			Generic: &indexv1alpha1.GenericNewznab{Protocol: commonv1alpha1.ProtocolTorrent, APIPath: "/api"},
		},
	}))
	return types.NamespacedName{Namespace: ns, Name: name}
}

// TestANewIndexerGetsItsFirstRssPoll is ruling R36's whole point.
//
// The RSS worker schedules the NEXT poll at the end of every poll, which
// keeps a chain alive but cannot begin one. Nothing seeded the first task, so
// no chain ever started: in a real cluster the release firehose was silent
// forever, and no unit test could see it because every one of them handed the
// worker a task the test itself had built.
func TestANewIndexerGetsItsFirstRssPoll(t *testing.T) {
	c := newTestClient(t)
	ns := newNamespace(t, context.Background(), c, "idx-seed")
	name := newSchedulableIndexer(t, c, ns, "seeded")

	r, nc := newJetStreamReconciler(t, c)
	_, err := reconcileOnce(t, r, name)
	require.NoError(t, err)

	idx := mustGet(t, c, name)
	task := requireScheduledPoll(t, nc, string(idx.UID),
		"nothing seeded the first RssTask, so this indexer is never polled and the release firehose never starts")

	require.Equal(t, name.Name, task.IndexerRef.Name)
	require.Equal(t, ns, task.IndexerRef.Namespace,
		"the worker Discards a task whose reference it cannot resolve to a namespace")
	require.Equal(t, string(idx.UID), task.IndexerRef.UID)
}

// TestADisabledThenReEnabledIndexerIsScheduledAgain is the second half, and
// it is the half that bites an operator rather than a cluster that was never
// working.
//
// The worker's disabled path acknowledges WITHOUT rescheduling, on the stated
// premise that "re-enabling bumps the generation, the reconciler reconciles
// and seeds a fresh schedule". Before R36 the reconciler did no such thing,
// so toggling spec.enabled off and on again stopped that indexer's RSS
// permanently -- with a Ready=True object and nothing at all in the logs.
func TestADisabledThenReEnabledIndexerIsScheduledAgain(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c, "idx-reenable")
	name := newSchedulableIndexer(t, c, ns, "toggled")

	r, nc := newJetStreamReconciler(t, c)
	_, err := reconcileOnce(t, r, name)
	require.NoError(t, err)

	idx := mustGet(t, c, name)
	uid := string(idx.UID)
	requireScheduledPoll(t, nc, uid, "setup: the first reconcile must seed")

	// Clear the queue, so what the assertions below read is what THIS part
	// of the lifecycle produced rather than the seed above.
	purgePolls(t, nc, uid)

	// Off. A disabled indexer must not be scheduled -- the seed's gate is
	// the worker's gate, so they agree on what "this indexer polls" means.
	setEnabled(t, c, name, false)
	_, err = reconcileOnce(t, r, name)
	require.NoError(t, err)
	_, queued := scheduledPoll(t, nc, uid)
	require.False(t, queued, "a disabled indexer must not be polled")

	// And on again.
	setEnabled(t, c, name, true)
	_, err = reconcileOnce(t, r, name)
	require.NoError(t, err)
	task := requireScheduledPoll(t, nc, uid,
		"re-enabling never restarted the chain: the worker acks a disabled poll without rescheduling, so this indexer's RSS is dead forever")
	require.Equal(t, name.Name, task.IndexerRef.Name)
}

// TestReSeedingTheSlotTheWorkerAlreadyChoseStoresNothing pins the reason the
// seed can run on EVERY reconcile rather than behind an in-memory "have I
// seeded this?" memo -- which would be a second truth about a cluster-wide
// fact, and wrong after every restart.
//
// Two things have to hold. rss.NextPollAt must return the slot the worker's
// own reschedule already chose, lastRssAt + rssInterval, and rss.TaskMsgID
// must quantise it so the two publishes carry ONE id that
// CLUSTARR_WORK_INDEXARR deduplicates. The stored sequence is what says
// whether a publish stored anything at all: it does not move.
//
// Asserting on the stream's message COUNT instead would be a false pass. A
// scheduled publish carries Nats-Rollup: sub, so even a broken msg-id --
// RFC3339Nano instead of a truncated second, say -- replaces the pending poll
// rather than adding to it and the count never grows. The rollup is a second
// safety net, not the mechanism, and a test that reads it cannot tell the two
// apart.
func TestReSeedingTheSlotTheWorkerAlreadyChoseStoresNothing(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c, "idx-reseed")
	name := newSchedulableIndexer(t, c, ns, "steady")

	r, nc := newJetStreamReconciler(t, c)
	_, err := reconcileOnce(t, r, name)
	require.NoError(t, err)
	idx := mustGet(t, c, name)
	uid := string(idx.UID)
	requireScheduledPoll(t, nc, uid, "setup: the first reconcile must seed")

	// A poll has now happened, written exactly as the RSS worker writes it:
	// status.lastRssAt is the instant the worker scheduled the next poll
	// from. Its slot is 15 minutes out, so it stays PENDING and the reads
	// below are not racing the broker's schedule tick.
	polledAt := time.Now().Add(-5 * time.Minute).Truncate(time.Second).UTC()
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerIndexarrWorker,
		indexac.Indexer(name.Name, ns).WithStatus(
			indexac.IndexerStatus().WithLastRssAt(metav1.NewTime(polledAt))))
	require.NoError(t, err)
	purgePolls(t, nc, uid)

	_, err = reconcileOnce(t, r, name)
	require.NoError(t, err)
	first := pendingPoll(t, nc, uid)
	require.Equal(t, "@at "+polledAt.Add(15*time.Minute).Format(time.RFC3339), first.schedule,
		"the reconciler picked a slot of its own instead of the one the worker already scheduled, so the two fight and spec.rssInterval is overridden by the reprobe tick")

	for range 3 {
		_, rerr := reconcileOnce(t, r, name)
		require.NoError(t, rerr)
	}

	again := pendingPoll(t, nc, uid)
	require.Equal(t, first.seq, again.seq,
		"a re-seed stored a message; at one reconcile every 15 minutes that is a poll queued per reconcile")
}

// TestAnIndexerWithRssTurnedOffIsNotSeeded pins the live half of the seed's
// gate. spec.enabled is already an early return in Reconcile; spec.enableRss
// is not, and it reaches the seed -- so without the check a healthy indexer
// with RSS deliberately switched off would be polled anyway, which is the
// worker acking and discarding one task per interval forever.
func TestAnIndexerWithRssTurnedOffIsNotSeeded(t *testing.T) {
	ctx := context.Background()
	c := newTestClient(t)
	ns := newNamespace(t, ctx, c, "idx-norss")
	name := newSchedulableIndexer(t, c, ns, "norss")

	idx := mustGet(t, c, name)
	idx.Spec.EnableRss = ptr.To(false)
	require.NoError(t, c.Update(ctx, &idx))

	r, nc := newJetStreamReconciler(t, c)
	_, err := reconcileOnce(t, r, name)
	require.NoError(t, err)

	got := mustGet(t, c, name)
	require.True(t, k8s.IsConditionTrue(got.Status.Conditions, indexv1alpha1.IndexerConditionReady),
		"setup: spec.enableRss: false is not a reason to be unready, so this is the healthy path")
	_, queued := scheduledPoll(t, nc, string(got.UID))
	require.False(t, queued, "spec.enableRss: false must not be polled")
}

// pendingPoll reads the one poll HELD for an indexer, and fails if the
// schedule has already fired -- the callers that use it arrange a slot in the
// future precisely so it cannot.
func pendingPoll(t *testing.T, nc *nats.Conn, uid string) (out struct {
	seq      uint64
	schedule string
},
) {
	t.Helper()
	hold, err := events.ScheduleSubject(events.WorkRSSSubject(uid))
	require.NoError(t, err)
	msg, err := stream(t, nc).GetLastMsgForSubject(context.Background(), hold)
	require.NoError(t, err,
		"no poll is HELD for this indexer: either nothing seeded one, or it was seeded for a slot that has ALREADY FIRED -- "+
			"a reconciler that seeds at `now` instead of at the slot the worker already chose overrides spec.rssInterval with the reprobe tick")
	require.Equal(t, events.WorkRSSSubject(uid), msg.Header.Get("Nats-Schedule-Target"),
		"the schedule must target the work subject, or it would re-trigger itself")
	out.seq = msg.Sequence
	out.schedule = msg.Header.Get("Nats-Schedule")
	return out
}

func setEnabled(t *testing.T, c client.Client, name types.NamespacedName, enabled bool) {
	t.Helper()
	idx := mustGet(t, c, name)
	idx.Spec.Enabled = ptr.To(enabled)
	require.NoError(t, c.Update(context.Background(), &idx))
}
