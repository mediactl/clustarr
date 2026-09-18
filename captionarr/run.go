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

// Package captionarr owns subtitle.clustarr.io: it plans which subtitles a
// MediaFile still wants and fetches them from the configured providers.
//
// §3 runs one controller replica and a fixed pair of fetch workers. The split
// matters because the providers are rate-limited per account, not per pod: the
// workers share a KV token bucket so N of them never exceed the provider's
// limit (§6.5).
package captionarr

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// Service identity, from §2 and §6.5.
const (
	// ServiceName is the subcommand, the controller field manager and the
	// NATS client name. Fetch workers apply status under
	// k8s.ManagerCaptionarrWorker instead.
	ServiceName = "captionarr"

	// LeaderElectionID follows §2's `<service>.clustarr.io`.
	LeaderElectionID = ServiceName + ".clustarr.io"

	// DefaultDataDir is the RWX media volume. Sidecars are written next to
	// their video file as <stem>.<lang>[.forced|.sdh].srt.
	DefaultDataDir = "/data"
)

// Role selects what a replica does.
type Role string

// The roles §6.5 lists for `clustarr captionarr --role`.
const (
	// RoleController runs the SubtitleProfile, SubtitleRequest and
	// SubtitleProvider reconcilers and the adaptive and upgrade cron
	// republishers. Leader-elected.
	RoleController Role = "controller"

	// RoleWorker consumes work.captionarr.fetch.>: it scores and downloads
	// candidates, post-processes them and writes the sidecar.
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

// RunsWorkers reports whether this role consumes queue work.
func (r Role) RunsWorkers() bool { return r == RoleWorker }

// Options is everything `clustarr captionarr` needs.
type Options struct {
	k8s.Options

	// Role is the --role value.
	Role Role

	// DataDir is the RWX media volume.
	DataDir string
}

// DefaultOptions returns the options the Deployment gets with no flags.
func DefaultOptions() Options {
	return Options{
		Options: k8s.DefaultOptions(),
		Role:    RoleController,
		DataDir: DefaultDataDir,
	}
}

// Validate checks the options before anything touches the cluster.
func (o Options) Validate() error {
	if !o.Role.Valid() {
		return fmt.Errorf("captionarr: unknown --role %q, want one of %v", o.Role, Roles())
	}
	if o.DataDir == "" {
		return fmt.Errorf("captionarr: --data-dir is required")
	}
	if !o.UsesBus() {
		return fmt.Errorf("captionarr: --nats-url is required; " +
			"the fetch queue and the shared provider throttle both live on the bus")
	}
	return o.Options.Validate()
}

// ManagerOptions renders the controller-runtime options for this role without
// contacting the cluster, so a test can assert them.
//
// Workers never take the lease: §12 runs two of them precisely so fetches
// proceed in parallel, bounded by the shared KV token bucket rather than by
// how many pods are running.
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
		return fmt.Errorf("captionarr: load kubeconfig: %w", err)
	}
	mgr, err := ctrl.NewManager(cfg, o.ManagerOptions())
	if err != nil {
		return fmt.Errorf("captionarr: build manager: %w", err)
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
	}
	if o.Role.RunsWorkers() {
		if err := setupWorkers(mgr, bus, o); err != nil {
			return err
		}
	}

	log.Info("starting", "role", o.Role, "dataDir", o.DataDir)
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("captionarr: manager: %w", err)
	}
	return nil
}

// setupControllers is the registration point for the subtitle reconcilers. It
// registers nothing yet.
//
// TODO(M5): subtitleprofile -- Watches MediaFiles of a video kind with
// k8s.StatusFieldChanged on status.probeHash per §10, ensuring one
// SubtitleRequest per file; subtitlerequest -- replan when profileGeneration
// or probeHash goes stale, publish fetch tasks for items whose nextSearchAt
// has passed, and run the adaptive and 12h upgrade cadences;
// subtitleprovider -- mirror the KV throttle and quota into status. (§6.5,
// §16 M5)
func setupControllers(mgr ctrl.Manager, o Options) error {
	_, _ = mgr, o
	return nil
}

// setupWorkers is the registration point for the fetch consumers.
//
// TODO(M5): consume work.captionarr.fetch.>, verify the file still matches its
// probeHash, iterate providers by priority under the shared KV token bucket,
// score with the Bazarr weights, post-process to UTF-8 SRT, write the sidecar
// through temp+rename, and apply status.items[langKey] under
// k8s.ManagerCaptionarrWorker. (§6.5, §16 M5)
func setupWorkers(mgr ctrl.Manager, bus events.Bus, o Options) error {
	_, _, _ = mgr, bus, o
	return nil
}
