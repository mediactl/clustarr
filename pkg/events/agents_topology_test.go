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

package events_test

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
)

// TestAgentRecordBucketsAreDurableHistoryOneAndBounded is ADR-0019 §4.3's
// table (the A plan's "Exact values"): six agent-written records buckets,
// 1,040 MiB of file-store reservations.
func TestAgentRecordBucketsAreDurableHistoryOneAndBounded(t *testing.T) {
	top := events.Default()
	require.NoError(t, top.Validate())
	week := events.RecordsTTL
	assert.Equal(t, 7*24*time.Hour, week)
	want := map[string]bucketCaps{
		events.BucketTransfers:     {week, 256 * events.MiB, 512 * events.KiB},
		events.BucketEngines:       {week, 8 * events.MiB, 16 * events.KiB},
		events.BucketImports:       {week, 128 * events.MiB, 512 * events.KiB},
		events.BucketSearches:      {events.SearchesTTL, 128 * events.MiB, 512 * events.KiB},
		events.BucketItemMetadata:  {week, 512 * events.MiB, 512 * events.KiB},
		events.BucketIndexerHealth: {week, 8 * events.MiB, 8 * events.KiB},
	}
	assert.Equal(t,
		[]string{"clustarr-transfers", "clustarr-engines", "clustarr-imports", "clustarr-searches", "clustarr-item-metadata", "clustarr-indexer-health"},
		[]string{events.BucketTransfers, events.BucketEngines, events.BucketImports, events.BucketSearches, events.BucketItemMetadata, events.BucketIndexerHealth})
	assert.EqualValues(t, 1040*events.MiB, assertRecordsBuckets(t, top, want), "§4.3: the agent buckets reserve 1,040 MiB")
}

// TestIntakeAndCommandStreamsAreDurableAndDiscardNew is ADR-0019 §4.3: a full
// intake or command stream refuses the publish, which the publisher retries;
// nothing is evicted, and a single node keeps both on file at full size. The
// task-events stream is the opposite: delivery state is history, so it is a
// small memory stream on a single node that discards its oldest.
func TestIntakeAndCommandStreamsAreDurableAndDiscardNew(t *testing.T) {
	top := events.Default()
	require.NoError(t, top.Validate())
	for _, tc := range []struct {
		name, filter string
		maxBytes     int64
	}{
		{events.StreamIntake, events.FilterIntake, 256 * events.MiB},
		{events.StreamWorkEngine, events.FilterWorkEngine, 64 * events.MiB},
	} {
		s, ok := top.Stream(tc.name)
		require.True(t, ok, tc.name)
		assert.Equal(t, []string{tc.filter}, s.Subjects, tc.name)
		assert.Equal(t, events.RetentionWorkQueue, s.Retention, tc.name)
		assert.Equal(t, events.StorageFile, s.Storage, tc.name)
		assert.Equal(t, events.DiscardNew, s.Discard, "%s refuses when full", tc.name)
		assert.False(t, s.AllowMsgSchedules, "%s: nats-server refuses schedules with DiscardNew", tc.name)
		assert.True(t, s.Durable, tc.name)
		assert.Equal(t, tc.maxBytes, s.MaxBytes, tc.name)
		assert.Zero(t, s.MaxAge, tc.name)
		assert.Equal(t, time.Hour, s.Duplicates, tc.name)
		assert.Equal(t, 3, s.Replicas, tc.name)

		single, ok := top.ForSingleNode().Stream(tc.name)
		require.True(t, ok)
		assert.Equal(t, events.StorageFile, single.Storage, "%s stays on file on a single node", tc.name)
		assert.Equal(t, tc.maxBytes, single.MaxBytes, "%s is not scaled into the memory budget", tc.name)
		assert.Equal(t, 1, single.Replicas, tc.name)
	}

	te, ok := top.Stream(events.StreamTaskEvents)
	require.True(t, ok)
	assert.False(t, te.Durable)
	assert.Equal(t, events.DiscardOld, te.Discard)
	assert.Equal(t, time.Hour, te.MaxAge)
	assert.Equal(t, int64(8*events.MiB), te.MaxBytes)
	assert.Zero(t, te.Duplicates)
	single, ok := top.ForSingleNode().Stream(events.StreamTaskEvents)
	require.True(t, ok)
	assert.Equal(t, events.StorageMemory, single.Storage)
	assert.Equal(t, int64(events.MiB), single.MaxBytes, "the 1 MiB floor of the memory budget")

	// Each intake subject lands on the intake stream and only its own
	// durable takes it; an engine command lands on the command stream.
	for _, tc := range []struct {
		subject, stream, durable string
	}{
		{events.IntakeCandidateSubject("Movie", "u1"), events.StreamIntake, events.ConsumerIntakeCandidate},
		{events.IntakeScanSubject("media", "scan-uid"), events.StreamIntake, events.ConsumerIntakeScan},
		{events.WorkEngineSubject("qbit", 0, "entry-uid"), events.StreamWorkEngine, ""},
		{events.WorkEngineResyncSubject("qbit", 0), events.StreamWorkEngine, ""},
		{events.WorkEngineIDSubject("qbit", 0, "dl:1"), events.StreamWorkEngine, ""},
	} {
		got, ok := top.StreamForSubject(tc.subject)
		require.True(t, ok, tc.subject)
		assert.Equal(t, tc.stream, got.Name, tc.subject)
		for _, c := range top.Consumers {
			matched := slices.ContainsFunc(c.Filters, func(f string) bool { return events.SubjectMatches(f, tc.subject) })
			assert.Equal(t, c.Name == tc.durable, matched, "%s: checked %s", tc.subject, c.Name)
		}
	}
	for _, name := range []string{events.ConsumerIntakeCandidate, events.ConsumerIntakeScan} {
		c, ok := top.Consumer(name)
		require.True(t, ok, name)
		assert.Equal(t, events.StreamIntake, c.Stream, name)
		assert.False(t, c.Dispatched, "%s: the manager's own inbox, not a task it dispatches", name)
	}
}

// TestTaskEventSubjectsListOnlyDispatchedConsumersAndNeverGt is ADR-0019 §8.2:
// CLUSTARR_TASK_EVENTS captures the MSG_NAKED and MSG_TERMINATED advisories of
// exactly the durables the manager dispatches, plus a wildcard for each
// stream whose dispatched durables are dynamic, and never ">", which would
// capture every advisory of every consumer.
func TestTaskEventSubjectsListOnlyDispatchedConsumersAndNeverGt(t *testing.T) {
	top := events.Default()
	subjects := events.TaskEventSubjects(top)

	stream, ok := top.Stream(events.StreamTaskEvents)
	require.True(t, ok)
	assert.Equal(t, subjects, stream.Subjects, "the stream is rendered from the finished topology")
	c, ok := top.Consumer(events.ConsumerTaskEvents)
	require.True(t, ok)
	assert.Equal(t, subjects, c.Filters, "the manager's intake reads every captured subject")

	for _, s := range subjects {
		assert.NotContains(t, strings.Split(s, "."), ">", "%s: never a > wildcard", s)
		assert.True(t, strings.HasPrefix(s, events.SubjectNakAdvisoryPrefix+".") ||
			strings.HasPrefix(s, events.SubjectTermAdvisoryPrefix+"."), "%s: a nak or term advisory", s)
	}

	captured := func(subject string) bool {
		return slices.ContainsFunc(subjects, func(f string) bool { return events.SubjectMatches(f, subject) })
	}
	dispatched := 0
	for _, c := range top.Consumers {
		nak, term := events.NakAdvisorySubject(c.Stream, c.Name), events.TermAdvisorySubject(c.Stream, c.Name)
		assert.Equal(t, c.Dispatched, captured(nak), "%s's naks", c.Name)
		assert.Equal(t, c.Dispatched, captured(term), "%s's terms", c.Name)
		if c.Dispatched {
			dispatched++
			assert.Contains(t, subjects, nak)
			assert.Contains(t, subjects, term)
		}
	}
	assert.Positive(t, dispatched)
	for _, name := range []string{events.ConsumerCatalogRSSMatcher, events.ConsumerCatalogHistory, events.ConsumerDLQProjector,
		events.ConsumerIntakeCandidate, events.ConsumerIntakeScan, events.ConsumerTaskEvents} {
		c, ok := top.Consumer(name)
		require.True(t, ok, name)
		assert.False(t, c.Dispatched, "%s is no task the manager dispatches", name)
	}

	// The dynamic durables: any pool's and any engine instance's.
	pool := events.TranscodeTaskConsumer("6f1c-uid", "nvidia")
	engine := events.EngineConsumer("qbit", 2)
	for _, d := range []events.ConsumerSpec{pool, engine} {
		require.True(t, d.Dispatched, d.Name)
		assert.True(t, captured(events.NakAdvisorySubject(d.Stream, d.Name)), d.Name)
		assert.True(t, captured(events.TermAdvisorySubject(d.Stream, d.Name)), d.Name)
		assert.False(t, captured(events.MaxDeliveriesAdvisorySubject(d.Stream, d.Name)),
			"%s: a MAX_DELIVERIES advisory is the dead-letter watcher's", d.Name)
	}
	assert.Len(t, subjects, 2*dispatched+4, "two per dispatched durable, two per dynamic stream")

	// A topology with no dispatched durable captures only the dynamic streams.
	assert.Len(t, events.TaskEventSubjects(events.Topology{}), 4)
}

// consumerNameGrammar is a JetStream durable name this repo can also use as
// a KV key: KVKeyToken's alphabet plus the separators the builders add, and
// none of ".", "*", ">", whitespace or a path separator.
var consumerNameGrammar = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// TestEngineConsumerNamesAreInjectiveAndKVSafe: one durable per engine
// instance, named from a DownloadClient name and an ordinal. Client names
// that differ only in bytes a durable name cannot hold get different
// durables, and every name is a legal durable and KV key (ADR-0019 §6.7).
func TestEngineConsumerNamesAreInjectiveAndKVSafe(t *testing.T) {
	clients := []string{
		"qbit", "qbit.a", "qbit:a", "qbit/a", "qbit a", "qbit-a", "qbit_a", "qbit--a", "qbit-2ea",
		"qbit*", "qbit>", "Qbit", "qbité", "", "-", "a-1", "a",
	}
	seen := map[string]string{}
	for _, client := range clients {
		for _, ordinal := range []int32{0, 1, 2, 10, 11, 12} {
			name := events.EngineConsumerName(client, ordinal)
			id := fmt.Sprintf("%q/%d", client, ordinal)
			prev, clash := seen[name]
			require.False(t, clash, "%s and %s share the durable %s", prev, id, name)
			seen[name] = id
			assert.True(t, strings.HasPrefix(name, events.EngineConsumerPrefix), name)
			assert.Regexp(t, consumerNameGrammar, name)
			assert.True(t, events.ValidKVKey(name), "%s is no KV key", name)

			spec := events.EngineConsumer(client, ordinal)
			assert.Equal(t, name, spec.Name)
			assert.Equal(t, events.StreamWorkEngine, spec.Stream)
			require.Len(t, spec.Filters, 1)
			assert.True(t, events.SubjectMatches(spec.Filters[0], events.WorkEngineSubject(client, ordinal, "e1")), id)
			assert.True(t, events.SubjectMatches(spec.Filters[0], events.WorkEngineResyncSubject(client, ordinal)), id)
			assert.False(t, events.SubjectMatches(spec.Filters[0], events.WorkEngineSubject(client, ordinal+1, "e1")),
				"%s: another instance's commands", id)
		}
	}
	// R20's timing, which the DownloadClient controller ensures per ordinal.
	c := events.EngineConsumer("qbit", 0)
	assert.Equal(t, 2*time.Minute, c.AckWait)
	assert.Equal(t, 20, c.MaxDeliver)
	assert.Equal(t, []time.Duration{10 * time.Second, time.Minute, 5 * time.Minute, 15 * time.Minute}, c.BackOff)
	assert.Equal(t, 4, c.Slots)
	// A command a handler naks with a delay -- an add whose indexer is down,
	// retried rather than read as gone -- stays unacknowledged until it is
	// redelivered. At MaxAckPending equal to Slots, four such commands froze
	// the usenet engine's whole queue (1,159 commands, removals and adds
	// alike) for as long as nzb.life answered 522 (talos, 2026-10-15). The
	// engine still runs four at once; the rest wait their turn beside them.
	assert.Equal(t, 256, c.MaxAckPending)
	assert.Greater(t, c.MaxAckPending, c.Slots, "a delayed retry must not hold a slot")
	assert.Equal(t, 30*time.Second, c.Heartbeat)
	// A command outlives its first delivery's deadline (BackOff[0], 10 s)
	// whenever its payload RPC is slow -- nzb.life's 522s took about 15 s --
	// and with no budget nothing told the broker it was still running: the
	// broker handed out a second copy mid-run, the engine fetched the payload
	// again (a grab the indexer counts), and the lapsed copies, served before
	// new work, kept 1,153 commands -- removals among them -- undelivered
	// (talos, 2026-10-15). A budgeted handler is heartbeated every third of
	// its deadline.
	assert.Equal(t, 2*time.Minute, c.HandlerTimeout)
	assert.Greater(t, c.HandlerTimeout, c.BackOff[0])
	assert.Equal(t, c.HandlerTimeout, c.Subscription().HandlerTimeout, "the subscription carries the budget")
	assert.Equal(t, events.SampleFrequencyEngine, c.SampleFrequency)
	assert.True(t, c.Dispatched)
	top := events.Default()
	top.Consumers = append(top.Consumers, c, events.DeadLetterWatcherSpec(c))
	assert.NoError(t, top.Validate(), "an engine durable and its watcher are valid topology")
}
