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

// Package seeder serves a real, known BitTorrent torrent -- a real .torrent
// file, a real BitTorrent listener seeding it, and a real BEP3 HTTP tracker
// that always answers with this seeder as the swarm -- so a real
// download.Client (pkg/download/torrent, D2-1) can complete a transfer
// against it over cluster networking. It never reaches the Internet: there
// is no DHT (nothing to bootstrap it from in an isolated cluster) and no
// upstream tracker, only the announce URL this package embeds in its own
// .torrent, pointing back at itself. The tracker tells clients to announce
// again every [ReannounceInterval] seconds rather than the usual five
// minutes, so a client that loses its connection finds the seeder again at
// once instead of stalling an e2e download (see singlePeerTracker).
//
// This mirrors test/fixtures/torznabstub's shape: a small, real server that
// speaks the real wire protocol its production counterpart parses, so a
// mistake here fails in `go test`, not twenty minutes into an e2e run.
package seeder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	anatorrent "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	httpTrackerServer "github.com/anacrolix/torrent/tracker/http/server"
	trackerServer "github.com/anacrolix/torrent/tracker/server"
)

const (
	// DefaultContentBytes clears pkg/fsops.IsSuspectedSample's 50MiB video
	// size floor (fsops.DefaultSampleMaxBytes) with roughly the same margin
	// images/Dockerfile.e2e-fixtures' clipgen stage uses for its own baked
	// clip, so a transfer completed against this seeder's default content is
	// classifiable as ClassMedia downstream rather than landing in
	// status.unmatched as a suspected sample. A caller that only needs to
	// prove the transfer mechanics -- this package's own tests -- sets
	// Config.ContentBytes much smaller.
	DefaultContentBytes int64 = 64 << 20

	// ContentName is the single file inside the fixture's torrent.
	//
	// It carries a real movie-release shape, not a bare "clustarr-fixture"
	// stem, for two independent reasons X12c (docs/superpowers/plans/
	// 2026-09-23-gap-fixes.md) proved by reading pkg/release directly, not
	// by assumption:
	//
	//   - The extension must be one pkg/fsops.MediaExtensions[KindVideo]
	//     recognises (.mkv here) or app/import/worker/fileimport's Walk
	//     classifies the download ClassOther and never even calls
	//     release.ParsePath on it -- this was the whole of the former
	//     "clustarr-fixture.bin" gap (remaining-work.md's carried defect).
	//   - pkg/release/movie.go's parseMovie requires a
	//     "[.\s_(](19|20)\d\d[.\s_)]" year token to match AT ALL (a bare
	//     "clustarr-fixture" has none), and quality tags a real
	//     QualityProfile accepts: config/e2e/quality-profile.yaml's
	//     "e2e-any" only admits qualities named in its single "HD" tier,
	//     which "Unknown" (parseQualityTags' own fallback for an
	//     untagged title) is not. ".2010.1080p.BluRay." supplies both, and
	//     is deliberately the SAME (resolution, source) pair as
	//     libraryscan_test.go's fixtureMovieFile ("Inception.2010.1080p.
	//     BluRay.x264-GROUP.mkv") -- see waitForImportOutcome's doc
	//     comment (helpers_test.go) for why that reuse is deliberate, not
	//     an oversight, and safe under this suite's sequential,
	//     alphabetical-by-file test ordering.
	//   - ".REPACK." bumps pkg/release's detected commonv1.Revision to
	//     Version 2 (release/quality.go's detectRevision), which is what
	//     lets import_test.go's TestFileImportUpgradeAttempt see a genuine
	//     pkg/quality.Upgrade verdict against fixtureMovieFile's
	//     Version-1 original -- same quality, same source, higher
	//     revision -- rather than FormatScoreNotHigher.
	ContentName = "Clustarr.Fixture.2010.1080p.BluRay.x264-CLUSTARR.REPACK.mkv"

	// pieceLength matches a real release's rough piece size; small enough
	// that hashing DefaultContentBytes at startup stays well under a second.
	pieceLength = 256 * 1024

	seedCompleteTimeout = 30 * time.Second
)

// Config configures one [Server].
type Config struct {
	// BTAddr is the BitTorrent listen address, "host:port". Empty defaults
	// to ":0" (ephemeral port, all interfaces). An explicit host (as tests
	// use, "127.0.0.1:0") also becomes the default PeerHost, since that is
	// the address this process is actually reachable on.
	BTAddr string

	// PeerHost is the IP other peers dial to reach this seeder's BitTorrent
	// listener -- e.g. a Pod's own IP from the Downward API (status.podIP)
	// in cluster. BEP3's compact peer format carries a raw IP, never a
	// hostname, so this must resolve to one concrete address. Empty uses
	// BTAddr's host if it named one, otherwise the first non-loopback IPv4
	// address this process can find.
	PeerHost string

	// HTTPAddr is the address the BEP3 tracker and the .torrent file are
	// served on. Empty defaults to ":0".
	HTTPAddr string

	// AnnounceHost is the "host:port" embedded in the served .torrent's
	// announce URL -- e.g. a Service DNS name in cluster. Empty derives it
	// from the HTTP listener's own bound address, which is only reachable
	// from outside this process when HTTPAddr named an explicit, routable
	// host (as tests do with "127.0.0.1:0"); a cluster deployment must set
	// this explicitly.
	AnnounceHost string

	// ContentBytes sizes the deterministic seeded file. <= 0 uses
	// DefaultContentBytes. Ignored when ContentPath is set: the real
	// file's own size is what gets seeded.
	ContentBytes int64

	// ContentPath, when set, is copied verbatim to become the torrent's
	// content instead of ContentBytes worth of synthesized, non-media
	// bytes -- test/fixtures/seed.BakedClipPath (a real, ffprobe-able
	// H.264/AAC clip images/Dockerfile.e2e-fixtures bakes into this same
	// image) is the intended value, and seeder_cmd.go defaults to exactly
	// that path. Empty keeps the synthetic generator this package's own
	// tests use: they run outside the fixture image, where no baked clip
	// exists, and only need to prove the BitTorrent/tracker mechanics,
	// not a real ffprobe downstream.
	ContentPath string

	// DataDir holds the seeded file and the torrent client's own state.
	// Empty uses a fresh temp directory.
	DataDir string

	Logger *slog.Logger
}

// Server seeds one known, deterministic torrent and answers BEP3 HTTP
// tracker announces for it with itself as the only peer.
type Server struct {
	cl           *anatorrent.Client
	torrentBytes []byte
	infoHash     metainfo.Hash
	peerAddr     netip.AddrPort
	httpLn       net.Listener
	httpSrv      *http.Server
	logger       *slog.Logger
	dataDir      string
}

// New seeds a deterministic file, builds its .torrent (announcing back to
// this process's own tracker) and blocks until the local client reports the
// torrent complete. The BitTorrent listener is already accepting peers when
// New returns; call Serve to start accepting the HTTP tracker/torrent-file
// requests too.
func New(cfg Config) (*Server, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	dataDir := cfg.DataDir
	if dataDir == "" {
		var err error
		dataDir, err = os.MkdirTemp("", "clustarr-seeder-*")
		if err != nil {
			return nil, fmt.Errorf("seeder: temp data dir: %w", err)
		}
	} else if err := os.MkdirAll(dataDir, 0o777); err != nil {
		return nil, fmt.Errorf("seeder: data dir: %w", err)
	}

	contentPath := filepath.Join(dataDir, ContentName)
	var size int64
	if cfg.ContentPath != "" {
		copied, err := copyContentFile(cfg.ContentPath, contentPath)
		if err != nil {
			return nil, fmt.Errorf("seeder: copy content from %s: %w", cfg.ContentPath, err)
		}
		size = copied
	} else {
		size = cfg.ContentBytes
		if size <= 0 {
			size = DefaultContentBytes
		}
		if err := writeDeterministicContent(contentPath, size); err != nil {
			return nil, err
		}
	}

	info := metainfo.Info{PieceLength: pieceLength}
	if err := info.BuildFromFilePath(contentPath); err != nil {
		return nil, fmt.Errorf("seeder: build torrent info: %w", err)
	}
	infoBytes, err := bencode.Marshal(info)
	if err != nil {
		return nil, fmt.Errorf("seeder: marshal info: %w", err)
	}

	httpAddr := cfg.HTTPAddr
	if httpAddr == "" {
		httpAddr = ":0"
	}
	httpLn, err := net.Listen("tcp", httpAddr)
	if err != nil {
		return nil, fmt.Errorf("seeder: listen http %s: %w", httpAddr, err)
	}

	announceHost := cfg.AnnounceHost
	if announceHost == "" {
		announceHost = httpLn.Addr().String()
	}

	mi := &metainfo.MetaInfo{
		InfoBytes:    infoBytes,
		Announce:     "http://" + announceHost + "/announce",
		CreationDate: time.Now().Unix(),
		CreatedBy:    "clustarr-e2e-fixtures seeder",
	}
	infoHash := mi.HashInfoBytes()

	torrentBytes, err := marshalMetaInfo(mi)
	if err != nil {
		_ = httpLn.Close()
		return nil, err
	}

	btAddr := cfg.BTAddr
	if btAddr == "" {
		btAddr = ":0"
	}
	btHost, btPortStr, err := net.SplitHostPort(btAddr)
	if err != nil {
		_ = httpLn.Close()
		return nil, fmt.Errorf("seeder: bt addr %q: %w", btAddr, err)
	}
	btPort, err := strconv.Atoi(btPortStr)
	if err != nil {
		_ = httpLn.Close()
		return nil, fmt.Errorf("seeder: bt port %q: %w", btPortStr, err)
	}

	acfg := anatorrent.NewDefaultClientConfig()
	acfg.DataDir = dataDir
	acfg.Seed = true
	// No DHT: an isolated cluster has no bootstrap node to reach, and this
	// fixture's whole point is that discovery works through the .torrent's
	// own announce URL alone. Trackers stay enabled (the zero value), which
	// is what makes that announce URL matter.
	acfg.NoDHT = true
	acfg.ListenPort = btPort
	if btHost != "" {
		acfg.ListenHost = listenHostFor(btHost)
	}

	cl, err := anatorrent.NewClient(acfg)
	if err != nil {
		_ = httpLn.Close()
		return nil, fmt.Errorf("seeder: new torrent client: %w", err)
	}

	spec, err := anatorrent.TorrentSpecFromMetaInfoErr(mi)
	if err != nil {
		cl.Close()
		_ = httpLn.Close()
		return nil, fmt.Errorf("seeder: torrent spec: %w", err)
	}
	t, _, err := cl.AddTorrentSpec(spec)
	if err != nil {
		cl.Close()
		_ = httpLn.Close()
		return nil, fmt.Errorf("seeder: add torrent: %w", err)
	}
	select {
	case <-t.Complete().On():
	case <-time.After(seedCompleteTimeout):
		cl.Close()
		_ = httpLn.Close()
		return nil, errors.New("seeder: seed torrent never reported complete")
	}

	peerHost := cfg.PeerHost
	if peerHost == "" {
		peerHost = btHost
	}
	if peerHost == "" {
		peerHost, err = detectPeerHost()
		if err != nil {
			cl.Close()
			_ = httpLn.Close()
			return nil, err
		}
	}
	peerIP, err := netip.ParseAddr(peerHost)
	if err != nil {
		cl.Close()
		_ = httpLn.Close()
		return nil, fmt.Errorf("seeder: peer host %q: %w", peerHost, err)
	}
	peerAddr := netip.AddrPortFrom(peerIP, uint16(cl.LocalPort())) //nolint:gosec // LocalPort is a uint16 port.

	mux := http.NewServeMux()
	mux.Handle("/announce", httpTrackerServer.Handler{
		Announce: &trackerServer.AnnounceHandler{AnnounceTracker: newSinglePeerTracker(infoHash, peerAddr)},
	})
	mux.HandleFunc("GET /fixture.torrent", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = w.Write(torrentBytes)
	})

	logger.Info("seeder: ready",
		"bt_addr", peerAddr.String(),
		"http_addr", httpLn.Addr().String(),
		"announce", mi.Announce,
		"info_hash", infoHash.HexString(),
		"content_bytes", size,
	)

	return &Server{
		cl:           cl,
		torrentBytes: torrentBytes,
		infoHash:     infoHash,
		peerAddr:     peerAddr,
		httpLn:       httpLn,
		httpSrv:      &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second},
		logger:       logger,
		dataDir:      dataDir,
	}, nil
}

// Serve accepts HTTP requests (the tracker and the .torrent file) until ctx
// is cancelled or the listener is closed. The BitTorrent side needs no
// separate Serve call: anacrolix's Client already accepts peers as soon as
// New returns.
func (s *Server) Serve(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() { _ = s.httpSrv.Close() })
	defer stop()

	err := s.httpSrv.Serve(s.httpLn)
	if errors.Is(err, http.ErrServerClosed) {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return nil
	}
	return err
}

// Close tears down the BitTorrent client and the HTTP listener/server. It
// does not remove DataDir.
func (s *Server) Close() error {
	s.cl.Close()
	return s.httpSrv.Close()
}

// TorrentBytes is the served .torrent file's exact body.
func (s *Server) TorrentBytes() []byte { return s.torrentBytes }

// InfoHash is the fixture torrent's info hash.
func (s *Server) InfoHash() metainfo.Hash { return s.infoHash }

// BTAddr is the address other peers dial to reach this seeder's BitTorrent
// listener.
func (s *Server) BTAddr() string { return s.peerAddr.String() }

// HTTPAddr is the bound address the tracker and .torrent file are served on.
func (s *Server) HTTPAddr() string { return s.httpLn.Addr().String() }

// marshalMetaInfo bencodes mi the same way [metainfo.MetaInfo.Write] does,
// returned as bytes rather than written to an io.Writer so callers can both
// serve it over HTTP and keep it for their own assertions without needing a
// second encode.
func marshalMetaInfo(mi *metainfo.MetaInfo) ([]byte, error) {
	b, err := bencode.Marshal(mi)
	if err != nil {
		return nil, fmt.Errorf("seeder: encode torrent: %w", err)
	}
	return b, nil
}

// copyContentFile copies src (e.g. seed.BakedClipPath) to dst verbatim and
// returns the number of bytes copied, so the caller can report it as
// Config.ContentBytes would have been. Unlike writeDeterministicContent,
// the bytes are real media -- this is what lets a real transfer against
// this seeder produce a file pkg/mediainfo can genuinely ffprobe once
// imported.
func copyContentFile(src, dst string) (int64, error) {
	in, err := os.Open(src) //nolint:gosec // fixture reads a path this package's own caller controls
	if err != nil {
		return 0, fmt.Errorf("seeder: open %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()

	out, err := os.Create(dst) //nolint:gosec // fixture writes into its own generated data dir
	if err != nil {
		return 0, fmt.Errorf("seeder: create %s: %w", dst, err)
	}
	defer func() { _ = out.Close() }()

	n, err := io.Copy(out, in)
	if err != nil {
		return 0, fmt.Errorf("seeder: copy %s -> %s: %w", src, dst, err)
	}
	if err := out.Close(); err != nil {
		return 0, fmt.Errorf("seeder: close %s: %w", dst, err)
	}
	return n, nil
}

// writeDeterministicContent streams a reproducible byte sequence to path:
// the same size always produces the same bytes, so every seeder process
// (and every test) builds byte-identical content without needing to share a
// baked asset.
func writeDeterministicContent(path string, size int64) error {
	f, err := os.Create(path) //nolint:gosec // fixture writes into its own generated data dir
	if err != nil {
		return fmt.Errorf("seeder: create content: %w", err)
	}
	defer func() { _ = f.Close() }()

	const chunkSize = 1 << 20
	buf := make([]byte, chunkSize)
	var written int64
	var seed byte
	for written < size {
		n := chunkSize
		if remaining := size - written; remaining < int64(chunkSize) {
			n = int(remaining)
		}
		for i := 0; i < n; i++ {
			buf[i] = byte((written+int64(i))*7) ^ seed
		}
		if _, err := f.Write(buf[:n]); err != nil {
			return fmt.Errorf("seeder: write content: %w", err)
		}
		written += int64(n)
		seed++
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("seeder: sync content: %w", err)
	}
	return nil
}

// listenHostFor returns anacrolix's ListenHost callback for a caller that
// named an explicit BTAddr host. anacrolix probes tcp4/udp4 and tcp6/udp6
// separately and a host of the wrong family for either probe is a hard
// listen error ("no suitable address found"), not a skip -- exactly what
// [anatorrent.LoopbackListenHost] avoids by switching on the network string.
// This does the same for an arbitrary host: match host's own family, wildcard
// (bind-all, which anacrolix's own zero-value ListenHost already does) for
// the other.
func listenHostFor(host string) func(network string) string {
	isV4 := true
	if ip := net.ParseIP(host); ip != nil {
		isV4 = ip.To4() != nil
	}
	return func(network string) string {
		if strings.Contains(network, "4") == isV4 {
			return host
		}
		return ""
	}
}

// detectPeerHost picks the first non-loopback IPv4 address this process can
// see, for a Config that named neither PeerHost nor an explicit BTAddr host.
// It is a reasonable default for a single-NIC Pod; a multi-homed one should
// set Config.PeerHost explicitly (the Downward API's status.podIP, in
// cluster).
func detectPeerHost() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", fmt.Errorf("seeder: detect peer host: %w", err)
	}
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() {
			continue
		}
		if ip4 := ipNet.IP.To4(); ip4 != nil {
			return ip4.String(), nil
		}
	}
	return "", errors.New("seeder: no non-loopback IPv4 address found; set Config.PeerHost explicitly")
}
