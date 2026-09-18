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
	"sort"
	"strconv"
	"strings"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
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
	// verify, atomic rename.
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
}

// DefaultOptions returns the options the Deployment gets with no flags.
func DefaultOptions() Options {
	return Options{
		Options: k8s.DefaultOptions(),
		Role:    RoleController,
		Slots:   DefaultSlots(),
		DataDir: DefaultDataDir,
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
	if o.Role == RoleWorker && o.JobName == "" {
		return fmt.Errorf("squasharr: --job is required for --role %s", RoleWorker)
	}
	if o.DataDir == "" {
		return fmt.Errorf("squasharr: --data-dir is required")
	}
	if !o.UsesBus() {
		return fmt.Errorf("squasharr: --nats-url is required; transcode progress uses the bus")
	}
	return o.Options.Validate()
}

// ManagerOptions renders the controller-runtime options for this role without
// contacting the cluster, so a test can assert them.
func (o Options) ManagerOptions() ctrl.Options {
	return o.Options.ManagerOptions(LeaderElectionID, o.LeaderElect && o.Role.RunsControllers())
}

// Run starts the manager and blocks until ctx is cancelled.
func Run(ctx context.Context, o Options) error {
	if err := o.Validate(); err != nil {
		return err
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

	bus, nc, err := k8s.ConnectBus(o.NATSURL, ServiceName)
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

	if err := k8s.AddProbes(mgr, map[string]healthz.Checker{
		"jetstream": k8s.BusReadyChecker(nc, bus),
	}); err != nil {
		return err
	}

	if o.Role.RunsControllers() {
		if err := setupControllers(mgr, o); err != nil {
			return err
		}
	} else if err := setupWorker(mgr, bus, o); err != nil {
		return err
	}

	log.Info("starting", "role", o.Role, "slots", FormatSlots(o.Slots))
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("squasharr: manager: %w", err)
	}
	return nil
}

// setupControllers is the registration point for the transcode reconcilers. It
// registers nothing yet.
//
// TODO(M4): transcodeprofile -- Watches MediaFiles, mapped to the profiles
// whose selector matches, with k8s.StatusFieldChanged on status.probeHash per
// §10, and creates TranscodeJobs by deterministic name (k8s.ChildName);
// transcodejob -- Pending to Planned to Queued (Job created suspended) to
// Running (unsuspended once a slot of its hardware class is free), mirroring
// the Job's conditions through Owns and cleaning up on TTL. (§6.4, §16 M4)
func setupControllers(mgr ctrl.Manager, o Options) error {
	_, _ = mgr, o
	return nil
}

// setupWorker is the registration point for the transcode worker.
//
// The worker is the entrypoint of a batch/v1 Job, not a long-lived controller:
// it transcodes one TranscodeJob and exits with 0, 2, 3 or 4 (§6.4). It is
// wired through the manager anyway so it inherits the client, the metrics
// endpoint and the probes; M4 replaces this with a one-shot Runnable that
// stops the manager when the encode finishes.
//
// TODO(M4): probe, run ffmpeg with -progress pipe:1, publish progress to
// progress.transcode.<uid>, verify packet counts, tag CLUSTARR_PROFILE and
// rename atomically over the source with the original going to the recycle
// bin. (§6.4, §16 M4)
func setupWorker(mgr ctrl.Manager, bus events.Bus, o Options) error {
	_, _, _ = mgr, bus, o
	return nil
}
