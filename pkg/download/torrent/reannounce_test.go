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
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"sync"
	"testing"
	"time"

	anatorrent "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
)

// testTracker is a BEP 3 HTTP tracker that answers every announce with the
// peers it is told to name, and an interval as short as the e2e seeder's
// (test/fixtures/seeder's ReannounceInterval), so a client that announces
// at all finds them within seconds. It records nothing about the peers
// that announce: who a client finds is decided by the test alone.
type testTracker struct {
	srv *httptest.Server

	mu    sync.Mutex
	peers []netip.AddrPort
	// fragments counts announces whose request carried a URL fragment:
	// none may, since a fragment is not part of what a tracker is sent.
	fragments int
}

func newTestTracker(t *testing.T) *testTracker {
	t.Helper()
	tr := &testTracker{}
	tr.srv = httptest.NewServer(http.HandlerFunc(tr.announce))
	t.Cleanup(tr.srv.Close)
	return tr
}

// announceURL is the URL a .torrent names to reach this tracker.
func (tr *testTracker) announceURL() string { return tr.srv.URL + "/announce" }

func (tr *testTracker) setPeers(peers ...netip.AddrPort) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	tr.peers = peers
}

func (tr *testTracker) announce(w http.ResponseWriter, r *http.Request) {
	tr.mu.Lock()
	var compact []byte
	for _, p := range tr.peers {
		ip := p.Addr().As4()
		compact = append(compact, ip[:]...)
		compact = binary.BigEndian.AppendUint16(compact, p.Port())
	}
	if r.URL.Fragment != "" || r.URL.RawFragment != "" {
		tr.fragments++
	}
	tr.mu.Unlock()
	body, err := bencode.Marshal(struct {
		Interval int32  `bencode:"interval"`
		Peers    string `bencode:"peers"`
	}{Interval: 2, Peers: string(compact)})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(body)
}

// withAnnounce is payload with announce as its tracker. The info dict is
// untouched, so the info hash is the same.
func withAnnounce(t *testing.T, payload []byte, announce string) []byte {
	t.Helper()
	mi, err := metainfo.Load(bytes.NewReader(payload))
	require.NoError(t, err)
	mi.Announce = announce
	var buf bytes.Buffer
	require.NoError(t, mi.Write(&buf))
	return buf.Bytes()
}

// loopbackPeer is a client's BitTorrent address on loopback, where every
// client in these tests listens.
func loopbackPeer(cl *anatorrent.Client) netip.AddrPort {
	return netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(cl.LocalPort())) //nolint:gosec // LocalPort is a port.
}

// trackedConfig is loopbackConfig with trackers on: a client that finds its
// peers through a .torrent's announce URL alone, as the engine does with
// the DHT off (no Internet to bootstrap it from).
func trackedConfig(t *testing.T) Config {
	t.Helper()
	cfg := loopbackConfig(t)
	cfg.DisableTrackers = false
	cfg.Seed = true
	return cfg
}

// skipUnderRace skips a test that re-adds a dropped info hash with trackers
// on. Not under the race detector: the re-added Torrent's tagged tracker URL
// is new to anacrolix, and v1.61.0 registers a new URL's tracker client by
// writing the dispatcher's trackerClients map under the client lock
// (initTrackerClient, client-tracker-announcer.go:877) while an announce in
// flight reads that map after releasing it (singleAnnounce, :555-559) -- here
// the dropped Torrent's own Stopped announce. The library's own race, there
// for every tracker URL a client first sees, not this package's; anacrolix's
// race-slowed uTP also stalled a first grab past the deadline once in about
// 25 runs, as TestProxiedClientDownloadsOverUTPThroughTheProxy records.
func skipUnderRace(t *testing.T) {
	t.Helper()
	if raceDetector {
		t.Skip("anacrolix v1.61.0 reads its tracker clients map unlocked while a new tracker URL is registered; see skipUnderRace")
	}
}

func waitCompletedWithin(t *testing.T, c *Client, id string, within time.Duration, why string) download.Item {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		item, err := c.Get(context.Background(), id)
		require.NoError(t, err)
		if item.Status == download.StatusCompleted {
			return item
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: never completed in %v, last status %q (%s), %d/%d bytes",
				why, within, item.Status, item.Message, item.DownloadedBytes, item.TotalBytes)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// The smoke test's stall (2026-10-08): a release grabbed again after the
// engine removed it -- a re-grab after a failure or removal, a redownload,
// an entry ADR-0019's convergence re-adds -- sat at "no peers connected"
// until the engine restarted. anacrolix v1.61.0's client-level announce
// dispatcher keeps a dropped torrent's (tracker, info hash) announce state
// for the life of the client and binds it to the first Torrent that
// registered it, so a re-added Torrent of the same info hash never got a
// tracker's peers. Here the leecher finds the seeder only through the
// tracker, completes, is removed with its data, and is added again in the
// same process: it must find the seeder and complete again.
func TestReAddAfterRemoveFindsItsPeersThroughTheTracker(t *testing.T) {
	skipUnderRace(t)
	tracker := newTestTracker(t)
	content := bytes.Repeat([]byte("grabbed, removed and grabbed again "), 4096)
	seeder, payload := newSeeder(t, content)
	tracker.setPeers(loopbackPeer(seeder))
	payload = withAnnounce(t, payload, tracker.announceURL())

	raw, err := New(trackedConfig(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	c := raw.(*Client)
	ctx := context.Background()
	req := download.AddRequest{Name: "movie", Category: "movies", Payload: payload}

	id, err := c.Add(ctx, req)
	require.NoError(t, err)
	waitCompletedWithin(t, c, id, 20*time.Second, "the first grab, through the tracker")

	require.NoError(t, c.Remove(ctx, id, true))

	again, err := c.Add(ctx, req)
	require.NoError(t, err)
	require.Equal(t, id, again)
	item := waitCompletedWithin(t, c, again, 20*time.Second, "the same release grabbed again in the same engine")
	require.Equal(t, int64(len(content)), item.DownloadedBytes)

	// And a third time: each re-add is announced afresh, not only the first.
	require.NoError(t, c.Remove(ctx, id, true))
	_, err = c.Add(ctx, req)
	require.NoError(t, err)
	waitCompletedWithin(t, c, id, 20*time.Second, "grabbed a third time")

	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	require.Zero(t, tracker.fragments, "an announce URL's fragment never reaches the tracker")
}

// The other drop and re-add of one info hash in the same process: a publish
// (spec.torrent.scratch) moves a finished transfer and adds it again from
// publishDir, where it seeds. It must still be announced, so a leecher the
// tracker names only afterwards -- one that knows nobody, and is reachable
// only by being dialled -- completes from the published copy.
func TestAPublishedTransferIsAnnouncedFromWhereItSeeds(t *testing.T) {
	skipUnderRace(t)
	tracker := newTestTracker(t)
	content := bytes.Repeat([]byte("published, then announced from publishDir "), 4096)
	seeder, payload := newSeeder(t, content)
	tracker.setPeers(loopbackPeer(seeder))
	payload = withAnnounce(t, payload, tracker.announceURL())

	cfg := trackedConfig(t)
	cfg.ScratchDir = t.TempDir()
	raw, err := New(cfg)
	require.NoError(t, err)
	t.Cleanup(func() { _ = raw.Close() })
	c := raw.(*Client)
	ctx := context.Background()

	// Seed criteria keep it uploading once published (with none, the goal
	// is met at completion and the client stops seeding).
	id, err := c.Add(ctx, download.AddRequest{
		Name: "movie", Category: "movies", Payload: payload,
		SeedCriteria: &commonv1alpha1.SeedCriteria{SeedTime: &metav1.Duration{Duration: time.Hour}},
	})
	require.NoError(t, err)
	item := waitCompletedWithin(t, c, id, 20*time.Second, "downloaded in scratch and published")
	require.NotEqual(t, cfg.ScratchDir, item.ContentRoot)
	seeder.Close()

	leecher := newRawClient(t, t.TempDir(), false)
	mi, err := metainfo.Load(bytes.NewReader(payload))
	require.NoError(t, err)
	spec, err := anatorrent.TorrentSpecFromMetaInfoErr(mi)
	require.NoError(t, err)
	lt, _, err := leecher.AddTorrentSpec(spec)
	require.NoError(t, err)
	lt.DownloadAll()
	tracker.setPeers(loopbackPeer(leecher))

	select {
	case <-lt.Complete().On():
	case <-time.After(20 * time.Second):
		t.Fatal("the published copy never learned of the leecher from the tracker")
	}
}

// A re-added Torrent's regular trackers carry the drop count in a fragment,
// which is a new anacrolix announce key and is never sent: the request URI
// is the original's. Anything anacrolix does not announce to through the
// dispatcher keeps its URL, and a publish's re-add replaces its own tag.
func TestTrackersForDropsTagsOnlyRegularTrackers(t *testing.T) {
	tiers := [][]string{
		{"http://tracker.example/announce", "udp://open.example:1337/announce"},
		{"https://private.example/a/passkey/announce?x=1", "wss://webtorrent.example"},
		{"http://user:pw@tracker.example/announce", "*disabled", "%zz"},
	}
	require.Equal(t, tiers, trackersForDrops(tiers, 0), "a first add announces as the .torrent says")

	got := trackersForDrops(tiers, 2)
	require.Equal(t, [][]string{
		{"http://tracker.example/announce#clustarr-reannounce-2", "udp://open.example:1337/announce#clustarr-reannounce-2"},
		{"https://private.example/a/passkey/announce?x=1#clustarr-reannounce-2", "wss://webtorrent.example"},
		{"http://user:pw@tracker.example/announce", "*disabled", "%zz"},
	}, got)
	for i, tier := range got {
		for j, tagged := range tier {
			u, err := url.Parse(tagged)
			if err != nil {
				continue
			}
			require.Equal(t, tagged, u.String(), "anacrolix keys a tracker by a URL that parses back to itself")
			orig, err := url.Parse(tiers[i][j])
			require.NoError(t, err)
			require.Equal(t, orig.RequestURI(), u.RequestURI(), "the tracker is sent what it always was")
		}
	}

	require.Equal(t, [][]string{{"http://tracker.example/announce#clustarr-reannounce-3"}},
		trackersForDrops([][]string{{"http://tracker.example/announce#clustarr-reannounce-2"}}, 3),
		"a publish re-adds from the Torrent's own tagged list")
}
