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
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// PAR2 verification and repair is an EXEC, not a Go module, and that is a
// recorded decision rather than an omission: hack/deps/deps.go's package
// doc says so, images/Dockerfile.media already ships par2cmdline-turbo
// v1.5.0, and the research note measured the only pure-Go alternative
// (akalin/gopar) as too slow and too memory-hungry for a 50GB release -- it
// reads everything into memory and has no SIMD.
//
// Tests that need real repair therefore skip when the binary is absent, the
// same way pkg/transcode's tests skip without ffmpeg.

// ErrPar2Unavailable is returned when repair is required but no par2 binary
// can be found. It is distinct from a failed repair: one is a deployment
// problem an operator fixes, the other is a download that cannot be saved.
var ErrPar2Unavailable = errors.New("usenet: par2 binary not found")

// ErrRepairFailed is returned when par2 ran and could not repair the set.
var ErrRepairFailed = errors.New("usenet: par2 repair failed")

// par2OutputMax caps how much of par2's output is read. par2cmdline prints a
// progress line per percent and the output of a badly damaged 50GB set is not
// small; the same "read through a cap" rule applies to a subprocess pipe as to
// an HTTP body.
const par2OutputMax = 1 << 20

// Par2Result is what one par2 run concluded.
type Par2Result struct {
	// AllCorrect is true when every file verified without repair.
	AllCorrect bool
	// Repaired is true when par2 rebuilt the damaged files.
	Repaired bool
	// BlocksNeeded is how many more recovery blocks par2 says it needs. It is
	// non-zero only when repair was impossible, and it is what tells the
	// engine whether fetching more .volNNN+MM.par2 files could help.
	BlocksNeeded int
	// Output is the tail of par2's own output, for status messages.
	Output string
}

var (
	par2AllCorrectRE = regexp.MustCompile(`(?i)All files are correct|Repair is not required`)
	par2RepairedRE   = regexp.MustCompile(`(?i)Repair complete`)
	par2NeedBlocksRE = regexp.MustCompile(`(?i)You need (\d+) more recovery blocks`)
	par2ImpossibleRE = regexp.MustCompile(`(?i)Repair is not possible`)
)

// Par2Runner execs par2cmdline-turbo.
type Par2Runner struct {
	// Path is the par2 binary. Empty means look "par2" up on PATH, which is
	// where images/Dockerfile.media puts it.
	Path string
}

// resolve finds the binary, or reports [ErrPar2Unavailable].
func (r Par2Runner) resolve() (string, error) {
	name := r.Path
	if name == "" {
		name = "par2"
	}
	p, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%w: %q: %w", ErrPar2Unavailable, name, err)
	}
	return p, nil
}

// Available reports whether a par2 binary can be found. Tests use it to skip.
func (r Par2Runner) Available() bool {
	_, err := r.resolve()
	return err == nil
}

// Repair runs "par2 r" over indexFile with dir as the working directory, and
// interprets par2cmdline's output.
//
// The output is parsed rather than the exit status trusted alone: par2cmdline
// and par2cmdline-turbo disagree about codes across versions, while the
// strings SABnzbd has parsed for a decade ("All files are correct", "Repair
// complete", "You need N more recovery blocks") are stable.
func (r Par2Runner) Repair(ctx context.Context, dir, indexFile string) (Par2Result, error) {
	ctx, span := tracing.Start(ctx, "usenet.par2.repair")
	defer span.End()

	bin, err := r.resolve()
	if err != nil {
		return Par2Result{}, err
	}

	// -q once: one level of quiet still prints the verdict lines but not the
	// per-percent progress spam. "--" stops par2 reading a file name that
	// begins with a dash as a flag; NZB-supplied names are untrusted.
	//
	// Every other file in the directory follows the index as an extra file
	// to scan, the way SABnzbd and NZBGet run par2. A set whose recovery
	// data describes obfuscated names ("z75QO...part070.rar") while the
	// files on disk carry the subjects' names ("13th.2016...part070.rar")
	// otherwise reports every target as missing and repair fails outright
	// -- the first real grab on the owner's cluster, 2026-09-24, after a
	// complete 10 GB transfer. Given the extras, par2 matches them by
	// content and repairs under the recorded names.
	args := append([]string{"r", "-q", "--", indexFile}, extraFiles(dir, indexFile)...)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir

	// A ring, not a cap that errors: an io.Writer that refuses further output
	// makes exec.Cmd.Run return THAT error instead of par2's verdict, which
	// would turn a long-but-successful repair into a failed download. The
	// bound is still absolute -- par2 prints a line per percent and a badly
	// damaged 50GB set is not small.
	sink := &tailWriter{max: par2OutputMax}
	cmd.Stdout = sink
	cmd.Stderr = sink

	runErr := cmd.Run()
	text := sink.String()

	logging.FromContext(ctx).DebugContext(ctx, "par2 finished",
		"index", indexFile, "error", runErr, "bytes", len(text))

	res := Par2Result{Output: tail(text, 2048)}
	switch {
	case par2RepairedRE.MatchString(text):
		res.Repaired = true
	case par2AllCorrectRE.MatchString(text):
		res.AllCorrect = true
	}
	if m := par2NeedBlocksRE.FindStringSubmatch(text); m != nil {
		res.BlocksNeeded, _ = strconv.Atoi(m[1])
	}

	if res.Repaired || res.AllCorrect {
		return res, nil
	}
	if par2ImpossibleRE.MatchString(text) || res.BlocksNeeded > 0 {
		return res, fmt.Errorf("%w: %s", ErrRepairFailed, res.Output)
	}
	if runErr != nil {
		return res, fmt.Errorf("%w: %w: %s", ErrRepairFailed, runErr, res.Output)
	}
	// par2 exited 0 but said nothing this parser recognises. Treat it as
	// success rather than failing a download on an unrecognised string, and
	// keep the output so an operator can see what it actually said.
	res.AllCorrect = true
	return res, nil
}

// par2BackupRE matches the backup par2cmdline leaves behind when it repairs a
// file: it renames the damaged "movie.mkv" to "movie.mkv.1" before writing the
// repaired one.
var par2BackupRE = regexp.MustCompile(`^(.*)\.\d+$`)

// removePar2Backups deletes those backups.
//
// They are not optional housekeeping: the job directory is renamed wholesale
// into the library, so a leftover "movie.mkv.1" is a second copy of the film
// that the importer has to reason about -- and it is a copy of the DAMAGED
// version. par2cmdline's own -p would remove them, but -p also purges the par2
// volumes, which PostProcessSpec.DeleteArchives says is the operator's choice
// and not ours.
func removePar2Backups(ctx context.Context, dir string, files []nzbFile) error {
	known := make(map[string]bool, len(files))
	for _, f := range files {
		known[strings.ToLower(safeName(f.Name))] = true
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("usenet: read %s: %w", dir, err)
	}
	for _, e := range entries {
		m := par2BackupRE.FindStringSubmatch(e.Name())
		if m == nil || !known[strings.ToLower(m[1])] {
			continue
		}
		if err := fsops.SafeRemove(ctx, dir, e.Name()); err != nil {
			return err
		}
	}
	return nil
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}

// extraFiles lists the regular files in dir other than indexFile, sorted,
// for par2 to scan as candidates for the set's targets. An unreadable dir
// yields none: par2 then judges the names it knows, as it did before.
func extraFiles(dir, indexFile string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && e.Name() != indexFile {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// par2IndexFile picks the index volume of a par2 set: the ".par2" with no
// "volNNN+MM" in its name. It is the file par2cmdline is pointed at; the
// recovery volumes are found from it.
//
// When several sets are present the one with the most recovery volumes wins,
// because that is the set covering the main content rather than a stray par2
// of a sample.
func par2IndexFile(files []nzbFile) string {
	blocks := map[string]int{}
	var indexes []string
	for _, f := range files {
		switch f.Kind {
		case kindPar2Index:
			indexes = append(indexes, f.Name)
		case kindPar2Volume:
			if m := par2VolumeRE.FindStringSubmatch(f.Name); m != nil {
				blocks[strings.ToLower(m[1])] += f.Blocks
			}
		case kindContent, kindArchive:
		}
	}
	if len(indexes) == 0 {
		// A set posted as "name.vol-01.par2".."vol-07.par2" has no separate
		// index: its smallest volume is the index-sized one (65 KB on the
		// 2026-09-24 nzbgeek post) and every volume carries the main and
		// file-description packets, so par2cmdline takes any of them as the
		// set's entry point. Returning "" here skipped repair for such a
		// set with 76 articles missing.
		return smallestPar2Volume(files)
	}
	sort.Strings(indexes)
	best, bestBlocks := indexes[0], -1
	for _, idx := range indexes {
		base := strings.ToLower(strings.TrimSuffix(idx, ".par2"))
		if b := blocks[base]; b > bestBlocks {
			best, bestBlocks = idx, b
		}
	}
	return best
}

// smallestPar2Volume names the smallest recovery volume, or "" without one.
// Ties break on name so the choice is stable across runs.
func smallestPar2Volume(files []nzbFile) string {
	best := ""
	var bestBytes int64
	for _, f := range files {
		if f.Kind != kindPar2Volume {
			continue
		}
		if best == "" || f.Bytes < bestBytes || (f.Bytes == bestBytes && f.Name < best) {
			best, bestBytes = f.Name, f.Bytes
		}
	}
	return best
}

// tailWriter keeps at most max bytes, discarding from the front. It never
// fails, so a subprocess that talks too much is bounded rather than killed.
type tailWriter struct {
	buf bytes.Buffer
	max int
}

func (w *tailWriter) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > w.max {
		p = p[len(p)-w.max:]
	}
	w.buf.Write(p)
	if w.buf.Len() > w.max {
		b := w.buf.Bytes()
		keep := append([]byte(nil), b[w.buf.Len()-w.max:]...)
		w.buf.Reset()
		w.buf.Write(keep)
	}
	return n, nil
}

func (w *tailWriter) String() string { return w.buf.String() }
