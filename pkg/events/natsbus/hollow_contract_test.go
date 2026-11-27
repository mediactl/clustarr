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

package natsbus_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/nats-io/nats.go/jetstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
)

// hollowRecords is how many of the artwork bucket's live records name
// chunks its stream does not hold, read from the stream itself: the last
// record of every metadata subject against the chunk subjects.
func (r indexRig) hollowRecords(ctx context.Context, t *testing.T) []string {
	t.Helper()
	stream := events.ObjectStoreStream(events.BucketArtwork)
	chunks, err := r.bus.Subjects(ctx, stream, "$O."+events.BucketArtwork+".C.>")
	require.NoError(t, err)
	records, err := r.bus.Subjects(ctx, stream, "$O."+events.BucketArtwork+".M.>")
	require.NoError(t, err)
	s, err := r.js.Stream(ctx, stream)
	require.NoError(t, err)
	held := map[string]bool{}
	for _, subject := range chunks {
		held[subject] = true
	}
	var hollow []string
	for _, subject := range records {
		msg, err := s.GetLastMsgForSubject(ctx, subject)
		require.NoError(t, err)
		var rec struct {
			Name    string `json:"name"`
			NUID    string `json:"nuid"`
			Size    uint64 `json:"size"`
			Deleted bool   `json:"deleted"`
		}
		require.NoError(t, json.Unmarshal(msg.Data, &rec))
		if !rec.Deleted && rec.Size > 0 && !held["$O."+events.BucketArtwork+".C."+rec.NUID] {
			hollow = append(hollow, rec.Name)
		}
	}
	return hollow
}

// purgeChunks removes name's chunks and leaves its record, as nats.go's Put
// does to an object whose record was stored before a publish failed.
func (r indexRig) purgeChunks(ctx context.Context, t *testing.T, name string) {
	t.Helper()
	s, err := r.js.Stream(ctx, events.ObjectStoreStream(events.BucketArtwork))
	require.NoError(t, err)
	msg, err := s.GetLastMsgForSubject(ctx,
		"$O."+events.BucketArtwork+".M."+base64.URLEncoding.EncodeToString([]byte(name)))
	require.NoError(t, err)
	var rec struct {
		NUID string `json:"nuid"`
	}
	require.NoError(t, json.Unmarshal(msg.Data, &rec))
	require.NoError(t, s.Purge(ctx, jetstream.WithPurgeSubject("$O."+events.BucketArtwork+".C."+rec.NUID)))
}

// A Put that fails after its record was stored leaves no record: nats.go
// purges the chunks it wrote and returns the error, and a record left behind
// would name content the store does not hold -- an object every Info finds
// and no Get can read.
//
// The failure here is a real one: the bucket has no room for the chunk, and
// the record, a few hundred bytes, still fits. nats.go publishes both before
// it reads either answer.
func TestAFailedPutLeavesNoRecordWithoutContent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	r := newIndexRig(ctx, t)
	kept := r.put(ctx, t, "movie/kept/poster/original", 4096)

	// Leave room for records and for no chunk.
	s, err := r.js.Stream(ctx, events.ObjectStoreStream(events.BucketArtwork))
	require.NoError(t, err)
	info, err := s.Info(ctx)
	require.NoError(t, err)
	cfg := info.Config
	cfg.MaxBytes = int64(info.State.Bytes) + 16<<10
	_, err = r.js.UpdateStream(ctx, cfg)
	require.NoError(t, err)

	left := 0
	for i := range 8 {
		name := fmt.Sprintf("movie/uid-%d/poster/overlay", i)
		_, err := r.store.Put(ctx, name, bytes.NewReader(bytes.Repeat([]byte{7}, 64<<10)),
			events.ObjectMeta{Headers: map[string]string{events.HeaderContentType: "image/jpeg"}})
		require.Error(t, err, "the bucket has no room for the chunk")

		_, err = r.store.Info(ctx, name)
		if err == nil {
			left++
		} else {
			require.ErrorIs(t, err, events.ErrObjectNotFound)
		}
	}
	assert.Zero(t, left, "failed puts left records")
	assert.Empty(t, r.hollowRecords(ctx, t))

	// What was whole before is whole still.
	got, rc, err := r.store.Get(ctx, "movie/kept/poster/original")
	require.NoError(t, err)
	body, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	assert.Equal(t, kept.Digest, got.Digest)
	assert.Len(t, body, 4096)
}

// A Put that fails before it wrote anything leaves the object it would have
// replaced as it was: the look after a failure removes only a record whose
// chunks are gone.
func TestAFailedPutKeepsTheObjectItDidNotReplace(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	r := newIndexRig(ctx, t)
	const name = "series/uid/poster/overlay"
	before := r.put(ctx, t, name, 4096)

	_, err := r.store.Put(ctx, name, failingReader{}, events.ObjectMeta{})
	require.Error(t, err)

	got, rc, err := r.store.Get(ctx, name)
	require.NoError(t, err)
	body, err := io.ReadAll(rc)
	require.NoError(t, err)
	require.NoError(t, rc.Close())
	assert.Equal(t, before.Digest, got.Digest)
	assert.Len(t, body, 4096)
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("the source went away") }

// The purge removes a record whose chunks are gone, once it is older than
// the grace, and nothing else: what a failed Put's own look could not
// remove, on a stream too slow to answer it.
func TestThePurgeRemovesARecordWhoseContentIsGone(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	r := newIndexRig(ctx, t)
	const n = 12
	for i := range n {
		r.put(ctx, t, fmt.Sprintf("movie/uid-%02d/poster/overlay", i), 8192)
	}
	gone := []string{"movie/uid-03/poster/overlay", "movie/uid-09/poster/overlay"}
	for _, name := range gone {
		r.purgeChunks(ctx, t, name)
	}
	require.ElementsMatch(t, gone, r.hollowRecords(ctx, t))
	for _, name := range gone {
		_, err := r.store.Info(ctx, name)
		require.NoError(t, err, "the record is what makes the object look present")
	}

	// Inside the grace the purge leaves a record be.
	_, _, err := r.bus.PurgeOrphanChunks(ctx, events.BucketArtwork, time.Hour)
	require.NoError(t, err)
	require.ElementsMatch(t, gone, r.hollowRecords(ctx, t))

	time.Sleep(20 * time.Millisecond)
	purged, _, err := r.bus.PurgeOrphanChunks(ctx, events.BucketArtwork, 10*time.Millisecond)
	require.NoError(t, err)
	assert.Zero(t, purged, "no chunk was orphaned")
	assert.Empty(t, r.hollowRecords(ctx, t))
	for i := range n {
		name := fmt.Sprintf("movie/uid-%02d/poster/overlay", i)
		_, rc, err := r.store.Get(ctx, name)
		if name == gone[0] || name == gone[1] {
			require.ErrorIs(t, err, events.ErrObjectNotFound, name)
			continue
		}
		require.NoError(t, err, name)
		body, err := io.ReadAll(rc)
		require.NoError(t, err, name)
		require.NoError(t, rc.Close())
		assert.Len(t, body, 8192, name)
	}
}
