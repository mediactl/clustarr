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

// Command segmentarr-worker detects skip segments: a pure consumer of the
// segmentarr-analyze durable (spec 2026-10-01 segment detection). It holds
// no Kubernetes credentials; tasks arrive over NATS and results leave over
// NATS, and it reads /data read-only.
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/pflag"

	"github.com/mediactl/clustarr/app/segments/worker"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/obsflags"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/segments/decode"
	"github.com/mediactl/clustarr/pkg/segments/textdet"
)

func main() { os.Exit(run(os.Args[1:], os.Getenv)) }

// run returns an exit code: ExitMisconfigured for bad flags or environment,
// ExitRetriable when NATS cannot be reached or the subscription ends,
// ExitDrained on SIGTERM.
func run(args []string, getenv func(string) string) int {
	fs := pflag.NewFlagSet("segmentarr-worker", pflag.ContinueOnError)
	concurrency := fs.Int("concurrency", 1, "Analysis tasks run at once.")
	ffmpeg := fs.String("ffmpeg", "ffmpeg", "The ffmpeg binary.")
	threads := fs.Int("ffmpeg-threads", 0, "Threads per ffmpeg run; 0 lets ffmpeg choose.")
	metricsAddr := fs.String("metrics-bind-address", ":8080", "Where /metrics is served; empty serves none.")
	lo, to := obsflags.Bind(fs)
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "segmentarr-worker:", err)
		return worker.ExitMisconfigured
	}
	need := map[string]string{}
	for _, k := range []string{"NATS_URL", "POD_NAME"} {
		if need[k] = getenv(k); need[k] == "" {
			fmt.Fprintf(os.Stderr, "segmentarr-worker: $%s is required\n", k)
			return worker.ExitMisconfigured
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	ctx = logging.NewContext(ctx, logging.New(*lo))
	log := logging.FromContext(ctx)
	to.ServiceName = "segmentarr-worker"
	shutdown, err := tracing.Setup(ctx, *to)
	if err != nil {
		log.ErrorContext(ctx, "tracing", "error", err)
		return worker.ExitMisconfigured
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdown(sctx)
	}()

	det, err := textdet.NewONNX(getenv("ORT_LIB_PATH"))
	if err != nil {
		log.ErrorContext(ctx, "the text detector is off; credits use the cheaper signals only", "error", err)
		det = nil
	} else {
		worker.DNNAvailable.Set(1)
	}
	if *metricsAddr != "" {
		srv := &http.Server{Addr: *metricsAddr, Handler: promhttp.Handler(), ReadHeaderTimeout: 10 * time.Second}
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.ErrorContext(ctx, "metrics", "error", err)
			}
		}()
		defer func() { _ = srv.Close() }()
	}

	nc, err := nats.Connect(need["NATS_URL"], nats.Name("segmentarr-worker/"+need["POD_NAME"]))
	if err != nil {
		log.ErrorContext(ctx, "nats connect", "error", err)
		return worker.ExitRetriable
	}
	defer nc.Close()
	// Equivalent to obs.BusHooks(), inlined: pkg/obs pulls in
	// controller-runtime, which this binary does not link.
	bus, err := natsbus.New(nc, natsbus.WithHooks(events.Hooks{BeforePublish: tracing.Inject, AfterReceive: tracing.Extract}))
	if err != nil {
		log.ErrorContext(ctx, "bus", "error", err)
		return worker.ExitRetriable
	}
	spec, ok := events.Default().Consumer(events.ConsumerSegmentarrAnalyze)
	if !ok {
		log.ErrorContext(ctx, "consumer missing from the default topology", "consumer", events.ConsumerSegmentarrAnalyze)
		return worker.ExitMisconfigured
	}
	h := &worker.Handler{
		Decoder:      decode.Decoder{FFmpeg: *ffmpeg, Threads: *threads},
		Fingerprints: bus.ObjectStore(events.ObjectStoreFingerprints),
		Bus:          bus,
	}
	if det != nil {
		h.Detector = det
	}
	sub := spec.Subscription()
	sub.MaxInFlight = max(*concurrency, 1)
	unsub, err := bus.Subscribe(ctx, sub, h.Handle)
	if err != nil {
		log.ErrorContext(ctx, "subscribe", "error", err)
		return worker.ExitRetriable
	}
	defer unsub()
	log.InfoContext(ctx, "segmentarr-worker serving", "concurrency", sub.MaxInFlight, "dnn", det != nil)
	<-ctx.Done()
	return worker.ExitDrained
}
