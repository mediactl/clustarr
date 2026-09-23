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

// Package squasharr owns transcode.clustarr.io: it watches MediaFiles for
// non-compliant video and schedules HEVC 10-bit / AAC transcodes as batch Jobs.
//
// §6.4 splits it into a controller that admits Jobs against a slot budget and
// a worker that is the Job's own entrypoint.
package squasharr

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/squasharr/controller/transcodejob"
	"github.com/mediactl/clustarr/squasharr/controller/transcodeprofile"
	"github.com/mediactl/clustarr/squasharr/worker"
)

// Service identity, from §2 and §6.4.
const (
	// ServiceName is the subcommand, the controller field manager and the
	// NATS client name. Worker Jobs apply status under
	// k8s.ManagerSquasharrWorker instead.
	ServiceName = "squasharr"

	// LeaderElectionID follows §2's `<service>.clustarr.io`.
	LeaderElectionID = ServiceName + ".clustarr.io"

	// DefaultDataDir is the RWX media volume the Jobs mount.
	DefaultDataDir = "/data"
)

// Hardware classes a transcode slot can be budgeted against, from §12's
// `--slots cpu=2,nvidia=1,intel=1`.
const (
	// HardwareCPU is a libx265 software encode.
	HardwareCPU = "cpu"

	// HardwareNVIDIA is an hevc_nvenc encode on an nvidia.com/gpu.
	HardwareNVIDIA = "nvidia"

	// HardwareIntel is a QSV/VAAPI encode on gpu.intel.com/i915 or /xe.
	HardwareIntel = "intel"
)

// Role selects what a replica does.
type Role string

// The roles §6.4 lists for `clustarr squasharr --role`.
const (
	// RoleController runs the TranscodeProfile and TranscodeJob reconcilers
	// and the slot scheduler. Leader-elected.
	RoleController Role = "controller"

	// RoleWorker is the entrypoint of a transcode Job pod: probe, ffmpeg,
	// verify, atomic rename. It runs no manager and exits with the
	// worker's code (see [ExitError]).
	RoleWorker Role = "worker"
)

// Roles lists the valid --role values in spec order.
func Roles() []Role { return []Role{RoleController, RoleWorker} }

// String returns the flag value.
func (r Role) String() string { return string(r) }

// Valid reports whether r is one of [Roles].
func (r Role) Valid() bool { return r == RoleController || r == RoleWorker }

// RunsControllers reports whether this role reconciles custom resources.
func (r Role) RunsControllers() bool { return r == RoleController }

// DefaultSlots is §12's default budget: two concurrent CPU encodes and one per
// GPU vendor. The scheduler admits a Queued TranscodeJob by unsuspending its
// Job only while a slot of its hardware class is free.
func DefaultSlots() map[string]int32 {
	return map[string]int32{HardwareCPU: 2, HardwareNVIDIA: 1, HardwareIntel: 1}
}

// ParseSlots reads the --slots flag, "cpu=2,nvidia=1,intel=1".
//
// An empty string returns [DefaultSlots]. A zero budget is legal and means
// "never admit this hardware class", which is how a cluster with no GPUs is
// configured; a negative one is not.
func ParseSlots(s string) (map[string]int32, error) {
	if strings.TrimSpace(s) == "" {
		return DefaultSlots(), nil
	}
	out := map[string]int32{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		hardware, budget, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("squasharr: --slots entry %q is not <hardware>=<count>", pair)
		}
		hardware = strings.TrimSpace(hardware)
		switch hardware {
		case HardwareCPU, HardwareNVIDIA, HardwareIntel:
		default:
			return nil, fmt.Errorf("squasharr: --slots names unknown hardware %q, want one of %s, %s, %s",
				hardware, HardwareCPU, HardwareNVIDIA, HardwareIntel)
		}
		if _, dup := out[hardware]; dup {
			return nil, fmt.Errorf("squasharr: --slots names %q twice", hardware)
		}
		n, err := strconv.ParseInt(strings.TrimSpace(budget), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("squasharr: --slots budget for %q: %w", hardware, err)
		}
		if n < 0 {
			return nil, fmt.Errorf("squasharr: --slots budget for %q is negative", hardware)
		}
		out[hardware] = int32(n)
	}
	if len(out) == 0 {
		return DefaultSlots(), nil
	}
	return out, nil
}

// FormatSlots renders a budget back into the --slots syntax, in a stable order
// so it can be logged and compared.
func FormatSlots(slots map[string]int32) string {
	keys := make([]string, 0, len(slots))
	for k := range slots {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+strconv.FormatInt(int64(slots[k]), 10))
	}
	return strings.Join(parts, ",")
}

// DefaultWorkerServiceAccount is the ServiceAccount transcode Job pods run
// as: config/manager/squasharr.yaml declares it and
// config/rbac/squasharr_worker_role_binding.yaml binds it to the worker's
// own ClusterRole. The chart names it <fullname>-squasharr-worker and passes
// that through $CLUSTARR_WORKER_SERVICE_ACCOUNT.
const DefaultWorkerServiceAccount = "squasharr-worker"

// Options is everything `clustarr squasharr` needs.
type Options struct {
	k8s.Options

	// Role is the --role value.
	Role Role

	// Slots is the per-hardware admission budget from --slots.
	Slots map[string]int32

	// DataDir is the RWX media volume.
	DataDir string

	// JobName is the TranscodeJob this worker is transcoding. Worker role
	// only; the Job template sets it.
	JobName string

	// WorkerImage is the image cpu and intel transcode Jobs run
	// (--worker-image). Required for the controller role: every Job it
	// creates is stamped with it.
	WorkerImage string

	// WorkerImageCUDA is the image nvidia transcode Jobs run
	// (--worker-image-cuda). Empty falls back to WorkerImage.
	WorkerImageCUDA string

	// WorkerServiceAccount is the ServiceAccount every transcode Job's pod
	// runs as (--worker-service-account). It must hold the worker's RBAC --
	// squasharr/worker/doc.go's markers, generated into their own
	// ClusterRole -- and NOT this controller's; left empty, the pods would
	// run as the namespace default and fail their first Get.
	WorkerServiceAccount string

	// DataClaimName is the RWX PersistentVolumeClaim transcode Jobs mount at
	// DataDir (--data-claim): the same claim this Deployment mounts.
	DataClaimName string

	// IntelRenderGroups are the supplementalGroups every Intel transcode
	// Job's pod gets: the GID(s) of the host group owning /dev/dri/renderD*
	// on the Intel GPU nodes. See transcodejob.JobConfig.IntelRenderGroups
	// for why there is no built-in value.
	IntelRenderGroups []int64

	// Logging configures this process's root logger. The zero value is a
	// reasonable default: JSON to stderr at info level.
	Logging logging.Options

	// Tracing configures the OpenTelemetry SDK. The zero value is a valid,
	// sampling TracerProvider that exports nowhere -- see
	// pkg/obs/tracing.Setup.
	Tracing tracing.Options
}

// DefaultOptions returns the options the Deployment gets with no flags.
func DefaultOptions() Options {
	return Options{
		Options:              k8s.DefaultOptions(),
		Role:                 RoleController,
		Slots:                DefaultSlots(),
		DataDir:              DefaultDataDir,
		WorkerServiceAccount: DefaultWorkerServiceAccount,
		DataClaimName:        transcodejob.DefaultDataClaimName,
	}
}

// Validate checks the options before anything touches the cluster.
func (o Options) Validate() error {
	if !o.Role.Valid() {
		return fmt.Errorf("squasharr: unknown --role %q, want one of %v", o.Role, Roles())
	}
	for hardware, budget := range o.Slots {
		if budget < 0 {
			return fmt.Errorf("squasharr: slot budget for %q is negative", hardware)
		}
	}
	for _, gid := range o.IntelRenderGroups {
		if gid < 0 {
			return fmt.Errorf("squasharr: intel render group %d is negative", gid)
		}
	}
	if o.DataDir == "" {
		return fmt.Errorf("squasharr: --data-dir is required")
	}
	switch o.Role {
	case RoleWorker:
		// The worker never touches the bus (Phase E ruling R6: progress is
		// status.progress, not NATS), so --nats-url is not asked of it.
		if o.JobName == "" {
			return fmt.Errorf("squasharr: --job is required for --role %s", RoleWorker)
		}
		if o.Namespace == "" {
			return fmt.Errorf("squasharr: --namespace (or $POD_NAMESPACE) is required for --role %s: "+
				"it names the TranscodeJob's namespace", RoleWorker)
		}
	case RoleController:
		if !o.UsesBus() {
			return fmt.Errorf("squasharr: --nats-url is required for --role %s", RoleController)
		}
		if o.WorkerImage == "" {
			return fmt.Errorf("squasharr: --worker-image is required for --role %s: "+
				"every transcode Job it creates runs that image", RoleController)
		}
		if o.WorkerServiceAccount == "" {
			return fmt.Errorf("squasharr: --worker-service-account is required for --role %s: "+
				"a Job pod on the namespace default ServiceAccount holds none of the worker's RBAC", RoleController)
		}
		if o.DataClaimName == "" {
			return fmt.Errorf("squasharr: --data-claim is required for --role %s", RoleController)
		}
	}
	return o.Options.Validate()
}

// ManagerOptions renders the controller-runtime options for this role without
// contacting the cluster, so a test can assert them.
func (o Options) ManagerOptions() ctrl.Options {
	return o.Options.ManagerOptions(LeaderElectionID, o.LeaderElect && o.Role.RunsControllers())
}

// ExitError is the worker's outcome as a process exit code, carried up to
// main so the code reaches os.Exit intact.
//
// The codes are a contract with the transcode Job's podFailurePolicy
// (Phase E ruling R4): 3 and 4 fail the Job outright; anything else is
// retried up to backoffLimit. A plain error from Run makes main exit 1,
// which is retried -- so without this, a source that changed since it was
// planned, or an output that failed verification, would be transcoded
// again backoffLimit times for the same answer.
type ExitError struct {
	// Code is the worker's exit code: squasharr/worker.ExitRetriable,
	// ExitInvalidSource or ExitVerifyFailed. Never 0.
	Code int

	// Err is the reason.
	Err error
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("squasharr worker: exit %d", e.Code)
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error { return e.Err }

// Run starts the manager and blocks until ctx is cancelled -- or, for
// --role worker, transcodes one TranscodeJob and returns its outcome, nil
// for success or an [*ExitError] carrying the exit code.
func Run(ctx context.Context, o Options) error {
	if err := o.Validate(); err != nil {
		return err
	}

	// One call stands up the logger, the controller-runtime bridge and
	// the TracerProvider; a failure here is a startup failure.
	ctx, shutdown, err := obs.Bootstrap(ctx, o.Logging, o.Tracing)
	if err != nil {
		return fmt.Errorf("squasharr: %w", err)
	}
	defer shutdown()

	if o.Role == RoleWorker {
		return runWorker(ctx, o)
	}

	log := ctrl.LoggerFrom(ctx).WithName(ServiceName)
	k8s.RegisterRESTClientMetrics()

	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("squasharr: load kubeconfig: %w", err)
	}
	mgr, err := ctrl.NewManager(cfg, o.ManagerOptions())
	if err != nil {
		return fmt.Errorf("squasharr: build manager: %w", err)
	}

	bus, nc, err := k8s.ConnectBus(o.NATSURL, ServiceName, k8s.WithBusHooks(obs.BusHooks()))
	if err != nil {
		return err
	}
	defer nc.Close()
	defer func() {
		if err := bus.Close(); err != nil {
			log.Error(err, "closing the bus")
		}
	}()
	if err := k8s.EnsureTopology(ctx, bus, o.BusTopology()); err != nil {
		return err
	}

	// Readiness on every replica, leader or not: the cache check is an
	// EveryReplica runnable, so a standby squasharr goes Ready beside the
	// lease holder instead of deadlocking a rollout.
	cacheReady, err := k8s.CacheSyncChecker(mgr)
	if err != nil {
		return err
	}
	if err := k8s.AddProbes(mgr, map[string]healthz.Checker{
		"jetstream": k8s.BusReadyChecker(nc, bus),
		"cache":     cacheReady,
	}); err != nil {
		return err
	}

	if err := setupControllers(mgr, o, bus); err != nil {
		return err
	}

	log.Info("starting", "role", o.Role, "slots", FormatSlots(o.Slots),
		"workerImage", o.WorkerImage, "workerServiceAccount", o.WorkerServiceAccount)
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("squasharr: manager: %w", err)
	}
	return nil
}

// setupControllers registers squasharr's two reconcilers (§6.4, §16 M4):
// the TranscodeProfile controller, which hashes profiles and creates a
// TranscodeJob per file a profile wins (task E-1), and the TranscodeJob
// controller, which plans, creates the suspended batch Job, admits it
// against the --slots budget and mirrors it into phase (task E-2).
//
// The TranscodeJob reconciler reads batch Jobs through mgr.GetAPIReader(),
// never the cache: admission counts the Jobs it unsuspended a moment ago,
// and a cache one event behind would admit past the budget (ADR-0005).
//
// bus carries the TranscodeJob controller's clustarr.evt.transcode.job.*
// history events (§5); the worker never uses it (Phase E ruling R6).
func setupControllers(mgr ctrl.Manager, o Options, bus events.Bus) error {
	if err := transcodeprofile.NewReconciler(
		mgr.GetClient(), mgr.GetScheme(), mgr.GetEventRecorder("transcodeprofile"),
	).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("squasharr: transcodeprofile: %w", err)
	}
	if err := (&transcodejob.Reconciler{
		Client:   mgr.GetClient(),
		Reader:   mgr.GetAPIReader(),
		Slots:    o.Slots,
		Recorder: mgr.GetEventRecorder("transcodejob"),
		Bus:      bus,
		Job: transcodejob.JobConfig{
			Image:              o.WorkerImage,
			ImageCUDA:          o.WorkerImageCUDA,
			DataClaimName:      o.DataClaimName,
			DataDir:            o.DataDir,
			ServiceAccountName: o.WorkerServiceAccount,
			IntelRenderGroups:  o.IntelRenderGroups,
			// §11: the Jobs create files with the same UMASK this
			// Deployment was given.
			Umask: os.Getenv(transcodejob.UmaskEnv),
			// NATSURL stays empty: the worker never uses the bus (R6),
			// and its role no longer requires --nats-url.

			// The worker logs and traces as this controller does: without
			// these its spans -- the ffmpeg run's among them -- were
			// recorded into a TracerProvider exporting nowhere.
			ExtraArgs: workerObservabilityArgs(o.Logging, o.Tracing),
		},
	}).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("squasharr: transcodejob: %w", err)
	}
	return nil
}

// workerObservabilityArgs renders the root command's --log-* and
// --tracing-* flags (pkg/obs/logging.BindFlags; cmd/clustarr's
// bindObservabilityFlags) for a transcode Job's worker, so a Job pod logs in
// the controller's format and level and exports its spans -- the
// squasharr.worker.run and transcode.run (ffmpeg) spans -- to the same
// collector. Only what differs from the flags' defaults is rendered.
// TestWorkerObservabilityArgsParse holds the names to the flags.
func workerObservabilityArgs(lo logging.Options, to tracing.Options) []string {
	var args []string
	if lo.Level != 0 {
		args = append(args, "--log-level="+lo.Level.String())
	}
	if lo.Format != "" {
		args = append(args, "--log-format="+lo.Format)
	}
	if lo.AddSource {
		args = append(args, "--log-add-source")
	}
	if to.Enabled {
		args = append(args, "--tracing-enabled")
		if to.Endpoint != "" {
			args = append(args, "--tracing-endpoint="+to.Endpoint)
		}
		if to.Insecure {
			args = append(args, "--tracing-insecure")
		}
	}
	// The sampler is parent-based, so a worker whose Job carries a sampled
	// traceparent is sampled whatever this says; it decides only a worker
	// started without one.
	args = append(args, "--tracing-sample-ratio="+strconv.FormatFloat(to.SampleRatio, 'g', -1, 64))
	return args
}

// runWorker is the whole of --role worker, the entrypoint of one transcode
// Job pod: transcode the TranscodeJob --job names and return.
//
// It starts no manager. A manager would bring a cache, a metrics listener,
// probes and leader election to a process that makes a handful of reads
// and exits, and its cache would be actively wrong here: every status
// apply re-reads the TranscodeJob first (the lost-update rule), and that
// re-read must see the apiserver, not an informer that lags it. So the
// client is built straight against the apiserver.
//
// The exit code is the point of the function. worker.Run classifies every
// failure at the place it happens; runWorker hands the code up as an
// [*ExitError], and main exits with it.
func runWorker(ctx context.Context, o Options) error {
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return fmt.Errorf("squasharr: load kubeconfig: %w", err)
	}
	c, err := client.New(cfg, client.Options{Scheme: k8s.MustNewScheme()})
	if err != nil {
		return fmt.Errorf("squasharr: build client: %w", err)
	}
	// The Job's CLUSTARR_TRACEPARENT is the controller's span that created
	// it: the worker's spans continue that trace rather than starting one.
	ctx = worker.ContextWithTraceParent(ctx, os.Getenv(worker.TraceParentEnv))
	code, err := worker.Run(ctx, c, worker.Options{
		JobName:   o.JobName,
		Namespace: o.Namespace,
		DataDir:   o.DataDir,
		Threads:   worker.ThreadsFromEnv(),
	})
	if code == worker.ExitOK {
		return nil
	}
	return &ExitError{Code: code, Err: err}
}
