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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
)

// TestKVMaxValueSize: a real server refuses a value over a records bucket's
// MaxValueSize, so an oversized answer can never evict or truncate a record
// (loop spec §4.3, §4.15). The in-memory bus has no such limit; pkg/records
// refuses first (ErrTooLarge).
func TestKVMaxValueSize(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bus, err := natsbus.New(connect(t))
	require.NoError(t, err)
	defer func() { _ = bus.Close() }()
	require.NoError(t, bus.Ensure(ctx, events.Default().ForSingleNode()))
	kv := bus.KV(events.BucketGrafts)

	_, err = kv.Put(ctx, events.RecordKey("at-the-cap"), bytes.Repeat([]byte("x"), events.GraftsMaxValueSize))
	require.NoError(t, err, "a value at the cap is accepted")
	_, err = kv.Put(ctx, events.RecordKey("over-the-cap"), bytes.Repeat([]byte("x"), events.GraftsMaxValueSize+1))
	require.Error(t, err, "a value over MaxValueSize is refused by the server")
	_, err = kv.Get(ctx, events.RecordKey("over-the-cap"))
	require.ErrorIs(t, err, events.ErrKeyNotFound)
	e, err := kv.Get(ctx, events.RecordKey("at-the-cap"))
	require.NoError(t, err)
	require.Len(t, e.Value, events.GraftsMaxValueSize, "the record at the cap is whole")
}
