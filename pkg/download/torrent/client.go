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

// Package torrent implements [download.Client] over
// github.com/anacrolix/torrent -- the BitTorrent half of D2's two engines.
//
// [Client] wraps one embedded anacrolix *torrent.Client and adds the
// bookkeeping anacrolix has no equivalent for: whether a transfer was told
// to pause (anacrolix exposes no getter for its own dataDownloadDisallowed
// flag), whether it has been imported, and the seed criteria to measure it
// against. That bookkeeping lives in [session], one per transfer, keyed by
// the same lowercase-hex v1 info hash this package uses as
// [download.Item.ID].
package torrent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	anatorrent "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// engineVersion is reported on [download.Info.Version]. It names the pinned
// github.com/anacrolix/torrent module version rather than being computed at
// runtime, because [download.Info.Version] is documented as "for logs and
// status messages only; nothing branches on it" -- not worth a
// debug.ReadBuildInfo lookup on every DownloadClient reconcile.
const engineVersion = "v1.61.0"

// Config configures [New]. It is this package's own type, not a Kubernetes
// API type, so unlike a CRD-derived client its zero values are deliberate
// rather than a gap to defend against: Seed defaults false because a library
// that silently starts uploading data without being told to is the wrong
// default for a caller that has not yet decided its seeding policy from
// DownloadClient.spec.
type Config struct {
	// DataDir is the root directory transfers are stored under. Required.
	// Add joins it with AddRequest.Category and AddRequest.Name to get each
	// transfer's own directory; see [Client.Add].
	DataDir string

	// ListenHost overrides the interface anacrolix listens on. Nil uses
	// anacrolix's own default (every interface). Tests set this to
	// anatorrent.LoopbackListenHost so two in-process clients only ever see
	// each other, never a real network.
	ListenHost func(network string) string

	// ListenPort is the TCP/UDP port to listen on. 0 picks an ephemeral
	// port, which is what a test wants; production sets an explicit port so
	// a restarted engine keeps the port peers already know about it by.
	ListenPort int

	// NoDHT disables DHT peer discovery. Tests set this true and connect
	// peers directly; production leaves it false.
	NoDHT bool

	// DisableTrackers disables tracker announces, for the same reason as
	// NoDHT.
	DisableTrackers bool

	// Seed keeps a completed torrent uploading -- and reachable for
	// AddClientPeer-style direct connections -- once it finishes. See the
	// Config doc comment for why this has no implicit default.
	Seed bool

	// Debug turns on anacrolix's own verbose logging.
	Debug bool

	// Logger, if set, receives anacrolix's internal log lines. anacrolix
	// takes a single *slog.Logger at construction rather than one per call,
	// so it cannot ride the request context the way this package's own
	// method-level logging (via pkg/obs/logging.FromContext) does; this
	// field is the explicit, constructor-time equivalent. Nil leaves
	// anacrolix's own non-slog logging path in place.
	Logger *slog.Logger
}

// session is the bookkeeping this package keeps for one transfer, alongside
// the *anatorrent.Torrent anacrolix already tracks. See the package doc for
// why anacrolix's own state is not enough.
type session struct {
	mu sync.Mutex

	contentRoot string

	paused   bool
	imported bool

	failed        bool
	failureReason downloadv1alpha1.DownloadFailureReason
	message       string

	hasSeedCriteria bool
	seedCriteria    commonv1alpha1.SeedCriteria
	completedAt     time.Time

	hasProgress       bool
	lastProgressAt    time.Time
	lastProgressBytes int64

	haveSample     bool
	lastSampleAt   time.Time
	lastDownloaded int64
	lastUploaded   int64
	downRate       int64
	upRate         int64
}

// Client implements [download.Client] over one embedded anacrolix
// *torrent.Client.
type Client struct {
	cl  *anatorrent.Client
	cfg Config

	mu       sync.Mutex
	sessions map[string]*session
}

var _ download.Client = (*Client)(nil)

// New starts an anacrolix torrent client rooted at cfg.DataDir.
func New(cfg Config) (download.Client, error) {
	if cfg.DataDir == "" {
		return nil, errors.New("torrent: Config.DataDir is required")
	}
	if err := os.MkdirAll(cfg.DataDir, 0o750); err != nil {
		return nil, fmt.Errorf("torrent: create data dir: %w", err)
	}

	acfg := anatorrent.NewDefaultClientConfig()
	acfg.DataDir = cfg.DataDir
	acfg.Seed = cfg.Seed
	acfg.NoDHT = cfg.NoDHT
	acfg.DisableTrackers = cfg.DisableTrackers
	acfg.NoDefaultPortForwarding = true
	acfg.ListenPort = cfg.ListenPort
	acfg.Debug = cfg.Debug
	if cfg.ListenHost != nil {
		acfg.ListenHost = cfg.ListenHost
	}
	if cfg.Logger != nil {
		acfg.Slogger = cfg.Logger
	}

	cl, err := anatorrent.NewClient(acfg)
	if err != nil {
		return nil, fmt.Errorf("torrent: new anacrolix client: %w", err)
	}

	return &Client{cl: cl, cfg: cfg, sessions: make(map[string]*session)}, nil
}

// Info implements [download.Client.Info].
func (c *Client) Info(ctx context.Context) (download.Info, error) {
	if err := ctx.Err(); err != nil {
		return download.Info{}, err
	}

	free, err := fsops.FreeBytes(c.cfg.DataDir)
	if err != nil {
		logging.FromContext(ctx).WarnContext(ctx, "torrent: statfs data dir failed", "dir", c.cfg.DataDir, "error", err)
		free = 0
	}

	info := download.Info{
		Protocol:       commonv1alpha1.ProtocolTorrent,
		Implementation: "anacrolix",
		Version:        engineVersion,
		FreeBytes:      free,
	}

	for _, t := range c.cl.Torrents() {
		id := idFromInfoHash(t.InfoHash())
		item := c.itemFromTorrent(id, t)
		info.DownloadRateBps += item.DownRate
		info.UploadRateBps += item.UpRate
		switch {
		case item.Stage == downloadv1alpha1.DownloadStageSeeding:
			info.Seeding++
		case item.Status == download.StatusDownloading || item.Status == download.StatusWarning:
			info.Active++
		case item.Status == download.StatusQueued || item.Status == download.StatusPaused:
			info.Queued++
		}
	}

	return info, nil
}

// Add implements [download.Client.Add].
//
// Idempotency rides entirely on anacrolix's own AddTorrentOpt: a torrent is
// keyed by info hash inside *anatorrent.Client, and adding one that already
// exists returns the existing *Torrent with new=false and touches neither
// its storage nor its pause state. This method only decides what happens on
// a genuinely new torrent -- everything below the AddTorrentSpec call is
// skipped when isNew is false, which is what makes re-attach safe.
func (c *Client) Add(ctx context.Context, req download.AddRequest) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if req.Name == "" {
		return "", errors.New("torrent: AddRequest.Name is required")
	}

	spec, err := resolveSpec(req)
	if err != nil {
		return "", err
	}
	if spec.InfoHash.IsZero() {
		return "", fmt.Errorf("torrent: %s: no v1 info hash (v2-only torrents are not supported)", req.Name)
	}
	id := idFromInfoHash(spec.InfoHash)

	if req.ExpectedInfoHash != "" && !strings.EqualFold(id, req.ExpectedInfoHash) {
		return "", download.ErrPayloadMismatch
	}

	dir := filepath.Join(c.cfg.DataDir, req.Category, req.Name)
	spec.Storage = storage.NewFile(dir)
	if spec.DisplayName == "" {
		spec.DisplayName = req.Name
	}
	// NOT spec.DisallowDataDownload/DisallowDataUpload: those AddTorrentOpts
	// fields are declared in this version of anacrolix but never read by
	// newTorrentOpt -- verified by grep, not assumed. Pause is applied
	// explicitly below instead, the same way [Client.Pause] does it.

	t, isNew, err := c.cl.AddTorrentSpec(spec)
	if err != nil {
		return "", fmt.Errorf("torrent: add %s: %w", id, err)
	}

	logger := logging.FromContext(ctx)
	if !isNew {
		logger.DebugContext(ctx, "torrent: already present, not restarted", "id", id, "name", req.Name)
		return id, nil
	}

	sess := c.sessionFor(id)
	sess.mu.Lock()
	sess.contentRoot = dir
	sess.paused = req.Paused
	if req.SeedCriteria != nil {
		sess.seedCriteria = *req.SeedCriteria
		sess.hasSeedCriteria = true
	}
	sess.mu.Unlock()

	if req.Paused {
		t.DisallowDataDownload()
		t.DisallowDataUpload()
	}

	// AddRequest has no per-file selection yet (D2-1 scope is the whole
	// interface, not season-pack file picking), so the whole torrent is
	// always wanted. DownloadAll is what makes anacrolix dial and request
	// peers at all -- a torrent with every piece at PiecePriorityNone (the
	// default on a freshly added Torrent) never wants peers, regardless of
	// DisallowDataDownload, which gates REQUESTS rather than intent.
	//
	// A magnet Add has no info yet, and DownloadAll before info arrives
	// iterates zero pieces -- there is nothing to mark wanted. wantAllOnInfo
	// waits for GotInfo (already closed immediately for a payload Add, since
	// MergeSpec sets info synchronously) and calls DownloadAll exactly once,
	// so both paths end up wanting the whole torrent.
	go wantAllOnInfo(t)
	applyPriorityBudget(t, req.Priority)
	t.SetOnWriteChunkError(sess.onWriteChunkError(t))

	logger.InfoContext(ctx, "torrent: added", "id", id, "name", req.Name, "paused", req.Paused)
	return id, nil
}

// Get implements [download.Client.Get].
func (c *Client) Get(ctx context.Context, id string) (download.Item, error) {
	if err := ctx.Err(); err != nil {
		return download.Item{}, err
	}
	t, _, err := c.lookup(id)
	if err != nil {
		return download.Item{}, err
	}
	return c.itemFromTorrent(id, t), nil
}

// List implements [download.Client.List].
func (c *Client) List(ctx context.Context) ([]download.Item, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ts := c.cl.Torrents()
	items := make([]download.Item, 0, len(ts))
	for _, t := range ts {
		items = append(items, c.itemFromTorrent(idFromInfoHash(t.InfoHash()), t))
	}
	return items, nil
}

// Pause implements [download.Client.Pause].
func (c *Client) Pause(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t, sess, err := c.lookup(id)
	if err != nil {
		return err
	}
	t.DisallowDataDownload()
	t.DisallowDataUpload()
	sess.mu.Lock()
	sess.paused = true
	sess.mu.Unlock()
	return nil
}

// Resume implements [download.Client.Resume].
//
// anatorrent.Torrent.AllowDataDownload/AllowDataUpload are themselves no-ops
// when the torrent is not currently disallowed, so calling them
// unconditionally already gives the documented "no-op on a non-paused
// transfer" behaviour without this method needing to branch on prior state.
func (c *Client) Resume(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t, sess, err := c.lookup(id)
	if err != nil {
		return err
	}
	t.AllowDataDownload()
	t.AllowDataUpload()
	sess.mu.Lock()
	sess.paused = false
	sess.mu.Unlock()
	return nil
}

// SetSeedCriteria implements [download.Client.SetSeedCriteria].
func (c *Client) SetSeedCriteria(ctx context.Context, id string, sc commonv1alpha1.SeedCriteria) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, sess, err := c.lookup(id)
	if err != nil {
		return err
	}
	sess.mu.Lock()
	sess.seedCriteria = sc
	sess.hasSeedCriteria = true
	sess.mu.Unlock()
	return nil
}

// MarkImported implements [download.Client.MarkImported].
func (c *Client) MarkImported(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, sess, err := c.lookup(id)
	if err != nil {
		return err
	}
	sess.mu.Lock()
	sess.imported = true
	sess.mu.Unlock()
	return nil
}

// Remove implements [download.Client.Remove].
func (c *Client) Remove(ctx context.Context, id string, deleteData bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	t, sess, err := c.lookup(id)
	if err != nil {
		return err
	}

	sess.mu.Lock()
	contentRoot := sess.contentRoot
	sess.mu.Unlock()

	// Drop closes the per-torrent storage (including the piece-completion
	// store) before returning, so it is safe to remove the directory
	// immediately after -- nothing still has it open.
	t.Drop()

	c.mu.Lock()
	delete(c.sessions, id)
	c.mu.Unlock()

	if deleteData && contentRoot != "" {
		// A hard link the importer made elsewhere is a separate directory
		// entry to the same inode, so removing contentRoot here does not
		// touch it -- nothing further is needed to honour "files already
		// hard-linked into the library survive" from the interface doc.
		if err := os.RemoveAll(contentRoot); err != nil {
			return fmt.Errorf("torrent: remove %s: delete data: %w", id, err)
		}
	}
	return nil
}

// Close implements [download.Client.Close].
func (c *Client) Close() error {
	if errs := c.cl.Close(); len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// resolveSpec turns req's payload into an anacrolix TorrentSpec. It never
// panics on malformed input -- req.Payload comes from an indexer over the
// network, so TorrentSpecFromMetaInfoErr (which reports unmarshal failure
// rather than TorrentSpecFromMetaInfo's panic) is the only safe choice.
func resolveSpec(req download.AddRequest) (*anatorrent.TorrentSpec, error) {
	hasMagnet := req.Magnet != ""
	hasPayload := len(req.Payload) > 0
	if hasMagnet == hasPayload {
		return nil, fmt.Errorf("torrent: %s: exactly one of Magnet and Payload must be set", req.Name)
	}

	if hasMagnet {
		spec, err := anatorrent.TorrentSpecFromMagnetUri(req.Magnet)
		if err != nil {
			return nil, fmt.Errorf("torrent: %s: parse magnet: %w", req.Name, err)
		}
		return spec, nil
	}

	mi, err := metainfo.Load(bytes.NewReader(req.Payload))
	if err != nil {
		return nil, fmt.Errorf("torrent: %s: parse metainfo: %w", req.Name, err)
	}
	spec, err := anatorrent.TorrentSpecFromMetaInfoErr(mi)
	if err != nil {
		return nil, fmt.Errorf("torrent: %s: metainfo info dict: %w", req.Name, err)
	}
	return spec, nil
}

// Peer-connection budgets applyPriorityBudget assigns per [download.DownloadPriority].
//
// anacrolix has no cross-torrent priority queue -- every added torrent
// competes for peers on equal footing -- so there is no direct analogue for
// "order this transfer within the client's queue". The closest lever is how
// many established peer connections a torrent may hold
// (Torrent.SetMaxEstablishedConns): a high-priority transfer gets more of
// the client's total connection budget to complete faster, a low-priority
// one gets less, leaving more for everything else. DownloadPriorityNormal
// and an empty value are left at anacrolix's own
// ClientConfig.EstablishedConnsPerTorrent default.
const (
	highPriorityConns = 100
	lowPriorityConns  = 10
)

func applyPriorityBudget(t *anatorrent.Torrent, p downloadv1alpha1.DownloadPriority) {
	switch p {
	case downloadv1alpha1.DownloadPriorityHigh:
		t.SetMaxEstablishedConns(highPriorityConns)
	case downloadv1alpha1.DownloadPriorityLow:
		t.SetMaxEstablishedConns(lowPriorityConns)
	}
}

// wantAllOnInfo marks every piece of t wanted as soon as its info is known,
// then returns. It exits without doing anything if t is dropped first, so it
// never leaks a goroutine for a magnet whose metadata never arrives.
func wantAllOnInfo(t *anatorrent.Torrent) {
	select {
	case <-t.GotInfo():
		t.DownloadAll()
	case <-t.Closed():
	}
}

// sessionFor returns the bookkeeping session for id, creating an empty one
// if this is the first time this Client has seen it. That fallback matters
// only for a torrent anacrolix already knows about that this process never
// Added itself -- not reachable within one process's lifetime today, since
// nothing else adds torrents to c.cl, but defensive rather than a lookup
// that could return nil.
func (c *Client) sessionFor(id string) *session {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.sessions[id]
	if !ok {
		s = &session{}
		c.sessions[id] = s
	}
	return s
}

// lookup resolves id to its anacrolix Torrent and session together, since
// every method past Add needs both and every one of them reports
// [download.ErrNotFound] the same way for an unknown or malformed id.
func (c *Client) lookup(id string) (*anatorrent.Torrent, *session, error) {
	var ih metainfo.Hash
	if err := ih.FromHexString(id); err != nil {
		return nil, nil, download.ErrNotFound
	}
	t, ok := c.cl.Torrent(ih)
	if !ok {
		return nil, nil, download.ErrNotFound
	}
	return t, c.sessionFor(id), nil
}

// idFromInfoHash is the one place this package turns a v1 info hash into a
// [download.Item.ID]. Lowercase because metainfo.Hash.FromHexString accepts
// either case but callers (this package's own Get/Remove/... and any
// ExpectedInfoHash a caller supplies) are compared with strings.EqualFold,
// so a single canonical case keeps map keys consistent.
func idFromInfoHash(ih metainfo.Hash) string {
	return strings.ToLower(ih.HexString())
}

// ratioMilli is uploadedBytes/downloadedBytes in thousandths, clamped to
// int32 the way [download.Item.RatioMilli] requires. downloadedBytes<=0
// reports 0 rather than dividing by zero -- a torrent that has uploaded
// bytes before downloading any of its own content does not happen over
// BitTorrent, but guarding it costs nothing.
func ratioMilli(uploadedBytes, downloadedBytes int64) int32 {
	if downloadedBytes <= 0 {
		return 0
	}
	r := uploadedBytes * 1000 / downloadedBytes
	switch {
	case r < 0:
		return 0
	case r > math.MaxInt32:
		return math.MaxInt32
	default:
		return int32(r)
	}
}

// progressPercent is downloadedBytes/totalBytes as a 0-100 integer.
func progressPercent(downloadedBytes, totalBytes int64) int32 {
	if totalBytes <= 0 {
		return 0
	}
	p := downloadedBytes * 100 / totalBytes
	switch {
	case p < 0:
		return 0
	case p > 100:
		return 100
	default:
		return int32(p)
	}
}

// bytesPerSecond computes an integer rate from a byte delta over elapsed,
// without ever forming a float -- deltaBytes and the result both stay in
// int64, consistent with the project's no-floating-point-telemetry rule even
// though this value's only destination (Item.DownRate/UpRate, then
// status.downloadRateBps/uploadRateBps) was already int64 either way.
func bytesPerSecond(deltaBytes int64, elapsed time.Duration) int64 {
	if deltaBytes <= 0 || elapsed <= 0 {
		return 0
	}
	return deltaBytes * int64(time.Second) / int64(elapsed)
}
