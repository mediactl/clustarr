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

package torrent

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/engine"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

// TestAddPersistsSelectionAndAgeAndReAttachRestoresBoth drives a pack
// through the real add path, a present command whose selection names two
// episodes (ADR-0019 §6.7: the selection replaces the engine's Episode
// reads): the AddRequest carries a WantFile narrowed to them, the descriptor
// records the selection and the client's AddedAt, and a restarted engine
// re-adds with both.
func TestAddPersistsSelectionAndAgeAndReAttachRestoresBoth(t *testing.T) {
	ctx := context.Background()
	fc := newFakeClient()
	e := &Engine{Client: fc, StateDir: t.TempDir()}
	_, err := e.ReAttach(ctx)
	require.NoError(t, err)
	tr := &Transfers{Engine: e}

	magnet := "magnet:?xt=urn:btih:showpack"
	owner := schema.ItemRef{Kind: "Series", Ref: schema.Ref{Namespace: "torrent-selection", Name: "show", UID: "uid-show"}}
	entry := schema.EntryRef{ID: "show-pack", UID: "uid-show-pack"}
	held, err := tr.Apply(ctx, schema.EngineCommand{
		Seq:      1,
		Owner:    owner,
		Entry:    entry,
		Desired:  schema.EngineDesiredPresent,
		Protocol: commonv1alpha1.ProtocolTorrent,
		Source:   &commonv1alpha1.DownloadSource{MagnetURL: &magnet},
		Selection: &schema.TransferSelection{
			Episodes: []schema.EpisodeNo{{Season: 1, Number: 5}, {Season: 1, Number: 3}},
		},
		Claim: schema.TransferClaim{Owner: owner, Entry: entry},
	}, nil)
	require.NoError(t, err)
	require.True(t, held.Found)

	require.Len(t, fc.addRequests, 1)
	want := fc.addRequests[0].WantFile
	require.NotNil(t, want, "a pack command must add with a file selection")
	assert.True(t, want("Show.S01/Show.S01E03.mkv", 1))
	assert.True(t, want("Show.S01/Show.S01E05.mkv", 1))
	assert.False(t, want("Show.S01/Show.S01E04.mkv", 1))

	id := held.Item.ID
	item, err := fc.Get(ctx, id)
	require.NoError(t, err)

	loaded, errs := loadDescriptors(e.StateDir)
	require.Empty(t, errs)
	require.Len(t, loaded, 1)
	require.NotNil(t, loaded[0].Desc.Selection)
	assert.Equal(t, []EpisodeNumber{{1, 3}, {1, 5}}, loaded[0].Desc.Selection.Episodes)
	assert.True(t, item.AddedAt.Equal(loaded[0].Desc.AddedAt), "the descriptor records the client's own AddedAt")

	// Restart: a fresh client and engine over the same state directory.
	restarted := newFakeClient()
	e2 := &Engine{Client: restarted, StateDir: e.StateDir}
	res, err := e2.ReAttach(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, res.Attached)
	require.Len(t, restarted.addRequests, 1)
	again := restarted.addRequests[0]
	assert.True(t, item.AddedAt.Equal(again.AddedAt), "re-attach hands the persisted age back")
	require.NotNil(t, again.WantFile, "re-attach rebuilds the selection from the descriptor")
	assert.False(t, again.WantFile("Show.S01/Show.S01E04.mkv", 1))
	assert.True(t, again.WantFile("Show.S01/Show.S01E03.mkv", 1))
}

// RemoveDetached forgets an entry's saved state by its entry uid -- both of
// its descriptor's files, so it is not re-added at the next restart -- and
// asks the client to remove it too, which it may not know; another entry's
// state stays.
func TestRemoveDetachedForgetsTheEntrysDescriptor(t *testing.T) {
	fc := newFakeClient()
	e := &Engine{Client: fc, StateDir: t.TempDir()}
	tr := &Transfers{Engine: e}
	for id, uid := range map[string]string{"id-a": "uid-a", "id-b": "uid-b"} {
		d := descriptor{Name: "entry-" + uid, Journal: engine.Journal{EntryUID: uid, Seq: 3, Desired: schema.EngineDesiredPresent}}
		require.NoError(t, saveDescriptor(e.StateDir, id, []byte("d8:announce0:e"), d))
	}

	require.NoError(t, tr.RemoveDetached(context.Background(), "uid-a", true))

	loaded, errs := loadDescriptors(e.StateDir)
	require.Empty(t, errs)
	require.Len(t, loaded, 1)
	require.Equal(t, "uid-b", loaded[0].Desc.Journal.EntryUID)
	_, err := os.Stat(torrentFileName(e.StateDir, "id-a"))
	require.ErrorIs(t, err, os.ErrNotExist)
}
