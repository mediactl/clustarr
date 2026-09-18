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

// Package grabarr owns download.clustarr.io: the DownloadClient and Download
// controllers, plus the embedded torrent and usenet engines that do the
// transfers.
//
// §3 splits it into a controller Deployment and, per DownloadClient, an engine
// StatefulSet (torrent) or Deployment (usenet). The engines are the same binary
// under a different --role, so an engine pod can watch only its own Downloads
// through the download.clustarr.io/engine label.
package grabarr

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// Service identity, from §2 and §6.3.
const (
	// ServiceName is the subcommand, the controller field manager and the
	// NATS client name. Engine pods apply status under
	// k8s.ManagerGrabarrEngine instead.
	ServiceName = "grabarr"

	// LeaderElectionID follows §2's `<service>.clustarr.io`.
	LeaderElectionID = ServiceName + ".clustarr.io"

	// DefaultDataDir is the RWX media volume. Torrent engines write
	// <DataDir>/torrents/<category>/<download> and keep their persisted
	// metainfo under <DataDir>/torrents/.state.
	DefaultDataDir = "/data"

	// DefaultScratchDir is the usenet engine's working area: sparse .part
	// files at yEnc offsets, PAR2 repair and extraction all happen here
	// before the result is copied into DataDir and renamed atomically.
	DefaultScratchDir = "/scratch"
)

// Role selects what a replica does.
type Role string

// The roles §6.3 lists for `clustarr grabarr --role`.
const (
	// RoleController runs the DownloadClient and Download reconcilers and the
	// blocklist sweeper. Leader-elected.
	RoleController Role = "controller"

	// RoleTorrentEngine runs the embedded anacrolix torrent client for one
	// StatefulSet ordinal.
	RoleTorrentEngine Role = "torrent-engine"

	// RoleUsenetEngine runs the embedded usenet pipeline: NNTP pools, yEnc
	// assembly, PAR2 repair and extraction.
	RoleUsenetEngine Role = "usenet-engine"
)

// Roles lists the valid --role values in spec order.
func Roles() []Role { return []Role{RoleController, RoleTorrentEngine, RoleUsenetEngine} }

// String returns the flag value.
func (r Role) String() string { return string(r) }

// Valid reports whether r is one of [Roles].
func (r Role) Valid() bool {
	for _, known := range Roles() {
		if r == known {
			return true
		}
	}
	return false
}

// RunsControllers reports whether this role reconciles custom resources.
func (r Role) RunsControllers() bool { return r == RoleController }

// IsEngine reports whether this role is an engine pod. An engine writes only
// the telemetry subset of Download.status, under k8s.ManagerGrabarrEngine.
func (r Role) IsEngine() bool { return r == RoleTorrentEngine || r == RoleUsenetEngine }

// Options is everything `clustarr grabarr` needs.
type Options struct {
	k8s.Options

	// Role is the --role value.
	Role Role

	// Engine is this pod's engine identity, "<client>-<ordinal>". §6.3
	// computes it from the StatefulSet ordinal and writes it to
	// Download.status.engine and to the matching label, so an engine can
	// select exactly its own work.
	Engine string

	// DataDir is the RWX media volume.
	DataDir string

	// ScratchDir is the usenet engine's working area.
	ScratchDir string
}

// DefaultOptions returns the options the Deployment gets with no flags.
func DefaultOptions() Options {
	return Options{
		Options:    k8s.DefaultOptions(),
		Role:       RoleController,
		DataDir:    DefaultDataDir,
		ScratchDir: DefaultScratchDir,
	}
}

// Validate checks the options before anything touches the cluster.
func (o Options) Validate() error {
	if !o.Role.Valid() {
		return fmt.Errorf("grabarr: unknown --role %q, want one of %v", o.Role, Roles())
	}
	if o.Role.IsEngine() && o.Engine == "" {
		return fmt.Errorf("grabarr: --engine is required for --role %s; "+
			"it is the <client>-<ordinal> identity the engine selects its Downloads by", o.Role)
	}
	if !o.Role.IsEngine() && o.Engine != "" {
		return fmt.Errorf("grabarr: --engine is only meaningful for an engine role, not %s", o.Role)
	}
	if o.DataDir == "" {
		return fmt.Errorf("grabarr: --data-dir is required")
	}
	if o.Role == RoleUsenetEngine && o.ScratchDir == "" {
		return fmt.Errorf("grabarr: --scratch-dir is required for --role %s", o.Role)
	}
	if !o.UsesBus() {
		return fmt.Errorf("grabarr: --nats-url is required; download events and progress both use the bus")
	}
	return o.Options.Validate()
}

// ManagerOptions renders the controller-runtime options for this role without
// contacting the cluster, so a test can assert them.
//
// Engines never take the lease: §3 runs one per StatefulSet ordinal and each
// owns a disjoint set of Downloads, so there is nothing to elect.
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
		return fmt.Errorf("grabarr: load kubeconfig: %w", err)
	}
	mgr, err := ctrl.NewManager(cfg, o.ManagerOptions())
	if err != nil {
		return fmt.Errorf("grabarr: build manager: %w", err)
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

	// TODO(M3): an engine role must also gate readiness on the re-attach
	// being complete (§6.3, §13): the torrent engine re-attaches every
	// labelled Download from its persisted metainfo and .part files before it
	// accepts a single new add, and reporting ready early would let the
	// controller hand it work it would then double-download.
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
	if o.Role.IsEngine() {
		if err := setupEngine(mgr, bus, o); err != nil {
			return err
		}
	}

	log.Info("starting", "role", o.Role, "engine", o.Engine, "dataDir", o.DataDir)
	if err := mgr.Start(ctx); err != nil {
		return fmt.Errorf("grabarr: manager: %w", err)
	}
	return nil
}

// setupControllers is the registration point for the download reconcilers. It
// registers nothing yet.
//
// TODO(M3): downloadclient -- own the engine StatefulSet (torrent) or
// Deployment (usenet), report DiskSpaceOK from statfs, and sweep the
// blocklist; download -- pick the ClientRef once, wait for EngineReady, pin
// status.engine from k8s.HashOrdinal over spec.replicas, publish
// evt.download.queued, and run the Drop/RemoveAll finalizer. (§6.3, §16 M3)
func setupControllers(mgr ctrl.Manager, o Options) error {
	_, _ = mgr, o
	return nil
}

// setupEngine is the registration point for the transfer engines, which run as
// manager Runnables with an informer filtered to this pod's engine label.
//
// TODO(M3): the torrent engine -- re-attach from persisted metainfo before
// accepting adds, AddTorrentOpt per download directory, Stats() every 5s and
// SSA telemetry every 10s under k8s.ManagerGrabarrEngine, seed-criteria
// enforcement; the usenet engine -- nzbparser, yEnc assembly into ScratchDir,
// PAR2 repair, extraction, and the atomic rename into DataDir. (§6.3, §16 M3)
func setupEngine(mgr ctrl.Manager, bus events.Bus, o Options) error {
	_, _, _ = mgr, bus, o
	return nil
}
