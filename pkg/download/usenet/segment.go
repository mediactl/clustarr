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

package usenet

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// bitset records which of a file's segments are already on disk, so a restart
// resumes instead of re-fetching a 50GB release from the first article.
//
// It is a []uint64 and not a []bool because it is checkpointed to disk on a
// timer: 140,000 segments is 17KB as bits and 140KB as bools, and the
// checkpoint is an fsops.AtomicWrite that fsyncs.
type bitset []uint64

func newBitset(n int) bitset {
	if n <= 0 {
		return nil
	}
	return make(bitset, (n+63)/64)
}

func (b bitset) set(i int) {
	if i < 0 || i/64 >= len(b) {
		return
	}
	b[i/64] |= 1 << (uint(i) % 64)
}

// clear unsets bit i.
func (b bitset) clear(i int) {
	if i/64 < len(b) {
		b[i/64] &^= 1 << (uint(i) % 64) //nolint:gosec // i is a segment index.
	}
}

func (b bitset) has(i int) bool {
	if i < 0 || i/64 >= len(b) {
		return false
	}
	return b[i/64]&(1<<(uint(i)%64)) != 0
}

func (b bitset) count() int {
	n := 0
	for _, w := range b {
		for ; w != 0; w &= w - 1 {
			n++
		}
	}
	return n
}

// rateMeter turns a monotonically increasing byte counter into a bytes/second
// rate over a sliding sample, without floating point reaching anything the API
// can see.
type rateMeter struct {
	mu     sync.Mutex
	anchor time.Time
	bytes  int64
	rate   int64
}

func (m *rateMeter) observe(total int64, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.anchor.IsZero() {
		m.anchor, m.bytes = now, total
		return
	}
	elapsed := now.Sub(m.anchor)
	if elapsed < time.Second {
		return
	}
	m.rate = (total - m.bytes) * int64(time.Second) / int64(elapsed)
	m.anchor, m.bytes = now, total
}

func (m *rateMeter) bps() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rate
}

// batch is one pipelined run of segments from a single file. Segments of one
// file are kept together because they land in one file handle and because a
// provider's cache is warmer for adjacent articles of the same post.
type batch struct {
	file int
	segs []int
}

// buildBatches groups every segment still missing into runs of at most depth,
// skipping the ones a previous run already wrote.
func (j *job) buildBatches(depth int) []batch {
	if depth < 1 {
		depth = 1
	}
	var out []batch
	for fi := range j.nzb.Files {
		cur := batch{file: fi}
		for si := range j.nzb.Files[fi].Segments {
			if j.done[fi].has(si) {
				continue
			}
			cur.segs = append(cur.segs, si)
			if len(cur.segs) == depth {
				out = append(out, cur)
				cur = batch{file: fi}
			}
		}
		if len(cur.segs) > 0 {
			out = append(out, cur)
		}
	}
	return out
}

// openTargets creates (or reopens) one file handle per NZB file.
//
// The part files are NOT written through fsops.AtomicWrite, and that is
// deliberate rather than an oversight: a 50GB release is assembled by random
// writes at yEnc offsets across hours, so there is no "complete content" to
// write atomically until the very end. Crash survival is carried instead by
// the manifest -- which IS an fsops.AtomicWrite -- plus the segment bitsets it
// holds, so a restart re-fetches only the articles that never landed. The one
// write that must be all-or-nothing is the publish, and that is a
// fsops.MoveAtomic.
func (j *job) openTargets() ([]*os.File, error) {
	if err := os.MkdirAll(j.contentDir(), 0o755); err != nil {
		return nil, fmt.Errorf("usenet: create %s: %w", j.contentDir(), err)
	}
	files := make([]*os.File, len(j.nzb.Files))
	for i := range j.nzb.Files {
		p := filepath.Join(j.contentDir(), safeName(j.nzb.Files[i].Name))
		f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE, 0o644)
		if err != nil {
			for _, open := range files {
				if open != nil {
					_ = open.Close()
				}
			}
			return nil, fmt.Errorf("usenet: create %s: %w", p, err)
		}
		files[i] = f
	}
	return files, nil
}

// safeName reduces an NZB-supplied filename to a single path element. NZB
// subjects are attacker-controlled: "../../etc/cron.d/x" must become a file in
// the job directory, not a write outside it.
func safeName(name string) string {
	name = filepath.Base(filepath.Clean("/" + strings.ReplaceAll(name, "\\", "/")))
	if name == "." || name == string(filepath.Separator) || name == "" {
		return "unnamed"
	}
	return name
}

// transfer fetches every missing article and writes it at its yEnc offset.
//
// Concurrency is the sum of the providers' connection allowances, but the
// ceiling that actually binds is each serverPool's own semaphore -- workers
// queue on it rather than opening a connection the provider would refuse.
func (j *job) transfer(ctx context.Context) error {
	ctx, span := tracing.Start(ctx, "usenet.job.transfer")
	defer span.End()

	log := logging.FromContext(ctx)

	// Honour a pause -- or a higher-priority transfer (priority.go) --
	// before the first article, not only between batches: a transfer added
	// paused must move no bytes at all, and fetchFirstArticles runs ahead of
	// the worker loop that does the per-batch check.
	if err := j.waitForTurn(ctx); err != nil {
		return err
	}

	targets, err := j.openTargets()
	if err != nil {
		return err
	}
	defer func() {
		for _, f := range targets {
			if f != nil {
				_ = f.Close()
			}
		}
	}()

	// First articles first. Learning each file's yEnc header before the bulk
	// transfer gives the authoritative decoded size -- the NZB only knows the
	// wire size -- so every file can be sized once instead of growing under
	// concurrent writes. It is also what NZBGet fetches first, for the same
	// reason par2 renaming needs it.
	if err := j.fetchFirstArticles(ctx, targets); err != nil {
		return err
	}

	batches := j.buildBatches(j.client.cfg.PipelineDepth)
	if len(batches) == 0 {
		return nil
	}

	work := make(chan batch)
	var wg sync.WaitGroup
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		fatalOnce sync.Once
		fatal     error
	)
	fail := func(err error) {
		fatalOnce.Do(func() {
			fatal = err
			cancel()
		})
	}

	for range j.client.workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b := range work {
				if err := j.waitForTurn(workerCtx); err != nil {
					return
				}
				if err := j.runBatch(workerCtx, targets, b); err != nil {
					if !errors.Is(err, context.Canceled) {
						fail(err)
					}
					return
				}
			}
		}()
	}

	stop := j.startCheckpoint(ctx)
	stopWatch := j.startStallWatch(workerCtx, fail)

dispatch:
	for _, b := range batches {
		select {
		case work <- b:
		case <-workerCtx.Done():
			break dispatch
		}
	}
	close(work)
	wg.Wait()
	stopWatch()
	stop()

	if fatal != nil {
		return fatal
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	if j.failedArticles() > 0 {
		if err := j.retryFailed(ctx); err != nil {
			return err
		}
	}
	if failed := j.failedArticles(); failed > 0 {
		log.WarnContext(ctx, "usenet transfer finished with missing articles",
			"download", j.id, "failed", failed, "total", j.nzb.TotalSegments)
	}
	return j.checkpoint()
}

// retryFailed is the second chance every missing article gets: one more
// pass over the failed segments, across every server, after
// Config.ArticleRetryDelay. It runs once per job -- from the health gate
// when a breach is imminent, else at the end of the transfer -- and clears
// whatever it recovers. SABnzbd gives this chance only on an operator's
// Retry; NZBGet's ArticleRetries gives it to every article. A 430 on a
// reseller is often a propagation or sync gap that has closed by the time
// the rest of the release is down (both grabs on 2026-09-24 lost 1-3% of
// their articles to a single provider). A provider outage during the pass
// is ErrProvidersUnavailable, which the caller waits out like any other.
func (j *job) retryFailed(ctx context.Context) error {
	j.retryMu.Lock()
	defer j.retryMu.Unlock()
	if j.retried {
		return nil
	}
	j.retried = true

	type seg struct{ fi, si int }
	var failed []seg
	j.mu.Lock()
	for fi, b := range j.failedSegs {
		for si := range j.nzb.Files[fi].Segments {
			if b.has(si) {
				failed = append(failed, seg{fi, si})
			}
		}
	}
	j.mu.Unlock()
	if len(failed) == 0 {
		return nil
	}

	log := logging.FromContext(ctx)
	log.InfoContext(ctx, "usenet: retrying missing articles once more",
		"download", j.id, "articles", len(failed), "delay", j.client.cfg.ArticleRetryDelay)
	if err := sleepCtx(ctx, j.client.cfg.ArticleRetryDelay); err != nil {
		return err
	}

	targets, err := j.openTargets()
	if err != nil {
		return err
	}
	defer func() {
		for _, f := range targets {
			if f != nil {
				_ = f.Close()
			}
		}
	}()

	depth := max(j.client.cfg.PipelineDepth, 1)
	recovered := 0
	for start := 0; start < len(failed); start += depth {
		end := min(start+depth, len(failed))
		chunk := failed[start:end]
		ids := make([]string, len(chunk))
		for i, sg := range chunk {
			ids[i] = j.nzb.Files[sg.fi].Segments[sg.si].ID
		}
		results, err := j.client.pool.FetchBatch(ctx, ids)
		if err != nil {
			return err
		}
		for i, r := range results {
			if errors.Is(r.Err, ErrProvidersUnavailable) {
				return r.Err
			}
			if r.Err != nil {
				continue
			}
			sg := chunk[i]
			if err := j.writeSegment(targets[sg.fi], sg.fi, sg.si, r); err != nil {
				return err
			}
			j.mu.Lock()
			j.failedSegs[sg.fi].clear(sg.si)
			j.mu.Unlock()
			recovered++
		}
	}
	log.InfoContext(ctx, "usenet: missing-article retry finished",
		"download", j.id, "recovered", recovered, "stillMissing", len(failed)-recovered)
	return j.checkpoint()
}

// missingBytes totals the failed segments of non-par2 files, SABnzbd's
// bytes_missing: par2 that is absent does not stop the job completing.
func (j *job) missingBytes() int64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	var total int64
	for fi, b := range j.failedSegs {
		f := &j.nzb.Files[fi]
		if f.Kind == kindPar2Index || f.Kind == kindPar2Volume {
			continue
		}
		for si := range f.Segments {
			if b.has(si) {
				total += f.Segments[si].Bytes
			}
		}
	}
	return total
}

// breached reports whether the job's missing articles cross a floor: the
// operator's abortHealthPercent, or SABnzbd's hopelessness rule -- and only
// once more than maxBadArticles are missing, which SABnzbd tolerates
// outright so a plain RAR set unrar can verify is not abandoned for a few.
func (j *job) breached() bool {
	if j.failedArticles() <= maxBadArticles {
		return false
	}
	return j.healthPercent() < j.abortHealth || hopeless(j.nzb.TotalBytes, j.nzb.par2Bytes(), j.missingBytes())
}

// fetchFirstArticles fetches segment 1 of every file in one pipelined sweep.
func (j *job) fetchFirstArticles(ctx context.Context, targets []*os.File) error {
	ids := make([]string, 0, len(j.nzb.Files))
	idx := make([]int, 0, len(j.nzb.Files))
	for fi := range j.nzb.Files {
		if len(j.nzb.Files[fi].Segments) == 0 || j.done[fi].has(0) {
			continue
		}
		ids = append(ids, j.nzb.Files[fi].Segments[0].ID)
		idx = append(idx, fi)
	}
	if len(ids) == 0 {
		return nil
	}

	results, err := j.client.pool.FetchBatch(ctx, ids)
	if err != nil {
		return err
	}
	var unavailable error
	for i, r := range results {
		fi := idx[i]
		if errors.Is(r.Err, ErrProvidersUnavailable) {
			// Not missing: nobody could be asked. Left neither done nor
			// failed, so the retry after the provider wait fetches it.
			unavailable = cmp.Or(unavailable, r.Err)
			continue
		}
		if r.Err != nil {
			j.recordFailure(fi, 0, r.Err)
			continue
		}
		if r.Meta.FileSize > 0 {
			if err := targets[fi].Truncate(r.Meta.FileSize); err != nil {
				return fmt.Errorf("usenet: size %s: %w", j.nzb.Files[fi].Name, err)
			}
		}
		if err := j.writeSegment(targets[fi], fi, 0, r); err != nil {
			return err
		}
	}
	if err := j.checkpoint(); err != nil {
		return err
	}
	return unavailable
}

// runBatch fetches one pipelined run and writes what came back.
func (j *job) runBatch(ctx context.Context, targets []*os.File, b batch) error {
	ids := make([]string, len(b.segs))
	for i, si := range b.segs {
		ids[i] = j.nzb.Files[b.file].Segments[si].ID
	}
	results, err := j.client.pool.FetchBatch(ctx, ids)
	if err != nil {
		return err
	}
	var unavailable error
	for i, r := range results {
		si := b.segs[i]
		if errors.Is(r.Err, ErrProvidersUnavailable) {
			// Not missing, and not counted against health: see
			// fetchFirstArticles.
			unavailable = cmp.Or(unavailable, r.Err)
			continue
		}
		if r.Err != nil {
			j.recordFailure(b.file, si, r.Err)
			continue
		}
		if err := j.writeSegment(targets[b.file], b.file, si, r); err != nil {
			return err
		}
	}
	if unavailable != nil {
		return unavailable
	}
	return j.healthGate(ctx)
}

// writeSegment puts one decoded part at its authoritative offset.
func (j *job) writeSegment(f *os.File, fi, si int, r Result) error {
	if _, err := f.WriteAt(r.Body, r.Meta.Offset); err != nil {
		return fmt.Errorf("usenet: write %s at %d: %w", j.nzb.Files[fi].Name, r.Meta.Offset, err)
	}
	j.mu.Lock()
	j.done[fi].set(si)
	// Progress is counted in the NZB's own segment bytes, not in decoded
	// bytes. The two differ by yEnc's ~2-3% overhead, and TotalBytes is the
	// sum of the NZB's figures -- so counting decoded bytes here would make
	// every completed download report about 97%.
	j.downloaded += j.nzb.Files[fi].Segments[si].Bytes
	j.lastProgress = time.Now()
	j.mu.Unlock()
	return nil
}

func (j *job) recordFailure(fi, si int, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.failedSegs[fi].has(si) {
		return
	}
	j.failedSegs[fi].set(si)
	j.lastError = err
}

func (j *job) failedArticles() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	n := 0
	for _, b := range j.failedSegs {
		n += b.count()
	}
	return n
}

// healthPercent is the share of articles fetched so far, 0-100.
func (j *job) healthPercent() int32 {
	if j.nzb.TotalSegments == 0 {
		return 100
	}
	failed := j.failedArticles()
	good := j.nzb.TotalSegments - failed
	return int32(good * 100 / j.nzb.TotalSegments) //nolint:gosec // bounded by 100.
}

// ErrUnrecoverable is returned when more articles are missing than the NZB's
// par2 volumes could ever replace, or than the configured health floor allows.
//
// It is a sentinel because it is the difference between a download worth
// continuing and one that should stop burning quota now: it maps to
// DownloadFailureMissingArticles, and the *arr contract on that is blocklist
// and re-search, not retry.
var ErrUnrecoverable = errors.New("usenet: too many articles missing to recover")

// healthGate stops a hopeless download early. SABnzbd calls it "abort jobs
// that cannot be completed" and NZBGet calls it critical health; both exist
// because finishing a 50GB transfer that par2 cannot repair wastes the whole
// transfer. What "stops" means is the health action's ([job.breach]).
func (j *job) healthGate(ctx context.Context) error {
	if !j.breached() {
		return nil
	}
	// Imminent breach: spend the second chance first, then judge again.
	if err := j.retryFailed(ctx); err != nil {
		return err
	}
	if !j.breached() {
		return nil
	}
	j.mu.Lock()
	last := j.lastError
	j.mu.Unlock()
	return j.breach(ctx, fmt.Sprintf("health %d%% is below the floor (abort %d%%, critical %d%%): %d of %d articles missing",
		j.healthPercent(), j.abortHealth, j.nzb.criticalHealthPercent(), j.failedArticles(), j.nzb.TotalSegments), last)
}

// unverifiableGate is the post-process rule SABnzbd does not have and the
// importer needs: articles still missing after the retry pass are only
// acceptable when par2 can repair them or an archive's own checksums will
// judge the result. Bare content with holes would otherwise be published
// as if whole, and the library would keep a damaged file.
func (j *job) unverifiableGate(ctx context.Context) error {
	failed := j.failedArticles()
	if failed == 0 || j.nzb.par2Bytes() > 0 || len(archiveEntryPoints(j.nzb.Files)) > 0 {
		return nil
	}
	j.mu.Lock()
	last := j.lastError
	j.mu.Unlock()
	return j.breach(ctx, fmt.Sprintf("health %d%%: %d of %d articles missing, with no par2 to repair them and no archive to verify the result",
		j.healthPercent(), failed, j.nzb.TotalSegments), last)
}

// breach applies [Config.HealthAction] to a health-floor breach described by
// detail, with cause the last article error if there is one.
//
// Under delete it returns [ErrUnrecoverable], which fails the job as
// missingArticles. Under pause it pauses the job -- once; the report and the
// checkpoint are written on the first breach only -- and returns nil, and
// the caller waits the pause out the way it waits out any other. After an
// operator has acknowledged a health pause (healthOverride) it returns nil
// and does nothing: NZBGet does not health-check a job it has already
// health-paused once.
func (j *job) breach(ctx context.Context, detail string, cause error) error {
	if j.client.cfg.HealthAction == downloadv1alpha1.HealthActionDelete {
		if cause != nil {
			return fmt.Errorf("%w: %s; last article error: %w", ErrUnrecoverable, detail, cause)
		}
		return fmt.Errorf("%w: %s", ErrUnrecoverable, detail)
	}

	j.mu.Lock()
	if j.healthOverride || j.healthPaused {
		j.mu.Unlock()
		return nil
	}
	j.healthPaused = true
	j.paused.Store(true)
	j.message = "article " + detail + ": paused by healthAction pause; " +
		"set spec.paused to true and back to false to continue without the health check, " +
		"or delete or blocklist the Download"
	j.mu.Unlock()
	if err := j.checkpoint(); err != nil {
		logging.FromContext(ctx).WarnContext(ctx, "usenet checkpoint failed", "download", j.id, "error", err)
	}
	logging.FromContext(ctx).WarnContext(ctx, "usenet download paused by its health action",
		"download", j.id, "detail", detail)
	return nil
}

// ErrStalled is returned when a transferring job completed no article for
// Config.StallTimeout: the counterpart of the torrent engine's stall, and
// like it a verdict on the release (blocklisted). A provider that cannot be
// asked is ErrProvidersUnavailable instead, and is waited out, not judged.
var ErrStalled = errors.New("usenet: no article completed within the stall timeout")

// startStallWatch fails the transfer through fail when no article has
// completed for Config.StallTimeout, measured from the later of the watch's
// start and the last completed article, and not while paused. It returns a
// stop func. Zero timeout means no watch.
func (j *job) startStallWatch(ctx context.Context, fail func(error)) func() {
	timeout := j.client.cfg.StallTimeout
	if timeout <= 0 {
		return func() {}
	}
	done := make(chan struct{})
	quit := make(chan struct{})
	start := time.Now()
	go func() {
		defer close(done)
		tick := min(timeout/4, 30*time.Second)
		if tick <= 0 {
			tick = time.Millisecond
		}
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-quit:
				return
			case now := <-t.C:
				if j.paused.Load() {
					start = now // a pause is the operator's time, not the release's
					continue
				}
				j.mu.Lock()
				last := j.lastProgress
				j.mu.Unlock()
				if last.Before(start) {
					last = start
				}
				if idle := now.Sub(last); idle >= timeout {
					fail(fmt.Errorf("%w: %s without a completed article (timeout %s)", ErrStalled, idle.Truncate(time.Second), timeout))
					return
				}
			}
		}
	}()
	return func() {
		close(quit)
		<-done
	}
}

// startCheckpoint persists the bitsets on a timer and returns a stop func.
//
// The interval is a trade between fsyncs and re-fetched articles after a
// crash: two seconds costs one small atomic write per two seconds and bounds
// the loss to whatever landed in that window.
func (j *job) startCheckpoint(ctx context.Context) func() {
	done := make(chan struct{})
	quit := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-quit:
				return
			case now := <-t.C:
				j.rate.observe(j.downloadedBytes(), now)
				if err := j.checkpoint(); err != nil {
					logging.FromContext(ctx).WarnContext(ctx, "usenet checkpoint failed",
						"download", j.id, "error", err)
				}
			}
		}
	}()
	// The stop func closes quit rather than relying on ctx: the transfer
	// finishing is not the same event as the job being cancelled, and waiting
	// on ctx here would hang every transfer that simply succeeded.
	return func() {
		close(quit)
		<-done
	}
}
