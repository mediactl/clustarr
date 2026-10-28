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
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	par2 "github.com/mediactl/par2go"
	"go.opentelemetry.io/otel/attribute"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/par2child"
)

// probeTimeout bounds one probe of the library.
const probeTimeout = 10 * time.Second

// LibraryRepairer repairs through par2go: in a child of this binary in the
// engine (par2child.Exec{}), in-process in tests (par2child.InProcess{})
// (split spec 2026-10-06 §8.5.2).
type LibraryRepairer struct {
	// Runner runs one repair.
	Runner par2child.Runner
	// Threads is par2's compute threads; 0 is par2go's default (GOMAXPROCS,
	// cgroup-aware).
	Threads int
	// FileThreads is how many files par2 verifies at once; 0 is par2go's
	// default, 2.
	FileThreads int
	// MemoryLimit is par2's buffer budget in bytes; 0 is par2go's default
	// (1/8 of the cgroup's limit, at least 256 MiB).
	MemoryLimit int64

	mu    sync.Mutex
	ready bool
}

// Available probes the library (in a child, for [par2child.Exec]) until a
// probe succeeds, then answers from that for the process's life. A failure
// probes again on the next call, which comes only when a job needs repair.
func (r *LibraryRepairer) Available() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ready {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	r.ready = r.Runner.Probe(ctx) == nil
	return r.ready
}

// Repair verifies dir against indexFile and repairs it when it can.
func (r *LibraryRepairer) Repair(ctx context.Context, dir, indexFile string) (Par2Result, error) {
	ctx, span := tracing.Start(ctx, "usenet.par2.repair")
	defer span.End()

	abs, err := filepath.Abs(dir)
	if err != nil {
		return Par2Result{}, fmt.Errorf("usenet: par2 job directory %s: %w", dir, err)
	}
	// Every file other than the index is passed as an extra, the way SABnzbd
	// and NZBGet run par2. A set whose recovery data describes obfuscated
	// names ("z75QO...part070.rar") while the files on disk carry the
	// subjects' names otherwise reports every target as missing and repair
	// fails outright -- the first real grab on the owner's cluster,
	// 2026-09-24, after a complete 10 GB transfer. Given the extras, par2
	// matches them by content and repairs under the recorded names.
	res, err := r.Runner.Run(ctx, par2child.Request{
		Op:          par2child.OpRepair,
		Index:       filepath.Join(abs, indexFile),
		Dir:         abs,
		Extras:      par2Extras(abs, indexFile),
		Threads:     r.Threads,
		FileThreads: r.FileThreads,
		MemoryLimit: r.MemoryLimit,
	})
	span.SetAttributes(
		attribute.String("par2.status", res.Status.String()),
		attribute.Int("par2.blocks_needed", res.BlocksNeeded),
		attribute.Int("par2.recovery_blocks", res.Headers.RecoveryBlocks),
		attribute.Int("par2.missing_files", res.Headers.MissingFiles),
	)
	log := logging.FromContext(ctx)
	log.DebugContext(ctx, "par2 finished", "index", indexFile, "status", res.Status, "error", err, "library", res.Library)
	if errors.Is(err, par2child.ErrChildDied) && ctx.Err() != nil {
		// Its deadline, grace and reap slack have passed: a read blocked in
		// the kernel (D state on a hung NFS mount). The child is reaped,
		// and par2child.Unreaped uncounts it, once that I/O returns.
		log.ErrorContext(ctx, "par2 child did not exit after its deadline", "dir", abs, "error", err)
	}
	out, verr := par2Verdict(res, err)
	if verr != nil {
		tracing.RecordError(span, verr)
	}
	return out, verr
}

// par2Verdict maps one library run onto the verdicts the exec'd par2's
// output gave (§8.5.3), with OD22: out of memory, or a child that died
// without a result, is [ErrPar2Crashed], a local fault, not a failed repair.
// It is pure, so it is table-tested without the library.
func par2Verdict(res par2child.Result, err error) (Par2Result, error) {
	out := Par2Result{BlocksNeeded: res.BlocksNeeded, Output: tail(res.Log, 2048)}
	switch {
	case err == nil && res.Status == par2.AllCorrect:
		out.AllCorrect = true
		return out, nil
	case err == nil && res.Status == par2.Repaired:
		out.Repaired = true
		return out, nil
	case err == nil && res.Status == par2.RepairNotPossible:
		return out, fmt.Errorf("%w: %d more recovery blocks needed: %s", ErrRepairFailed, res.BlocksNeeded, out.Output)
	case err == nil:
		return out, fmt.Errorf("%w: par2 reported %s: %s", ErrRepairFailed, res.Status, out.Output)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return out, err
	case errors.Is(err, par2.ErrUnavailable):
		return out, fmt.Errorf("%w: %w", ErrPar2Unavailable, err)
	case errors.Is(err, par2.ErrMemory), errors.Is(err, par2child.ErrChildDied):
		return out, fmt.Errorf("%w: %w", ErrPar2Crashed, err)
	case errors.Is(err, par2.ErrInvalidOptions):
		return out, fmt.Errorf("usenet: par2 refused its options: %w", err)
	}
	return out, fmt.Errorf("%w: %w: %s", ErrRepairFailed, err, out.Output)
}

// par2Extras is [extraFiles] as absolute paths, without empty files, as the
// par2 CLI filtered them (commandline.cpp: exists, inside basepath,
// non-empty, no duplicates; the rest hold by construction).
func par2Extras(dir, indexFile string) []string {
	var out []string
	for _, name := range extraFiles(dir, indexFile) {
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && st.Size() > 0 {
			out = append(out, p)
		}
	}
	return out
}
