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

package agentrecords_test

import (
	"os"
	"testing"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
)

const (
	seriesA = "7f3c2a10-0b6e-4c1e-9b8a-2d5e6f708192"
	seriesB = "a1b2c3d4-e5f6-4a7b-8c9d-0e1f2a3b4c5d"
	episode = "0c0ffee0-1111-4222-8333-444455556666"
)

// searchesBucket is the shipped single-node topology pruned to
// clustarr-searches and the two streams every topology must carry.
func searchesBucket(t *testing.T) events.Topology {
	t.Helper()
	def := events.Default().ForSingleNode()
	var top events.Topology
	for _, s := range def.Streams {
		if s.Name == events.StreamDLQ || s.Name == events.StreamAdvisories {
			top.Streams = append(top.Streams, s)
		}
	}
	for _, b := range def.Buckets {
		if b.Name == events.BucketSearches {
			top.Buckets = append(top.Buckets, b)
		}
	}
	require.Len(t, top.Buckets, 1, "the default topology has %s", events.BucketSearches)
	return top
}

// realBus is natsbus on an embedded JetStream server: membus has no key
// grammar, so only a real server can say a key is legal.
func realBus(t *testing.T) *natsbus.Bus {
	t.Helper()
	dir, err := os.MkdirTemp(t.TempDir(), "jetstream")
	require.NoError(t, err)
	srv, err := natsserver.NewServer(&natsserver.Options{
		ServerName: "agentrecords", Host: "127.0.0.1", Port: -1, JetStream: true, StoreDir: dir, NoLog: true, NoSigs: true,
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
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(t.Context(), searchesBucket(t)))
	return bus
}

func item(kind, uid string) *schema.ItemRef {
	return &schema.ItemRef{Kind: kind, Ref: schema.Ref{Namespace: "media", Name: kind + "-" + uid[:8], UID: uid}}
}

// seasonRecord is a season task's search record, as the catalog agent's
// search worker renders one: the Series' task, its header's Sub naming the
// season.
func seasonRecord(seriesUID string, season int32) *schema.SearchRecord {
	return &schema.SearchRecord{
		RecordHeader: schema.RecordHeader{Item: item("Series", seriesUID), Sub: schema.SeasonSub(season), Seq: 1, State: records.StateAnswered},
		Task:         seriesUID,
	}
}

// TestSeasonSearchKeyIsAValidKVKeyAndInjective (Wave S S1): two seasons and
// two series give four distinct keys, none the series' own search key, every
// one a key a real NATS server accepts; each season's record is written and
// read back under its own key.
func TestSeasonSearchKeyIsAValidKVKeyAndInjective(t *testing.T) {
	bus := realBus(t)
	kv := bus.KV(events.BucketSearches)
	w := records.NewWriter(kv, agentrecords.Searches(), "catalog-0", "v-test")
	r := records.NewReader(kv, agentrecords.Searches())

	seen := map[string]string{}
	var keys []string
	for _, series := range []string{seriesA, seriesB} {
		for _, season := range []int32{0, 1, 10} {
			k := agentrecords.SeasonSearchKey(series, season)
			what := series + " season " + schema.SeasonSub(season)
			require.True(t, events.ValidKVKey(k), "%s: %q", what, k)
			if prev, dup := seen[k]; dup {
				t.Fatalf("%s and %s share the key %q", prev, what, k)
			}
			seen[k] = what
			keys = append(keys, k)
			assert.NotEqual(t, agentrecords.SearchKey(series), k, "a season's key is not the series' own search key")

			v, err := w.Write(t.Context(), k, seasonRecord(series, season))
			require.NoError(t, err, "the real server accepts %q", k)
			require.Equal(t, records.Wrote, v)
		}
	}
	for _, series := range []string{seriesA, seriesB} {
		_, ok := seen[agentrecords.SearchKey(series)]
		require.False(t, ok)
	}

	for _, k := range keys {
		got, _, ok, err := r.Get(t.Context(), k)
		require.NoError(t, err)
		require.True(t, ok, "%s reads its own record", seen[k])
		assert.Equal(t, agentrecords.SeasonSearchKey(got.Task, seasonOf(t, got.Sub)), k)
	}
	listed, err := r.Keys(t.Context(), "")
	require.NoError(t, err)
	assert.ElementsMatch(t, keys, listed, "one record per (series, season), none overwritten")
}

// seasonOf inverts schema.SeasonSub for the assertion above.
func seasonOf(t *testing.T, sub string) int32 {
	t.Helper()
	for n := range int32(100) {
		if schema.SeasonSub(n) == sub {
			return n
		}
	}
	t.Fatalf("%q is no season sub", sub)
	return -1
}

// TestSearchRecordKeyOfCarriesTheSub (Wave S S1): Searches().KeyOf is
// (Task, Sub), so an episode's record keys as before, by its task alone, and
// a season task's record keys under its sub; neither is read or written under
// the other's key.
func TestSearchRecordKeyOfCarriesTheSub(t *testing.T) {
	sp := agentrecords.Searches()
	episodeRec := &schema.SearchRecord{
		RecordHeader: schema.RecordHeader{Item: item("Episode", episode), Seq: 1, State: records.StateAnswered},
		Task:         episode,
		Container:    item("Series", seriesA),
	}
	season := seasonRecord(seriesA, 3)
	uid, sub := sp.KeyOf(episodeRec)
	assert.Equal(t, [2]string{episode, ""}, [2]string{uid, sub})
	uid, sub = sp.KeyOf(season)
	assert.Equal(t, [2]string{seriesA, "season-3"}, [2]string{uid, sub})

	bus := membus.New(nil)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(t.Context(), events.Default().ForSingleNode()))
	kv := bus.KV(events.BucketSearches)
	w := records.NewWriter(kv, sp, "catalog-0", "v-test")
	r := records.NewReader(kv, sp)

	for _, tc := range []struct {
		name string
		key  string
		rec  *schema.SearchRecord
		ok   bool
	}{
		{"an episode's record under its task", agentrecords.SearchKey(episode), episodeRec, true},
		{"a season's record under its sub", agentrecords.SeasonSearchKey(seriesA, 3), season, true},
		{"the series' own record under its task", agentrecords.SearchKey(seriesA), &schema.SearchRecord{
			RecordHeader: schema.RecordHeader{Item: item("Series", seriesA), Seq: 1, State: records.StateAnswered},
			Task:         seriesA,
		}, true},
		{"a season's record under the series' task", agentrecords.SearchKey(seriesA), season, false},
		{"a season's record under another season", agentrecords.SeasonSearchKey(seriesA, 4), season, false},
		{"an episode's record under a season key", agentrecords.SeasonSearchKey(episode, 3), episodeRec, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := w.Write(t.Context(), tc.key, tc.rec)
			if !tc.ok {
				require.Error(t, err, "the writer refuses it")
				return
			}
			require.NoError(t, err)
			require.Equal(t, records.Wrote, v)
			got, _, ok, err := r.Get(t.Context(), tc.key)
			require.NoError(t, err)
			require.True(t, ok)
			assert.Equal(t, tc.rec.Task, got.Task)
			assert.Equal(t, tc.rec.Sub, got.Sub)
		})
	}
	listed, err := r.Keys(t.Context(), "")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{
		agentrecords.SearchKey(episode), agentrecords.SeasonSearchKey(seriesA, 3), agentrecords.SearchKey(seriesA),
	}, listed, "the series' record and its season's record live side by side")
}
