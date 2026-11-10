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
	"io"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/contracttest"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
)

// TestObjectStoreReadBoundedByItsContextEndsWithTheContextsError is research
// E14 at the store's own boundary, on a real server (membus has no chunks to
// purge): an object overwritten while it is read stalls the reader -- Put
// purges the old chunks, and the ordered consumer Get bound to them has
// nothing more to deliver -- until the caller's deadline, and the read then
// fails with the caller's own ctx.Err(), never nats.go's private
// nats.ErrTimeout, which nats.go reports whenever its pipe read ends after
// the context is done (and the pipe's bare "i/o timeout" when it ends
// before; the two raced about six to four). ui/art.go's readArt classifies
// the context's error as a retry, so TestArtRetriesAnObjectOverwrittenMidRead
// failed on about four reads in ten until 2026-11-10.
func TestObjectStoreReadBoundedByItsContextEndsWithTheContextsError(t *testing.T) {
	ctx := t.Context()
	bus, err := natsbus.New(connect(t))
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, bus.Close()) })
	require.NoError(t, bus.Ensure(ctx, contracttest.Topology()))
	store := bus.ObjectStore(events.BucketArtwork)

	name := "movie/uid-overwritten/poster/original"
	big := bytes.Repeat([]byte{0xAB}, 8<<20) // eight chunks of BucketArtwork's 1 MiB
	_, err = store.Put(ctx, name, bytes.NewReader(big),
		events.ObjectMeta{Headers: map[string]string{"Content-Type": "image/png"}})
	require.NoError(t, err)

	rctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_, rc, err := store.Get(rctx, name)
	require.NoError(t, err)
	defer func() { _ = rc.Close() }()
	head := make([]byte, 128<<10)
	_, err = io.ReadFull(rc, head)
	require.NoError(t, err, "the first chunk is being delivered")

	_, err = store.Put(ctx, name, bytes.NewReader([]byte("replacement")),
		events.ObjectMeta{Headers: map[string]string{"Content-Type": "image/png"}})
	require.NoError(t, err, "overwrite the object mid-read")

	started := time.Now()
	rest, err := io.ReadAll(rc)
	require.Error(t, err, "the read did not stall: E14's premise moved (%d more bytes arrived)", len(rest))
	require.ErrorIs(t, err, context.DeadlineExceeded, "the caller's own deadline, however nats.go named it")
	assert.NotErrorIs(t, err, nats.ErrTimeout, "nats.go's private sentinel never reaches a caller")
	assert.True(t, strings.HasPrefix(err.Error(), "natsbus: read "+events.BucketArtwork+"/"+name+": "), err.Error())
	assert.GreaterOrEqual(t, time.Since(started), 500*time.Millisecond, "the stall ran to the deadline")
}
