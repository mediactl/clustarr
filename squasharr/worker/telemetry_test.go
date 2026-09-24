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

package worker

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/transcode"
)

// countingKV counts the Puts reaching the bucket, and can fail them.
type countingKV struct {
	events.KV
	puts atomic.Int64
	mu   sync.Mutex
	err  error
}

func (k *countingKV) Put(ctx context.Context, key string, val []byte) (uint64, error) {
	k.puts.Add(1)
	k.mu.Lock()
	err := k.err
	k.mu.Unlock()
	if err != nil {
		return 0, err
	}
	return k.KV.Put(ctx, key, val)
}

// progressKV is the clustarr-progress bucket of a real embedded JetStream
// server, made by the default topology the services install: NATS, not the
// in-memory bus, is the oracle for a key's grammar (CLAUDE.md).
func progressKV(t *testing.T) events.KV {
	t.Helper()
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "squasharr-telemetry", Host: "127.0.0.1", Port: -1,
		JetStream: true, StoreDir: t.TempDir(), NoLog: true, NoSigs: true,
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	require.NoError(t, bus.Ensure(ctx, events.Default().ForSingleNode()))
	return bus.KV(events.BucketProgress)
}

func readTelemetry(t *testing.T, kv events.KV, uid string) schema.TranscodeProgress {
	t.Helper()
	e, err := kv.Get(context.Background(), ProgressKey(uid))
	require.NoError(t, err)
	var got schema.TranscodeProgress
	require.NoError(t, json.Unmarshal(e.Value, &got))
	return got
}

// Every key the telemetry builds is one a real NATS server takes, a
// Kubernetes UID and the escapes KVKeyToken exists for alike, and distinct
// UIDs stay distinct keys.
func TestProgressKeysAreKeysARealNATSServerAccepts(t *testing.T) {
	kv := progressKV(t)
	ctx := context.Background()
	seen := map[string]bool{}
	for _, uid := range []string{"0b5c8f5e-2f3a-4c1e-9d7a-6f1e2d3c4b5a", "", "a.b", "a_b", "..", "ü/ *>"} {
		k := ProgressKey(uid)
		require.False(t, seen[k], "uid %q collides on key %q", uid, k)
		seen[k] = true
		_, err := kv.Put(ctx, k, []byte("{}"))
		require.NoError(t, err, "NATS rejected key %q", k)
		require.NoError(t, kv.Delete(ctx, k), "NATS rejected deleting key %q", k)
	}
}

// Samples inside one interval are one write, carrying the last of them;
// nothing new is nothing written; a failed write is dropped, not retried,
// and the next sample is written again.
func TestTelemetryWritesTheLatestSampleOncePerInterval(t *testing.T) {
	kv := &countingKV{KV: progressKV(t)}
	job := schema.Ref{Namespace: "media", Name: "film-hevc", UID: "0b5c8f5e-2f3a-4c1e-9d7a-6f1e2d3c4b5a"}
	pod := &schema.Ref{Namespace: "media", Name: "film-hevc-x7k2p"}
	at := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	tel := newTelemetry(kv, time.Hour, job, pod, 10_000, func() time.Time { return at })
	ctx := context.Background()

	tel.flush(ctx)
	assert.Zero(t, kv.puts.Load(), "no sample, no write")

	tel.start(ctx)
	for i := int64(1); i <= 50; i++ {
		tel.observe(transcode.Progress{
			Frame: i, OutTimeMillis: i * 100, FPSMilli: 23976, SpeedMilli: 1250, OutputBytes: i * 1000,
		})
	}
	tel.stop(ctx)
	require.Equal(t, int64(1), kv.puts.Load(), "fifty samples inside one interval are one write")
	assert.Equal(t, schema.TranscodeProgress{
		JobRef: job, PercentMilli: 50_000, Frame: 50, FPSCentis: 2397, SpeedCentis: 125,
		OutTimeMillis: 5000, TotalMillis: 10_000, OutputBytes: 50_000, WorkerRef: pod, At: at,
	}, readTelemetry(t, kv, job.UID))
	tel.flush(ctx)
	assert.Equal(t, int64(1), kv.puts.Load(), "nothing new, nothing written")

	kv.mu.Lock()
	kv.err = errors.New("nats: timeout")
	kv.mu.Unlock()
	tel.observe(transcode.Progress{Frame: 60, OutTimeMillis: 9_999_999})
	tel.flush(ctx)
	tel.flush(ctx)
	assert.Equal(t, int64(2), kv.puts.Load(), "a failed write is dropped, not retried")

	kv.mu.Lock()
	kv.err = nil
	kv.mu.Unlock()
	tel.observe(transcode.Progress{Frame: 72, Percent: 100, OutTimeMillis: 10_000})
	tel.flush(ctx)
	got := readTelemetry(t, kv, job.UID)
	assert.Equal(t, int64(72), got.Frame)
	assert.Equal(t, int32(100_000), got.PercentMilli, "progress=end is 100%")
}

func TestTelemetryWithoutABusDoesNothing(t *testing.T) {
	tel := newTelemetry(nil, time.Second, schema.Ref{Name: "x"}, nil, 1000, time.Now)
	require.Nil(t, tel)
	ctx := context.Background()
	tel.start(ctx)
	tel.observe(transcode.Progress{Frame: 1})
	tel.stop(ctx)
}

func TestPercentMilliOf(t *testing.T) {
	assert.Equal(t, int32(0), percentMilliOf(transcode.Progress{OutTimeMillis: 500}, 0), "no duration, no percentage")
	assert.Equal(t, int32(25_000), percentMilliOf(transcode.Progress{OutTimeMillis: 250}, 1000))
	assert.Equal(t, int32(33_333), percentMilliOf(transcode.Progress{OutTimeMillis: 1}, 3))
	assert.Equal(t, int32(99_999), percentMilliOf(transcode.Progress{OutTimeMillis: 1200}, 1000), "only progress=end says 100")
	assert.Equal(t, int32(100_000), percentMilliOf(transcode.Progress{Percent: 100}, 1000))
	assert.Equal(t, int32(0), percentMilliOf(transcode.Progress{OutTimeMillis: -5}, 1000))
}

// A real encode writes its telemetry to a real clustarr-progress bucket
// under the TranscodeJob's key: bounded to one write per interval, and
// ending on the encode's final state.
func TestRunWritesTranscodeTelemetryToTheProgressBucket(t *testing.T) {
	c := requireCluster(t)
	requireFFmpeg(t)
	f := newFixture(t, c)
	kv := &countingKV{KV: progressKV(t)}

	const interval = 100 * time.Millisecond
	o := f.options()
	o.Telemetry, o.TelemetryInterval, o.PodName = kv, interval, "film-hevc-x7k2p"
	start := time.Now()
	out := f.processWith(t, c, o)
	elapsed := time.Since(start)
	require.NoError(t, out.Err)
	require.Equal(t, ExitOK, out.Code)

	tj := f.get(t, c)
	got := readTelemetry(t, kv, string(tj.UID))
	assert.Equal(t, schema.Ref{Namespace: f.ns, Name: f.job, UID: string(tj.UID)}, got.JobRef)
	require.NotNil(t, got.WorkerRef)
	assert.Equal(t, schema.Ref{Namespace: f.ns, Name: "film-hevc-x7k2p"}, *got.WorkerRef)
	assert.Equal(t, int32(100_000), got.PercentMilli, "the last write is the finished encode")
	assert.Positive(t, got.Frame)
	assert.Positive(t, got.OutputBytes)
	assert.InDelta(t, 2000, got.TotalMillis, 100, "the 2 s source (the audio stream runs a little past the video)")
	assert.False(t, got.At.IsZero())

	puts := kv.puts.Load()
	assert.Positive(t, puts)
	assert.LessOrEqual(t, puts, int64(elapsed/interval)+2, "at most one write per interval, plus the last")
}
