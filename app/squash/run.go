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
// non-compliant video and dispatches HEVC 10-bit / AAC transcodes, against a
// slot budget, to per-(profile, class) worker pools over NATS (spec
// 2026-09-23). The pool pods run cmd/squasharr-worker, not this package.
package squasharr

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	"github.com/mediactl/clustarr/app/squash/controller/pool"
	"github.com/mediactl/clustarr/app/squash/controller/transcodejob"
	"github.com/mediactl/clustarr/app/squash/controller/transcodeprofile"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Service identity, from §2 and §6.4.
const (
	// ServiceName is the subcommand, the field manager of all of
	// TranscodeJob.status and the NATS client name.
	ServiceName = "squasharr"

	// LeaderElectionID follows §2's `<service>.clustarr.io`.
	LeaderElectionID = ServiceName + ".clustarr.io"

	// DefaultDataDir is the RWX media volume the pool pods mount.
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

// The roles `clustarr squasharr --role` accepts. The transcode itself runs
// in pool pods, as cmd/squasharr-worker (spec 2026-09-23 §9): there is no
// worker role here any more.
const (
	// RoleController runs the TranscodeProfile and TranscodeJob reconcilers,
	// the slot scheduler and the results consumer. Leader-elected.
	RoleController Role = "controller"
)

// Roles lists the valid --role values in spec order.
func Roles() []Role { return []Role{RoleController} }

// String returns the flag value.
func (r Role) String() string { return string(r) }

// Valid reports whether r is one of [Roles].
func (r Role) Valid() bool { return r == RoleController }

// RunsControllers reports whether this role reconciles custom resources.
func (r Role) RunsControllers() bool { return r == RoleController }

// DefaultSlots is §12's default budget: two concurrent CPU encodes and one per
// GPU vendor. The scheduler dispatches a Planned TranscodeJob to its pool
// only while a slot of its hardware class is free.
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

	// WorkerImage is the image cpu and intel pools run (--worker-image).
	// Required for the controller role: every pool Job is stamped with it.
	WorkerImage string

	// WorkerImageCUDA is the image nvidia pools run (--worker-image-cuda).
	// Empty falls back to WorkerImage.
	WorkerImageCUDA string

	// WorkerServiceAccount is the ServiceAccount every transcode Job's pod
	// runs as (--worker-service-account). It must hold the worker's RBAC --
	// squasharr/worker/doc.go's markers, generated into their own
	// ClusterRole -- and NOT this controller's; left empty, the pods would
	// run as the namespace default and fail their first Get.
	WorkerServiceAccount string

	// DataClaimName is the RWX PersistentVolumeClaim pool pods mount at
	// DataDir (--data-claim): the same claim this Deployment mounts.
	DataClaimName string

	// IntelRenderGroups are the supplementalGroups every Intel pool pod
	// gets: the GID(s) of the host group owning /dev/dri/renderD* on the
	// Intel GPU nodes, which varies per host install, so there is no
	// built-in value (pool.Config.IntelRenderGroups).
	IntelRenderGroups []int64

	// NodeLabelNVIDIA and NodeLabelIntel are the node labels, set to "true",
	// that mark a GPU node of each class (--gpu-node-label-nvidia and
	// --gpu-node-label-intel; spec §18.5): an auto job is sent to a class's
	// pool only while a Ready node carries its label, and that pool's pods
	// and Job are held to it (pool.Config.NodeLabel). Empty means the
	// pool.DefaultNodeLabel* the GPU operators set.
	NodeLabelNVIDIA string
	NodeLabelIntel  string

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
		DataClaimName:        pool.DefaultDataClaimName,
		NodeLabelNVIDIA:      pool.DefaultNodeLabelNVIDIA,
		NodeLabelIntel:       pool.DefaultNodeLabelIntel,
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
	for _, l := range [...]struct{ flag, key string }{
		{"--gpu-node-label-nvidia", o.NodeLabelNVIDIA}, {"--gpu-node-label-intel", o.NodeLabelIntel},
	} {
		// A malformed key would reach every GPU pool's node affinity and
		// topology constraint, and the apiserver would refuse the pool.
		if errs := validation.IsQualifiedName(l.key); l.key != "" && len(errs) > 0 {
			return fmt.Errorf("squasharr: %s %q is not a label key: %s", l.flag, l.key, strings.Join(errs, "; "))
		}
	}
	if o.DataDir == "" {
		return fmt.Errorf("squasharr: --data-dir is required")
	}
	if !o.UsesBus() {
		return fmt.Errorf("squasharr: --nats-url is required for --role %s: tasks are dispatched on it", o.Role)
	}
	if o.WorkerImage == "" {
		return fmt.Errorf("squasharr: --worker-image is required for --role %s: "+
			"every transcode pool it creates runs that image", o.Role)
	}
	if o.WorkerServiceAccount == "" {
		return fmt.Errorf("squasharr: --worker-service-account is required for --role %s: "+
			"a pod on the namespace default ServiceAccount holds none of the worker's RBAC", o.Role)
	}
	if o.DataClaimName == "" {
		return fmt.Errorf("squasharr: --data-claim is required for --role %s", o.Role)
	}
	return o.Options.Validate()
}

// ManagerOptions renders the controller-runtime options for this role without
// contacting the cluster, so a test can assert them.
//
// The cache holds only the pool Jobs of batch/v1 Jobs
// (transcodejob.PoolJobCache): the TranscodeJob controller watches them, and
// nothing else in this process has any business with the cluster's other
// Jobs. The flip side, which PoolJobCache documents: a Job read through the
// cached client silently misses every Job that is not a pool.
func (o Options) ManagerOptions() ctrl.Options {
	opts := o.Options.ManagerOptions(LeaderElectionID, o.LeaderElect && o.Role.RunsControllers())
	if opts.Cache.ByObject == nil {
		opts.Cache.ByObject = map[client.Object]cache.ByObject{}
	}
	opts.Cache.ByObject[&batchv1.Job{}] = transcodejob.PoolJobCache(o.Namespace)
	return opts
}

// Run starts the manager and blocks until ctx is cancelled.
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

// setupControllers registers squasharr's two reconcilers (§6.4, §16 M4) and
// the results consumer: the TranscodeProfile controller, which hashes
// profiles and creates a TranscodeJob per file a profile wins, and the
// TranscodeJob controller, which plans, admits against the --slots budget
// and dispatches each job's task to its pool over bus, and whose results
// consumer turns the pool workers' status events on
// squasharr-transcode-results into TranscodeJob status (spec 2026-09-23
// §18.2).
//
// The TranscodeJob reconciler reads TranscodeJobs through
// mgr.GetAPIReader(), never the cache: its status writes are conditional on
// the resourceVersion they read, and admission counts the jobs it
// dispatched a moment ago; a cache one event behind would conflict on the
// first and admit past the budget on the second (ADR-0005).
//
// bus carries the tasks, the results and the controller's
// clustarr.evt.transcode.job.* history events (§5); Leases is the bucket
// withdrawal writes its cancel markers to.
func setupControllers(mgr ctrl.Manager, o Options, bus events.Bus) error {
	if err := transcodeprofile.NewReconciler(
		mgr.GetClient(), mgr.GetScheme(), mgr.GetEventRecorder("transcodeprofile"),
	).SetupWithManager(mgr); err != nil {
		return fmt.Errorf("squasharr: transcodeprofile: %w", err)
	}
	rec := &transcodejob.Reconciler{
		Client:   mgr.GetClient(),
		Reader:   mgr.GetAPIReader(),
		Slots:    o.Slots,
		Pool:     poolConfig(o),
		Recorder: mgr.GetEventRecorder("transcodejob"),
		Bus:      bus,
		Leases:   bus.KV(events.BucketTranscodeLeases),
	}
	// natsbus and membus both implement events.StreamAdmin; the comma-ok
	// form only keeps a bus that does not (a narrower test double) from
	// panicking a real run.
	if admin, ok := bus.(events.StreamAdmin); ok {
		rec.Admin = admin
	}
	if err := rec.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("squasharr: transcodejob: %w", err)
	}
	if err := mgr.Add(rec.ResultsConsumer()); err != nil {
		return fmt.Errorf("squasharr: transcode results consumer: %w", err)
	}
	return nil
}

// poolConfig is what the TranscodeJob controller renders every pool Job
// with, from this controller's own options and environment. It is a
// function of o alone so a test can hold each option to the pool it reaches
// (TestPoolConfigCarriesTheControllerOptions): several of them -- the render
// groups, the claim -- have legal empty values that only fail on a real
// node.
func poolConfig(o Options) pool.Config {
	return pool.Config{
		Namespace:         o.Namespace,
		Image:             o.WorkerImage,
		ImageCUDA:         o.WorkerImageCUDA,
		DataClaimName:     o.DataClaimName,
		DataDir:           o.DataDir,
		IntelRenderGroups: o.IntelRenderGroups,
		// §18.5: the labels a GPU class's nodes carry, which admission
		// reads to choose an auto job's class and the class's pools are
		// held to.
		NodeLabelNVIDIA: o.NodeLabelNVIDIA,
		NodeLabelIntel:  o.NodeLabelIntel,
		// §11: the pools create files with the same UMASK this
		// Deployment was given.
		Umask: os.Getenv(pool.UmaskEnv),
		// The pool pods pull their tasks from, and report on, the
		// controller's own bus.
		NATSURL: o.NATSURL,
		// The workers log and trace as this controller does: without
		// these their spans -- the ffmpeg run's among them -- would be
		// recorded into a TracerProvider exporting nowhere.
		ExtraArgs: workerObservabilityArgs(o.Logging, o.Tracing),
	}
}

// workerObservabilityArgs renders the root command's --log-* and
// --tracing-* flags (pkg/obs/obsflags.Bind, which both cmd/clustarr's
// bindObservabilityFlags and cmd/squasharr-worker call) for a pool's
// workers, so a pool pod logs in the controller's format and level and
// exports its spans -- the squasharr.worker.process and transcode.run
// (ffmpeg) spans -- to the same collector. Only what differs from the flags'
// defaults is rendered. TestWorkerObservabilityArgsParse holds the names to
// the flags.
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
	// The sampler is parent-based, so a worker whose task carries a sampled
	// trace is sampled whatever this says; it decides only a trace the
	// worker starts itself.
	args = append(args, "--tracing-sample-ratio="+strconv.FormatFloat(to.SampleRatio, 'g', -1, 64))
	return args
}
