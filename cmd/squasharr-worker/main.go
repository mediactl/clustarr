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
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/spf13/pflag"

	"github.com/mediactl/clustarr/app/squash/worker"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/obsflags"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

func main() { os.Exit(run(os.Args[1:], os.Getenv)) }

// run never returns 0: a work-queue Job ends when any pod succeeds.
func run(args []string, getenv func(string) string) int {
	fs := pflag.NewFlagSet("squasharr-worker", pflag.ContinueOnError)
	dataDir := fs.String("data-dir", worker.LogicalDataRoot, "Where the RWX /data volume is mounted.")
	lo, to := obsflags.Bind(fs)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "squasharr-worker:", err)
		return worker.WorkerExitMisconfigured
	}
	if err := fsops.ApplyUmaskFromEnv(); err != nil {
		fmt.Fprintln(os.Stderr, "squasharr-worker:", err)
		return worker.WorkerExitMisconfigured
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

	if err := worker.CheckFFmpeg(worker.Options{}); err != nil {
		log.ErrorContext(ctx, "not available", "error", err)
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
	err = worker.Serve(ctx, bus, worker.ServeOptions{
		Options: worker.Options{
			DataDir: *dataDir, Threads: worker.ThreadsFromEnv(), PodName: need["POD_NAME"],
			Telemetry: bus.KV(events.BucketProgress),
		},
		ProfileUID: need["CLUSTARR_POOL_PROFILE_UID"], Class: need["CLUSTARR_POOL_CLASS"], Node: getenv("NODE_NAME"),
		Leases: bus.KV(events.BucketTranscodeLeases), // status events go to the stream through bus
	})
	if ctx.Err() != nil {
		return worker.WorkerExitDrained
	}
	log.ErrorContext(ctx, "serve", "error", err)
	return worker.WorkerExitRetriable
}
