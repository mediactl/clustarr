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

	"github.com/mediactl/clustarr/pkg/events/schema"
)

// A remove for an entry whose transfer the client does not list -- a
// magnet still fetching its metadata, a torrent mid-verification -- found
// nothing and only wrote "removed", leaving the transfer's saved state, so
// it came back at the next restart: 28 removed torrents, 25.1 GB on /data,
// re-added and some downloading again on talos (2026-10-20). Such a remove
// now forgets the entry's state too, and its data when asked.
func TestARemoveTheClientCannotSeeForgetsTheEntrysState(t *testing.T) {
	hs := newHarness(t)
	hs.start(t)
	cmd := command(entryID, entryUID, 12, schema.EngineDesiredAbsent)
	cmd.RemoveData = true
	require.NoError(t, hs.h.Handle(context.Background(), delivery(t, cmd)))
	require.Contains(t, hs.src.Calls(), "removeDetached "+entryUID+" data=true")
	rec, ok := hs.transfer(t, entryUID)
	require.True(t, ok)
	require.Equal(t, schema.TransferStateRemoved, rec.State)
}

// At start, a transfer whose entry's record already says removed -- at the
// transfer's own seq or later -- is one such remove left behind: it is
// removed from the client, its bytes kept (deleting what a remove did not
// ask to delete is the owner's call), and its record stays removed. A
// live transfer, and one re-added after an older removal, are left alone.
func TestAStartRemovesATransferWhoseEntryWasRemoved(t *testing.T) {
	stale := journaled(entryID, entryUID, 3)
	live := journaled("other-entry", "uid-other", 4)
	readded := journaled("again-entry", "uid-again", 10)
	hs := newHarness(t, stale, live, readded)
	ctx := context.Background()
	require.NoError(t, hs.rep.WriteRemoved(ctx, command(entryID, entryUID, 9, schema.EngineDesiredAbsent)))
	require.NoError(t, hs.rep.WriteRemoved(ctx, command("again-entry", "uid-again", 5, schema.EngineDesiredAbsent)))
	hs.start(t)

	calls := hs.src.Calls()
	require.Contains(t, calls, "removeByID dl-"+entryUID+" data=false")
	require.NotContains(t, calls, "removeByID dl-uid-other data=false")
	require.NotContains(t, calls, "removeByID dl-uid-again data=false")
	rec, ok := hs.transfer(t, entryUID)
	require.True(t, ok)
	require.Equal(t, schema.TransferStateRemoved, rec.State)
	_, held := hs.src.get(entryID)
	require.False(t, held)
}
