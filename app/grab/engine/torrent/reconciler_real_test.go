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
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"

	"github.com/anacrolix/generics"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	httpTrackerServer "github.com/anacrolix/torrent/tracker/http/server"
	trackerServer "github.com/anacrolix/torrent/tracker/server"
	"github.com/anacrolix/torrent/tracker/udp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	downloadac "github.com/mediactl/clustarr/api/applyconfiguration/download/download/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
	dltorrent "github.com/mediactl/clustarr/pkg/download/torrent"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/test/fixtures/seeder"
)

// newRealSeeder starts a real, loopback-only fixture seeder with a small
// content size so the test stays fast -- the same pattern
// test/fixtures/seeder/seeder_test.go uses for its own proof that
// pkg/download/torrent completes a transfer against it with zero manual peer
// wiring.
func newRealSeeder(t *testing.T, contentBytes int64) *seeder.Server {
	t.Helper()
	srv, err := seeder.New(seeder.Config{
		BTAddr:       "127.0.0.1:0",
		HTTPAddr:     "127.0.0.1:0",
		ContentBytes: contentBytes,
		DataDir:      t.TempDir(),
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

// redialTracker answers every announce the way the fixture seeder's own
// tracker does -- the seeder as the only peer -- but with a one-second
// re-announce interval instead of the HTTP tracker server's default of five
// minutes.
//
// That interval is what made TestRealLoopCompletesSeedsAndRemovesOnPolicy
// flaky under load (X9: stalled at 49152/65536 bytes for the whole 20s
// window). anacrolix takes a peer address out of its candidate set when it
// dials it and never puts it back when the connection drops
// (Torrent.openNewConns pops, Torrent.deletePeerConn does not re-add), so
// with one seeder and no DHT or PEX, the only way back to the seeder after a
// dropped connection is the next announce -- regularTrackerAnnounceDispatcher
// schedules it at the last announce plus the tracker's interval. A single
// connection dropped by a loaded machine therefore stalled the transfer for
// five minutes. With this tracker the redial happens within a second, so
// the transfer completes whether or not a connection drops on the way.
type redialTracker struct{ peer netip.AddrPort }

func (redialTracker) TrackAnnounce(context.Context, udp.AnnounceRequest, trackerServer.AnnounceAddr) error {
	return nil
}

func (redialTracker) Scrape(_ context.Context, ihs []trackerServer.InfoHash) ([]udp.ScrapeInfohashResult, error) {
	return make([]udp.ScrapeInfohashResult, len(ihs)), nil
}

func (r redialTracker) GetPeers(
	context.Context, trackerServer.InfoHash, trackerServer.GetPeersOpts, trackerServer.AnnounceAddr,
) trackerServer.ServerAnnounceResult {
	return trackerServer.ServerAnnounceResult{
		Peers:    []trackerServer.PeerInfo{{AnnounceAddr: r.peer}},
		Interval: generics.Some(int32(1)),
		Seeders:  generics.Some(int32(1)),
	}
}

// serveThroughRedialTracker serves srv's own .torrent -- the same info dict,
// so the same info hash -- with its announce URL pointed at a
// [redialTracker], and returns the URL to fetch it from. Discovery is still a
// real tracker announce and the payload is still a real HTTP fetch; only the
// re-announce interval differs from the fixture's.
func serveThroughRedialTracker(t *testing.T, srv *seeder.Server) string {
	t.Helper()
	peer, err := netip.ParseAddrPort(srv.BTAddr())
	require.NoError(t, err)

	mi, err := metainfo.Load(bytes.NewReader(srv.TorrentBytes()))
	require.NoError(t, err)

	var torrentBytes []byte
	mux := http.NewServeMux()
	mux.Handle("/announce", httpTrackerServer.Handler{
		Announce: &trackerServer.AnnounceHandler{AnnounceTracker: redialTracker{peer: peer}},
	})
	mux.HandleFunc("GET /fixture.torrent", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = w.Write(torrentBytes)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	mi.Announce = ts.URL + "/announce"
	mi.AnnounceList = nil
	torrentBytes, err = bencode.Marshal(mi)
	require.NoError(t, err)
	return ts.URL + "/fixture.torrent"
}

// TestRealLoopCompletesSeedsAndRemovesOnPolicy is D2-1's own carried item,
// settled: seed-criteria and CanBeRemoved are implemented in
// pkg/download/torrent but were untested because nothing drove a real
// controller loop against them. This is that loop -- a real anacrolix
// client (dltorrent.New), a real fixture seeder (no mocks, no manual peer
// wiring: discovery is through a real tracker announce, as in
// TestRealClientCompletesATransferThroughTheTrackerAlone, though through
// [redialTracker] rather than the seeder's own -- see it for why), and this
// package's own Reconciler driving Add, telemetry, MarkImported and the
// seed-goal-triggered Remove end to end against a real envtest apiserver.
//
// It also happens to exercise spec.source.torrentURL's HTTP fetch path for
// real (resolveSource), and closes with the managedFields assertion so a
// full run through this package's own code, not just app/grab/status's, is on
// record as respecting the split.
func TestRealLoopCompletesSeedsAndRemovesOnPolicy(t *testing.T) {
	const contentBytes = 64 * 1024
	srv := newRealSeeder(t, contentBytes)

	rawClient, err := dltorrent.New(dltorrent.Config{
		DataDir: t.TempDir(),
		NoDHT:   true, // no Internet reachable from this test; only the fixture's own tracker.
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = rawClient.Close() })

	e := &Engine{Client: rawClient, StateDir: t.TempDir()}
	_, err = e.ReAttach(context.Background())
	require.NoError(t, err)
	require.True(t, e.Ready())

	ctx := context.Background()
	c := newEnvtestClient(t)
	const ns = "torrent-real-loop"
	newTestNamespace(t, ctx, c, ns)
	mkTorrentDownloadClient(t, ctx, c, ns, "torrents")

	r := &Reconciler{
		Client:     c,
		Engine:     e,
		EngineID:   "torrents-0",
		StateDir:   e.StateDir,
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
	}

	torrentURL := serveThroughRedialTracker(t, srv)
	// A short SeedTime so the test does not sit through a real seed window;
	// Ratio is left unset so only SeedTime gates the goal (session.go's own
	// seedGoalMetLocked prefers SeedTime over PackSeedTime when both are
	// nil, and this sets neither ratio).
	seedTime := metav1.Duration{Duration: 300 * time.Millisecond}
	dl := mkTorrentDownload(t, ctx, c, ns, "seeder-fixture", "torrents-0", "torrents", func(d *downloadv1alpha1.Download) {
		d.Spec.Source = downloadv1alpha1.DownloadSource{TorrentURL: &torrentURL}
		d.Spec.SeedCriteria = &commonv1alpha1.SeedCriteria{SeedTime: &seedTime}
	})
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(dl)}

	// First reconcile: resolves torrentURL for real, Adds, persists the
	// descriptor, and applies the first telemetry snapshot.
	res, err := r.Reconcile(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, defaultPollInterval, res.RequeueAfter)

	var got downloadv1alpha1.Download
	require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
	require.NotEmpty(t, got.Status.DownloadID)
	id := got.Status.DownloadID

	// Drive Reconcile in a tight loop (bypassing the real 5s RequeueAfter,
	// which a real manager would honour but a test should not sit through)
	// until the transfer completes. The window is for a hang, not a speed
	// test, so it restarts on every byte of progress: a loaded machine may be
	// slow, and a dropped connection costs a re-announce (redialTracker), but
	// thirty seconds with nothing moving is a real stall.
	const noProgressWindow = 30 * time.Second
	deadline := time.Now().Add(noProgressWindow)
	var lastBytes int64
	for {
		_, err := r.Reconcile(ctx, req)
		require.NoError(t, err)
		require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
		if got.Status.DownloadedBytes == contentBytes {
			break
		}
		if got.Status.DownloadedBytes > lastBytes {
			lastBytes = got.Status.DownloadedBytes
			deadline = time.Now().Add(noProgressWindow)
		}
		if time.Now().After(deadline) {
			t.Fatalf("transfer made no progress for %s via the real loop; last downloadedBytes=%d/%d",
				noProgressWindow, got.Status.DownloadedBytes, contentBytes)
		}
		time.Sleep(50 * time.Millisecond)
	}
	assert.True(t, got.Status.CanMoveFiles, "a completed torrent must report CanMoveFiles")
	assert.False(t, got.Status.CanBeRemoved, "not imported yet, so CanBeRemoved must still be false")

	// importarr's file-import worker (D2-7) is the only writer of
	// status.import; simulate it exactly as app/grab/status's own test does.
	_, err = k8s.PatchStatus(ctx, c, k8s.ManagerImportarr,
		downloadac.Download(dl.Name, ns).WithStatus(downloadac.DownloadStatus().
			WithImport(downloadac.ImportState().WithState(downloadv1alpha1.ImportPhaseImported))))
	require.NoError(t, err)

	// Drive Reconcile until the seed goal is met and this engine removes the
	// transfer on policy (RemoveOnImport defaults true). No network is
	// involved here -- a 300ms seed time on a torrent already complete -- so
	// the window only has to outlast a loaded scheduler.
	deadline = time.Now().Add(30 * time.Second)
	for {
		_, err := r.Reconcile(ctx, req)
		require.NoError(t, err)
		_, getErr := rawClient.Get(ctx, id)
		if errors.Is(getErr, download.ErrNotFound) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the transfer was never removed once imported and the seed goal was met")
		}
		time.Sleep(50 * time.Millisecond)
	}

	require.NoError(t, c.Get(ctx, req.NamespacedName, &got))
	assert.True(t, got.Status.CanBeRemoved, "the last telemetry snapshot before removal must show CanBeRemoved=true")
	assert.True(t, got.Status.SeedGoalReached, "the engine must report the seed goal it removed the transfer for")
	assert.NoFileExists(t, sidecarFileName(e.StateDir, id), "the descriptor must be cleaned up once removed")

	items, err := rawClient.List(ctx)
	require.NoError(t, err)
	assert.Empty(t, items, "the client must hold no transfers once this engine removed it")

	// Finally, the same managedFields assertion this package's other tests
	// make on a synthetic Item -- here on the object a full real loop
	// produced.
	statusManagers := map[string]bool{}
	for _, entry := range got.ManagedFields {
		if entry.Subresource == "status" {
			statusManagers[entry.Manager] = true
		}
	}
	assert.True(t, statusManagers[k8s.ManagerGrabarrEngine.String()], "this engine's own applies must be recorded")
	assert.True(t, statusManagers[k8s.ManagerImportarr.String()], "the simulated importarr write must be recorded")
	assert.False(t, statusManagers[k8s.ManagerGrabarr.String()], "no controller write happened in this test")
}
