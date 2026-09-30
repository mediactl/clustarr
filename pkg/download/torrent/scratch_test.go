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
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	anatorrent "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

// scratchClient is a loopback client whose transfers download in scratch
// and publish to pub (spec.torrent.scratch, 2026-09-30).
func scratchClient(t *testing.T, scratch, pub string) *Client {
	t.Helper()
	cfg := loopbackConfig(t)
	cfg.DataDir, cfg.ScratchDir, cfg.Seed = pub, scratch, true
	raw, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	return raw.(*Client)
}

// A transfer downloads in scratch and reads Completed only once it has moved
// to publishDir, where it keeps seeding: a second leecher completes from it
// after the original seeder is gone.
func TestScratchTransferPublishesWhenComplete(t *testing.T) {
	content := []byte("a finished torrent belongs on the data volume")
	seeder, payload := newSeeder(t, content)
	scratch, pub := t.TempDir(), t.TempDir()
	c := scratchClient(t, scratch, pub)
	ctx := context.Background()

	// Seed criteria keep it uploading: with none, the goal is met at
	// completion and the engine stops seeding (seedGoalMetLocked).
	id, err := c.Add(ctx, download.AddRequest{
		Name: "movie", Category: "movies", Payload: payload,
		SeedCriteria: &commonv1alpha1.SeedCriteria{SeedTime: &metav1.Duration{Duration: time.Hour}},
	})
	require.NoError(t, err)
	item, err := c.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, filepath.Join(scratch, "movies", "movie"), item.ContentRoot, "downloads in scratch")

	connectPeer(t, c, id, seeder)
	item = waitCompleted(t, c, id)
	dest := filepath.Join(pub, "movies", "movie")
	require.Equal(t, dest, item.OutputPath)
	require.Equal(t, dest, item.ContentRoot)
	got, err := os.ReadFile(filepath.Join(dest, "greeting.txt"))
	require.NoError(t, err)
	require.Equal(t, content, got)
	_, err = os.Stat(filepath.Join(scratch, "movies", "movie"))
	require.True(t, os.IsNotExist(err), "the scratch copy is gone")

	seeder.Close()
	item, err = c.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, download.StatusCompleted, item.Status, "complete from the moved pieces, with no peer left")

	leecher := newRawClient(t, t.TempDir(), false)
	mi, err := metainfo.Load(bytes.NewReader(payload))
	require.NoError(t, err)
	spec, err := anatorrent.TorrentSpecFromMetaInfoErr(mi)
	require.NoError(t, err)
	lt, _, err := leecher.AddTorrentSpec(spec)
	require.NoError(t, err)
	lt.DownloadAll()
	require.Positive(t, lt.AddClientPeer(c.cl))
	select {
	case <-lt.Complete().On():
	case <-time.After(15 * time.Second):
		t.Fatal("a leecher never completed from the published copy")
	}
}

// A restarted engine finds a published transfer where it was published,
// complete, and does not download it again.
func TestScratchReattachFindsThePublishedTransfer(t *testing.T) {
	seeder, payload := newSeeder(t, []byte("published before the restart"))
	scratch, pub := t.TempDir(), t.TempDir()
	c := scratchClient(t, scratch, pub)
	ctx := context.Background()
	id, err := c.Add(ctx, download.AddRequest{Name: "movie", Category: "movies", Payload: payload})
	require.NoError(t, err)
	connectPeer(t, c, id, seeder)
	waitCompleted(t, c, id)
	require.NoError(t, c.Close())
	seeder.Close()

	again := scratchClient(t, scratch, pub)
	id, err = again.Add(ctx, download.AddRequest{Name: "movie", Category: "movies", Payload: payload})
	require.NoError(t, err)
	item := waitCompleted(t, again, id)
	require.Equal(t, filepath.Join(pub, "movies", "movie"), item.OutputPath)
}

// A publish that cannot write its destination is a local fault, never a
// verdict on the release, and the transfer stays in scratch.
func TestScratchPublishFailureIsALocalFault(t *testing.T) {
	seeder, payload := newSeeder(t, []byte("nowhere to go"))
	scratch, pub := t.TempDir(), t.TempDir()
	// A file where the category directory should go: the move cannot land.
	require.NoError(t, os.WriteFile(filepath.Join(pub, "movies"), []byte("x"), 0o600))
	c := scratchClient(t, scratch, pub)
	ctx := context.Background()
	id, err := c.Add(ctx, download.AddRequest{Name: "movie", Category: "movies", Payload: payload})
	require.NoError(t, err)
	connectPeer(t, c, id, seeder)

	deadline := time.Now().Add(15 * time.Second)
	var item download.Item
	for time.Now().Before(deadline) {
		item, err = c.Get(ctx, id)
		require.NoError(t, err)
		if item.Status == download.StatusFailed {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.Equal(t, download.StatusFailed, item.Status, "message: %s", item.Message)
	require.Equal(t, downloadv1alpha1.DownloadFailureWriteError, item.FailureReason)
	require.False(t, item.FailureReason.IsReleaseFault())
	_, err = os.Stat(filepath.Join(scratch, "movies", "movie", "greeting.txt"))
	require.NoError(t, err, "the scratch copy is kept")
}
