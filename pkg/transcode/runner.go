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

package transcode

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// runCancelGrace is how long Run waits after sending SIGINT before
// exec.Cmd.WaitDelay escalates to SIGKILL (note §10 RunOptions: "ctx
// cancellation sends SIGINT... then SIGKILL after grace").
const runCancelGrace = 5 * time.Second

// Progress mirrors TranscodeJobStatus.Progress's scaled-int fields exactly
// -- FPSMilli, SpeedMilli, OutTimeMillis, Percent, BitrateKbps -- so a
// controller can copy a Progress straight into the CRD status without a
// float anywhere in between. OutputBytes (ffmpeg's total_size) has no
// status field; it feeds the 1 Hz schema.TranscodeProgress telemetry.
type Progress struct {
	Frame         int64
	FPSMilli      int32
	SpeedMilli    int32
	OutTimeMillis int64
	BitrateKbps   int32
	Percent       int32
	OutputBytes   int64
}

// ParseProgressStream reads ffmpeg's `-progress pipe:1` key=value lines from
// sc, accumulating one block per "progress=continue"/"progress=end" line
// (ffmpeg repeats the block at -stats_period cadence during a real run) and
// calling emit once per block. durationMillis is the source duration, used
// to derive Percent; 0 disables the percentage (Percent stays 0).
func ParseProgressStream(sc *bufio.Scanner, durationMillis int64, emit func(Progress)) error {
	return scanProgressBlocks(sc, func(fields map[string]string, _ bool) {
		emit(progressFromFields(fields, durationMillis))
	})
}

// scanProgressBlocks is the shared block-accumulator behind
// ParseProgressStream and Runner.Run: it reads sc's key=value lines,
// accumulating one block per "progress=continue"/"progress=end" line, and
// calls onBlock once per block with the raw fields and whether this was the
// terminal "end" block. Run needs the isEnd flag itself (to decide whether
// ffmpeg finished cleanly), which is why it builds on this rather than
// ParseProgressStream directly.
func scanProgressBlocks(sc *bufio.Scanner, onBlock func(fields map[string]string, isEnd bool)) error {
	fields := make(map[string]string)
	for sc.Scan() {
		key, value, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		fields[key] = value

		if key != "progress" || (value != "continue" && value != "end") {
			continue
		}
		onBlock(fields, value == "end")
		fields = make(map[string]string)
	}
	return sc.Err()
}

// progressFromFields converts one accumulated -progress block into a
// Progress. Parsing rules transcribed from docs/research/transcode.md §7:
// out_time_us is authoritative (out_time_ms is also microseconds despite
// its name, a documented ffmpeg quirk, and is ignored here); speed and fps
// are scaled by 1000; bitrate strips the "kbits/s" suffix and truncates to
// whole kbps; any missing or "N/A" value parses as 0, never panics.
func progressFromFields(fields map[string]string, durationMillis int64) Progress {
	p := Progress{
		Frame:         parseIntField(fields["frame"]),
		FPSMilli:      parseMilliField(fields["fps"]),
		OutTimeMillis: parseIntField(fields["out_time_us"]) / 1000,
		SpeedMilli:    parseMilliField(strings.TrimSuffix(fields["speed"], "x")),
		BitrateKbps:   parseBitrateKbps(fields["bitrate"]),
		OutputBytes:   max(parseIntField(fields["total_size"]), 0),
	}
	if durationMillis > 0 {
		pct := p.OutTimeMillis * 100 / durationMillis
		switch {
		case pct < 0:
			pct = 0
		case pct > 100:
			pct = 100
		}
		p.Percent = int32(pct)
	}
	return p
}

func parseIntField(s string) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// parseMilliField scales a decimal ffmpeg progress value (fps, speed) by
// 1000. The float64 is a local, immediately-rounded intermediate -- never
// an exported value.
func parseMilliField(s string) int32 {
	s = strings.TrimSpace(s)
	if s == "" || s == "N/A" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int32(math.Round(f * 1000))
}

func parseBitrateKbps(s string) int32 {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "kbits/s"))
	if s == "" || s == "N/A" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int32(f) // truncates towards zero, matching "truncates to whole kbps"
}

// stderrTailLimit matches TranscodeJobStatus.StderrTail's MaxLength (see
// api/transcode/v1alpha1), so RunError.StderrTail can be copied straight
// into CRD status.
const stderrTailLimit = 4096

// stderrTail is an io.Writer that keeps only the most recently written
// stderrTailLimit bytes.
type stderrTail struct{ buf []byte }

func (w *stderrTail) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	if len(w.buf) > stderrTailLimit {
		w.buf = w.buf[len(w.buf)-stderrTailLimit:]
	}
	return len(p), nil
}

func (w *stderrTail) String() string { return string(w.buf) }

// RunError is returned by Runner.Run on any non-nil-error exit: a bad exit
// code, a signal, a failure reading ffmpeg's -progress stream, or ffmpeg
// exiting cleanly without ever reporting progress=end. StderrTail is capped
// at 4096 bytes, the same limit as TranscodeJobStatus.StderrTail, so a
// caller can copy it directly into CRD status.
//
// Err carries EVERY cause Run saw, joined (errors.Join): when ffmpeg both
// exits non-zero and its progress stream fails to parse, both are there,
// and errors.Is/As reach either one. Reporting only the wait error, as Run
// once did, hid the reader failure that often explains it.
type RunError struct {
	ExitCode   int
	StderrTail string
	Err        error
}

func (e *RunError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("transcode: ffmpeg exited %d: %s", e.ExitCode, e.StderrTail)
	}
	return fmt.Sprintf("transcode: ffmpeg exited %d (%s): %s", e.ExitCode,
		strings.ReplaceAll(e.Err.Error(), "\n", "; "), e.StderrTail)
}

func (e *RunError) Unwrap() error { return e.Err }

// Runner runs ffmpeg for a rendered Plan.
type Runner struct {
	FFmpegPath string

	// CancelGrace is how long Run waits after sending SIGINT (on ctx
	// cancellation) before exec.Cmd.WaitDelay escalates to SIGKILL. Zero
	// (the default returned by NewRunner) means runCancelGrace (5s).
	// Tests that exercise cancellation against a slow real encoder should
	// set this to something short.
	CancelGrace time.Duration
}

// NewRunner returns a Runner that shells out to ffmpegPath, with
// CancelGrace defaulted to runCancelGrace.
func NewRunner(ffmpegPath string) Runner { return Runner{FFmpegPath: ffmpegPath} }

// Run executes plan's rendered argv (via Args(plan)), streaming Progress to
// the progress callback at ffmpeg's own -stats_period cadence (Args always
// renders -stats_period 1) until a final progress=end block. Returns nil on
// a clean exit with progress=end observed; otherwise a *RunError.
//
// The run is one "transcode.run" span (amendment §A2.2: every ffmpeg
// invocation is spanned), tagged with the plan's decision, tier and
// container and, on failure, ffmpeg's exit code. Never a path or a title:
// those are unbounded.
func (r Runner) Run(ctx context.Context, plan *PlanResult, progress func(Progress)) error {
	ctx, span := tracing.Start(ctx, "transcode.run", trace.WithAttributes(
		attribute.String("transcode.decision", string(plan.Decision)),
		attribute.String("transcode.tier", string(plan.Tier)),
		attribute.String("transcode.container", string(plan.Container)),
	))
	defer span.End()

	args := Args(plan)
	logger := logging.FromContext(ctx)
	logger.Info("transcode: running ffmpeg", "argv", args)

	cmd := exec.CommandContext(ctx, r.FFmpegPath, args...)
	// exec.CommandContext's default cancellation is an immediate
	// Process.Kill (SIGKILL), which never lets ffmpeg flush a clean
	// trailer. Send SIGINT first and only escalate to SIGKILL after
	// runCancelGrace (exec.Cmd.Cancel/WaitDelay, stdlib since Go 1.20).
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGINT) }
	grace := r.CancelGrace
	if grace <= 0 {
		grace = runCancelGrace
	}
	cmd.WaitDelay = grace

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("transcode: run: stdout pipe: %w", err)
	}
	tail := &stderrTail{}
	cmd.Stderr = tail

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("transcode: run: start: %w", err)
	}

	var sawEnd bool
	var last Progress
	scanDone := make(chan error, 1)
	go func() {
		scanDone <- scanProgressBlocks(bufio.NewScanner(stdout), func(fields map[string]string, isEnd bool) {
			p := progressFromFields(fields, 0)
			if isEnd {
				sawEnd = true
				p.Percent = 100
			}
			last = p
			progress(p)
		})
	}()

	// Drain stdout to EOF BEFORE calling Wait. Per the stdlib's own
	// StdoutPipe doc: "Wait will close the pipe after seeing the command
	// exit... it is thus incorrect to call Wait before all reads from the
	// pipe have completed." Concretely (os/exec/exec.go's Wait): it calls
	// closeDescriptors(c.parentIOPipes) -- which includes StdoutPipe's
	// reader -- unconditionally, with no synchronization against a reader
	// goroutine we manage ourselves (c.goroutineErr only covers pipes
	// exec.Cmd copies internally, i.e. Stdout set to an io.Writer, not
	// StdoutPipe). Calling Wait first can therefore close the pipe out
	// from under an in-flight Read, discarding already-buffered-but-
	// unread bytes -- including the final progress=end block.
	scanErr := <-scanDone
	waitErr := cmd.Wait()

	if waitErr != nil || scanErr != nil || !sawEnd {
		// Cancellation is not an encode failure: report ctx.Err() (wrapped,
		// so errors.Is(err, context.DeadlineExceeded/Canceled) holds for
		// the caller) instead of a *RunError built from the SIGINT/SIGKILL
		// exit status.
		if ctx.Err() != nil {
			cancelErr := fmt.Errorf("transcode: run: %w", ctx.Err())
			tracing.RecordError(span, cancelErr)
			logger.Info("transcode: ffmpeg run cancelled", "error", cancelErr, "progress", last)
			removePartialOutput(logger, plan.Output)
			return cancelErr
		}

		exitCode := -1
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
		runErr := &RunError{ExitCode: exitCode, StderrTail: tail.String(), Err: runCause(waitErr, scanErr)}
		span.SetAttributes(attribute.Int("transcode.exit_code", exitCode))
		tracing.RecordError(span, runErr)
		logger.Error("transcode: ffmpeg run failed", "error", runErr, "exitCode", exitCode, "progress", last)
		removePartialOutput(logger, plan.Output)
		return runErr
	}

	logger.Info("transcode: ffmpeg run complete", "progress", last)
	return nil
}

// runCause is every reason a run failed, joined: the wait error, the
// progress-reader error, or -- when neither happened -- the missing
// progress=end block that is the only remaining way to get here.
func runCause(waitErr, scanErr error) error {
	var causes []error
	if waitErr != nil {
		causes = append(causes, waitErr)
	}
	if scanErr != nil {
		causes = append(causes, fmt.Errorf("transcode: run: reading progress: %w", scanErr))
	}
	if len(causes) == 0 {
		return errors.New("transcode: run: ffmpeg exited cleanly without a progress=end block")
	}
	return errors.Join(causes...)
}

// removePartialOutput deletes the .part.<ext> file Run was writing, on any
// failure or cancellation -- Run owns that file for its own lifetime; only
// a successful run's Output is handed to the caller, which atomically
// replaces the source with it (see PlanResult.Output's doc comment).
// Best-effort: a missing file is not an error, and a removal failure is
// logged rather than returned, since it must never shadow the real Run
// error.
func removePartialOutput(logger *slog.Logger, output string) {
	if output == "" {
		return
	}
	if err := os.Remove(output); err != nil && !errors.Is(err, os.ErrNotExist) {
		logger.Warn("transcode: failed to remove partial output after a failed run", "path", output, "error", err)
	}
}
