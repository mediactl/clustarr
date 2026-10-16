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

package engine_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// The usenet connection grant rides the transfer record, and a change of
// grant is a change worth writing (spec 2026-10-16 §4.4).
func TestTheGrantReachesTheTransferRecordAndItsFingerprint(t *testing.T) {
	h := journaled("Granted.Release", "uid-grant", 3)
	h.Item.Grant = &download.Grant{Server: "frugal", Connections: 20}
	hs := newHarness(t, h)
	hs.start(t)
	ctx := context.Background()

	require.NoError(t, hs.rep.WriteHeld(ctx, h, true))
	rec, ok := hs.transfer(t, "uid-grant")
	require.True(t, ok)
	require.Equal(t, &schema.TransferGrant{Server: "frugal", Connections: 20}, rec.Grant)

	before := len(*hs.writes)
	require.NoError(t, hs.rep.WriteHeld(ctx, h, false))
	require.Len(t, *hs.writes, before, "an unchanged grant is not a write")

	h.Item.Grant = &download.Grant{Server: "easynews", Connections: 4}
	require.NoError(t, hs.rep.WriteHeld(ctx, h, false))
	require.Greater(t, len(*hs.writes), before, "a changed grant must be written")
	rec, _ = hs.transfer(t, "uid-grant")
	require.Equal(t, "easynews", rec.Grant.Server)

	h.Item.Grant = nil
	require.NoError(t, hs.rep.WriteHeld(ctx, h, false))
	rec, _ = hs.transfer(t, "uid-grant")
	require.Nil(t, rec.Grant, "a transfer that gave its grant back carries none")
}
