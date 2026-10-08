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

package contracttest

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
)

// testRetiredConsumers holds Topology.Retired (loop spec §4.15, §7.3.9):
// Ensure deletes a retired durable and purges its filter, reports the lag the
// durable still had (results nobody will apply), and stays idempotent. It
// builds its own durable on the DLQ stream, which every topology keeps, so it
// outlives the results durables the fold retires (F3.4, F6.6).
func testRetiredConsumers(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	const durable, filter = "contracttest-retired", "clustarr.dlq.contracttest.>"
	spec, ok := events.Default().Consumer(events.ConsumerDLQProjector)
	if !ok {
		t.Fatalf("%s is not in the default topology", events.ConsumerDLQProjector)
	}
	spec.Name, spec.Filters = durable, []string{filter}
	live := Topology()
	live.Consumers = []events.ConsumerSpec{spec}
	if err := bus.Ensure(ctx, live); err != nil {
		t.Fatalf("Ensure with the durable: %v", err)
	}
	for i := range 3 {
		id := fmt.Sprintf("result-%d", i)
		if _, err := bus.Publish(ctx, events.DLQSubject("contracttest", "retired", id), envelope(id, "contracttest.Result.v1", map[string]int{"n": i}), events.WithMsgID(id)); err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}
	admin := bus.(events.StreamAdmin)
	st, err := admin.ConsumerState(ctx, events.StreamDLQ, durable)
	if err != nil || st.Lag() != 3 {
		t.Fatalf("ConsumerState before retiring = %+v, %v; want a lag of 3", st, err)
	}

	gone := events.RetiredConsumer{Stream: events.StreamDLQ, Durable: "contracttest-never-existed"}
	retiring := events.RetiredConsumer{Stream: events.StreamDLQ, Durable: durable, Purge: filter}
	retired := Topology()
	retired.Retired = []events.RetiredConsumer{retiring, gone}
	for range 2 {
		if err := bus.Ensure(ctx, retired); err != nil {
			t.Fatalf("Ensure retiring the durable (idempotent): %v", err)
		}
	}
	if _, err := admin.ConsumerState(ctx, events.StreamDLQ, durable); !errors.Is(err, events.ErrConsumerNotFound) {
		t.Errorf("the retired durable still exists: %v", err)
	}
	if subjects, err := admin.Subjects(ctx, events.StreamDLQ, filter); err != nil || len(subjects) != 0 {
		t.Errorf("Subjects under the retired filter = %v, %v; want none", subjects, err)
	}
	reports := bus.(events.RetiredReporter).RetiredReports()
	if len(reports) != 2 {
		t.Fatalf("RetiredReports = %+v; want one per retired durable", reports)
	}
	for _, r := range reports {
		switch r.Durable {
		case retiring.Durable:
			if !r.Existed || r.Lag != 3 {
				t.Errorf("report for %s = %+v; want Existed with a lag of 3 (the first Ensure's, kept)", r.Durable, r)
			}
		case gone.Durable:
			if r.Existed {
				t.Errorf("report for %s = %+v; want not Existed", r.Durable, r)
			}
		}
	}
}

// nextEntry waits for one watch delivery.
func nextEntry(t *testing.T, ch <-chan events.Entry) events.Entry {
	t.Helper()
	select {
	case e, ok := <-ch:
		if !ok {
			t.Fatal("the watch closed")
		}
		return e
	case <-time.After(Timeout):
		t.Fatal("timed out waiting for a watch delivery")
	}
	return events.Entry{}
}

// testKVWatchDeliversPastTheBuffer: a watcher opened over 200 keys that then
// falls 1,000 updates behind loses nothing, in revision order (membus dropped
// past 64 until F1.3; loop spec §4.15).
func testKVWatchDeliversPastTheBuffer(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	kv := bus.KV(events.BucketTranscodes)
	for i := range 200 {
		if _, err := kv.Put(ctx, fmt.Sprintf("pre-%04d", i), []byte("v")); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, err := kv.Watch(watchCtx, ">")
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	for i := range 1000 {
		if _, err := kv.Put(ctx, fmt.Sprintf("post-%04d", i), []byte("v")); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	seen := map[string]bool{}
	var last uint64
	for len(seen) < 1200 {
		e := nextEntry(t, ch)
		if e.Revision <= last {
			t.Fatalf("revision %d after %d: out of order", e.Revision, last)
		}
		last = e.Revision
		seen[e.Key] = true
	}
}

func testKVWatchUpdatesOnly(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	kv := bus.KV(events.BucketTranscodes)
	for _, k := range []string{"a", "b"} {
		if _, err := kv.Put(ctx, k, []byte("before")); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, err := kv.Watch(watchCtx, ">", events.WatchUpdatesOnly())
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	if _, err := kv.Put(ctx, "c", []byte("after")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if e := nextEntry(t, ch); e.Key != "c" {
		t.Fatalf("first delivery = %q; an updates-only watch replays nothing", e.Key)
	}
}

func testKVWatchFromRevision(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	kv := bus.KV(events.BucketTranscodes)
	revs := map[string]uint64{}
	for _, k := range []string{"a", "b", "c"} {
		rev, err := kv.Put(ctx, k, []byte(k))
		if err != nil {
			t.Fatalf("Put: %v", err)
		}
		revs[k] = rev
	}
	watchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, err := kv.Watch(watchCtx, ">", events.WatchFromRevision(revs["b"]))
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	for _, want := range []string{"b", "c"} {
		if e := nextEntry(t, ch); e.Key != want || e.Revision != revs[want] {
			t.Fatalf("delivery = %q@%d; want %q@%d from revision %d", e.Key, e.Revision, want, revs[want], revs["b"])
		}
	}
	if _, err := kv.Put(ctx, "d", []byte("d")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if e := nextEntry(t, ch); e.Key != "d" {
		t.Fatalf("delivery = %q; want the update d", e.Key)
	}
}

func testKVKeys(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	kv := bus.KV(events.BucketTranscodes)
	keys, err := kv.Keys(ctx)
	if err != nil || len(keys) != 0 {
		t.Fatalf("Keys of an empty bucket = %v, %v; want none and no error", keys, err)
	}
	for _, k := range []string{"x-2", "x-1", "x-3"} {
		if _, err := kv.Put(ctx, k, []byte("v")); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if err := kv.Delete(ctx, "x-3"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	keys, err = kv.Keys(ctx)
	if err != nil || !slices.Equal(keys, []string{"x-1", "x-2"}) {
		t.Fatalf("Keys = %v, %v; want [x-1 x-2], sorted, the deleted key gone", keys, err)
	}
}

// testKVStatus: a bucket's last revision and creation time, which the records
// waker seeds from and checks for a recreated bucket (loop spec §4.9).
func testKVStatus(t *testing.T, newBus func() events.Bus) {
	ctx, bus := setup(t, newBus)
	kv := bus.KV(events.BucketTranscodes)
	before, err := kv.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if before.Bucket != events.BucketTranscodes || before.Created.IsZero() {
		t.Fatalf("Status = %+v; want the bucket and its creation time", before)
	}
	rev, err := kv.Put(ctx, "k", []byte("v"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	after, err := kv.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if after.LastRevision != rev || !after.Created.Equal(before.Created) {
		t.Fatalf("Status after a put = %+v; want LastRevision %d and Created unchanged %v", after, rev, before.Created)
	}
	if _, err := bus.KV("clustarr-no-such-bucket").Status(ctx); !errors.Is(err, events.ErrBucketNotFound) {
		t.Fatalf("Status of a missing bucket = %v; want ErrBucketNotFound", err)
	}
}
