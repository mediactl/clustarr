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

package seeder_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	httpTracker "github.com/anacrolix/torrent/tracker/http"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/download"
	dltorrent "github.com/mediactl/clustarr/pkg/download/torrent"
	"github.com/mediactl/clustarr/test/fixtures/seeder"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestSeeder starts a Server bound entirely to loopback, with a small
// content size so the test stays fast, and returns it already Serve()-ing
// its HTTP side in the background.
func newTestSeeder(t *testing.T, contentBytes int64) *seeder.Server {
	t.Helper()
	srv, err := seeder.New(seeder.Config{
		BTAddr:       "127.0.0.1:0",
		HTTPAddr:     "127.0.0.1:0",
		ContentBytes: contentBytes,
		DataDir:      t.TempDir(),
		Logger:       discardLogger(),
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		_ = srv.Close()
	})
	return srv
}

func TestServerServesTheTorrentFileOverHTTP(t *testing.T) {
	srv := newTestSeeder(t, 256*1024)

	resp, err := http.Get("http://" + srv.HTTPAddr() + "/fixture.torrent") //nolint:noctx,gosec // fixture test, loopback only
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, srv.TorrentBytes(), body, "the HTTP handler must serve exactly what TorrentBytes returns")

	mi, err := metainfo.Load(bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, srv.InfoHash(), mi.HashInfoBytes(), "the served .torrent must hash to the info hash the seeder reports")
	require.Contains(t, mi.Announce, "/announce", "the .torrent must announce back to this seeder's own tracker")

	info, err := mi.UnmarshalInfo()
	require.NoError(t, err)
	require.Equal(t, seeder.ContentName, info.Name)
	require.Equal(t, int64(256*1024), info.TotalLength())
}

func TestAnnounceReturnsThisSeederAsTheOnlyPeer(t *testing.T) {
	srv := newTestSeeder(t, 64*1024)

	peerID := make([]byte, 20)
	_, err := rand.Read(peerID)
	require.NoError(t, err)

	q := url.Values{}
	q.Set("info_hash", string(srv.InfoHash().Bytes()))
	q.Set("peer_id", string(peerID))
	q.Set("port", "6881")
	q.Set("uploaded", "0")
	q.Set("downloaded", "0")
	q.Set("left", "1")
	q.Set("compact", "1")

	resp, err := http.Get("http://" + srv.HTTPAddr() + "/announce?" + q.Encode()) //nolint:noctx,gosec // fixture test, loopback only
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode, "body: %s", body)

	var httpResp httpTracker.HttpResponse
	require.NoError(t, bencode.Unmarshal(body, &httpResp))
	require.Empty(t, httpResp.FailureReason)
	require.EqualValues(t, 1, httpResp.Complete, "the seeder itself must be counted as a seeder")
	require.Len(t, httpResp.Peers.List, 1, "this fixture seeds exactly one file to exactly one peer -- itself")

	wantAddr, err := netip.ParseAddrPort(srv.BTAddr())
	require.NoError(t, err)
	gotPeer := httpResp.Peers.List[0]
	gotAddr, ok := gotPeer.ToNetipAddrPort()
	require.True(t, ok)
	require.Equal(t, wantAddr, gotAddr, "the tracker must advertise the seeder's own BitTorrent listen address")
}

func TestAnnounceForADifferentInfoHashFindsNobody(t *testing.T) {
	srv := newTestSeeder(t, 64*1024)

	other := make([]byte, 20)
	_, err := rand.Read(other)
	require.NoError(t, err)
	peerID := make([]byte, 20)
	_, err = rand.Read(peerID)
	require.NoError(t, err)

	q := url.Values{}
	q.Set("info_hash", string(other))
	q.Set("peer_id", string(peerID))
	q.Set("port", "6881")
	q.Set("left", "1")
	q.Set("compact", "1")

	resp, err := http.Get("http://" + srv.HTTPAddr() + "/announce?" + q.Encode()) //nolint:noctx,gosec // fixture test, loopback only
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var httpResp httpTracker.HttpResponse
	require.NoError(t, bencode.Unmarshal(body, &httpResp))
	require.Empty(t, httpResp.Peers.List, "a torrent this fixture never seeded must find nobody")
}

// TestRealClientCompletesATransferThroughTheTrackerAlone is the fixture's
// own proof of the behaviour it exists for: pkg/download/torrent -- the
// production client, trackers enabled, no manual peer wiring of any kind --
// discovers this seeder purely from the .torrent's announce URL and
// completes a real transfer against it. No AddClientPeer, no AddPeers: if
// discovery through the tracker were broken, this test would hang until its
// own deadline and fail, the same way a real cluster run would.
func TestRealClientCompletesATransferThroughTheTrackerAlone(t *testing.T) {
	const contentBytes = 300 * 1024
	srv := newTestSeeder(t, contentBytes)

	resp, err := http.Get("http://" + srv.HTTPAddr() + "/fixture.torrent") //nolint:noctx,gosec // fixture test, loopback only
	require.NoError(t, err)
	torrentBytes, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	raw, err := dltorrent.New(dltorrent.Config{
		DataDir: t.TempDir(),
		NoDHT:   true, // no Internet reachable from this test; only the fixture's own tracker.
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })

	id, err := raw.Add(context.Background(), download.AddRequest{Name: "seeder-fixture", Payload: torrentBytes})
	require.NoError(t, err)

	deadline := time.Now().Add(20 * time.Second)
	var item download.Item
	for time.Now().Before(deadline) {
		item, err = raw.Get(context.Background(), id)
		require.NoError(t, err)
		if item.Status == download.StatusCompleted {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	require.Equal(t, download.StatusCompleted, item.Status,
		"never completed via tracker-only discovery; last status %q, %d/%d bytes", item.Status, item.DownloadedBytes, item.TotalBytes)
	require.Equal(t, int64(contentBytes), item.DownloadedBytes)

	require.Len(t, item.Files, 1)
	got, err := os.ReadFile(filepath.Join(item.ContentRoot, filepath.FromSlash(item.Files[0].Path)))
	require.NoError(t, err)
	require.Len(t, got, contentBytes)
}
