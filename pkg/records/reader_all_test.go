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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/records"
)

// All answers what Keys and a Get of each key do: every record a Get reads,
// at Get's revision, and nothing a Get reads as absent.
func TestReaderAllReadsWhatGetReadsOfEveryKey(t *testing.T) {
	bus := newBus(t)
	kv := bus.KV(bucket)
	w := records.NewWriter(kv, spec(), "writer", "v1")
	uids := []string{"uid-a", "uid-b", "uid-c"}
	for i, uid := range uids {
		r := &rec{RecordHeader: schema.RecordHeader{MediaFile: schema.Ref{Namespace: "media", Name: uid, UID: uid}, Seq: int64(i + 1), State: records.StateAnswered}, Result: uid}
		_, err := w.Write(t.Context(), events.RecordKey(uid), r)
		require.NoError(t, err)
	}
	// What a Get reads as absent: bytes that are no record, a record under
	// another file's key, and a deleted key.
	_, err := kv.Put(t.Context(), events.RecordKey("uid-garbage"), []byte("{"))
	require.NoError(t, err)
	seed(t, bus, &rec{RecordHeader: schema.RecordHeader{MediaFile: schema.Ref{UID: "uid-elsewhere"}, Seq: 1, State: records.StateAnswered}})
	require.NoError(t, kv.Delete(t.Context(), events.RecordKey("uid-b")))

	reader := records.NewReader(kv, spec())
	all, err := reader.All(t.Context())
	require.NoError(t, err)

	var got []string
	for _, s := range all {
		got = append(got, s.Record.MediaFile.UID)
		want, rev, ok, err := reader.Get(t.Context(), events.RecordKey(s.Record.MediaFile.UID))
		require.NoError(t, err)
		require.True(t, ok)
		assert.Equal(t, rev, s.Revision)
		assert.Equal(t, want, s.Record)
	}
	assert.Equal(t, []string{"uid-a", "uid-c"}, got, "in revision order, without the absent")
}
