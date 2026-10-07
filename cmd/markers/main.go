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
	"runtime"
	"strconv"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/pflag"

	"github.com/mediactl/clustarr/app/segments/worker"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/ffruntime"
	"github.com/mediactl/clustarr/pkg/ffruntime/ffmetrics"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/obsflags"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/presence"
	"github.com/mediactl/clustarr/pkg/segments"
	"github.com/mediactl/clustarr/pkg/segments/decode"
	"github.com/mediactl/clustarr/pkg/segments/textdet"
	"github.com/mediactl/clustarr/pkg/version"
)

func main() { os.Exit(run(os.Args[1:], os.Getenv)) }

// flags is markers' flag set: newFlags builds it, run parses it, and the
// installers' markers args are parsed through it.
type flags struct {
	fs                 *pflag.FlagSet
	metricsAddr        *string
	threads            *int
	version, selfCheck *bool
	lo                 *logging.Options
	to                 *tracing.Options
}

// newFlags builds the flag set. --concurrency is gone: per-pod in-flight is
// segmentarr-analyze's ConsumerSpec.Slots, overridable through
// CLUSTARR_CONSUMER_SLOTS, the same number the autoscaler targets (§3.7).
func newFlags() flags {
	fs := pflag.NewFlagSet("markers", pflag.ContinueOnError)
	f := flags{fs: fs}
	f.threads = fs.Int("decode-threads", 0, "Threads per decoder; 0 is max(1, GOMAXPROCS / segmentarr-analyze's slots).")
	f.selfCheck = fs.Bool("self-check", false, "Check this image can decode and run the text detector, print the report as JSON and exit: 0 when it can.")
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
// or when FFmpeg 9 or what decoding needs is missing; ExitRetriable when NATS
// cannot be reached, the subscription ends, or in-process FFmpeg calls are
// stuck (ffruntime.Wedged); ExitDrained on SIGTERM.
func run(args []string, getenv func(string) string) int {
	f := newFlags()
	if err := f.fs.Parse(args); err != nil {
		fmt.Fprintln(os.Stderr, "markers:", err)
		return worker.ExitMisconfigured
	}
	if *f.version {
		return printVersion(os.Stdout)
	}
	if *f.selfCheck { // before the environment checks: no NATS, no pod
		return selfCheck(os.Stdout, getenv)
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
	// §3.7 steps 7-8: FFmpeg 9 and what decoding needs, or the image is broken.
	if _, err := ffruntime.Load(); err != nil {
		log.ErrorContext(ctx, "FFmpeg is unavailable: the native image is broken", "error", err)
		return worker.ExitMisconfigured
	}
	if err := ffruntime.Require(decode.Needs); err != nil {
		log.ErrorContext(ctx, "FFmpeg lacks what decoding needs", "error", err)
		return worker.ExitMisconfigured
	}
	ffruntime.RouteLog(log)
	if err := ffmetrics.Register(prometheus.DefaultRegisterer); err != nil {
		log.ErrorContext(ctx, "metrics", "error", err)
		return worker.ExitMisconfigured
	}

	// /readyz is green only once NATS is connected and the subscription is
	// bound; /healthz fails only on a bus wedged by handlers that ignore
	// their context (split §3.3 as amended 2026-10-07); a wedged decode
	// exits the process.
	var (
		subscribed atomic.Bool
		ncRef      atomic.Pointer[nats.Conn]
		busRef     atomic.Pointer[natsbus.Bus]
	)
	if *f.metricsAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", promhttp.Handler())
		mux.Handle("/healthz", healthzHandler(wedgeFunc(func() error {
			if b := busRef.Load(); b != nil {
				return b.Wedged()
			}
			return nil
		})))
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
	busRef.Store(bus)
	sub := spec.Subscription()
	sub.MaxInFlight = events.SlotsFor(spec, overrides)
	// Presence (ADR-0019 §5.3, ruling R14): this pod's liveness and slots in
	// clustarr-progress, for the manager's admission. It ends with ctx.
	pw := &presence.Writer{
		KV: bus.KV(events.BucketProgress), Domain: "markers", Pod: need["POD_NAME"],
		Node: getenv("NODE_NAME"), Version: version.String(),
		Slots:    map[string]int{events.ConsumerSegmentarrAnalyze: sub.MaxInFlight},
		Durables: []string{events.ConsumerSegmentarrAnalyze},
		Capabilities: func() map[string]string {
			caps := map[string]string{}
			if rep, err := ffruntime.Load(); err == nil {
				caps["ffmpeg"] = strconv.Itoa(rep.FFmpegMajor)
			}
			if det != nil {
				caps["textdet"] = "onnx"
			}
			return caps
		},
	}
	pctx, pcancel := context.WithCancel(ctx)
	presenceDone := make(chan struct{})
	go func() {
		defer close(presenceDone)
		if err := pw.Run(pctx); err != nil {
			log.ErrorContext(ctx, "presence", "error", err)
		}
	}()
	// Whatever ends run, the writer deletes its key on the way out (best
	// effort; the bucket's TTL retires it otherwise).
	defer func() {
		pcancel()
		select {
		case <-presenceDone:
		case <-time.After(2 * time.Second):
		}
	}()
	h := &worker.Handler{
		Decoder:      decode.Decoder{Threads: decodeThreads(*f.threads, sub.MaxInFlight, runtime.GOMAXPROCS(0))},
		Fingerprints: bus.ObjectStore(events.ObjectStoreFingerprints),
		Records:      segments.NewStore(bus.KV(events.BucketSegments)),
	}
	if det != nil {
		h.Detector = det
	}
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
	select {
	case <-ctx.Done():
		return worker.ExitDrained
	case <-ffruntime.Wedged():
		log.ErrorContext(ctx, "in-process FFmpeg calls are stuck; restarting", "abandoned", ffruntime.Abandoned())
		return worker.ExitRetriable
	}
}

// wedgeFunc adapts a func to events.WedgeReporter, so /healthz, served
// before the bus exists, reads it once it does.
type wedgeFunc func() error

// Wedged implements events.WedgeReporter.
func (f wedgeFunc) Wedged() error { return f() }

// healthzHandler is markers' /healthz: 200 "ok", or 500 with the wedge the
// bus reports (the `bus` liveness check, split §3.3 as amended 2026-10-07).
// markers links no pkg/busconn (§4.5.4), so it asks the bus itself.
func healthzHandler(w events.WedgeReporter) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, _ *http.Request) {
		if w != nil {
			if err := w.Wedged(); err != nil {
				http.Error(rw, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		rw.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(rw, "ok")
	})
}

// decodeThreads is --decode-threads: an explicit value, else the cgroup's
// CPUs (GOMAXPROCS follows the quota) shared by the per-pod slots.
func decodeThreads(flag, slots, procs int) int {
	if flag > 0 {
		return flag
	}
	return max(1, procs/max(slots, 1))
}
