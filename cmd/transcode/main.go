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

// Command transcode is a transcode pool's pod: a pure consumer of the
// transcode work queue (spec §9). It holds no Kubernetes credentials; tasks
// arrive over NATS and results leave over NATS. With --graft-task it is an
// audio graft Job's pod instead, which answers its task in clustarr-grafts
// (loop spec §4.12). Its exit codes are a contract with the pool Job's
// podFailurePolicy (spec §3.8).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/spf13/pflag"

	"github.com/mediactl/clustarr/app/transcode/grafttask"
	"github.com/mediactl/clustarr/app/transcode/jobspec"
	"github.com/mediactl/clustarr/app/transcode/worker"
	"github.com/mediactl/clustarr/app/transcode/worker/graft"
	"github.com/mediactl/clustarr/app/transcode/worker/inprocess"
	"github.com/mediactl/clustarr/pkg/busconn"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/ffruntime"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/obsflags"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/transcode"
	"github.com/mediactl/clustarr/pkg/transcode/selfcheck"
	"github.com/mediactl/clustarr/pkg/version"
)

// The in-process engine grafts a joined dub into a transcode in one pass.
var _ worker.GraftEngine = inprocess.Engine{}

func main() { os.Exit(run(os.Args[1:], os.Getenv)) }

// flags is transcode's flag set: newFlags builds it, run parses it, and
// manifest_test.go parses every argv transcode stamps onto a pool or graft
// pod through it.
type flags struct {
	fs                                        *pflag.FlagSet
	dataDir, selfCheck, scratchDir, graftTask *string
	trial, version                            *bool
	lo                                        *logging.Options
	to                                        *tracing.Options
}

func newFlags() flags {
	fs := pflag.NewFlagSet("transcode", pflag.ContinueOnError)
	f := flags{fs: fs}
	f.dataDir = fs.String("data-dir", jobspec.LogicalDataRoot, "Where the RWX /data volume is mounted.")
	f.selfCheck = fs.String("self-check", "", "Check this image can transcode for a class (cpu, cuda, intel), print the report as JSON and exit: 0 when it can.")
	f.trial = fs.Bool("trial", false, "With --self-check, also encode for real on the class's GPU.")
	f.scratchDir = fs.String("scratch-dir", os.TempDir(), "With --trial, where the trial writes its clip.")
	f.graftTask = fs.String("graft-task", "", "Run this audio graft (grafttask.Task v2 JSON) instead of serving a pool, answer it in clustarr-grafts and exit: 0 when it grafted, found the language present or was superseded.")
	f.version = fs.Bool("version", false, "Print the version and exit.")
	f.lo, f.to = obsflags.Bind(fs)
	return f
}

// printVersion answers --version, right after the flag parse and before the
// environment checks (the image check, spec §10.1.4). It is not a pool run,
// so its 0 is not the "run never returns 0" contract below.
func printVersion(w io.Writer) int {
	_, _ = fmt.Fprintf(w, "transcode %s\n", version.String())
	return 0
}

// run never returns 0: a work-queue Job ends when any pod succeeds.
func run(args []string, getenv func(string) string) int {
	f := newFlags()
	dataDir, selfCheck, trial, scratchDir := f.dataDir, f.selfCheck, f.trial, f.scratchDir
	graftTask, lo, to := f.graftTask, f.lo, f.to
	if err := f.fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "transcode:", err)
		return jobspec.WorkerExitMisconfigured
	}
	if *f.version {
		return printVersion(os.Stdout)
	}
	if *selfCheck != "" {
		return runSelfCheck(selfcheck.Class(*selfCheck), *trial, *scratchDir)
	}
	if err := fsops.ApplyUmaskFromEnv(); err != nil {
		fmt.Fprintln(os.Stderr, "transcode:", err)
		return jobspec.WorkerExitMisconfigured
	}
	if *graftTask != "" {
		return runGraft(*graftTask, *dataDir, getenv, logging.New(*lo))
	}
	need := map[string]string{}
	for _, k := range []string{"NATS_URL", "CLUSTARR_POOL_PROFILE_UID", "CLUSTARR_POOL_CLASS", "POD_NAME"} {
		if need[k] = getenv(k); need[k] == "" {
			fmt.Fprintf(os.Stderr, "transcode: $%s is required\n", k)
			return jobspec.WorkerExitMisconfigured
		}
	}

	ctx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stopSignals()
	ctx = logging.NewContext(ctx, logging.New(*lo))
	log := logging.FromContext(ctx)
	to.ServiceName = "transcode"
	shutdown, err := tracing.Setup(ctx, *to)
	if err != nil {
		log.ErrorContext(ctx, "tracing", "error", err)
		return jobspec.WorkerExitMisconfigured
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdown(sctx); err != nil {
			log.Warn("tracing shutdown", "err", err)
		}
	}()

	// The engine needs FFmpeg 9, its ffgo shim and the pool class's
	// encoders, muxers and filters, which the transcoder image carries;
	// without them this pod can do nothing (spec 2026-10-06 §7.4).
	eng, err := inprocess.New(transcode.Hardware(need["CLUSTARR_POOL_CLASS"]))
	if err != nil {
		log.ErrorContext(ctx, "the in-process engine is unavailable", "error", err)
		return jobspec.WorkerExitRetriable
	}
	ffruntime.RouteLog(log)
	if err := busconn.ValidateURL(need["NATS_URL"]); err != nil {
		log.ErrorContext(ctx, "$NATS_URL", "error", err)
		return jobspec.WorkerExitMisconfigured
	}
	nc, err := nats.Connect(need["NATS_URL"],
		append([]nats.Option{nats.Name("transcode/" + need["POD_NAME"])}, busconn.ReconnectOptions()...)...)
	if err != nil {
		log.ErrorContext(ctx, "nats connect", "error", err)
		return jobspec.WorkerExitRetriable
	}
	// Equivalent to obs.BusHooks(), inlined: pkg/obs (the top-level package)
	// pulls in controller-runtime, which this binary must not link.
	bus, err := natsbus.New(nc, natsbus.WithHooks(events.Hooks{
		BeforePublish: tracing.Inject,
		AfterReceive:  tracing.Extract,
	}))
	if err != nil {
		log.ErrorContext(ctx, "bus", "error", err)
		busconn.ShutdownAfter(ctx, shutdownBudget, nil, nc)()
		return jobspec.WorkerExitRetriable
	}
	// The one shutdown path (ADR-0020 §1): once Serve has returned, the
	// claim's answer and the lease's release still reach the server.
	defer busconn.ShutdownAfter(ctx, shutdownBudget, bus, nc)()
	opts := worker.Options{
		DataDir: *dataDir, Threads: worker.ThreadsFromEnv(), PodName: need["POD_NAME"],
		Telemetry: bus.KV(events.BucketProgress),
	}
	opts.Engine = eng
	err = worker.Serve(ctx, bus, worker.ServeOptions{
		Options:    opts,
		ProfileUID: need["CLUSTARR_POOL_PROFILE_UID"], Class: need["CLUSTARR_POOL_CLASS"], Node: getenv("NODE_NAME"),
		Leases: bus.KV(events.BucketTranscodeLeases),
		// The worker claims and answers each dispatch here (loop spec
		// §4.12); it publishes no status event and writes no Kubernetes
		// object.
		Records: bus.KV(events.BucketTranscodes),
	})
	if ctx.Err() != nil {
		return jobspec.WorkerExitDrained
	}
	log.ErrorContext(ctx, "serve", "error", err)
	return jobspec.WorkerExitRetriable
}

// shutdownBudget bounds the bus and connection drain once the pod is told to
// stop (busconn.ShutdownAfter): inside the pool pod's default 30 s
// terminationGracePeriodSeconds, after the worker's own return.
const shutdownBudget = 20 * time.Second

// runSelfCheck runs the image check CI and the pool's start run: no
// cluster, no environment.
func runSelfCheck(class selfcheck.Class, trial bool, dir string) int {
	ctx := context.Background()
	var (
		r   selfcheck.Report
		err error
	)
	if trial {
		r, err = selfcheck.Trial(ctx, class, dir)
	} else {
		r, err = selfcheck.Check(ctx, class)
	}
	out, _ := json.MarshalIndent(r, "", "  ")
	fmt.Println(string(out))
	if err != nil {
		fmt.Fprintln(os.Stderr, "transcode: self-check:", err)
		return jobspec.WorkerExitMisconfigured
	}
	return 0
}

// graftWithFFmpeg runs one graft behind the runtime gate: an image that
// cannot load FFmpeg 9 and the shim answers ReasonError naming why, never a
// graft.Run that fails inside a C call (spec 2026-10-06 §7.4).
func graftWithFFmpeg(ctx context.Context, t grafttask.Task, dataDir string, log *slog.Logger) grafttask.Result {
	if _, err := ffruntime.Load(); err != nil {
		return grafttask.Failed(grafttask.ReasonError, "FFmpeg is unavailable in this image: %v", err)
	}
	ffruntime.RouteLog(log)
	return graft.Run(ctx, t, graft.Options{DataDir: dataDir})
}

// runGraft is one graft Job's pod (loop spec §4.12 "Pod"): NATS and /data,
// no Kubernetes credentials. It reads its record, claims it, grafts behind
// the runtime gate and answers in clustarr-grafts; the loop's graft planner
// incorporates the answer. A task the loop did not issue (no v2 schema, no
// file or seq) and a pod with no NATS are refused.
func runGraft(taskJSON, dataDir string, getenv func(string) string, log *slog.Logger) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	ctx = logging.NewContext(ctx, log)
	var t grafttask.Task
	if err := json.Unmarshal([]byte(taskJSON), &t); err != nil || t.Schema != grafttask.TaskSchema || t.File.UID == "" || t.Seq <= 0 {
		log.ErrorContext(ctx, "a --graft-task the loop did not issue", "error", err, "schema", t.Schema)
		return 1
	}
	url, pod := getenv("NATS_URL"), getenv("POD_NAME")
	if url == "" || pod == "" {
		fmt.Fprintln(os.Stderr, "transcode: a graft pod needs $NATS_URL and $POD_NAME")
		return jobspec.WorkerExitMisconfigured
	}
	if err := busconn.ValidateURL(url); err != nil {
		log.ErrorContext(ctx, "$NATS_URL", "error", err)
		return jobspec.WorkerExitMisconfigured
	}
	nc, err := nats.Connect(url, append([]nats.Option{nats.Name("transcode/graft/" + pod)}, busconn.ReconnectOptions()...)...)
	if err != nil {
		log.ErrorContext(ctx, "nats connect", "error", err)
		return 1
	}
	bus, err := natsbus.New(nc)
	if err != nil {
		log.ErrorContext(ctx, "bus", "error", err)
		busconn.ShutdownAfter(ctx, shutdownBudget, nil, nc)()
		return 1
	}
	// The graft's answer is a KV write: drained, not dropped (ADR-0020 §1).
	defer busconn.ShutdownAfter(ctx, shutdownBudget, bus, nc)()
	res, err := grafttask.Serve(ctx, grafttask.NewAnswerer(bus, pod), t, func(ctx context.Context, t grafttask.Task) grafttask.Result {
		return graftWithFFmpeg(ctx, t, dataDir, log)
	})
	switch {
	case errors.Is(err, grafttask.ErrSuperseded):
		log.InfoContext(ctx, "graft superseded", "file", t.File.Name, "seq", t.Seq)
		return 0
	case err != nil:
		log.ErrorContext(ctx, "graft", "file", t.File.Name, "seq", t.Seq, "error", err)
		return 1
	}
	log.InfoContext(ctx, "graft finished", "file", t.File.Name, "seq", t.Seq, "phase", res.Phase, "reason", res.Reason, "message", res.Message)
	if res.Phase == grafttask.PhaseSucceeded {
		return 0
	}
	return 1
}
