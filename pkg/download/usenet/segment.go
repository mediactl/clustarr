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
	stop()

	if fatal != nil {
		return fatal
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	failed := j.failedArticles()
	if failed > 0 {
		log.WarnContext(ctx, "usenet transfer finished with missing articles",
			"download", j.id, "failed", failed, "total", j.nzb.TotalSegments)
	}
	return j.checkpoint()
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
	return j.healthGate()
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
// transfer.
func (j *job) healthGate() error {
	health := j.healthPercent()
	if health >= j.abortHealth && health >= j.nzb.criticalHealthPercent() {
		return nil
	}
	j.mu.Lock()
	last := j.lastError
	j.mu.Unlock()
	if last != nil {
		return fmt.Errorf("%w: health %d%% is below the floor (abort %d%%, critical %d%%); last article error: %w",
			ErrUnrecoverable, health, j.abortHealth, j.nzb.criticalHealthPercent(), last)
	}
	return fmt.Errorf("%w: health %d%% is below the floor (abort %d%%, critical %d%%)",
		ErrUnrecoverable, health, j.abortHealth, j.nzb.criticalHealthPercent())
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
