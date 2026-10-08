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

package records_test

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
)

// rec is a record of a test remediation: its inputs, its result, and whether
// the answer changed the file on disk (a fact, as a transcode swap is).
// Count is a counter a Writer.Update increments.
type rec struct {
	schema.RecordHeader
	Inputs  string `json:"inputs,omitempty"`
	Result  string `json:"result,omitempty"`
	Swapped bool   `json:"swapped,omitempty"`
	Count   int    `json:"count,omitempty"`
}

const (
	bucket     = "clustarr-records-test"
	testSchema = "records.test.v1"
)

var (
	file = schema.Ref{Namespace: "media", Name: "heat-abc", UID: "uid-heat"}
	key  = events.RecordKey(file.UID)
)

func spec() records.Spec[*rec] {
	return records.Spec[*rec]{
		Remediation: "test", Bucket: bucket, Schema: testSchema, MaxValue: 4 << 10,
		New:      func() *rec { return new(rec) },
		Answered: records.TerminalAnswered,
		Fact:     func(r *rec) bool { return r.State == records.StateAnswered && r.Swapped },
	}
}

// newBus is a membus with the shipped single-node topology plus the test
// remediation's bucket.
func newBus(t *testing.T) *membus.Bus {
	t.Helper()
	bus := membus.New(nil)
	top := events.Default().ForSingleNode()
	top.Buckets = append(top.Buckets, events.BucketSpec{
		Name: bucket, History: 1, Storage: events.StorageMemory, Replicas: 1, LimitMarkerTTL: 5 * time.Minute,
	})
	require.NoError(t, bus.Ensure(t.Context(), top))
	t.Cleanup(func() { _ = bus.Close() })
	return bus
}

func at(seq int64, state string) *rec {
	return &rec{RecordHeader: schema.RecordHeader{MediaFile: file, Seq: seq, State: state}, Inputs: "in"}
}

// seed puts r under key as another writer would, and returns its revision.
func seed(t *testing.T, bus events.Bus, r *rec) uint64 {
	t.Helper()
	cp := *r
	cp.Schema = testSchema
	b, err := json.Marshal(&cp)
	require.NoError(t, err)
	rev, err := bus.KV(bucket).Put(t.Context(), key, b)
	require.NoError(t, err)
	return rev
}

type current struct {
	rec *rec
	rev uint64
	ok  bool
}

func read(t *testing.T, bus events.Bus) current {
	t.Helper()
	r, rev, ok, err := records.NewRequester(bus.KV(bucket), spec()).Get(t.Context(), key)
	require.NoError(t, err)
	return current{r, rev, ok}
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 10, 6, 20, 0, 0, 0, time.UTC)} }

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// interleavedKV runs after once, right after the first Get through it returns:
// a second writer landing between a worker's read and its compare-and-swap.
// after must write through the underlying KV, never through this one.
type interleavedKV struct {
	events.KV
	after func()
	once  sync.Once
	gets  atomic.Int32
}

func (k *interleavedKV) Get(ctx context.Context, key string) (events.Entry, error) {
	e, err := k.KV.Get(ctx, key)
	k.gets.Add(1)
	k.once.Do(k.after)
	return e, err
}
