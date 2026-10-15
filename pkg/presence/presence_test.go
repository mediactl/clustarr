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

package presence_test

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/presence"
)

var t0 = time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

// clock is a fake clock a test moves by hand.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

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

// countingKV records every presence report Put and counts Keys listings.
type countingKV struct {
	events.KV
	mu   sync.Mutex
	puts []schema.AgentPresence
	keys atomic.Int32
}

func (k *countingKV) Put(ctx context.Context, key string, val []byte) (uint64, error) {
	var p schema.AgentPresence
	if err := json.Unmarshal(val, &p); err == nil {
		k.mu.Lock()
		k.puts = append(k.puts, p)
		k.mu.Unlock()
	}
	return k.KV.Put(ctx, key, val)
}

func (k *countingKV) Keys(ctx context.Context) ([]string, error) {
	k.keys.Add(1)
	return k.KV.Keys(ctx)
}

func (k *countingKV) written() []schema.AgentPresence {
	k.mu.Lock()
	defer k.mu.Unlock()
	return append([]schema.AgentPresence(nil), k.puts...)
}

func progress(t *testing.T) events.KV {
	t.Helper()
	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(t.Context(), events.Default().ForSingleNode()))
	return bus.KV(events.BucketProgress)
}

// run starts w until the test ends or stop is called; stop waits for Run to
// return.
func run(t *testing.T, w *presence.Writer) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx) }()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			cancel()
			require.NoError(t, <-done, "Run returns nil when its context ends")
		})
	}
	t.Cleanup(stop)
	return stop
}

func present(t *testing.T, kv events.KV, key string) bool {
	t.Helper()
	_, err := kv.Get(context.Background(), key)
	if err != nil {
		require.ErrorIs(t, err, events.ErrKeyNotFound)
		return false
	}
	return true
}

// TestPresenceWritesAtStartAndEveryInterval (ADR-0019 §5.3): a Writer puts
// its report at start, not one interval later, again every Interval with a
// fresh timestamp and fresh capabilities, rebuilds its key within an interval
// of its loss, and deletes it when it stops.
func TestPresenceWritesAtStartAndEveryInterval(t *testing.T) {
	assert.Equal(t, 30*time.Second, presence.Interval)
	require.Error(t, (&presence.Writer{}).Run(t.Context()), "a writer with no bucket, domain or pod cannot run")

	t.Run("at start", func(t *testing.T) {
		kv := &countingKV{KV: progress(t)}
		c := &clock{t: t0}
		var asked atomic.Int32
		w := &presence.Writer{
			KV: kv, Domain: "torrent-engine", Pod: "engine-0", Node: "node-a", Version: "v-test",
			Slots: map[string]int{"durable-a": 2}, Durables: []string{"durable-a"},
			Capabilities: func() map[string]string { asked.Add(1); return map[string]string{"data": "mounted"} },
			Now:          c.Now, Interval: time.Hour,
		}
		assert.Equal(t, events.PresenceKey("torrent-engine", "engine-0"), w.Key())
		stop := run(t, w)
		require.Eventually(t, func() bool { return len(kv.written()) == 1 }, 2*time.Second, 5*time.Millisecond,
			"the first write is at start, not an interval later")
		e, err := kv.Get(t.Context(), w.Key())
		require.NoError(t, err)
		var got schema.AgentPresence
		require.NoError(t, json.Unmarshal(e.Value, &got))
		assert.Equal(t, schema.AgentPresence{
			Domain: "torrent-engine", Pod: "engine-0", Node: "node-a", Version: "v-test",
			Slots: map[string]int{"durable-a": 2}, Durables: []string{"durable-a"},
			Capabilities: map[string]string{"data": "mounted"}, At: t0,
		}, got)
		time.Sleep(100 * time.Millisecond)
		assert.Len(t, kv.written(), 1, "nothing more before the interval")
		assert.EqualValues(t, 1, asked.Load())

		stop()
		assert.False(t, present(t, kv, w.Key()), "a stopped writer deletes its key")
	})

	t.Run("every interval", func(t *testing.T) {
		kv := &countingKV{KV: progress(t)}
		c := &clock{t: t0}
		var asked atomic.Int32
		w := &presence.Writer{
			KV: kv, Domain: "catalog", Pod: "catalog-0",
			Capabilities: func() map[string]string { asked.Add(1); return map[string]string{"providers": "tmdb"} },
			Now: func() time.Time {
				now := c.Now()
				c.Add(time.Second)
				return now
			},
			Interval: 50 * time.Millisecond,
		}
		stop := run(t, w)
		require.Eventually(t, func() bool { return len(kv.written()) >= 4 }, 5*time.Second, 5*time.Millisecond)
		puts := kv.written()
		for i := 1; i < len(puts); i++ {
			assert.True(t, puts[i].At.After(puts[i-1].At), "write %d is stamped anew", i)
		}
		assert.GreaterOrEqual(t, int(asked.Load()), 4, "capabilities are asked at every write")

		// A key lost under a live writer (an expired TTL, a purge) is back
		// within an interval.
		require.NoError(t, kv.Delete(t.Context(), w.Key()))
		require.Eventually(t, func() bool { return present(t, kv, w.Key()) }, 2*time.Second, 5*time.Millisecond)

		stop()
		assert.False(t, present(t, kv, w.Key()), "a stopped writer deletes its key")
	})
}

// TestThePresenceReaderCachesForFiveSeconds (ADR-0019 §5.3, §8.1): the
// manager's Reader lists the bucket once and serves every call from that read
// for five seconds -- a pod that came or went meanwhile is seen only after --
// returns a domain's reports sorted by pod, and ignores keys that are no
// presence report.
func TestThePresenceReaderCachesForFiveSeconds(t *testing.T) {
	assert.Equal(t, 5*time.Second, presence.DefaultReaderTTL)
	kv := &countingKV{KV: progress(t)}
	c := &clock{t: t0}
	writer := func(domain, pod string, caps map[string]string) *presence.Writer {
		return &presence.Writer{
			KV: kv.KV, Domain: domain, Pod: pod, Interval: time.Hour, Now: c.Now,
			Capabilities: func() map[string]string { return caps },
		}
	}
	wait := func(w *presence.Writer) {
		t.Helper()
		require.Eventually(t, func() bool { return present(t, kv.KV, w.Key()) }, 2*time.Second, 5*time.Millisecond)
	}
	pods := func(ps []schema.AgentPresence) []string {
		out := []string{}
		for _, p := range ps {
			out = append(out, p.Pod)
		}
		return out
	}

	b := writer("catalog", "catalog-b", map[string]string{"providers": "tmdb"})
	stopB := run(t, b)
	wait(b)
	e := writer("torrent-engine", "engine-0", map[string]string{"data": "mounted"})
	run(t, e)
	wait(e)
	// Keys in the bucket that are no presence report: telemetry, and a
	// presence key whose value does not decode.
	_, err := kv.KV.Put(t.Context(), "transfer.uid-1", []byte(`{"downloaded":1}`))
	require.NoError(t, err)
	_, err = kv.KV.Put(t.Context(), events.PresenceKey("catalog", "garbage"), []byte("{not json"))
	require.NoError(t, err)

	r := &presence.Reader{KV: kv, Now: c.Now}
	ps, err := r.Domain(t.Context(), "catalog")
	require.NoError(t, err)
	assert.Equal(t, []string{"catalog-b"}, pods(ps))
	assert.EqualValues(t, 1, kv.keys.Load())

	a := writer("catalog", "catalog-a", map[string]string{"providers": "tvdb"})
	run(t, a)
	wait(a)
	stopB()
	require.False(t, present(t, kv.KV, b.Key()))

	c.Add(presence.DefaultReaderTTL - time.Millisecond)
	ps, err = r.Domain(t.Context(), "catalog")
	require.NoError(t, err)
	assert.Equal(t, []string{"catalog-b"}, pods(ps), "inside five seconds the first read is served")
	ok, err := r.Present(t.Context(), "torrent-engine")
	require.NoError(t, err)
	assert.True(t, ok, "a domain whose token is escaped is found by its name")
	ok, err = r.Capable(t.Context(), "torrent-engine", "data")
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = r.Capable(t.Context(), "torrent-engine", "ffgo")
	require.NoError(t, err)
	assert.False(t, ok, "a capability no pod reports")
	ok, err = r.Present(t.Context(), "import")
	require.NoError(t, err)
	assert.False(t, ok)
	assert.EqualValues(t, 1, kv.keys.Load(), "one listing served every call")

	c.Add(time.Millisecond)
	ps, err = r.Domain(t.Context(), "catalog")
	require.NoError(t, err)
	assert.Equal(t, []string{"catalog-a"}, pods(ps), "after five seconds the bucket is read again")
	assert.EqualValues(t, 2, kv.keys.Load())
	assert.Equal(t, map[string]string{"providers": "tvdb"}, ps[0].Capabilities)

	run(t, b)
	wait(b)
	short := &presence.Reader{KV: kv, Now: c.Now, TTL: time.Second}
	ps, err = short.Domain(t.Context(), "catalog")
	require.NoError(t, err)
	assert.Equal(t, []string{"catalog-a", "catalog-b"}, pods(ps), "sorted by pod")
	ps[0].Pod = "mutated"
	ps, err = short.Domain(t.Context(), "catalog")
	require.NoError(t, err)
	assert.Equal(t, []string{"catalog-a", "catalog-b"}, pods(ps), "a caller's copy is its own")
}

// A per-node domain's pods size their own slots from their nodes, so one
// domain's reports differ (markers: two on a 4-CPU node, eight on a 16-CPU
// one). The reader keeps each pod's own, and the manager's dispatch, which
// reads only Present (its Budget is the durable's MaxAckPending, never a
// pod's slots), sees the domain attended.
func TestADomainsPodsReportTheirOwnSlots(t *testing.T) {
	kv := progress(t)
	for _, w := range []*presence.Writer{
		{
			KV: kv, Domain: "markers", Pod: "markers-small", Node: "worker-4a", Interval: time.Hour,
			Slots: map[string]int{events.ConsumerMarkersAnalyze: 2}, Durables: []string{events.ConsumerMarkersAnalyze},
		},
		{
			KV: kv, Domain: "markers", Pod: "markers-big", Node: "worker-16", Interval: time.Hour,
			Slots: map[string]int{events.ConsumerMarkersAnalyze: 8}, Durables: []string{events.ConsumerMarkersAnalyze},
		},
	} {
		run(t, w)
		require.Eventually(t, func() bool { return present(t, kv, w.Key()) }, 2*time.Second, 5*time.Millisecond)
	}
	r := &presence.Reader{KV: kv}
	ps, err := r.Domain(t.Context(), "markers")
	require.NoError(t, err)
	got := map[string]int{}
	for _, p := range ps {
		got[p.Pod] = p.Slots[events.ConsumerMarkersAnalyze]
	}
	assert.Equal(t, map[string]int{"markers-big": 8, "markers-small": 2}, got)
	ok, err := r.Present(t.Context(), "markers")
	require.NoError(t, err)
	assert.True(t, ok)
}

// apiTypes is the one package through which pkg/events/schema names
// Kubernetes types (k8s.io/apimachinery's metav1 and resource): types, not a
// client.
const apiTypes = "github.com/mediactl/clustarr/api/common/v1alpha1"

// TestPresenceLinksNoKubernetes: cmd/markers links pkg/presence and links no
// Kubernetes client (ADR-0019 §5.3), so presence imports pkg/events, its
// schema and the context logger only, and every k8s.io or sigs.k8s.io package
// it links comes through the schema's API types.
func TestPresenceLinksNoKubernetes(t *testing.T) {
	out, err := exec.Command("go", "list", "-f", `{{join .Imports "\n"}}`, ".").Output()
	require.NoError(t, err)
	for _, imp := range strings.Fields(string(out)) {
		if !strings.Contains(strings.SplitN(imp, "/", 2)[0], ".") {
			continue // the standard library
		}
		assert.Contains(t, []string{
			"github.com/mediactl/clustarr/pkg/events", "github.com/mediactl/clustarr/pkg/events/schema",
			"github.com/mediactl/clustarr/pkg/obs/logging",
		}, imp, "pkg/presence imports %s", imp)
	}

	deps := goListDeps(t, ".")
	require.Contains(t, deps, "github.com/mediactl/clustarr/pkg/events", "go list -deps read the package")
	allowed := goListDeps(t, apiTypes)
	for dep := range deps {
		for _, banned := range []string{
			"sigs.k8s.io/controller-runtime", "k8s.io/client-go", "k8s.io/api", "github.com/prometheus/client_golang",
			"github.com/mediactl/ffgo", "github.com/ebitengine/purego",
		} {
			if dep == banned || strings.HasPrefix(dep, banned+"/") {
				t.Errorf("pkg/presence links %s", dep)
			}
		}
		if (strings.HasPrefix(dep, "k8s.io/") || strings.HasPrefix(dep, "sigs.k8s.io/")) && !allowed[dep] {
			t.Errorf("pkg/presence links %s, which %s does not bring", dep, apiTypes)
		}
	}
}

func goListDeps(t *testing.T, pkg string) map[string]bool {
	t.Helper()
	out, err := exec.Command("go", "list", "-deps", pkg).Output()
	require.NoError(t, err)
	deps := map[string]bool{}
	for _, d := range strings.Fields(string(out)) {
		deps[d] = true
	}
	return deps
}
