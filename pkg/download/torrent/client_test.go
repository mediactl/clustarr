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

// Internal (package torrent, not torrent_test) so tests can reach Client.cl
// to wire two in-process anacrolix clients directly to each other over
// loopback via Torrent.AddClientPeer -- there is no way to do that through
// the public download.Client interface alone, and these tests must prove
// real bytes do or do not move without any network or fixture server.
package torrent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	anatorrent "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/download"
)

// buildTestTorrent writes content to <dir>/<name> and returns the .torrent
// file bytes for it. It is this package's own minimal stand-in for
// anacrolix's internal testutil.GreetingTestTorrent, which this module
// cannot import (it lives under an "internal" path in a different module).
func buildTestTorrent(t *testing.T, dir, name string, content []byte) []byte {
	t.Helper()

	filePath := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(filePath, content, 0o644))

	info := metainfo.Info{PieceLength: 16 * 1024}
	require.NoError(t, info.BuildFromFilePath(filePath))

	infoBytes, err := bencode.Marshal(info)
	require.NoError(t, err)

	mi := &metainfo.MetaInfo{InfoBytes: infoBytes}
	var buf bytes.Buffer
	require.NoError(t, mi.Write(&buf))
	return buf.Bytes()
}

// loopbackConfig is a Config that only ever talks to peers this test adds
// explicitly: no DHT, no trackers, loopback-only listener. Tests connect two
// clients by calling Torrent.AddClientPeer, never by relying on discovery.
func loopbackConfig(t *testing.T) Config {
	t.Helper()
	return Config{
		DataDir:         t.TempDir(),
		ListenHost:      anatorrent.LoopbackListenHost,
		ListenPort:      0,
		NoDHT:           true,
		DisableTrackers: true,
	}
}

// newSeeder starts a bare anacrolix client (not wrapped by this package)
// seeding content from dir, and returns it alongside the .torrent payload a
// download.Client under test can Add.
func newSeeder(t *testing.T, content []byte) (seeder *anatorrent.Client, payload []byte) {
	t.Helper()

	seedDir := t.TempDir()
	payload = buildTestTorrent(t, seedDir, "greeting.txt", content)

	mi, err := metainfo.Load(bytes.NewReader(payload))
	require.NoError(t, err)
	spec, err := anatorrent.TorrentSpecFromMetaInfoErr(mi)
	require.NoError(t, err)

	acfg := anatorrent.NewDefaultClientConfig()
	acfg.ListenHost = anatorrent.LoopbackListenHost
	acfg.ListenPort = 0
	acfg.NoDHT = true
	acfg.DisableTrackers = true
	acfg.DataDir = seedDir
	acfg.Seed = true

	seeder, err = anatorrent.NewClient(acfg)
	require.NoError(t, err)
	t.Cleanup(func() { seeder.Close() })

	seederTorrent, _, err := seeder.AddTorrentSpec(spec)
	require.NoError(t, err)
	select {
	case <-seederTorrent.Complete().On():
	case <-time.After(10 * time.Second):
		t.Fatal("seeder never reported the torrent complete")
	}

	return seeder, payload
}

func newTestClient(t *testing.T) *Client {
	t.Helper()
	raw, err := New(loopbackConfig(t))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, raw.Close()) })
	return raw.(*Client)
}

// connectPeer attaches seeder as a direct peer of the transfer id on c, by
// address rather than DHT/tracker discovery.
func connectPeer(t *testing.T, c *Client, id string, seeder *anatorrent.Client) {
	t.Helper()
	tr, _, err := c.lookup(id)
	require.NoError(t, err)
	require.Positive(t, tr.AddClientPeer(seeder))
}

func TestAdd_IdempotentOnReturnedID(t *testing.T) {
	seeder, payload := newSeeder(t, []byte("hello from the seeder, repeated to fill a piece"))
	_ = seeder
	c := newTestClient(t)
	ctx := context.Background()

	req := download.AddRequest{Name: "movie", Payload: payload, Paused: true}

	id1, err := c.Add(ctx, req)
	require.NoError(t, err)
	require.NotEmpty(t, id1)

	// A second Add of the same payload, even with Paused now false, must
	// return the same id and must NOT restart (or un-pause) the transfer --
	// this is what makes the controller's re-attach path safe when it
	// cannot know whether the transfer is already running.
	req2 := req
	req2.Paused = false
	id2, err := c.Add(ctx, req2)
	require.NoError(t, err)
	require.Equal(t, id1, id2)

	items, err := c.List(ctx)
	require.NoError(t, err)
	require.Len(t, items, 1, "idempotent Add must not create a second transfer")

	item, err := c.Get(ctx, id1)
	require.NoError(t, err)
	require.Equal(t, download.StatusPaused, item.Status, "second Add must not un-pause the existing transfer")
}

func TestAdd_ExpectedInfoHashMismatchRejected(t *testing.T) {
	_, payload := newSeeder(t, []byte("content the indexer says one thing about"))
	c := newTestClient(t)
	ctx := context.Background()

	wrongHash := strings.Repeat("0", 40)
	_, err := c.Add(ctx, download.AddRequest{
		Name:             "movie",
		Payload:          payload,
		ExpectedInfoHash: wrongHash,
	})
	require.ErrorIs(t, err, download.ErrPayloadMismatch)

	items, err := c.List(ctx)
	require.NoError(t, err)
	require.Empty(t, items, "a rejected Add must not leave a transfer behind")
}

func TestAdd_ExpectedInfoHashMatchAccepted(t *testing.T) {
	_, payload := newSeeder(t, []byte("content the indexer describes correctly"))
	c := newTestClient(t)
	ctx := context.Background()

	mi, err := metainfo.Load(bytes.NewReader(payload))
	require.NoError(t, err)
	wantHash := mi.HashInfoBytes().HexString()

	id, err := c.Add(ctx, download.AddRequest{
		Name:             "movie",
		Payload:          payload,
		ExpectedInfoHash: wantHash,
	})
	require.NoError(t, err)
	require.Equal(t, wantHash, id)
}

func TestPause_ReportsPausedAndTransfersNothing(t *testing.T) {
	content := bytes.Repeat([]byte("seed data for the pause test "), 4096) // several pieces
	seeder, payload := newSeeder(t, content)
	c := newTestClient(t)
	ctx := context.Background()

	id, err := c.Add(ctx, download.AddRequest{Name: "paused-movie", Payload: payload, Paused: true})
	require.NoError(t, err)

	item, err := c.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, download.StatusPaused, item.Status)

	// Connect a real, complete seeder over loopback. If Pause were not
	// actually suppressing requests, data would start moving as soon as the
	// peer connection is up.
	connectPeer(t, c, id, seeder)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		item, err = c.Get(ctx, id)
		require.NoError(t, err)
		require.Equal(t, download.StatusPaused, item.Status, "must stay Paused while suspended")
		require.Zero(t, item.DownloadedBytes, "a paused transfer must fetch nothing")
		time.Sleep(50 * time.Millisecond)
	}
}

func TestResume_ActuallyTransfers(t *testing.T) {
	content := bytes.Repeat([]byte("seed data for the resume test  "), 4096)
	seeder, payload := newSeeder(t, content)
	c := newTestClient(t)
	ctx := context.Background()

	id, err := c.Add(ctx, download.AddRequest{Name: "resumable-movie", Payload: payload, Paused: true})
	require.NoError(t, err)
	connectPeer(t, c, id, seeder)

	// Give the paused transfer a moment with a connected peer, then confirm
	// resuming is what makes it move -- the contrast that gives the pause
	// test above its meaning.
	time.Sleep(200 * time.Millisecond)
	item, err := c.Get(ctx, id)
	require.NoError(t, err)
	require.Zero(t, item.DownloadedBytes)

	require.NoError(t, c.Resume(ctx, id))

	// Resuming a transfer that is already running must also be a no-op, not
	// an error -- the controller cannot know current state before calling
	// it.
	require.NoError(t, c.Resume(ctx, id))

	deadline := time.Now().Add(10 * time.Second)
	for {
		item, err = c.Get(ctx, id)
		require.NoError(t, err)
		if item.Status == download.StatusCompleted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("resumed transfer never completed, last status %q, %d/%d bytes", item.Status, item.DownloadedBytes, item.TotalBytes)
		}
		time.Sleep(50 * time.Millisecond)
	}

	require.Equal(t, int64(len(content)), item.DownloadedBytes)
	require.True(t, item.CanMoveFiles)

	// The content is really on disk at ContentRoot/File.Path, not just
	// reported as complete.
	require.Len(t, item.Files, 1)
	got, err := os.ReadFile(filepath.Join(item.ContentRoot, filepath.FromSlash(item.Files[0].Path)))
	require.NoError(t, err)
	require.Equal(t, content, got)
}

// TestGet_UnknownID proves ErrNotFound rather than a panic or a zero Item.
// mistaken for success, since the Download controller reads ErrNotFound
// specifically as "not re-attached yet".
func TestGet_UnknownID(t *testing.T) {
	c := newTestClient(t)
	_, err := c.Get(context.Background(), "0123456789abcdef0123456789abcdef01234567")
	require.ErrorIs(t, err, download.ErrNotFound)
}

func TestRemove_UnknownIDIsIdempotent(t *testing.T) {
	c := newTestClient(t)
	err := c.Remove(context.Background(), "0123456789abcdef0123456789abcdef01234567", false)
	require.ErrorIs(t, err, download.ErrNotFound)
}
