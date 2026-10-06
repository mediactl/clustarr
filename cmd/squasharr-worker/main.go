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

// Command squasharr-worker is a transcode pool's pod: a pure consumer of the
// squasharr work queue (spec §9). It holds no Kubernetes credentials; tasks
// arrive over NATS and results leave over NATS.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/spf13/pflag"

	"github.com/mediactl/clustarr/app/squash/grafttask"
	"github.com/mediactl/clustarr/app/squash/worker"
	"github.com/mediactl/clustarr/app/squash/worker/graft"
	"github.com/mediactl/clustarr/app/squash/worker/inprocess"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/obsflags"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/transcode/selfcheck"
)

// The in-process engine grafts a joined dub into a transcode in one pass.
var _ worker.GraftEngine = inprocess.Engine{}

func main() { os.Exit(run(os.Args[1:], os.Getenv)) }

// run never returns 0: a work-queue Job ends when any pod succeeds.
func run(args []string, getenv func(string) string) int {
	fs := pflag.NewFlagSet("squasharr-worker", pflag.ContinueOnError)
	dataDir := fs.String("data-dir", worker.LogicalDataRoot, "Where the RWX /data volume is mounted.")
	selfCheck := fs.String("self-check", "", "Check this image can transcode for a class (cpu, cuda, intel), print the report as JSON and exit: 0 when it can.")
	trial := fs.Bool("trial", false, "With --self-check, also encode for real on the class's GPU.")
	scratchDir := fs.String("scratch-dir", os.TempDir(), "With --trial, where the trial writes its clip.")
	graftTask := fs.String("graft-task", "", "Run this audio graft (grafttask.Task JSON) instead of serving a pool, write its result to --termination-log and exit: 0 when it succeeded.")
	termLog := fs.String("termination-log", "/dev/termination-log", "With --graft-task, where the result is written (the pod's termination message).")
	lo, to := obsflags.Bind(fs)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "squasharr-worker:", err)
		return worker.WorkerExitMisconfigured
	}
	if *selfCheck != "" {
		return runSelfCheck(selfcheck.Class(*selfCheck), *trial, *scratchDir)
	}
	if err := fsops.ApplyUmaskFromEnv(); err != nil {
		fmt.Fprintln(os.Stderr, "squasharr-worker:", err)
		return worker.WorkerExitMisconfigured
	}
	if *graftTask != "" {
		return runGraft(*graftTask, *termLog, *dataDir, logging.New(*lo))
	}
	need := map[string]string{}
	for _, k := range []string{"NATS_URL", "CLUSTARR_POOL_PROFILE_UID", "CLUSTARR_POOL_CLASS", "POD_NAME"} {
		if need[k] = getenv(k); need[k] == "" {
			fmt.Fprintf(os.Stderr, "squasharr-worker: $%s is required\n", k)
			return worker.WorkerExitMisconfigured
		}
	}

	ctx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stopSignals()
	ctx = logging.NewContext(ctx, logging.New(*lo))
	log := logging.FromContext(ctx)
	to.ServiceName = "squasharr-worker"
	shutdown, err := tracing.Setup(ctx, *to)
	if err != nil {
		log.ErrorContext(ctx, "tracing", "error", err)
		return worker.WorkerExitMisconfigured
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdown(sctx); err != nil {
			log.Warn("tracing shutdown", "err", err)
		}
	}()

	// The engine needs FFmpeg 9 and its ffgo shim, which the transcoder
	// image carries; without them this pod can do nothing.
	eng, err := inprocess.New()
	if err != nil {
		log.ErrorContext(ctx, "the in-process engine is unavailable", "error", err)
		return worker.WorkerExitRetriable
	}
	nc, err := nats.Connect(need["NATS_URL"], nats.Name("squasharr-worker/"+need["POD_NAME"]))
	if err != nil {
		log.ErrorContext(ctx, "nats connect", "error", err)
		return worker.WorkerExitRetriable
	}
	defer nc.Close()
	// Equivalent to obs.BusHooks(), inlined: pkg/obs (the top-level package)
	// pulls in controller-runtime, which this binary must not link.
	bus, err := natsbus.New(nc, natsbus.WithHooks(events.Hooks{
		BeforePublish: tracing.Inject,
		AfterReceive:  tracing.Extract,
	}))
	if err != nil {
		log.ErrorContext(ctx, "bus", "error", err)
		return worker.WorkerExitRetriable
	}
	opts := worker.Options{
		DataDir: *dataDir, Threads: worker.ThreadsFromEnv(), PodName: need["POD_NAME"],
		Telemetry: bus.KV(events.BucketProgress),
	}
	opts.Engine = eng
	err = worker.Serve(ctx, bus, worker.ServeOptions{
		Options:    opts,
		ProfileUID: need["CLUSTARR_POOL_PROFILE_UID"], Class: need["CLUSTARR_POOL_CLASS"], Node: getenv("NODE_NAME"),
		Leases: bus.KV(events.BucketTranscodeLeases), // status events go to the stream through bus
	})
	if ctx.Err() != nil {
		return worker.WorkerExitDrained
	}
	log.ErrorContext(ctx, "serve", "error", err)
	return worker.WorkerExitRetriable
}

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
		fmt.Fprintln(os.Stderr, "squasharr-worker: self-check:", err)
		return worker.WorkerExitMisconfigured
	}
	return 0
}

// runGraft is one graft Job's pod (anime dual-audio spec §7.2): no NATS and
// no pool environment, only /data; the controller reads the result from the
// termination message, whatever the exit code.
func runGraft(taskJSON, termLog, dataDir string, log *slog.Logger) int {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	ctx = logging.NewContext(ctx, log)
	var t grafttask.Task
	res := grafttask.Failed(grafttask.ReasonInvalidTask, "decode --graft-task")
	if err := json.Unmarshal([]byte(taskJSON), &t); err == nil {
		res = graft.Run(ctx, t, graft.Options{DataDir: dataDir})
	} else {
		res.Message = grafttask.Clamp("decode --graft-task: " + err.Error())
	}
	log.InfoContext(ctx, "graft finished", "graft", t.Graft, "phase", res.Phase, "reason", res.Reason, "message", res.Message)
	if err := os.WriteFile(termLog, res.Encode(), 0o644); err != nil {
		log.ErrorContext(ctx, "write the termination message", "error", err)
	}
	if res.Phase == grafttask.PhaseSucceeded {
		return 0
	}
	return 1
}
