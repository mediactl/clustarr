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
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/app/squash/controller/audiograft"
	"github.com/mediactl/clustarr/app/squash/controller/pool"
	"github.com/mediactl/clustarr/app/squash/controller/transcodejob"
	squashmanager "github.com/mediactl/clustarr/app/squash/manager"
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

// Hardware classes and the slot budget live in app/squash/manager; these
// keep cmd/clustarr's references working until Wave 5 deletes this package.
const (
	DefaultJobWindow    = squashmanager.DefaultJobWindow
	DefaultJobRetention = squashmanager.DefaultJobRetention
)

// DefaultSlots is [squashmanager.DefaultSlots].
func DefaultSlots() map[string]int32 { return squashmanager.DefaultSlots() }

// ParseSlots is [squashmanager.ParseSlots].
func ParseSlots(s string) (map[string]int32, error) { return squashmanager.ParseSlots(s) }

// FormatSlots is [squashmanager.FormatSlots].
func FormatSlots(slots map[string]int32) string { return squashmanager.FormatSlots(slots) }

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

	// JobWindow is the most non-terminal TranscodeJobs a profile keeps
	// (--job-window): the next files, not one job per matching file. 0 is
	// no limit.
	JobWindow int

	// JobRetention is how long a Succeeded TranscodeJob is kept once its
	// MediaFile has been re-probed (--job-retention). 0 keeps it for good.
	JobRetention time.Duration

	// GraftConcurrency is the most audio grafts running at once
	// (--graft-concurrency).
	GraftConcurrency int

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
		Options:          k8s.DefaultOptions(),
		Role:             RoleController,
		Slots:            DefaultSlots(),
		DataDir:          DefaultDataDir,
		DataClaimName:    pool.DefaultDataClaimName,
		NodeLabelNVIDIA:  pool.DefaultNodeLabelNVIDIA,
		NodeLabelIntel:   pool.DefaultNodeLabelIntel,
		JobWindow:        DefaultJobWindow,
		JobRetention:     DefaultJobRetention,
		GraftConcurrency: audiograft.DefaultConcurrency,
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
	mgr, err := ctrl.NewManager(cfg, k8s.WithBaseContext(o.ManagerOptions(), ctx))
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
	var ready k8s.Checks
	if err := ready.Add("jetstream", k8s.BusReadyChecker(nc, bus)); err != nil {
		return err
	}
	if err := ready.Add("cache", cacheReady); err != nil {
		return err
	}
	if err := k8s.AddProbes(mgr, &ready, nil); err != nil {
		return err
	}

	if err := squashmanager.Register(mgr, bus, managerOptions(o)); err != nil {
		return err
	}

	log.Info("starting", "role", o.Role, "slots", FormatSlots(o.Slots), "workerImage", o.WorkerImage)
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("squasharr: manager: %w", err)
	}
	return nil
}

// managerOptions is the manager registration's slice of o: every squasharr
// option the reconcilers, the results consumer and the pools they render
// read.
func managerOptions(o Options) squashmanager.Options {
	return squashmanager.Options{
		Options: o.Options, Slots: o.Slots, DataDir: o.DataDir, WorkerImage: o.WorkerImage,
		DataClaimName: o.DataClaimName, IntelRenderGroups: o.IntelRenderGroups,
		NodeLabelNVIDIA: o.NodeLabelNVIDIA, NodeLabelIntel: o.NodeLabelIntel,
		JobWindow: o.JobWindow, JobRetention: o.JobRetention, GraftConcurrency: o.GraftConcurrency,
		Logging: o.Logging, Tracing: o.Tracing,
	}
}
