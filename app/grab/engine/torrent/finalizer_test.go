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
	"errors"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/app/grab/engine"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func readyEngine(t *testing.T, fc *fakeClient) *Engine {
	t.Helper()
	e := &Engine{Client: fc, StateDir: t.TempDir()}
	_, err := e.ReAttach(context.Background())
	require.NoError(t, err)
	return e
}

// TestEngineFinalizerIsHeldWhileRemoveFails is the ordering half of ruling
// R-6: the engine finalizer comes off only AFTER the transfer is removed. A
// Remove that fails must leave it on, so the Download controller's
// removeDataOnDelete finalizer -- which waits for this one -- does not
// delete files the transfer still holds open.
func TestEngineFinalizerIsHeldWhileRemoveFails(t *testing.T) {
	ctx := context.Background()
	c := newEnvtestClient(t)
	const ns = "torrent-finalizer-held"
	newTestNamespace(t, ctx, c, ns)
	mkTorrentDownloadClient(t, ctx, c, ns, "torrents")

	fc := newFakeClient()
	e := readyEngine(t, fc)
	r := &Reconciler{Client: c, Engine: e, EngineID: "torrents-0", StateDir: e.StateDir}

	dl := mkTorrentDownload(t, ctx, c, ns, "movie-held", "torrents-0", "torrents", nil)
	key := client.ObjectKeyFromObject(dl)
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, key, dl))
	id := dl.Status.DownloadID
	require.NotEmpty(t, id)
	require.FileExists(t, sidecarFileName(e.StateDir, id))

	require.NoError(t, c.Delete(ctx, dl))
	fc.removeErr = errors.New("client wedged")
	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
	require.Error(t, err)

	var held downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, key, &held), "the engine finalizer must hold the object while the transfer is live")
	assert.Equal(t, []string{engine.Finalizer}, held.Finalizers)

	fc.removeErr = nil
	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
	require.NoError(t, err)
	err = c.Get(ctx, key, &downloadv1alpha1.Download{})
	assert.True(t, apierrors.IsNotFound(err), "with the transfer removed and no other finalizer, the Download is gone")
	assert.NoFileExists(t, sidecarFileName(e.StateDir, id), "the re-attach descriptor goes with the transfer")
	_, err = fc.Get(ctx, id)
	assert.ErrorIs(t, err, download.ErrNotFound)
}

// TestEngineFinalizerIsDroppedForADownloadThatNeverGotATransfer: the
// finalizer is added before the Add, so an Add that never succeeded still
// leaves it on; deletion must release it rather than wait for a transfer
// that does not exist.
func TestEngineFinalizerIsDroppedForADownloadThatNeverGotATransfer(t *testing.T) {
	ctx := context.Background()
	c := newEnvtestClient(t)
	const ns = "torrent-finalizer-noxfer"
	newTestNamespace(t, ctx, c, ns)
	mkTorrentDownloadClient(t, ctx, c, ns, "torrents")

	fc := newFakeClient()
	fc.addErr = errors.New("indexer down")
	e := readyEngine(t, fc)
	r := &Reconciler{Client: c, Engine: e, EngineID: "torrents-0", StateDir: e.StateDir}

	dl := mkTorrentDownload(t, ctx, c, ns, "movie-noxfer", "torrents-0", "torrents", nil)
	key := client.ObjectKeyFromObject(dl)
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
	require.Error(t, err)
	require.NoError(t, c.Get(ctx, key, dl))
	require.Equal(t, []string{engine.Finalizer}, dl.Finalizers, "the finalizer precedes the Add")
	require.Empty(t, dl.Status.DownloadID)

	require.NoError(t, c.Delete(ctx, dl))
	_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
	require.NoError(t, err)
	err = c.Get(ctx, key, &downloadv1alpha1.Download{})
	assert.True(t, apierrors.IsNotFound(err))
	assert.Empty(t, fc.removeCalls, "there was no transfer to remove")
}

// TestAddPersistsSelectionAndAgeAndReAttachRestoresBoth drives a pack
// Download through the real add path with an EpisodeReader: the AddRequest
// carries a WantFile narrowed to the targeted episode, the descriptor
// records the selection and the client's AddedAt, and a restarted engine
// re-adds with both.
func TestAddPersistsSelectionAndAgeAndReAttachRestoresBoth(t *testing.T) {
	ctx := context.Background()
	c := newEnvtestClient(t)
	const ns = "torrent-selection"
	newTestNamespace(t, ctx, c, ns)
	mkTorrentDownloadClient(t, ctx, c, ns, "torrents")

	fc := newFakeClient()
	e := readyEngine(t, fc)
	r := &Reconciler{
		Client: c, Engine: e, EngineID: "torrents-0", StateDir: e.StateDir,
		EpisodeReader: episodeReader(episode(ns, "show-s01e03", 1, 3, nil), episode(ns, "show-s01e05", 1, 5, nil)),
	}

	dl := mkTorrentDownload(t, ctx, c, ns, "show-pack", "torrents-0", "torrents", func(d *downloadv1alpha1.Download) {
		d.Spec.Target = commonv1alpha1.MediaRef{
			Kind: commonv1alpha1.MediaKindSeries, Name: "show", Keys: []string{"show-s01e03", "show-s01e05"},
		}
	})
	key := client.ObjectKeyFromObject(dl)
	_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
	require.NoError(t, err)

	require.Len(t, fc.addRequests, 1)
	want := fc.addRequests[0].WantFile
	require.NotNil(t, want, "a pack Download must add with a file selection")
	assert.True(t, want("Show.S01/Show.S01E03.mkv", 1))
	assert.True(t, want("Show.S01/Show.S01E05.mkv", 1))
	assert.False(t, want("Show.S01/Show.S01E04.mkv", 1))

	require.NoError(t, c.Get(ctx, key, dl))
	id := dl.Status.DownloadID
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

// TestReaperReapsAnOldOrphanOnTheFirstPassAfterARestart is the carried
// item's fix, end to end: an orphan whose descriptor says it was added an
// hour ago is re-attached by a restarted engine and reaped on the reaper's
// FIRST pass, without a fresh grace period -- and its descriptor goes with
// it, so the next restart does not bring it back.
func TestReaperReapsAnOldOrphanOnTheFirstPassAfterARestart(t *testing.T) {
	ctx := context.Background()
	stateDir := t.TempDir()
	payload := []byte("an orphaned transfer's metainfo")
	id := fakeID(download.AddRequest{Payload: payload})
	require.NoError(t, saveDescriptor(stateDir, id, payload, descriptor{
		Name: "gone", Category: "movies", AddedAt: time.Now().Add(-time.Hour),
	}))

	fc := newFakeClient()
	e := &Engine{Client: fc, StateDir: stateDir}
	_, err := e.ReAttach(ctx)
	require.NoError(t, err)

	reaper := &Reaper{
		Client:      fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build(), // no Downloads at all
		Engine:      e,
		EngineID:    "torrents-0",
		OrphanGrace: 10 * time.Minute,
	}
	require.NoError(t, reaper.ReapOnce(ctx))

	_, err = fc.Get(ctx, id)
	assert.ErrorIs(t, err, download.ErrNotFound, "an orphan older than the grace is reaped on the first pass after a restart")
	_, statErr := os.Stat(sidecarFileName(stateDir, id))
	assert.True(t, os.IsNotExist(statErr), "the reaped transfer's descriptor must go too")
	_, statErr = os.Stat(torrentFileName(stateDir, id))
	assert.True(t, os.IsNotExist(statErr))
}
