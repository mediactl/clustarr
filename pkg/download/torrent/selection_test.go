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
	"strings"
	"testing"
	"time"

	anatorrent "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/download"
)

// testPieceLength is the piece size every multi-file fixture uses. Each
// fixture file is a whole number of pieces long, so no piece straddles two
// files and "the unwanted file got no bytes" is exact rather than "at most
// one boundary piece's worth".
const testPieceLength = 16 * 1024

type seedFile struct {
	name    string
	content []byte
}

// newMultiFileSeeder starts a bare anacrolix client seeding a multi-file
// torrent named dirName, and returns it with the .torrent payload.
func newMultiFileSeeder(t *testing.T, dirName string, files []seedFile) (*anatorrent.Client, []byte) {
	t.Helper()

	seedDir := t.TempDir()
	root := filepath.Join(seedDir, dirName)
	require.NoError(t, os.MkdirAll(root, 0o755))
	for _, f := range files {
		require.NoError(t, os.WriteFile(filepath.Join(root, f.name), f.content, 0o644))
	}

	info := metainfo.Info{PieceLength: testPieceLength}
	require.NoError(t, info.BuildFromFilePath(root))
	infoBytes, err := bencode.Marshal(info)
	require.NoError(t, err)
	var buf bytes.Buffer
	require.NoError(t, (&metainfo.MetaInfo{InfoBytes: infoBytes}).Write(&buf))
	payload := buf.Bytes()

	seeder := newRawClient(t, seedDir, true)
	mi, err := metainfo.Load(bytes.NewReader(payload))
	require.NoError(t, err)
	spec, err := anatorrent.TorrentSpecFromMetaInfoErr(mi)
	require.NoError(t, err)
	st, _, err := seeder.AddTorrentSpec(spec)
	require.NoError(t, err)
	select {
	case <-st.Complete().On():
	case <-time.After(10 * time.Second):
		t.Fatal("seeder never reported the torrent complete")
	}
	return seeder, payload
}

// newRawClient is a bare anacrolix client on loopback with no discovery,
// for fixtures and for the upstream-behaviour canaries in upstream_test.go.
func newRawClient(t *testing.T, dataDir string, seed bool) *anatorrent.Client {
	t.Helper()
	acfg := anatorrent.NewDefaultClientConfig()
	acfg.ListenHost = anatorrent.LoopbackListenHost
	acfg.ListenPort = 0
	acfg.NoDHT = true
	acfg.DisableTrackers = true
	acfg.DataDir = dataDir
	acfg.Seed = seed
	cl, err := anatorrent.NewClient(acfg)
	require.NoError(t, err)
	t.Cleanup(func() { cl.Close() })
	return cl
}

func waitCompleted(t *testing.T, c *Client, id string) download.Item {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		item, err := c.Get(context.Background(), id)
		require.NoError(t, err)
		if item.Status == download.StatusCompleted {
			return item
		}
		if time.Now().After(deadline) {
			t.Fatalf("transfer never completed, last status %q, %d/%d bytes", item.Status, item.DownloadedBytes, item.TotalBytes)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestAdd_WantFileFetchesOnlyTheSelectedFiles is the carried item's own
// case: a season pack grabbed for one missing episode. The transfer must
// report Completed once the wanted file is on disk, measure its totals over
// that file alone, list the other file as Skipped -- and must not have
// fetched a byte of it.
func TestAdd_WantFileFetchesOnlyTheSelectedFiles(t *testing.T) {
	wantedContent := bytes.Repeat([]byte("E03"), 4*testPieceLength/3+1)[:4*testPieceLength]
	otherContent := bytes.Repeat([]byte("E04"), 4*testPieceLength/3+1)[:4*testPieceLength]
	seeder, payload := newMultiFileSeeder(t, "Show.S01.1080p", []seedFile{
		{name: "Show.S01E03.1080p.mkv", content: wantedContent},
		{name: "Show.S01E04.1080p.mkv", content: otherContent},
	})
	c := newTestClient(t)
	ctx := context.Background()

	id, err := c.Add(ctx, download.AddRequest{
		Name:    "show-s01e03",
		Payload: payload,
		WantFile: func(path string, _ int64) bool {
			return strings.HasSuffix(path, "S01E03.1080p.mkv")
		},
	})
	require.NoError(t, err)

	// The selection is applied before Add returns for a payload, so even the
	// first observation is already narrowed.
	first, err := c.Get(ctx, id)
	require.NoError(t, err)
	require.Equal(t, int64(len(wantedContent)), first.TotalBytes, "totals must cover the wanted file only")

	connectPeer(t, c, id, seeder)
	item := waitCompleted(t, c, id)

	assert.Equal(t, int64(len(wantedContent)), item.TotalBytes)
	assert.Equal(t, int64(len(wantedContent)), item.DownloadedBytes)
	assert.Zero(t, item.RemainingBytes)
	assert.Equal(t, int32(100), item.ProgressPercent)
	assert.True(t, item.CanMoveFiles)

	require.Len(t, item.Files, 2, "a skipped file is still listed")
	skipped := map[string]bool{}
	for _, f := range item.Files {
		skipped[filepath.Base(f.Path)] = f.Skipped
	}
	assert.Equal(t, map[string]bool{"Show.S01E03.1080p.mkv": false, "Show.S01E04.1080p.mkv": true}, skipped)

	got, err := os.ReadFile(filepath.Join(item.ContentRoot, "Show.S01.1080p", "Show.S01E03.1080p.mkv"))
	require.NoError(t, err)
	assert.Equal(t, wantedContent, got)

	tr, _, err := c.lookup(id)
	require.NoError(t, err)
	for _, f := range tr.Files() {
		if strings.HasSuffix(f.Path(), "S01E04.1080p.mkv") {
			assert.Zero(t, f.BytesCompleted(), "the unwanted episode must not have been fetched")
		}
	}
	assert.False(t, tr.Complete().Bool(), "the torrent as a whole is incomplete; only the selection is done")
}

// TestAdd_WantFileRejectingEverythingWantsEverything proves the fallback: a
// selector that matches no file must not produce a transfer that wants
// nothing and "completes" with nothing on disk.
func TestAdd_WantFileRejectingEverythingWantsEverything(t *testing.T) {
	a := bytes.Repeat([]byte("a"), 2*testPieceLength)
	b := bytes.Repeat([]byte("b"), 2*testPieceLength)
	seeder, payload := newMultiFileSeeder(t, "Pack", []seedFile{{name: "a.mkv", content: a}, {name: "b.mkv", content: b}})
	c := newTestClient(t)
	ctx := context.Background()

	id, err := c.Add(ctx, download.AddRequest{
		Name:     "pack",
		Payload:  payload,
		WantFile: func(string, int64) bool { return false },
	})
	require.NoError(t, err)
	connectPeer(t, c, id, seeder)

	item := waitCompleted(t, c, id)
	assert.Equal(t, int64(len(a)+len(b)), item.TotalBytes)
	for _, f := range item.Files {
		assert.False(t, f.Skipped, "nothing may be skipped once the selection fell back to everything: %s", f.Path)
	}
}

// TestAdd_AddedAtAndOutputPath covers the two other Item fields this change
// set: AddedAt comes back from the request on a re-attach and defaults to
// now otherwise, and a torrent's OutputPath is its content root from the
// start (it downloads in place).
func TestAdd_AddedAtAndOutputPath(t *testing.T) {
	_, payload := newSeeder(t, []byte("content whose age matters to the reaper"))
	_, payload2 := newSeeder(t, []byte("a second, freshly added transfer"))
	c := newTestClient(t)
	ctx := context.Background()

	past := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	id, err := c.Add(ctx, download.AddRequest{Name: "reattached", Payload: payload, AddedAt: past})
	require.NoError(t, err)
	item, err := c.Get(ctx, id)
	require.NoError(t, err)
	assert.True(t, past.Equal(item.AddedAt), "a re-attach must keep the persisted age, got %v", item.AddedAt)
	assert.NotEmpty(t, item.OutputPath)
	assert.Equal(t, item.ContentRoot, item.OutputPath)

	before := time.Now()
	id2, err := c.Add(ctx, download.AddRequest{Name: "fresh", Payload: payload2})
	require.NoError(t, err)
	item2, err := c.Get(ctx, id2)
	require.NoError(t, err)
	assert.False(t, item2.AddedAt.Before(before), "a fresh Add is dated now")

	// Idempotent Add changes nothing, including the age.
	_, err = c.Add(ctx, download.AddRequest{Name: "reattached", Payload: payload, AddedAt: time.Now()})
	require.NoError(t, err)
	item, err = c.Get(ctx, id)
	require.NoError(t, err)
	assert.True(t, past.Equal(item.AddedAt))
}
