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

// Package usenet is the NNTP half of [download.Client]: it turns a .nzb body
// into finished content under a data directory, and reports the transfer the
// way grabarr's controllers expect.
//
// # What it owns end to end
//
// Parse the NZB, fetch every article over a bounded multi-provider connection
// pool, yEnc-decode each part straight to its authoritative offset, verify and
// repair with par2, unpack, clean up, and publish with one atomic rename. The
// stages surface as downloadv1alpha1.DownloadStage values and the article
// accounting surfaces as [download.Item].Health, which is this client's alone
// to populate.
//
// # Why the connection pool is hand-written
//
// Plan ruling R9. github.com/javi11/nntppool/v4 would have supplied the pool,
// 430 failover, pipelining and quota accounting, but it is cgo-only --
// verified both ways -- and cmd/clustarr is a single binary built
// CGO_ENABLED=0 onto distroless/static, the same constraint ADR-0003 R8
// already settled for the SQLite driver. So pool.go and conn.go are ours. See
// conn.go for why github.com/Tensai75/nntp could not supply the one
// connection under it either.
//
// # What it deliberately does not do
//
// It does not seed, so [Client.SetSeedCriteria] is a no-op; the reasoning is
// on the interface. It does not write Download.status -- the engine runnable
// does, through download.ApplyStatus -- and it never reads a Secret: the
// caller resolves provider credentials into [Provider].
package usenet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/download"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// ErrUnsupportedPayload is returned by [Client.Add] for a magnet URI. A usenet
// client has no way to resolve one, and silently accepting it would strand the
// Download in Queued forever.
var ErrUnsupportedPayload = errors.New("usenet: a usenet client needs an nzb payload, not a magnet uri")

// Config configures one usenet client.
type Config struct {
	// Providers are the NNTP servers, credentials already resolved.
	Providers []Provider

	// ScratchDir is the node-local working area: part files, par2 volumes and
	// the _UNPACK_ directory live here. Research note §5 puts it on local NVMe
	// because assembly is heavy random I/O and NFS latency triples it.
	ScratchDir string

	// DataDir is the shared volume the importer reads, and the root the
	// free-space check and every removal are contained to. It must be the
	// volume the library is on, or the importer's hardlink degrades to a
	// copy.
	DataDir string

	// PublishDir is where finished content is published, as
	// <PublishDir>/<category>/<name>. Empty means DataDir. It must be on the
	// same filesystem as ScratchDir for the publish to be one rename;
	// fsops.MoveAtomic falls back to a copy across filesystems.
	PublishDir string

	// Par2Path is the par2cmdline-turbo binary. Empty means look up "par2".
	Par2Path string

	// PostProcess controls repair, unpacking and cleanup.
	PostProcess PostProcess

	// PropagationDelay is how long after posting a release may be started, so
	// articles have time to propagate. Zero disables the wait.
	PropagationDelay time.Duration

	// PreCheck STATs every article before committing disk and quota.
	PreCheck bool

	// AbortHealthPercent is the article-health floor. A download below it --
	// or below the NZB's own par2 critical health -- gets [Config.HealthAction].
	// Zero means the default (90), matching UsenetSpec's own default.
	AbortHealthPercent int32

	// HealthAction is what a breach of the health floor does
	// (UsenetSpec.healthAction). HealthActionDelete fails the job with
	// DownloadFailureMissingArticles, which blocklists the release.
	// HealthActionPause -- and the zero value, matching the CRD default --
	// holds the job paused for an operator, NZBGet's HealthCheck=pause:
	// [download.Item.HealthPaused] reports it, [Client.Resume] leaves it in
	// place, and [Client.Pause] followed by [Client.Resume] continues the job
	// with the health check off, which is what resuming a health-paused
	// download does in NZBGet (QueueCoordinator::CheckHealth skips a job
	// whose HealthPaused flag is already set).
	HealthAction downloadv1alpha1.HealthAction

	// PipelineDepth is how many commands one connection keeps in flight.
	PipelineDepth int

	// MaxArticleBytes caps one decoded article.
	MaxArticleBytes int64

	// MaxNZBBytes caps a .nzb payload.
	MaxNZBBytes int64

	// Workers is how many batches are fetched concurrently. Zero means the
	// sum of the providers' connection allowances, which is the only number
	// that keeps every connection busy without queuing on the pool.
	Workers int

	ConnectTimeout time.Duration
	IOTimeout      time.Duration

	// DownloadTimeout bounds a whole job, measured from its first Add
	// (AddedAt, so a restart does not extend it) and including time spent
	// paused. A job still running at the deadline fails with
	// DownloadFailureTimeout (UsenetSpec.downloadTimeout). Zero means no
	// deadline.
	DownloadTimeout time.Duration

	// ProviderRetryDelay is how long a job waits before trying again when no
	// configured server could be asked for its articles
	// ([ErrProvidersUnavailable]). Zero means [defaultProviderRetryDelay].
	ProviderRetryDelay time.Duration
}

// defaultProviderRetryDelay is [Config.ProviderRetryDelay]'s default. It is
// the pool's own refused-connection penalty, so a retry lands just as a
// penalised provider becomes available again rather than finding it still
// sitting out.
const defaultProviderRetryDelay = penaltyRefused

// PostProcess mirrors downloadv1alpha1.PostProcessSpec with its pointers
// resolved.
type PostProcess struct {
	Par2            bool
	Unpack          bool
	DeleteArchives  bool
	CleanupPatterns []string
}

// Client is the usenet [download.Client].
type Client struct {
	cfg  Config
	pool *Pool
	par2 Par2Runner

	workers int

	mu   sync.Mutex
	jobs map[string]*job

	closeOnce sync.Once
	wg        sync.WaitGroup
}

var _ download.Client = (*Client)(nil)

// New builds a usenet client and re-attaches every job already in the scratch
// area.
//
// Re-attach happens here rather than lazily because an engine that reported
// ready before re-attaching would be handed work it is already doing -- plan
// ruling R4 -- and because [Client.Add] is idempotent on the id it returns,
// which it cannot be if a restart forgot the ids it had issued.
func New(cfg Config) (download.Client, error) {
	if len(cfg.Providers) == 0 {
		return nil, ErrNoProviders
	}
	if cfg.ScratchDir == "" || cfg.DataDir == "" {
		return nil, errors.New("usenet: ScratchDir and DataDir are required")
	}
	if cfg.PublishDir == "" {
		cfg.PublishDir = cfg.DataDir
	}
	if cfg.PipelineDepth <= 0 {
		cfg.PipelineDepth = defaultPipelineDepth
	}
	if cfg.MaxArticleBytes <= 0 {
		cfg.MaxArticleBytes = defaultMaxArticleBytes
	}
	if cfg.MaxNZBBytes <= 0 {
		cfg.MaxNZBBytes = defaultMaxNZBBytes
	}
	if cfg.ConnectTimeout <= 0 {
		cfg.ConnectTimeout = defaultConnectTimeout
	}
	if cfg.IOTimeout <= 0 {
		cfg.IOTimeout = defaultIOTimeout
	}
	if cfg.AbortHealthPercent <= 0 {
		cfg.AbortHealthPercent = 90
	}
	if cfg.ProviderRetryDelay <= 0 {
		cfg.ProviderRetryDelay = defaultProviderRetryDelay
	}

	workers := cfg.Workers
	if workers <= 0 {
		for _, p := range cfg.Providers {
			workers += max(p.Connections, 1)
		}
	}

	pool, err := NewPool(cfg.Providers, cfg.PipelineDepth, cfg.ConnectTimeout, cfg.IOTimeout, cfg.MaxArticleBytes)
	if err != nil {
		return nil, err
	}

	for _, dir := range []string{cfg.ScratchDir, cfg.DataDir, cfg.PublishDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			pool.Close()
			return nil, fmt.Errorf("usenet: create %s: %w", dir, err)
		}
	}

	c := &Client{
		cfg:     cfg,
		pool:    pool,
		par2:    Par2Runner{Path: cfg.Par2Path},
		workers: workers,
		jobs:    map[string]*job{},
	}
	if err := c.reattach(); err != nil {
		pool.Close()
		return nil, err
	}
	return c, nil
}

// manifest is the crash-recovery record for one job. It is the only file in
// the scratch area written through fsops.AtomicWrite, because it is the only
// one that must be either wholly the old version or wholly the new: a torn
// manifest would lose every id the client had issued.
type manifest struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Category   string `json:"category"`
	Status     string `json:"status"`
	Stage      string `json:"stage"`
	Reason     string `json:"reason,omitempty"`
	Message    string `json:"message,omitempty"`
	OutputPath string `json:"outputPath,omitempty"`
	Imported   bool   `json:"imported,omitempty"`
	Encrypted  bool   `json:"encrypted,omitempty"`
	Paused     bool   `json:"paused,omitempty"`
	// Priority is the job's spec.priority class; see [job.getPriority].
	Priority string `json:"priority,omitempty"`
	// HealthPaused and HealthOverride are the health action's state; see
	// [job.healthPaused]. Persisted so a restart neither resumes a job an
	// operator has not looked at nor re-pauses one they told to carry on.
	HealthPaused   bool `json:"healthPaused,omitempty"`
	HealthOverride bool `json:"healthOverride,omitempty"`
	// AddedAt is when the job was first added -- [download.Item.AddedAt],
	// which an orphan reaper ages the transfer by. A manifest written before
	// it existed decodes to zero, which the reaper reads as "unknown".
	AddedAt time.Time `json:"addedAt,omitzero"`
	Done    []bitset  `json:"done,omitempty"`
	Failed  []bitset  `json:"failed,omitempty"`
}

const (
	manifestName = "manifest.json"
	nzbName      = "source.nzb"
)

// job is one transfer.
type job struct {
	client *Client

	id       string
	name     string
	category string
	dir      string

	nzb     *nzbJob
	payload []byte

	abortHealth int32

	// prio is the job's spec.priority class, a downloadv1alpha1.DownloadPriority.
	// [Client.SetPriority] changes it after the Add, and [Client.outranked]
	// reads it for other jobs under only the client lock, so it is atomic
	// rather than guarded by mu. See [job.getPriority].
	prio atomic.Value

	// addedAt is fixed at the first Add and restored from the manifest on
	// re-attach, so it is read without mu too.
	addedAt time.Time

	// checkpointMu serialises checkpoint: one manifest write at a time, each
	// taking its snapshot inside the section, so the newest snapshot is
	// always the last one renamed into place. Without it two concurrent
	// checkpoints (a Resume beside the transfer's own progress write) raced on
	// fsops.AtomicWrite's single ".partial" name -- one rename moved the
	// other's file away and failed it -- and an older snapshot could land
	// after a newer one. It is separate from mu so a slow fsync still never
	// stalls the fetch workers.
	checkpointMu sync.Mutex

	mu           sync.Mutex
	status       download.Status
	stage        downloadv1alpha1.DownloadStage
	reason       downloadv1alpha1.DownloadFailureReason
	message      string
	outputPath   string
	downloaded   int64
	lastProgress time.Time
	lastError    error
	encrypted    bool
	imported     bool

	// healthPaused is set when the health floor is breached under the pause
	// health action ([job.breach]); the job is paused with it, and only an
	// operator's Pause (which clears it and sets healthOverride) followed by
	// a Resume carries on. healthOverride turns the health check off for the
	// rest of the job, so the breach that paused it does not pause it again.
	healthPaused   bool
	healthOverride bool

	done           []bitset
	failedSegs     []bitset
	publishedFiles []download.File

	paused atomic.Bool
	rate   rateMeter

	// cancel and running are guarded by mu. A job that was re-attached in a
	// terminal state is never started, so waiting on finished would block for
	// the whole grace period -- which is why "is it running" is tracked
	// rather than inferred from cancel being non-nil.
	cancel   context.CancelFunc
	running  bool
	finished chan struct{}
}

// stop cancels the job and returns a channel that closes when its goroutine
// has left, or nil when it was never started.
func (j *job) stop() <-chan struct{} {
	j.mu.Lock()
	cancel, running := j.cancel, j.running
	j.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if !running {
		return nil
	}
	return j.finished
}

func (j *job) downloadedBytes() int64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.downloaded
}

func (j *job) setStage(stage downloadv1alpha1.DownloadStage, status download.Status) {
	j.mu.Lock()
	j.stage, j.status = stage, status
	j.mu.Unlock()
	_ = j.checkpoint()
}

func (j *job) fail(reason downloadv1alpha1.DownloadFailureReason, err error) {
	j.mu.Lock()
	j.status = download.StatusFailed
	j.stage = downloadv1alpha1.DownloadStageDone
	j.reason = reason
	if err != nil {
		j.message = err.Error()
		j.lastError = err
	}
	j.mu.Unlock()
	_ = j.checkpoint()
}

// checkpoint persists the manifest. The bitsets are copied under mu and
// written outside it, so a slow fsync does not stall every fetch worker;
// checkpointMu keeps two checkpoints from overlapping (see its doc).
func (j *job) checkpoint() error {
	j.checkpointMu.Lock()
	defer j.checkpointMu.Unlock()
	j.mu.Lock()
	m := manifest{
		ID:             j.id,
		Name:           j.name,
		Category:       j.category,
		Status:         string(j.status),
		Stage:          string(j.stage),
		Reason:         string(j.reason),
		Message:        j.message,
		OutputPath:     j.outputPath,
		Imported:       j.imported,
		Encrypted:      j.encrypted,
		Paused:         j.paused.Load(),
		Priority:       string(j.getPriority()),
		HealthPaused:   j.healthPaused,
		HealthOverride: j.healthOverride,
		AddedAt:        j.addedAt,
		Done:           cloneBitsets(j.done),
		Failed:         cloneBitsets(j.failedSegs),
	}
	j.mu.Unlock()

	buf, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("usenet: encode manifest: %w", err)
	}
	return fsops.AtomicWrite(filepath.Join(j.dir, manifestName), bytes.NewReader(buf), 0o644)
}

func cloneBitsets(in []bitset) []bitset {
	out := make([]bitset, len(in))
	for i, b := range in {
		out[i] = append(bitset(nil), b...)
	}
	return out
}

// reattach rebuilds the job table from the scratch area.
func (c *Client) reattach() error {
	entries, err := os.ReadDir(c.cfg.ScratchDir)
	if err != nil {
		return fmt.Errorf("usenet: read scratch: %w", err)
	}
	for _, catDir := range entries {
		if !catDir.IsDir() {
			continue
		}
		jobs, err := os.ReadDir(filepath.Join(c.cfg.ScratchDir, catDir.Name()))
		if err != nil {
			return fmt.Errorf("usenet: read scratch category: %w", err)
		}
		for _, jd := range jobs {
			if !jd.IsDir() {
				continue
			}
			dir := filepath.Join(c.cfg.ScratchDir, catDir.Name(), jd.Name())
			j, err := c.loadJob(dir)
			if err != nil {
				// A job whose manifest is unreadable is left on disk and
				// skipped rather than deleted: the files may be most of a
				// 50GB transfer, and an operator can see why.
				continue
			}
			c.jobs[j.id] = j
			if j.status != download.StatusCompleted && j.status != download.StatusFailed {
				c.start(context.Background(), j)
			}
		}
	}
	return nil
}

func (c *Client) loadJob(dir string) (*job, error) {
	raw, err := os.ReadFile(filepath.Join(dir, manifestName))
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	payload, err := os.ReadFile(filepath.Join(dir, nzbName))
	if err != nil {
		return nil, err
	}
	parsed, err := parseNZB(payload, c.cfg.MaxNZBBytes)
	if err != nil {
		return nil, err
	}
	j := c.newJob(m.ID, m.Name, m.Category, dir, parsed, payload)
	j.status = download.Status(m.Status)
	j.stage = downloadv1alpha1.DownloadStage(m.Stage)
	j.reason = downloadv1alpha1.DownloadFailureReason(m.Reason)
	j.message = m.Message
	j.outputPath = m.OutputPath
	j.imported = m.Imported
	j.encrypted = m.Encrypted
	j.paused.Store(m.Paused)
	j.prio.Store(downloadv1alpha1.DownloadPriority(m.Priority))
	j.healthPaused = m.HealthPaused
	j.healthOverride = m.HealthOverride
	j.addedAt = m.AddedAt
	restoreBitsets(j.done, m.Done)
	restoreBitsets(j.failedSegs, m.Failed)
	for i := range j.done {
		j.downloaded += completedBytes(parsed.Files[i], j.done[i])
	}
	return j, nil
}

func restoreBitsets(dst, src []bitset) {
	for i := range dst {
		if i < len(src) && len(src[i]) == len(dst[i]) {
			copy(dst[i], src[i])
		}
	}
}

func completedBytes(f nzbFile, done bitset) int64 {
	var n int64
	for i := range f.Segments {
		if done.has(i) {
			n += f.Segments[i].Bytes
		}
	}
	return n
}

func (c *Client) newJob(id, name, category, dir string, parsed *nzbJob, payload []byte) *job {
	j := &job{
		client:      c,
		id:          id,
		name:        name,
		category:    category,
		dir:         dir,
		nzb:         parsed,
		payload:     payload,
		abortHealth: c.cfg.AbortHealthPercent,
		status:      download.StatusQueued,
		stage:       downloadv1alpha1.DownloadStageFetchingMetadata,
		reason:      downloadv1alpha1.DownloadFailureNone,
		finished:    make(chan struct{}),
	}
	j.prio.Store(downloadv1alpha1.DownloadPriority(""))
	j.done = make([]bitset, len(parsed.Files))
	j.failedSegs = make([]bitset, len(parsed.Files))
	for i := range parsed.Files {
		j.done[i] = newBitset(len(parsed.Files[i].Segments))
		j.failedSegs[i] = newBitset(len(parsed.Files[i].Segments))
	}
	return j
}

// Info reports the client's own state.
func (c *Client) Info(_ context.Context) (download.Info, error) {
	free, err := fsops.FreeBytes(c.cfg.DataDir)
	if err != nil {
		// Zero means "not known", which the DownloadClient controller reports
		// as DiskSpaceOK=Unknown rather than as a full disk.
		free = 0
	}

	info := download.Info{
		Protocol:       commonv1alpha1.ProtocolUsenet,
		Implementation: "nntp",
		Version:        implementationVersion,
		FreeBytes:      free,
	}

	c.mu.Lock()
	jobs := make([]*job, 0, len(c.jobs))
	for _, j := range c.jobs {
		jobs = append(jobs, j)
	}
	c.mu.Unlock()

	for _, j := range jobs {
		switch j.snapshotStatus() {
		case download.StatusDownloading, download.StatusWarning:
			info.Active++
			info.DownloadRateBps += j.rate.bps()
		case download.StatusQueued, download.StatusPaused:
			info.Queued++
		case download.StatusCompleted, download.StatusFailed:
		}
	}
	// Seeding stays zero: a usenet transfer uploads nothing and has no swarm.
	return info, nil
}

// implementationVersion identifies this engine in status messages. It is not a
// metric label and nothing branches on it.
const implementationVersion = "clustarr-usenet/1"

// Add starts one transfer and returns the client's id for it.
//
// The id is a hash of the .nzb payload, which is what makes Add idempotent:
// the same payload added twice returns the same id and the running transfer,
// so a controller that did not observe the first Add cannot start a second
// copy of the same download.
func (c *Client) Add(ctx context.Context, req download.AddRequest) (string, error) {
	ctx, span := tracing.Start(ctx, "usenet.client.add")
	defer span.End()

	if req.Magnet != "" {
		return "", fmt.Errorf("%w: %q", ErrUnsupportedPayload, req.Name)
	}
	if len(req.Payload) == 0 {
		return "", fmt.Errorf("%w: empty payload for %q", ErrUnsupportedPayload, req.Name)
	}

	parsed, err := parseNZB(req.Payload, c.cfg.MaxNZBBytes)
	if err != nil {
		return "", err
	}

	// Everything below runs under the client lock, including the two small
	// writes. Publishing a job into the map before it is started would let a
	// concurrent Remove find one with no goroutine behind it and then wait
	// out the whole grace period for a transfer that never began; Add is not
	// a hot path, and the lock is only contended by Get and List.
	c.mu.Lock()
	defer c.mu.Unlock()

	if existing, ok := c.jobs[parsed.ID]; ok {
		if req.Paused {
			existing.paused.Store(true)
		}
		return existing.id, nil
	}

	category := req.Category
	if category == "" {
		category = "default"
	}
	dir := filepath.Join(c.cfg.ScratchDir, safeName(category), parsed.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("usenet: create %s: %w", dir, err)
	}
	// The payload is stored before anything else: it is what a restart
	// re-parses, and an id issued for a payload that was never written is an
	// id no restart can honour.
	if err := fsops.AtomicWrite(filepath.Join(dir, nzbName), bytes.NewReader(req.Payload), 0o644); err != nil {
		return "", err
	}

	j := c.newJob(parsed.ID, req.Name, category, dir, parsed, req.Payload)
	j.paused.Store(req.Paused)
	j.prio.Store(req.Priority)
	j.addedAt = req.AddedAt
	if j.addedAt.IsZero() {
		j.addedAt = time.Now()
	}
	if err := j.checkpoint(); err != nil {
		return "", err
	}

	c.start(ctx, j)
	c.jobs[parsed.ID] = j
	return j.id, nil
}

func (c *Client) forget(id string) {
	c.mu.Lock()
	delete(c.jobs, id)
	c.mu.Unlock()
}

// start detaches the job from the caller's context. The reconcile that called
// Add returns long before a 50GB transfer finishes, so its cancellation must
// not reach the transfer -- but its logger and trace must, which is exactly
// what context.WithoutCancel preserves.
//
// A configured [Config.DownloadTimeout] becomes the run context's deadline,
// anchored at the job's AddedAt so a re-attached job keeps the deadline it
// was first given; [job.abort] reads its expiry as DownloadFailureTimeout.
func (c *Client) start(ctx context.Context, j *job) {
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	if c.cfg.DownloadTimeout > 0 && !j.addedAt.IsZero() {
		cancel()
		runCtx, cancel = context.WithDeadline(context.WithoutCancel(ctx), j.addedAt.Add(c.cfg.DownloadTimeout))
	}
	j.mu.Lock()
	j.cancel = cancel
	j.running = true
	j.mu.Unlock()

	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		defer cancel()
		defer close(j.finished)
		j.run(runCtx)
	}()
}

// run is the whole pipeline, in the order research note §2.6 settled on:
// propagation wait, optional pre-check, transfer, verify/repair, unpack,
// cleanup, publish.
func (j *job) run(ctx context.Context) {
	ctx = logging.With(ctx, "download", j.id, "protocol", "usenet")
	log := logging.FromContext(ctx)

	if err := j.waitForPropagation(ctx); err != nil {
		j.abort(ctx, err)
		return
	}
	if err := j.preCheck(ctx); err != nil {
		j.abort(ctx, err)
		return
	}

	for {
		j.setStage(downloadv1alpha1.DownloadStageTransferring, download.StatusDownloading)
		err := j.transfer(ctx)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrProvidersUnavailable) {
			j.abort(ctx, err)
			return
		}
		// No server could be asked: an outage or a credentials problem, not
		// the release. Wait and resume from the checkpointed segments; the
		// articles that were never asked for are neither done nor failed,
		// so the next pass fetches exactly those.
		if werr := j.waitForProviders(ctx, err); werr != nil {
			j.abort(ctx, werr)
			return
		}
	}

	if err := j.postProcess(ctx); err != nil {
		j.abort(ctx, err)
		return
	}

	log.InfoContext(ctx, "usenet download complete",
		"output", j.snapshotOutputPath(), "articles", j.nzb.TotalSegments, "failed", j.failedArticles())
}

// waitForProviders reports cause on the job -- StatusWarning, the state
// [download.StatusWarning] documents for "a provider refusing articles" --
// and waits [Config.ProviderRetryDelay]. It returns the run context's error
// if the job is cancelled or reaches its deadline while waiting.
func (j *job) waitForProviders(ctx context.Context, cause error) error {
	j.mu.Lock()
	j.status = download.StatusWarning
	j.message = "waiting for a usenet provider: " + cause.Error()
	j.mu.Unlock()
	_ = j.checkpoint()
	logging.FromContext(ctx).WarnContext(ctx, "no usenet provider could be asked; waiting to retry",
		"download", j.id, "retryIn", j.client.cfg.ProviderRetryDelay, "error", cause)

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(j.client.cfg.ProviderRetryDelay):
	}
	j.mu.Lock()
	j.message = ""
	j.mu.Unlock()
	return nil
}

// abort maps a pipeline error onto the machine-readable reason the *arr
// failed-download contract keys off -- see [failureReason] -- and fails the
// job with it.
func (j *job) abort(ctx context.Context, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	reason := failureReason(ctx, err)
	if reason == downloadv1alpha1.DownloadFailureEncrypted {
		j.mu.Lock()
		j.encrypted = true
		j.mu.Unlock()
	}
	j.fail(reason, err)
	logging.FromContext(ctx).ErrorContext(ctx, "usenet download failed", "reason", reason, "error", err)
}

// failureReason classifies a pipeline error. ctx is the job's run context:
// its deadline having passed makes the failure a timeout whatever shape the
// error took on the way out -- a par2 or unrar child killed by the deadline
// reports its own exit status, not context.DeadlineExceeded, and must not be
// read as a failed repair.
//
//   - timeout: the job's DownloadTimeout expired.
//   - encrypted: an archive needs a password.
//   - missingArticles: every server that was asked answered "no such
//     article" often enough that the health floor was crossed
//     ([ErrUnrecoverable], also the pre-check's verdict), or par2 ran and
//     could not repair the set ([ErrRepairFailed]).
//   - diskFull: the scratch or data volume is out of space (the statfs
//     guard's [fsops.ErrInsufficientSpace], or ENOSPC/EDQUOT from a write).
//   - writeError: anything else -- a filesystem error, and the local
//     configuration faults (no par2 binary for a set that needs repair).
//
// The first three blocklist the release; diskFull and writeError do not
// (DownloadFailureReason.IsReleaseFault). An archive that fails to extract
// for a reason other than a password or a write is classed writeError, the
// conservative side: blocklisting needs evidence against the release.
func failureReason(ctx context.Context, err error) downloadv1alpha1.DownloadFailureReason {
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(ctx.Err(), context.DeadlineExceeded):
		return downloadv1alpha1.DownloadFailureTimeout
	case errors.Is(err, ErrEncrypted):
		return downloadv1alpha1.DownloadFailureEncrypted
	case errors.Is(err, ErrUnrecoverable), errors.Is(err, ErrArticleMissing), errors.Is(err, ErrRepairFailed):
		return downloadv1alpha1.DownloadFailureMissingArticles
	case errors.Is(err, fsops.ErrInsufficientSpace), errors.Is(err, syscall.ENOSPC), errors.Is(err, syscall.EDQUOT):
		return downloadv1alpha1.DownloadFailureDiskFull
	default:
		return downloadv1alpha1.DownloadFailureWriteError
	}
}

// waitForPropagation holds a release until its articles have had time to
// spread across the backbone. Starting too early looks exactly like a release
// with missing articles, which is how a perfectly good grab gets blocklisted.
func (j *job) waitForPropagation(ctx context.Context) error {
	delay := j.client.cfg.PropagationDelay
	if delay <= 0 || j.nzb.Posted.IsZero() {
		return nil
	}
	ready := j.nzb.Posted.Add(delay)
	wait := time.Until(ready)
	if wait <= 0 {
		return nil
	}
	j.setStage(downloadv1alpha1.DownloadStageFetchingMetadata, download.StatusQueued)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

// preCheck STATs every article before committing disk and quota to a release
// that is already half gone.
func (j *job) preCheck(ctx context.Context) error {
	if !j.client.cfg.PreCheck {
		return nil
	}
	j.setStage(downloadv1alpha1.DownloadStageVerifying, download.StatusQueued)

	ids := make([]string, 0, j.nzb.TotalSegments)
	for _, f := range j.nzb.Files {
		for _, s := range f.Segments {
			ids = append(ids, s.ID)
		}
	}
	missing, err := j.client.pool.Exists(ctx, ids)
	if errors.Is(err, ErrProvidersUnavailable) {
		// The pre-check is an optimisation; it cannot judge a release it
		// could not look at. The transfer waits for the providers instead.
		logging.FromContext(ctx).WarnContext(ctx, "usenet pre-check skipped: no provider could be asked",
			"download", j.id, "error", err)
		return nil
	}
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		return nil
	}
	health := int32((len(ids) - len(missing)) * 100 / len(ids)) //nolint:gosec // bounded by 100.
	if health < j.abortHealth || health < j.nzb.criticalHealthPercent() {
		if err := j.breach(ctx, fmt.Sprintf("pre-check found %d of %d articles missing (health %d%%)",
			len(missing), len(ids), health), nil); err != nil {
			return err
		}
		return j.waitWhilePaused(ctx)
	}
	return nil
}

// postProcess verifies, repairs, unpacks, cleans up and publishes.
func (j *job) postProcess(ctx context.Context) error {
	ctx, span := tracing.Start(ctx, "usenet.job.postProcess")
	defer span.End()

	if err := j.healthGate(ctx); err != nil {
		return err
	}
	// The gate pauses rather than failing under the pause health action; the
	// transfer's workers wait that out between batches, but nothing else
	// would stop repair and unpack from running on a set the operator has
	// not yet decided about.
	if j.isHealthPaused() {
		if err := j.waitWhilePaused(ctx); err != nil {
			return err
		}
	}

	content := j.contentDir()
	if j.client.cfg.PostProcess.Par2 {
		if err := j.repair(ctx); err != nil {
			return err
		}
	}

	publishFrom := content
	if j.client.cfg.PostProcess.Unpack && len(archiveEntryPoints(j.nzb.Files)) > 0 {
		j.setStage(downloadv1alpha1.DownloadStageExtracting, download.StatusDownloading)
		unpackDir := filepath.Join(j.dir, unpackDirName)
		res, err := unpackArchives(ctx, content, unpackDir, j.nzb.Password, j.nzb.Files)
		if err != nil {
			return err
		}
		if res.Encrypted {
			return fmt.Errorf("%w: %s", ErrEncrypted, j.name)
		}
		publishFrom = unpackDir
	}

	pp := j.client.cfg.PostProcess
	if publishFrom == content {
		// Nothing was unpacked, so the downloaded files ARE the content: only
		// the par2 volumes and the junk patterns go.
		if err := cleanupAfterUnpack(ctx, content, j.nzb.Files, false, pp.DeleteArchives, pp.CleanupPatterns); err != nil {
			return err
		}
	} else if len(pp.CleanupPatterns) > 0 {
		if err := cleanupAfterUnpack(ctx, publishFrom, nil, false, false, pp.CleanupPatterns); err != nil {
			return err
		}
	}

	if err := j.publish(ctx, publishFrom); err != nil {
		return err
	}
	if publishFrom != content {
		// The archives have been extracted; the scratch copy can go whether
		// or not deleteArchives is set, because it is scratch and not the
		// published content.
		if err := fsops.SafeRemove(ctx, j.dir, content); err != nil {
			return err
		}
	}
	return nil
}

// contentDir is where articles are assembled.
//
// It is a SUBDIRECTORY of the job directory and not the job directory itself,
// because publishing is a rename of this path into the data directory: with
// the two collapsed, the manifest and the stored .nzb -- the two files that
// keep Add idempotent across a restart -- would be renamed into the library
// along with the content, and the next restart would have no record that this
// download ever happened.
func (j *job) contentDir() string { return filepath.Join(j.dir, "content") }

func (j *job) repair(ctx context.Context) error {
	index := par2IndexFile(j.nzb.Files)
	if index == "" {
		return nil
	}
	if !j.client.par2.Available() {
		// No binary and nothing missing: the quick-check equivalent. A set
		// with every article present and correct -- every part's pcrc32
		// verified on the way in -- does not need par2 at all, so a missing
		// binary is only fatal when there is damage to repair.
		if j.failedArticles() == 0 {
			return nil
		}
		return fmt.Errorf("%w: %d articles missing and no par2 to repair them",
			ErrPar2Unavailable, j.failedArticles())
	}
	j.setStage(downloadv1alpha1.DownloadStageRepairing, download.StatusDownloading)
	if _, err := j.client.par2.Repair(ctx, j.contentDir(), safeName(index)); err != nil {
		return err
	}
	return removePar2Backups(ctx, j.contentDir(), j.nzb.Files)
}

// publish moves the finished content into the data directory with one atomic
// rename, so the importer never sees a half-written release.
func (j *job) publish(ctx context.Context, content string) error {
	j.setStage(downloadv1alpha1.DownloadStagePublishing, download.StatusDownloading)

	name := j.name
	if name == "" {
		name = j.nzb.Title
	}
	dest := filepath.Join(j.client.cfg.PublishDir, safeName(j.category), safeName(name))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("usenet: create %s: %w", filepath.Dir(dest), err)
	}
	// A previous, failed publish may have left the destination behind. It is
	// removed rather than merged into: a half-published directory plus a new
	// one is how an importer picks up a file from a run that failed.
	if _, err := os.Lstat(dest); err == nil {
		if err := fsops.SafeRemove(ctx, j.client.cfg.PublishDir, dest); err != nil {
			return err
		}
	}
	if err := fsops.MoveAtomic(content, dest); err != nil {
		return err
	}

	files, err := listContent(dest)
	if err != nil {
		return err
	}

	j.mu.Lock()
	j.outputPath = dest
	j.publishedFiles = files
	j.status = download.StatusCompleted
	j.stage = downloadv1alpha1.DownloadStageDone
	j.mu.Unlock()
	return j.checkpoint()
}

// listContent walks a published directory into [download.File] entries, whose
// Path is relative to it.
func listContent(root string) ([]download.File, error) {
	var out []download.File
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		out = append(out, download.File{Path: rel, SizeBytes: info.Size()})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("usenet: list %s: %w", root, err)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Path < out[b].Path })
	return out, nil
}

func (j *job) snapshotStatus() download.Status {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.pausedLocked() {
		return download.StatusPaused
	}
	return j.status
}

// pausedLocked reports whether the job reads as paused: a pause while it
// transfers, or a health pause at any point before it finishes -- the
// pre-check's included, which runs as Queued. The caller holds mu.
func (j *job) pausedLocked() bool {
	switch {
	case j.status == download.StatusCompleted || j.status == download.StatusFailed:
		return false
	case j.healthPaused:
		return true
	default:
		return j.paused.Load() && j.status == download.StatusDownloading
	}
}

func (j *job) isHealthPaused() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.healthPaused
}

func (j *job) snapshotOutputPath() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.outputPath
}

// item renders the transfer as the engine's view of it.
func (j *job) item() download.Item {
	// Before j.mu: outranked takes the client lock and then every other
	// job's lock, and nothing may take the client lock while holding a job's.
	outranked := j.client.outranked(j)

	j.mu.Lock()
	defer j.mu.Unlock()

	total := j.nzb.TotalBytes
	downloaded := min(j.downloaded, total)
	remaining := total - downloaded

	status := j.status
	message := j.message
	switch {
	case j.pausedLocked():
		status = download.StatusPaused
	case outranked && status == download.StatusDownloading:
		// Waiting its turn behind a higher-priority transfer: it moves no
		// bytes, which is what Queued means. Stage stays where the pipeline
		// is, since a transfer that already started keeps its partial files.
		status = download.StatusQueued
		message = "waiting for higher-priority transfers to finish"
	}

	var progress int32
	if total > 0 {
		progress = int32(downloaded * 100 / total) //nolint:gosec // bounded by 100.
	}

	failed := 0
	for _, b := range j.failedSegs {
		failed += b.count()
	}
	health := int32(100)
	if j.nzb.TotalSegments > 0 {
		health = int32((j.nzb.TotalSegments - failed) * 100 / j.nzb.TotalSegments) //nolint:gosec // bounded by 100.
	}

	it := download.Item{
		ID:              j.id,
		Status:          status,
		Stage:           j.stage,
		FailureReason:   j.reason,
		TotalBytes:      total,
		RemainingBytes:  remaining,
		DownloadedBytes: downloaded,
		DownRate:        j.rate.bps(),
		ProgressPercent: progress,
		OutputPath:      j.outputPath,
		IsEncrypted:     j.encrypted,
		HealthPaused:    j.healthPaused && j.pausedLocked(),
		Message:         message,
		AddedAt:         j.addedAt,
		Health: &downloadv1alpha1.UsenetHealth{
			HealthPercent:         health,
			CriticalHealthPercent: j.nzb.criticalHealthPercent(),
			FailedArticles:        int32(failed),              //nolint:gosec // bounded by the NZB.
			TotalArticles:         int32(j.nzb.TotalSegments), //nolint:gosec // bounded by the NZB.
		},
	}

	if j.outputPath != "" {
		it.ContentRoot = j.outputPath
		it.Files = append([]download.File(nil), j.publishedFiles...)
	} else {
		it.ContentRoot = j.contentDir()
		for _, f := range j.nzb.Files {
			it.Files = append(it.Files, download.File{Path: safeName(f.Name), SizeBytes: f.Bytes})
		}
	}

	// A usenet transfer publishes with an atomic rename, so the moment it is
	// Completed the files are final and the importer may move them. That is
	// the *arr invariant (research note §3: "usenet files move immediately
	// upon completion"), and it is why CanMoveFiles is not gated on anything
	// further.
	it.CanMoveFiles = j.status == download.StatusCompleted && j.outputPath != ""
	// Nothing keeps a finished usenet transfer alive: there is no seeding, so
	// once the import is done -- or the transfer has failed -- the engine has
	// no reason to hold it.
	it.CanBeRemoved = j.imported || j.status == download.StatusFailed

	if !j.lastProgress.IsZero() {
		at := j.lastProgress
		it.LastProgressAt = &at
	}
	if rate := j.rate.bps(); rate > 0 && remaining > 0 {
		eta := time.Duration(remaining/rate) * time.Second
		it.ETA = &eta
	}
	return it
}

// Get returns one transfer by id.
func (c *Client) Get(_ context.Context, id string) (download.Item, error) {
	j, err := c.lookup(id)
	if err != nil {
		return download.Item{}, err
	}
	return j.item(), nil
}

// List returns every transfer the client holds, re-attached ones included.
func (c *Client) List(_ context.Context) ([]download.Item, error) {
	c.mu.Lock()
	jobs := make([]*job, 0, len(c.jobs))
	for _, j := range c.jobs {
		jobs = append(jobs, j)
	}
	c.mu.Unlock()

	out := make([]download.Item, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, j.item())
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out, nil
}

func (c *Client) lookup(id string) (*job, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	j, ok := c.jobs[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", download.ErrNotFound, id)
	}
	return j, nil
}

// Pause suspends a transfer. The partial files stay in the scratch area, so
// resuming re-fetches only the articles that never landed.
//
// Pausing a health-paused job is the operator acknowledging the breach: the
// health pause becomes an ordinary pause, and the health check is off for the
// rest of the job, so the Resume that follows carries on rather than pausing
// again at the next batch ([Config.HealthAction]).
func (c *Client) Pause(_ context.Context, id string) error {
	j, err := c.lookup(id)
	if err != nil {
		return err
	}
	j.mu.Lock()
	if j.healthPaused {
		j.healthPaused = false
		j.healthOverride = true
		j.message = ""
	}
	j.mu.Unlock()
	j.paused.Store(true)
	return j.checkpoint()
}

// Resume undoes Pause. Resuming a transfer that is not paused is a no-op
// returning nil, because the controller calls it from a level-driven reconcile
// that cannot know the client's current state.
//
// It is also a no-op on a health-paused job. The engine resumes whenever
// spec.paused is false, which it is for a job the health action paused, so a
// Resume that lifted the health pause would undo it on the next poll; the
// operator lifts it with a Pause first ([Client.Pause]).
func (c *Client) Resume(_ context.Context, id string) error {
	j, err := c.lookup(id)
	if err != nil {
		return err
	}
	if j.isHealthPaused() {
		return nil
	}
	j.paused.Store(false)
	return j.checkpoint()
}

// SetPriority changes the job's priority class. The class only gates the
// transfer ([Client.outranked]), so the change takes effect at the next batch
// boundary; the same class again is a no-op and writes nothing.
func (c *Client) SetPriority(_ context.Context, id string, priority downloadv1alpha1.DownloadPriority) error {
	j, err := c.lookup(id)
	if err != nil {
		return err
	}
	if !j.setPriority(priority) {
		return nil
	}
	return j.checkpoint()
}

// SetSeedCriteria is a no-op returning nil.
//
// A usenet transfer never seeds, so "stop when this goal is met" is vacuously
// already met. [download.Client] spells the reasoning out: returning an error
// instead would force the Download controller to branch on protocol for a call
// whose outcome it does not use.
//
// It returns nil even for an unknown id. download.ErrNotFound's own doc lists
// SetSeedCriteria among the methods that report it, and the tension is real,
// but the controller reads ErrNotFound as "this engine has not re-attached
// yet, retry" -- so reporting it here would schedule retries of a call that
// can never do anything, for a protocol that has no seeding at all.
func (c *Client) SetSeedCriteria(_ context.Context, _ string, _ commonv1alpha1.SeedCriteria) error {
	return nil
}

// MarkImported tells the client the content is in the library, so the scratch
// area may go.
func (c *Client) MarkImported(ctx context.Context, id string) error {
	j, err := c.lookup(id)
	if err != nil {
		return err
	}
	j.mu.Lock()
	j.imported = true
	j.mu.Unlock()
	if err := j.checkpoint(); err != nil {
		return err
	}
	// The scratch area is per-job and node-local; the published content lives
	// in the data directory and is untouched. Hardlinks the importer made
	// survive either way -- a hard link is not a copy.
	return j.cleanScratch(ctx)
}

// cleanScratch removes everything in the job directory except the manifest and
// the stored NZB, which are what keep [Client.Add] idempotent across restarts.
func (j *job) cleanScratch(ctx context.Context) error {
	entries, err := os.ReadDir(j.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("usenet: read %s: %w", j.dir, err)
	}
	for _, e := range entries {
		if e.Name() == manifestName || e.Name() == nzbName {
			continue
		}
		if err := fsops.SafeRemove(ctx, j.dir, e.Name()); err != nil {
			return err
		}
	}
	return nil
}

// Remove stops a transfer and forgets it.
//
// Forgetting is durable: the job's scratch directory -- manifest, stored
// NZB and any partial content -- goes on every Remove, deleteData or not
// ([download.Client.Remove]). Keeping the manifest would let the next
// restart re-attach the job this call forgot, which is how a reaped orphan
// (the reapers always pass deleteData=false) came back after every restart.
// The scratch area is this engine's private working space, not the
// downloaded data; deleteData governs only the published content in
// DataDir.
//
// Removing an unknown id reports [download.ErrNotFound], which a finalizer
// treats as success -- that is what makes Remove idempotent.
func (c *Client) Remove(ctx context.Context, id string, deleteData bool) error {
	j, err := c.lookup(id)
	if err != nil {
		return err
	}

	if finished := j.stop(); finished != nil {
		select {
		case <-finished:
		case <-time.After(30 * time.Second):
			// The transfer is wedged on a provider that will not answer.
			// Forget it anyway: holding the Download object hostage to a hung
			// socket is worse than leaking one goroutine that its own
			// deadline will end.
		}
	}

	c.forget(id)

	if err := fsops.SafeRemove(ctx, c.cfg.ScratchDir, j.dir); err != nil {
		return err
	}
	if !deleteData {
		return nil
	}
	out := j.snapshotOutputPath()
	if out == "" {
		return nil
	}
	return fsops.SafeRemove(ctx, c.cfg.PublishDir, out)
}

// Close stops every transfer and releases the pool's connections. It blocks
// until in-flight work has stopped, so the process can exit without leaving a
// half-written file behind.
func (c *Client) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		jobs := make([]*job, 0, len(c.jobs))
		for _, j := range c.jobs {
			jobs = append(jobs, j)
		}
		c.mu.Unlock()
		for _, j := range jobs {
			j.stop()
		}
		c.wg.Wait()
		c.pool.Close()
	})
	return nil
}

// ProviderFromSpec flattens a CRD provider plus its resolved credentials into
// a [Provider]. It lives here so the engine runnable does not restate the
// defaulting the CRD already documents.
func ProviderFromSpec(p downloadv1alpha1.NNTPProvider, username, password string) Provider {
	tls := true
	if p.TLS != nil {
		tls = *p.TLS
	}
	port := int(p.Port)
	if port == 0 {
		if tls {
			port = 563
		} else {
			port = 119
		}
	}
	conns := int(p.Connections)
	if conns <= 0 {
		conns = 8
	}
	prov := Provider{
		Name:        p.Name,
		Host:        strings.TrimSpace(p.Host),
		Port:        port,
		TLS:         tls,
		Username:    username,
		Password:    password,
		Connections: conns,
		Backup:      p.Backup,
		Priority:    p.Priority,
	}
	if p.QuotaBytes != nil {
		prov.QuotaBytes = *p.QuotaBytes
	}
	return prov
}
