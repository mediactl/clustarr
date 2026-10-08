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

package maintenance_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"strconv"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/maintenance"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/segments"
	"github.com/mediactl/clustarr/pkg/segments/chromaprint"
	"github.com/mediactl/clustarr/pkg/segments/fingerprintmeta"
)

// natsBus is a JetStream bus on an embedded server, with the raw connection
// a test uses to write what no bus method would.
func natsBus(t *testing.T) (*natsbus.Bus, *nats.Conn) {
	t.Helper()
	dir, err := os.MkdirTemp(t.TempDir(), "jetstream")
	require.NoError(t, err)
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "clustarr-maintenance", Host: "127.0.0.1", Port: -1,
		JetStream: true, StoreDir: dir, NoLog: true, NoSigs: true,
	})
	require.NoError(t, err)
	go srv.Start()
	require.True(t, srv.ReadyForConnections(20*time.Second), "embedded NATS server did not become ready")
	t.Cleanup(srv.Shutdown)
	nc, err := nats.Connect(srv.ClientURL())
	require.NoError(t, err)
	t.Cleanup(nc.Close)
	bus, err := natsbus.New(nc)
	require.NoError(t, err)
	return bus, nc
}

// chunkSubjects is every chunk subject of bucket's stream: one per object
// whose chunks are stored, live or orphaned.
func chunkSubjects(ctx context.Context, t *testing.T, bus *natsbus.Bus, bucket string) []string {
	t.Helper()
	s, err := bus.Subjects(ctx, "OBJ_"+bucket, "$O."+bucket+".C.>")
	require.NoError(t, err)
	return s
}

// TestEveryObjectStoreIsPurgedOfOrphans (ADR-0020 §5): one sweep purges the
// orphaned chunks -- chunks no object's meta names, as a writer that died
// mid-Put leaves -- of every object store in the topology, a store added to
// it later included, and spares a Put younger than the grace and every live
// object's chunks.
func TestEveryObjectStoreIsPurgedOfOrphans(t *testing.T) {
	ctx := t.Context()
	bus, nc := natsBus(t)
	top := events.Default().ForSingleNode()
	top.ObjectStores = append(append([]events.ObjectStoreSpec(nil), top.ObjectStores...),
		events.ObjectStoreSpec{Name: "clustarr-added-later", Storage: events.StorageFile, MaxBytes: 64 << 20})
	require.NoError(t, bus.Ensure(ctx, top))
	require.GreaterOrEqual(t, len(top.ObjectStoreNames()), 3)

	js, err := jetstream.New(nc)
	require.NoError(t, err)
	body := bytes.Repeat([]byte("live"), 64<<10)
	for _, b := range top.ObjectStoreNames() {
		_, err := bus.ObjectStore(b).Put(ctx, "live", bytes.NewReader(body), events.ObjectMeta{})
		require.NoError(t, err, b)
		// A writer that died after its chunks and before its meta.
		for range 2 {
			_, err := js.Publish(ctx, "$O."+b+".C.orphanedPutOfADeadWriter", bytes.Repeat([]byte("o"), 1024))
			require.NoError(t, err, b)
		}
		require.Len(t, chunkSubjects(ctx, t, bus, b), 2, b)
	}

	young := &maintenance.ObjectStores{Admin: bus, Topology: top, Grace: time.Hour}
	require.NoError(t, young.Sweep(ctx))
	for _, b := range top.ObjectStoreNames() {
		assert.Len(t, chunkSubjects(ctx, t, bus, b), 2, "%s: chunks younger than the grace are a Put in progress", b)
	}

	time.Sleep(20 * time.Millisecond)
	m := &maintenance.ObjectStores{Admin: bus, Topology: top, Grace: 10 * time.Millisecond}
	require.NoError(t, m.Sweep(ctx))
	for _, b := range top.ObjectStoreNames() {
		assert.Len(t, chunkSubjects(ctx, t, bus, b), 1, "%s: only the live object's chunks remain", b)
		_, r, err := bus.ObjectStore(b).Get(ctx, "live")
		require.NoError(t, err, b)
		got, err := io.ReadAll(r)
		require.NoError(t, r.Close())
		require.NoError(t, err)
		assert.Equal(t, body, got, "%s: the live object is whole", b)
	}
}

// TestBackfillFingerprintMetaSetsTheCompleteMap (ADR-0021 §3, Wave N N7): the
// sweep's fingerprint audit, wired as the manager wires it, gives every
// fingerprint written before its metadata existed the complete map -- what
// its name says and the analyzer's fixed parameters, never a window length it
// cannot know -- through SetMeta, so its chunks stay as they are; a partial
// map it carried is replaced, not merged; an object already described and a
// name that is no fingerprint key are left alone; and a second sweep changes
// nothing.
func TestBackfillFingerprintMetaSetsTheCompleteMap(t *testing.T) {
	ctx := t.Context()
	bus, _ := natsBus(t)
	top := events.Default().ForSingleNode()
	require.NoError(t, bus.Ensure(ctx, top))
	store := bus.ObjectStore(events.ObjectStoreFingerprints)
	const writtenBy = "manager@v0.0.0-test"
	m := &maintenance.ObjectStores{Admin: bus, Topology: top, Audits: []maintenance.Audit{{
		Name: events.ObjectStoreFingerprints,
		Run: func(ctx context.Context) (int, error) {
			return fingerprintmeta.BackfillStore(ctx, store, writtenBy)
		},
	}}}

	put := func(name string, meta events.ObjectMeta) events.ObjectInfo {
		t.Helper()
		info, err := store.Put(ctx, name, bytes.NewReader([]byte("fingerprint of "+name)), meta)
		require.NoError(t, err)
		return info
	}
	legacy := put("abc123.start", events.ObjectMeta{})
	partial := put("abc123.end.v2", events.ObjectMeta{Metadata: map[string]string{
		events.ObjectMetaKeyWrittenBy: "segments@v0.0.0-old",
	}})
	currentMeta := fingerprintmeta.Build("def456", "start", 90, "segments@v0.0.0-new")
	current := put(segments.FingerprintKey("def456", "start"), currentMeta)
	stranger := put("not-a-fingerprint", events.ObjectMeta{})

	require.NoError(t, m.Sweep(ctx))

	complete := func(probeHash, which, version string) map[string]string {
		return map[string]string{
			"clustarr.io/meta-version":        "1",
			"clustarr.io/kind":                "fingerprint",
			"clustarr.io/probe-hash":          probeHash,
			"clustarr.io/window":              which,
			"clustarr.io/fingerprint-version": version,
			"clustarr.io/sample-rate":         strconv.Itoa(chromaprint.SampleRate),
			"clustarr.io/channels":            strconv.Itoa(fingerprintmeta.Channels),
			"clustarr.io/written-by":          writtenBy,
		}
	}
	for _, tc := range []struct {
		before events.ObjectInfo
		want   map[string]string
	}{
		{legacy, complete("abc123", "start", "1")},
		{partial, complete("abc123", "end", "2")},
		{current, currentMeta.Metadata},
		{stranger, nil},
	} {
		after, err := store.Info(ctx, tc.before.Name)
		require.NoError(t, err)
		if tc.want == nil {
			assert.Empty(t, after.Metadata, "%s is no fingerprint: left alone", tc.before.Name)
		} else {
			assert.Equal(t, tc.want, after.Metadata, "%s", tc.before.Name)
		}
		assert.Equal(t, tc.before.Digest, after.Digest, "%s: metadata only, never a re-upload", tc.before.Name)
		assert.Equal(t, tc.before.Size, after.Size, "%s", tc.before.Name)
		_, r, err := store.Get(ctx, tc.before.Name)
		require.NoError(t, err)
		b, err := io.ReadAll(r)
		require.NoError(t, r.Close())
		require.NoError(t, err)
		assert.Equal(t, "fingerprint of "+tc.before.Name, string(b))
	}

	n, err := fingerprintmeta.BackfillStore(ctx, store, writtenBy)
	require.NoError(t, err)
	assert.Zero(t, n, "a backfilled object carries its meta-version: nothing is left to backfill")
}
