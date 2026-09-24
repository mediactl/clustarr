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

package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	transcodev1alpha1 "github.com/mediactl/clustarr/api/transcode/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/mediainfo"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/squasharr/task"
)

// Exit codes, the contract with the caller: [Process]'s Outcome.Code carries
// one of these up to whatever runs the task -- a transitional in-cluster Job
// (whose podFailurePolicy fails outright on 3 and 4, Phase E ruling R4) or,
// once Task 6 lands, a pool worker reporting task.Outcome/Reason instead.
const (
	// ExitOK: the output is verified and in place, or was already.
	ExitOK = 0
	// ExitRetriable: the cause may be the environment -- the apiserver, the
	// node, disk space, a signal. Running the task again may succeed.
	ExitRetriable = 2
	// ExitInvalidSource: the task's inputs are wrong -- the source changed
	// since it was planned, is missing, unreadable, outside every root
	// folder, or its objects are gone. Running it again cannot help.
	ExitInvalidSource = 3
	// ExitVerifyFailed: ffmpeg produced an output that failed verification.
	// The same input and argv would produce the same output again.
	ExitVerifyFailed = 4
)

// TraceParentEnv carries the W3C traceparent of the controller span that
// created the Job (squasharr/controller/transcodejob), so the worker's spans
// join that trace ([ContextWithTraceParent]).
const TraceParentEnv = "CLUSTARR_TRACEPARENT"

// ContextWithTraceParent returns ctx carrying the remote span context
// traceparent encodes, so a span started from it continues that trace. An
// empty or malformed value returns ctx unchanged: tracing is diagnostics,
// never a reason for a transcode not to run.
func ContextWithTraceParent(ctx context.Context, traceparent string) context.Context {
	if traceparent == "" {
		return ctx
	}
	return propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": traceparent})
}

// TraceParent renders ctx's span context as a W3C traceparent, "" when ctx
// carries none. The TranscodeJob controller stamps it on each Job it creates
// as [TraceParentEnv].
func TraceParent(ctx context.Context) string {
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	return carrier.Get("traceparent")
}

// CPULimitEnv carries x265's pools= size (§6.4): x265 otherwise sizes its
// pool from the host's CPU count, not the cgroup quota (note §3.7). The
// TranscodeJob controller wires it from the Downward API's limits.cpu when
// the Job's container has a CPU limit, and otherwise writes the stated
// default it planned with (its threadsFromResources) as a literal, because
// the Downward API would then report the node's CPUs.
const CPULimitEnv = "CLUSTARR_CPU_LIMIT"

// Verifier is what the worker needs from transcode.Verifier; an interface
// only so a test can make a real output fail verification.
type Verifier interface {
	Verify(ctx context.Context, src, dst string, exp transcode.Expectation) (*transcode.Report, error)
}

// Options configures one [Process] call.
type Options struct {
	// DataDir is where the /data volume is mounted in this process. Every
	// path in a CRD is a logical /data path and is mapped through it; in a
	// Job pod it is /data and the mapping is the identity.
	DataDir string

	// FFmpegPath and FFprobePath default to "ffmpeg" and "ffprobe" on PATH.
	// pkg/mediainfo.Probe always uses "ffprobe" from PATH.
	FFmpegPath  string
	FFprobePath string

	// Threads is x265's pool size, normally [ThreadsFromEnv]. Zero lets
	// pkg/transcode leave pools= unset.
	Threads int32

	// ProgressInterval bounds how often OnProgress is called. Zero means
	// [DefaultProgressInterval].
	ProgressInterval time.Duration

	// OnProgress reports one status-shaped progress sample at most every
	// ProgressInterval. Process makes no Kubernetes client of its own, so
	// applying a sample anywhere -- status.progress or otherwise -- is
	// entirely the caller's business; nil drops status-shaped progress.
	OnProgress func(context.Context, transcodev1alpha1.Progress) error

	// Telemetry is the clustarr-progress bucket the encode's 1 Hz
	// schema.TranscodeProgress goes to, under [ProgressKey] (spec §5). Nil
	// writes none: the bus is optional for a worker, and OnProgress (when
	// set) is the record either way.
	Telemetry events.KV

	// TelemetryInterval is how often Telemetry is written at most. Zero
	// means [DefaultTelemetryInterval].
	TelemetryInterval time.Duration

	// PodName names this worker's Pod in the telemetry (WorkerRef); empty
	// leaves it out.
	PodName string

	// Verifier overrides transcode.NewVerifier(FFprobePath). Tests only.
	Verifier Verifier

	// Now overrides time.Now. Tests only.
	Now func() time.Time
}

// ThreadsFromEnv reads [CPULimitEnv]. The Downward API renders limits.cpu
// as a whole number of cores (rounded up) with divisor 1, and a literal is
// already one; an unset or unparseable value yields 0.
func ThreadsFromEnv() int32 {
	n, err := strconv.ParseInt(os.Getenv(CPULimitEnv), 10, 32)
	if err != nil || n < 0 {
		return 0
	}
	return int32(n)
}

func (o Options) withDefaults() Options {
	if o.DataDir == "" {
		o.DataDir = LogicalDataRoot
	}
	if o.FFmpegPath == "" {
		o.FFmpegPath = "ffmpeg"
	}
	if o.FFprobePath == "" {
		o.FFprobePath = "ffprobe"
	}
	if o.ProgressInterval <= 0 {
		o.ProgressInterval = DefaultProgressInterval
	}
	if o.Verifier == nil {
		o.Verifier = transcode.NewVerifier(o.FFprobePath)
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o
}

// failure carries an exit code with its cause, and -- when squasharr itself
// decided why -- the task.Reason a redispatching caller can act on without
// reparsing the message (spec §18.1, §18.3). Every error [Process] returns
// was classified at the point it happened, where the reason is known; an
// unclassified error is a bug and is reported as retriable.
type failure struct {
	code   int
	reason task.Reason
	err    error
}

func (f *failure) Error() string { return f.err.Error() }
func (f *failure) Unwrap() error { return f.err }

func retriable(format string, a ...any) error {
	return &failure{code: ExitRetriable, err: fmt.Errorf(format, a...)}
}

func invalidSource(format string, a ...any) error {
	return &failure{code: ExitInvalidSource, err: fmt.Errorf(format, a...)}
}

func verifyFailed(format string, a ...any) error {
	return &failure{code: ExitVerifyFailed, err: fmt.Errorf(format, a...)}
}

// sourceChanged: the live file is not the one that was planned. squasharr
// fails it unblocked, and the profile replaces the job for the new file.
func sourceChanged(format string, a ...any) error {
	return &failure{code: ExitInvalidSource, reason: task.ReasonSourceChanged, err: fmt.Errorf(format, a...)}
}

// gpuUnavailable: ffmpeg lacks the GPU encoder the plan wants.
func gpuUnavailable(format string, a ...any) error {
	return &failure{code: ExitRetriable, reason: task.ReasonGPUUnavailable, err: fmt.Errorf(format, a...)}
}

// gpuEncodeFailed: ffmpeg failed while encoding on a GPU tier.
func gpuEncodeFailed(err error) error {
	return &failure{code: ExitRetriable, reason: task.ReasonGPUEncodeFailed, err: err}
}

// gpuTier reports whether tier is a hardware encoder rather than the libx265
// software tier: gpuUnavailable and gpuEncodeFailed only ever apply to one
// of these, since a CPU failure has no GPU tier to route away from.
func gpuTier(tier transcode.Tier) bool { return tier != transcode.TierCPUx265 }

// ExitCode classifies an error returned by [Process]: nil is [ExitOK], and an
// error not classified at its source is [ExitRetriable].
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var f *failure
	if errors.As(err, &f) {
		return f.code
	}
	return ExitRetriable
}

// runner is one [Process] call's state.
type runner struct {
	o Options
	t task.Task

	// out accumulates the Outcome Process returns: run() never touches
	// Kubernetes, so recording progress, stderr and the result is writing
	// into this struct rather than applying status.
	out Outcome

	// Metric labels, known once the plan is.
	tier, resolution string
	started          time.Time
	speedMilli       int32
}

// CheckFFmpeg verifies o.FFmpegPath and o.FFprobePath (and "ffprobe" on
// PATH, which pkg/mediainfo.Probe always uses) are runnable. A missing
// binary is retriable -- it is the image, not the task's inputs -- and a
// caller that orchestrates around Process (cmd/squasharr-worker, before it
// serves) should call this before doing anything else, so a missing binary
// is never misclassified by whatever its other setup (a lease claim)
// happens to fail with first. [Process] also
// checks, so calling it here is an optimization, not a requirement for
// correctness.
func CheckFFmpeg(o Options) error {
	o = o.withDefaults()
	for _, bin := range []string{o.FFmpegPath, o.FFprobePath, "ffprobe"} {
		if _, err := exec.LookPath(bin); err != nil {
			return retriable("squasharr worker: %s not available: %w", bin, err)
		}
	}
	return nil
}

func (r *runner) run(ctx context.Context) error {
	log := logging.FromContext(ctx)

	if err := CheckFFmpeg(r.o); err != nil {
		return err
	}

	source := r.t.SourcePath
	local, err := localPath(r.o.DataDir, source)
	if err != nil {
		return invalidSource("squasharr worker: source: %w", err)
	}
	if !within(r.t.Root.Path, source) {
		return invalidSource("squasharr worker: source %s is outside root folder %s", source, r.t.Root.Path)
	}
	bin, err := localPath(r.o.DataDir, r.t.Root.RecycleBin)
	if err != nil {
		return invalidSource("squasharr worker: recycle bin: %w", err)
	}

	// Where the output lands (gap-fix ruling R-11; OutputPath, resolved by
	// BuildTask into r.t.OutputPath/OutputRoot). In place is the Phase E
	// swap; anywhere else is a new file, and the source is then retired
	// (replaceSource=true) or kept (false).
	sw := swap{
		source: source, local: local, bin: bin,
		replace: ReplaceSource(r.t.Profile.Spec.Policy), recycle: RecycleBin(r.t.Profile.Spec.Policy),
	}
	sw.out = r.t.OutputPath
	if sw.localOut, err = localPath(r.o.DataDir, sw.out); err != nil {
		return invalidSource("squasharr worker: output: %w", err)
	}
	if !sw.inPlace() && (r.t.OutputRoot == "" || !within(r.t.OutputRoot, sw.out)) {
		return invalidSource("squasharr worker: output %s is under no RootFolder; refusing to write it", sw.out)
	}
	tag := r.t.Profile.Name + "@" + r.t.Profile.Hash
	if r.t.SourceProbeHash == "" {
		return invalidSource("squasharr worker: spec.sourceProbeHash is empty; cannot prove the source is the planned file")
	}

	// 2. Is the work already done, and is this the file that was planned? (R3)
	if !sw.inPlace() {
		done, err := r.producedEarlier(ctx, sw, tag)
		if err != nil {
			return err
		}
		if done {
			return r.finishElsewhere(ctx, sw, r.t.SourceProbeHash, r.t.SourceSizeBytes)
		}
	}
	st, err := os.Stat(local)
	if errors.Is(err, os.ErrNotExist) {
		return invalidSource("squasharr worker: source %s does not exist", source)
	}
	if err != nil {
		return retriable("squasharr worker: stat source: %w", err)
	}
	if !st.Mode().IsRegular() {
		return invalidSource("squasharr worker: source %s is not a regular file", source)
	}
	liveHash := mediainfo.ProbeHash(source, st.Size(), st.ModTime())
	if liveHash != r.t.SourceProbeHash {
		if !sw.inPlace() {
			return sourceChanged("squasharr worker: source %s changed since it was planned (probe hash mismatch)", source)
		}
		return r.alreadySwappedOrChanged(ctx, r.t.SourceSizeBytes, source, local, st, tag)
	}

	// 3. Probe, capabilities, plan, space.
	mi, raw, err := mediainfo.Probe(ctx, local)
	if err != nil {
		return invalidSource("squasharr worker: probe source: %w", err)
	}
	info, err := transcode.FromProbe(mi, raw)
	if err != nil {
		return invalidSource("squasharr worker: %w", err)
	}
	info.Path = local
	info.Modifier = r.t.SourceModifier
	if len(info.Video) > 0 {
		r.resolution = resolutionClass(info.Video[0].Height)
	}

	caps, err := transcode.ProbeCapabilities(ctx, r.o.FFmpegPath)
	if err != nil {
		return retriable("squasharr worker: %w", err)
	}
	profile := ProfileSpec(r.t.Profile.Spec, r.t.Profile.Hardware)
	if len(info.Video) > 0 {
		want, err := transcode.SelectTier(profile, info)
		if err != nil {
			return invalidSource("squasharr worker: %w", err)
		}
		// Plan would call this a reject, but a missing encoder is this
		// node's ffmpeg build, not the source: another pod may land on a
		// node that has it.
		if _, ok := transcode.FallbackTier(want, caps); !ok {
			if gpuTier(want) {
				return gpuUnavailable("squasharr worker: this node's ffmpeg has no encoder for tier %s", want)
			}
			return retriable("squasharr worker: this node's ffmpeg has no encoder for tier %s", want)
		}
	}
	plan, err := transcode.Plan(info, profile, caps, transcode.PlanMeta{
		ProfileName: r.t.Profile.Name, ProfileHash: r.t.Profile.Hash, Threads: r.o.Threads, OutputPath: sw.localOut,
	})
	if err != nil {
		return invalidSource("squasharr worker: plan: %w", err)
	}
	r.compareWithRecordedPlan(ctx, r.t.ArgsHash, plan)
	if plan.Decision == transcode.DecisionSkip || plan.Decision == transcode.DecisionReject {
		// The controller planned this file for work from the stored probe
		// of the same bytes. Exiting 0 would report a transcode that never
		// happened, and catalogarr would incorporate it as a swap.
		return invalidSource("squasharr worker: live plan is %s (%s), not the work the controller planned", plan.Decision, plan.Reason)
	}
	r.tier = string(plan.Tier)
	log.InfoContext(ctx, "squasharr worker: planned", "decision", plan.Decision, "tier", plan.Tier, "reason", plan.Reason)

	// The .part is written beside the output, so the final rename is on one
	// filesystem; an explicit spec.outputPath may name a folder that does not
	// exist yet. Budget for an output as large as the source.
	if !sw.inPlace() {
		if err := os.MkdirAll(filepath.Dir(sw.localOut), 0o775); err != nil {
			return retriable("squasharr worker: create the output's folder: %w", err)
		}
	}
	if err := fsops.EnsureFreeSpace(filepath.Dir(sw.localOut), st.Size()); err != nil {
		return retriable("squasharr worker: scratch space: %w", err)
	}

	// 4. Encode.
	if err := r.encode(ctx, plan, info.Format.Duration.Milliseconds()); err != nil {
		return err
	}

	// 5. Verify. From here until the rename, every failure removes the
	// output and leaves the source exactly as it was.
	report, err := r.o.Verifier.Verify(ctx, local, plan.Output, plan.Expect)
	if err != nil {
		removePart(ctx, plan.Output)
		// ffprobe could not run: that is the environment, not the output.
		return retriable("squasharr worker: verify: %w", err)
	}
	if !report.OK {
		removePart(ctx, plan.Output)
		return verifyFailed("squasharr worker: output failed verification: %v", report.Problems)
	}
	if limit := MaxOutputToSourcePercent(r.t.Profile.Spec.Policy); limit > 0 && report.SizeBytes*100 > st.Size()*int64(limit) {
		removePart(ctx, plan.Output)
		return verifyFailed("squasharr worker: output is %d%% of the source, above policy.maxOutputToSourcePercent %d",
			sizePercent(report.SizeBytes, st.Size()), limit)
	}

	// The encode can take hours. If the source changed under it, the
	// output is a transcode of a file that no longer exists.
	if st2, err := os.Stat(local); err != nil || mediainfo.ProbeHash(source, st2.Size(), st2.ModTime()) != liveHash {
		removePart(ctx, plan.Output)
		return sourceChanged("squasharr worker: source %s changed during the encode", source)
	}

	// 6. The swap. See the package doc for why each order and what a crash
	// between any two steps leaves.
	if !sw.inPlace() {
		// Elsewhere (R-11): the verified output takes its own name first, so
		// the library holds a complete file at every instant, then the source
		// is retired -- or, with replaceSource=false, kept.
		if err := fsops.MoveAtomic(plan.Output, sw.localOut); err != nil {
			removePart(ctx, plan.Output)
			return retriable("squasharr worker: place output: %w", err)
		}
		log.InfoContext(ctx, "squasharr worker: output placed", "output", sw.out, "replaceSource", sw.replace)
		if err := sw.retireSource(ctx); err != nil {
			return err
		}
		return r.finish(ctx, sw.out, sw.localOut, st.Size())
	}

	// In place (R5). Link the original into the bin, then rename the
	// verified output over the source path. With policy.recycleBin=false
	// there is no link: the rename alone drops the library's name for the
	// original, and a seeding hard link elsewhere keeps its inode alive
	// regardless.
	var recycled string
	if sw.recycle {
		recycled, err = fsops.RecycleLink(bin, local)
		if err != nil {
			removePart(ctx, plan.Output)
			return retriable("squasharr worker: recycle source: %w", err)
		}
	}
	if err := fsops.MoveAtomic(plan.Output, local); err != nil {
		removePart(ctx, plan.Output)
		return retriable("squasharr worker: replace source: %w", err)
	}
	log.InfoContext(ctx, "squasharr worker: swapped", "recycled", recycled)

	return r.finish(ctx, source, local, st.Size())
}

// swap is where one run's output goes and what happens to its source.
type swap struct {
	source, local string // the source, logical and in this process
	out, localOut string // the final output, logical and in this process
	bin           string // the RootFolder's recycle bin, in this process
	replace       bool   // policy.replaceSource
	recycle       bool   // policy.recycleBin
}

// inPlace reports whether the output replaces the source path itself --
// Phase E's only case, and still the default for a same-container profile.
func (s swap) inPlace() bool { return s.out == s.source }

// retireSource ends the source's life in the library once the output is in
// place elsewhere: moved into the recycle bin (policy.recycleBin), or
// unlinked -- a seeding hard link under /data/torrents keeps its own name
// either way (§6.4). With replaceSource=false it does nothing. A source
// already gone is a retry finding its own earlier work, not an error.
func (s swap) retireSource(ctx context.Context) error {
	if !s.replace {
		return nil
	}
	if _, err := os.Lstat(s.local); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if s.recycle {
		recycled, err := fsops.Recycle(s.bin, s.local)
		if err != nil {
			return retriable("squasharr worker: recycle source: %w", err)
		}
		logging.FromContext(ctx).InfoContext(ctx, "squasharr worker: source retired", "recycled", recycled)
		return nil
	}
	if err := os.Remove(s.local); err != nil && !errors.Is(err, os.ErrNotExist) {
		return retriable("squasharr worker: remove source: %w", err)
	}
	return nil
}

// producedEarlier reports whether an earlier attempt already placed this
// transcode's output at its own (not-in-place) path: the file there carries
// this profile's CLUSTARR_PROFILE tag. A file there WITHOUT that tag is not
// ours, and is never overwritten -- the job fails outright rather than
// clobbering a file a user or another job put there.
func (r *runner) producedEarlier(ctx context.Context, sw swap, tag string) (bool, error) {
	if _, err := os.Lstat(sw.localOut); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, retriable("squasharr worker: stat output: %w", err)
	}
	_, raw, err := mediainfo.Probe(ctx, sw.localOut)
	if err == nil && raw != nil && raw.Format != nil && formatTag(raw, "CLUSTARR_PROFILE") == tag {
		logging.FromContext(ctx).InfoContext(ctx,
			"squasharr worker: the output already carries this profile's tag; an earlier attempt placed it", "output", sw.out)
		return true, nil
	}
	return false, invalidSource("squasharr worker: output path %s already holds a file that is not this transcode; refusing to overwrite it", sw.out)
}

// finishElsewhere completes a run whose output an earlier attempt already
// placed: retire the source if that attempt did not get to (only when it is
// still the planned file -- a source that changed since is not ours to
// remove), then record the result. sourceSize is the MediaFile's recorded
// size, the original's, for the ratio.
func (r *runner) finishElsewhere(ctx context.Context, sw swap, plannedHash string, sourceSize int64) error {
	if st, err := os.Stat(sw.local); err == nil {
		if mediainfo.ProbeHash(sw.source, st.Size(), st.ModTime()) != plannedHash {
			return invalidSource("squasharr worker: source %s changed since it was planned; leaving it and the output %s", sw.source, sw.out)
		}
		sourceSize = st.Size()
		if err := sw.retireSource(ctx); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return retriable("squasharr worker: stat source: %w", err)
	}
	return r.finish(ctx, sw.out, sw.localOut, sourceSize)
}

// compareWithRecordedPlan checks the argv about to run against the
// controller's status.plan.argsHash, carried onto the task as ArgsHash by
// BuildTask. They are built by the same renderer from the same bytes
// (transcode.FromSummary/FromProbe) with the same thread count
// ([CPULimitEnv]), so a difference means the two were given different
// inputs -- a /data mounted elsewhere, an image with a different ffprobe, a
// LimitRange that changed the pod's CPU limit -- and is logged, never fatal:
// the worker's own plan, from the live file, is the one that runs. An empty
// recorded value (no plan recorded yet) skips the comparison.
func (r *runner) compareWithRecordedPlan(ctx context.Context, recorded string, plan *transcode.PlanResult) {
	if recorded == "" {
		return
	}
	got := transcode.ArgsHash(plan)
	trace.SpanFromContext(ctx).SetAttributes(attribute.Bool("transcode.args_match_plan", got == recorded))
	if got != recorded {
		logging.FromContext(ctx).WarnContext(ctx, "squasharr worker: the argv differs from the one status.plan records",
			"planArgsHash", recorded, "argsHash", got)
	}
}

// encode runs ffmpeg with throttled progress applies.
func (r *runner) encode(ctx context.Context, plan *transcode.PlanResult, durationMillis int64) error {
	metrics.TranscodeJobsActive.WithLabelValues(r.tier).Inc()
	defer metrics.TranscodeJobsActive.WithLabelValues(r.tier).Dec()
	r.started = r.o.Now()

	rep := newProgressReporter(r.o.ProgressInterval, durationMillis, r.o.Now, r.applyProgress)
	var pod *schema.Ref
	if r.o.PodName != "" {
		pod = &schema.Ref{Namespace: r.t.Job.Namespace, Name: r.o.PodName}
	}
	tel := newTelemetry(r.o.Telemetry, r.o.TelemetryInterval, r.t.Job, pod, durationMillis, r.o.Now)
	rep.start(ctx)
	tel.start(ctx)
	runErr := transcode.NewRunner(r.o.FFmpegPath).Run(ctx, plan, func(p transcode.Progress) {
		rep.observe(p)
		tel.observe(p)
	})
	// A cancelled ctx cannot carry the final apply; the pod is going away.
	rep.stop(ctx)
	tel.stop(ctx)
	if p, ok := rep.last(); ok {
		r.speedMilli = p.SpeedMilli
	}

	if runErr == nil {
		return nil
	}
	var re *transcode.RunError
	if errors.As(runErr, &re) {
		r.applyStderrTail(re.StderrTail)
	}
	// A non-zero ffmpeg exit may be a bad source, but equally OOM, a node
	// drain, a GPU fault. On a GPU tier that is worth naming
	// (task.ReasonGPUEncodeFailed) so a redispatching pool can route the
	// next attempt to a different class; on the CPU tier there is nowhere
	// else to route it, so it stays retriable exactly as before.
	if gpuTier(plan.Tier) {
		return gpuEncodeFailed(fmt.Errorf("squasharr worker: ffmpeg: %w", runErr))
	}
	return retriable("squasharr worker: ffmpeg: %w", runErr)
}

// alreadySwappedOrChanged handles a source whose probe hash no longer
// matches the plan. Either an earlier attempt of this Job swapped the
// output in and died before recording it -- the file then carries this
// profile's CLUSTARR_PROFILE tag -- or the file really changed and must not
// be touched. sourceSize is the MediaFile's recorded size, the original's,
// used when the tag matches and finish computes the output ratio.
func (r *runner) alreadySwappedOrChanged(ctx context.Context, sourceSize int64,
	source, local string, st os.FileInfo, tag string,
) error {
	_, raw, err := mediainfo.Probe(ctx, local)
	if err == nil && raw != nil && raw.Format != nil {
		if got := formatTag(raw, "CLUSTARR_PROFILE"); got == tag {
			logging.FromContext(ctx).InfoContext(ctx,
				"squasharr worker: source already carries this profile's tag; an earlier attempt swapped it in", "tag", tag)
			return r.finish(ctx, source, local, sourceSize)
		}
	}
	return sourceChanged("squasharr worker: source %s changed since it was planned (probe hash mismatch)", source)
}

// finish records status.result for the output now at out (logical; local in
// this process) -- the source path itself for an in-place swap -- into
// r.out.Result. sourceSize is the original's size, for the ratio; zero
// leaves it at 0.
func (r *runner) finish(ctx context.Context, out, local string, sourceSize int64) error {
	st, err := os.Stat(local)
	if err != nil {
		return retriable("squasharr worker: stat output: %w", err)
	}
	res := transcodev1alpha1.Result{
		OutputPath:            out,
		OutputSizeBytes:       st.Size(),
		OutputToSourcePercent: sizePercent(st.Size(), sourceSize),
	}
	if mi, _, err := mediainfo.Probe(ctx, local); err == nil {
		res.MediaInfo = mi
	} else {
		logging.FromContext(ctx).WarnContext(ctx, "squasharr worker: probing the output for status.result failed", "error", err)
	}
	r.out.Result = &res

	if sourceSize > 0 && r.tier != "" {
		metrics.TranscodeSizeRatio.WithLabelValues(r.tier, r.resolution).Observe(float64(st.Size()) / float64(sourceSize))
	}
	return nil
}

// applyProgress reports one progress sample through o.OnProgress, when the
// caller gave one; nil drops it on the floor.
func (r *runner) applyProgress(ctx context.Context, p transcodev1alpha1.Progress) error {
	if r.o.OnProgress == nil {
		return nil
	}
	return r.o.OnProgress(ctx, p)
}

// applyStderrTail records tail into the Outcome. It cannot fail: unlike the
// Kubernetes-era apply, there is no round trip to lose.
func (r *runner) applyStderrTail(tail string) {
	if len(tail) > 4096 {
		tail = tail[len(tail)-4096:]
	}
	r.out.StderrTail = tail
}

// observeOutcome records the duration and speed metrics once the plan
// fixed the tier. Labels are tier, resolution class and outcome only:
// never a title or a path.
func (r *runner) observeOutcome(code int) {
	if r.tier == "" || r.started.IsZero() {
		return
	}
	outcome := map[int]string{
		ExitOK: "succeeded", ExitRetriable: "retriable",
		ExitInvalidSource: "invalid_source", ExitVerifyFailed: "verify_failed",
	}[code]
	metrics.TranscodeDuration.WithLabelValues(r.tier, r.resolution, outcome).
		Observe(r.o.Now().Sub(r.started).Seconds())
	if r.speedMilli > 0 {
		metrics.TranscodeSpeedRatio.WithLabelValues(r.tier).Set(float64(r.speedMilli) / 1000)
	}
}

// removePart deletes the unverified or rejected output. Best effort: it
// must never replace the error that explains why.
func removePart(ctx context.Context, path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		logging.FromContext(ctx).WarnContext(ctx, "squasharr worker: removing output failed", "path", path, "error", err)
	}
}

// formatTag reads a container-level tag case-insensitively: Matroska
// upper-cases tag names, MP4 may not.
func formatTag(raw *mediainfo.Raw, key string) string {
	for k, v := range raw.Format.TagList {
		if s, ok := v.(string); ok && strings.EqualFold(k, key) {
			return s
		}
	}
	return ""
}

func sizePercent(out, src int64) int32 {
	if src <= 0 || out < 0 {
		return 0
	}
	return int32(min(out*100/src, int64(^uint32(0)>>1)))
}

// resolutionClass is the bounded resolution label, the same sd/hd/uhd split
// pkg/transcode picks a CRF by.
func resolutionClass(height int32) string {
	switch {
	case height <= 576:
		return "sd"
	case height <= 1080:
		return "hd"
	default:
		return "uhd"
	}
}
