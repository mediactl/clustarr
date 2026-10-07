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

// Command markers detects skip segments: a pure consumer of the
// segmentarr-analyze durable (spec 2026-10-01 segment detection). It holds
// no Kubernetes credentials; tasks arrive over NATS and results leave over
// NATS, and it reads /data read-only. Its exit codes are a contract with its
// Deployment (spec §3.7).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/pflag"

	"github.com/mediactl/clustarr/app/segments/worker"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/obsflags"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/segments/decode"
	"github.com/mediactl/clustarr/pkg/segments/textdet"
	"github.com/mediactl/clustarr/pkg/version"
)

func main() { os.Exit(run(os.Args[1:], os.Getenv)) }

// flags is markers' flag set: newFlags builds it, run parses it, and the
// installers' markers args are parsed through it.
type flags struct {
	fs                  *pflag.FlagSet
	ffmpeg, metricsAddr *string
	threads             *int
	version             *bool
	lo                  *logging.Options
	to                  *tracing.Options
}

// newFlags builds the flag set. --concurrency is gone: per-pod in-flight is
// segmentarr-analyze's ConsumerSpec.Slots, overridable through
// CLUSTARR_CONSUMER_SLOTS, the same number the autoscaler targets (§3.7).
func newFlags() flags {
	fs := pflag.NewFlagSet("markers", pflag.ContinueOnError)
	f := flags{fs: fs}
	f.ffmpeg = fs.String("ffmpeg", "ffmpeg", "The ffmpeg binary.")
	f.threads = fs.Int("ffmpeg-threads", 0, "Threads per ffmpeg run; 0 lets ffmpeg choose.")
	f.metricsAddr = fs.String("metrics-bind-address", ":8080",
		"Where /metrics, /healthz and /readyz are served; empty serves none.")
	f.version = fs.Bool("version", false, "Print the version and exit.")
	f.lo, f.to = obsflags.Bind(fs)
	return f
}

// printVersion answers --version, right after the flag parse and before the
// environment checks (the image check, spec §10.1.4).
func printVersion(w io.Writer) int {
	fmt.Fprintf(w, "markers %s\n", version.String())
	return 0
}

// run returns an exit code: ExitMisconfigured for bad flags or environment,
// ExitRetriable when NATS cannot be reached or the subscription ends,
// ExitDrained on SIGTERM.
func run(args []string, getenv func(string) string) int {
	f := newFlags()
	if err := f.fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "markers:", err)
		return worker.ExitMisconfigured
	}
	if *f.version {
		return printVersion(os.Stdout)
	}
	need := map[string]string{}
	for _, k := range []string{"NATS_URL", "POD_NAME"} {
		if need[k] = getenv(k); need[k] == "" {
			fmt.Fprintf(os.Stderr, "markers: $%s is required\n", k)
			return worker.ExitMisconfigured
		}
	}
	if err := fsops.ApplyUmaskFromEnv(); err != nil {
		fmt.Fprintln(os.Stderr, "markers:", err)
		return worker.ExitMisconfigured
	}
	spec, ok := events.Default().Consumer(events.ConsumerSegmentarrAnalyze)
	if !ok {
		fmt.Fprintln(os.Stderr, "markers: consumer missing from the default topology:", events.ConsumerSegmentarrAnalyze)
		return worker.ExitMisconfigured
	}
	overrides, err := events.ParseSlotOverrides(getenv(events.SlotsEnv))
	if err != nil {
		fmt.Fprintln(os.Stderr, "markers: $"+events.SlotsEnv+":", err)
		return worker.ExitMisconfigured
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	ctx = logging.NewContext(ctx, logging.New(*f.lo))
	log := logging.FromContext(ctx)
	f.to.ServiceName = "markers"
	shutdown, err := tracing.Setup(ctx, *f.to)
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

	// /readyz is green only once NATS is connected and the subscription is
	// bound; /healthz always answers (a wedged decode exits the process).
	var (
		subscribed atomic.Bool
		ncRef      atomic.Pointer[nats.Conn]
	)
	if *f.metricsAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
			if nc := ncRef.Load(); subscribed.Load() && nc != nil && nc.IsConnected() {
				w.WriteHeader(http.StatusOK)
				return
			}
			w.WriteHeader(http.StatusServiceUnavailable)
		})
		srv := &http.Server{Addr: *f.metricsAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.ErrorContext(ctx, "metrics", "error", err)
			}
		}()
		defer func() { _ = srv.Close() }()
	}

	nc, err := nats.Connect(need["NATS_URL"], nats.Name("markers/"+need["POD_NAME"]))
	if err != nil {
		log.ErrorContext(ctx, "nats connect", "error", err)
		return worker.ExitRetriable
	}
	defer nc.Close()
	ncRef.Store(nc)
	// Equivalent to obs.BusHooks(), inlined: pkg/obs pulls in
	// controller-runtime, which this binary does not link.
	bus, err := natsbus.New(nc, natsbus.WithHooks(events.Hooks{BeforePublish: tracing.Inject, AfterReceive: tracing.Extract}))
	if err != nil {
		log.ErrorContext(ctx, "bus", "error", err)
		return worker.ExitRetriable
	}
	h := &worker.Handler{
		Decoder:      decode.Decoder{Threads: *f.threads},
		Fingerprints: bus.ObjectStore(events.ObjectStoreFingerprints),
		Bus:          bus,
		KV:           bus.KV(events.BucketSegments),
	}
	if det != nil {
		h.Detector = det
	}
	sub := spec.Subscription()
	sub.MaxInFlight = events.SlotsFor(spec, overrides)
	// §3.7 step 11: unsubscribing waits for in-flight handlers up to the
	// drain, the durable's declared handler budget.
	sub.Drain = spec.AckWait
	unsub, err := bus.Subscribe(ctx, sub, h.Handle)
	if err != nil {
		log.ErrorContext(ctx, "subscribe", "error", err)
		return worker.ExitRetriable
	}
	defer unsub()
	subscribed.Store(true)
	log.InfoContext(ctx, "markers serving", "slots", sub.MaxInFlight, "dnn", det != nil)
	<-ctx.Done()
	return worker.ExitDrained
}
