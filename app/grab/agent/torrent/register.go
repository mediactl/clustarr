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

// Package torrent is the agent's torrent-engine domain (spec §3.5.3): one
// StatefulSet replica's embedded anacrolix client, its Download reconciler,
// its orphan reaper and its progress publisher.
package torrent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	catalogagent "github.com/mediactl/clustarr/app/catalog/agent"
	"github.com/mediactl/clustarr/app/grab/engine"
	torrentengine "github.com/mediactl/clustarr/app/grab/engine/torrent"
	dltorrent "github.com/mediactl/clustarr/pkg/download/torrent"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// Options is what the torrent engine's Register takes.
type Options struct {
	k8s.Options

	// Client is the DownloadClient's name, read from Namespace.
	Client string

	// Engine is this replica's "<client>-<ordinal>", the Downloads' engine
	// label.
	Engine string

	// DataDir is the RWX media volume.
	DataDir string

	// ScratchDir is the working area, used only when spec.torrent.scratch
	// is set.
	ScratchDir string
}

// Register builds the embedded anacrolix client from the named
// DownloadClient's spec.torrent, re-attaches it synchronously (R4), and
// registers [torrentengine.Reconciler], [torrentengine.Reaper] -- a
// manager.Runnable that NOTHING else registers (plan task D2-8b, commit
// d5c01d2) -- and its [engine.ProgressPublisher]. It returns the
// torrent-engine.reattach readiness check and the client's Close. (§6.3,
// §16 M3; plan tasks D2-5, D2-8)
func Register(ctx context.Context, mgr ctrl.Manager, bus events.Bus, o Options) (catalogagent.Registration, error) {
	if bus == nil {
		return catalogagent.Registration{}, errors.New("torrent engine: Register needs the bus")
	}
	if o.Client == "" || o.Engine == "" || o.DataDir == "" {
		return catalogagent.Registration{}, errors.New("torrent engine: Client, Engine and DataDir are required")
	}

	// mgr.GetAPIReader(), not mgr.GetClient(): this runs before mgr.Start,
	// and the cache-backed client's own error is unambiguous about why --
	// "the cache is not started, can not read objects" -- it does not
	// lazily start the one informer it needs the way some controller-runtime
	// callers assume. GetAPIReader talks to the apiserver directly and needs
	// no cache at all.
	var dc downloadv1alpha1.DownloadClient
	if err := mgr.GetAPIReader().Get(ctx, types.NamespacedName{Namespace: o.Namespace, Name: o.Client}, &dc); err != nil {
		return catalogagent.Registration{}, fmt.Errorf("grabarr: get DownloadClient %s/%s: %w", o.Namespace, o.Client, err)
	}
	if dc.Spec.Protocol != commonv1alpha1.ProtocolTorrent || dc.Spec.Torrent == nil {
		return catalogagent.Registration{}, fmt.Errorf(
			"grabarr: DownloadClient %s/%s is not a torrent client", o.Namespace, o.Client)
	}

	// §6.3 / grabarr.DefaultDataDir's own doc comment: torrent content lives
	// under <DataDir>/torrents/<category>/<download>, persisted re-attach
	// state under <DataDir>/torrents/.state.
	// spec.torrent.publishDir and scratch (2026-09-30) move where content
	// lives; the re-attach state stays put, so changing them never loses
	// track of a transfer.
	torrentDataDir, scratchDir := torrentEngineDirs(o, dc.Spec.Torrent)
	stateDir := filepath.Join(o.DataDir, "torrents", ".state")

	// spec.torrent.proxy: every byte through a SOCKS5 proxy, UDP included,
	// and -- with hostnameLookup -- every public name resolved through it.
	// net.DefaultResolver is this engine process's alone (the engine runs
	// in its own pod; `clustarr all` runs none), and cluster names still go
	// to cluster DNS, so NATS and the apiserver are reached as before.
	recorder := mgr.GetEventRecorder("grabarr-engine")
	pcfg, err := torrentProxy(ctx, mgr.GetAPIReader(), &dc, func(reason, message string) {
		logging.FromContext(ctx).WarnContext(ctx, "torrent engine proxy", "reason", reason, "message", message)
		recorder.Eventf(&dc, nil, corev1.EventTypeWarning, reason, "Start", "%s", message)
	})
	if err != nil {
		return catalogagent.Registration{}, err
	}
	if pcfg != nil && dc.Spec.Torrent.Proxy.HostnameLookupOrDefault() {
		net.DefaultResolver = pcfg.Proxy.Resolver(dc.Spec.Torrent.Proxy.DNSServerOrDefault(), nil)
	}

	enableDHT := dc.Spec.Torrent.EnableDHT == nil || *dc.Spec.Torrent.EnableDHT
	rawClient, err := dltorrent.New(dltorrent.Config{
		Proxy:        pcfg,
		DataDir:      torrentDataDir,
		ScratchDir:   scratchDir,
		ListenPort:   int(dc.Spec.Torrent.ListenPort),
		NoDHT:        !enableDHT,
		StallTimeout: torrentengine.StallTimeout(dc.Spec.Torrent),
		// Seed keeps a completed torrent uploading once finished; a
		// DownloadClient exists to run the engine spec.seedCriteria (via
		// Download.spec.seedCriteria/DownloadClientSpec.Torrent.Seed)
		// governs, so the client-wide switch is always on.
		Seed:   true,
		Logger: logging.FromContext(ctx),
	})
	if err != nil {
		return catalogagent.Registration{}, fmt.Errorf("grabarr: build torrent client: %w", err)
	}

	e := &torrentengine.Engine{Client: rawClient, StateDir: stateDir}
	if _, err := e.ReAttach(ctx); err != nil {
		_ = rawClient.Close()
		return catalogagent.Registration{}, fmt.Errorf("grabarr: torrent engine re-attach: %w", err)
	}

	r := &torrentengine.Reconciler{
		Client:     mgr.GetClient(),
		HTTPClient: proxyHTTPClient(pcfg),
		Engine:     e,
		EngineID:   o.Engine,
		StateDir:   stateDir,
		Resolver:   torrentengine.NewBusIndexerResolver(bus),
		// Uncached: the Episodes a pack targets are read once per Download,
		// at its first Add, for file selection -- not worth an Episode
		// informer in every engine pod.
		EpisodeReader: mgr.GetAPIReader(),
	}
	if err := r.SetupWithManager(mgr); err != nil {
		return catalogagent.Registration{}, fmt.Errorf("grabarr: torrent-engine reconciler: %w", err)
	}

	// [torrentengine.Reaper] needs mgr.GetCache() for its cache-sync gate
	// (its own doc comment: "D2-8's wiring must set this to mgr.GetCache(),
	// or this guard does nothing") and NeedLeaderElection()==false already
	// makes it run on every replica -- mgr.Add is enough, no k8s.EveryReplica
	// wrapper needed, unlike a bare func.
	reaper := &torrentengine.Reaper{
		Client:   mgr.GetClient(),
		Engine:   e,
		EngineID: o.Engine,
		Cache:    mgr.GetCache(),
	}
	if err := mgr.Add(reaper); err != nil {
		return catalogagent.Registration{}, fmt.Errorf("grabarr: torrent reaper: %w", err)
	}

	// Design spec §5's 1 Hz telemetry into clustarr-progress, gated on
	// re-attach like everything else that reads the client.
	if err := mgr.Add(&engine.ProgressPublisher{
		Client:   mgr.GetClient(),
		Download: rawClient,
		EngineID: o.Engine,
		KV:       bus.KV(events.BucketProgress),
		Cache:    mgr.GetCache(),
		Ready:    e.Ready,
	}); err != nil {
		return catalogagent.Registration{}, fmt.Errorf("grabarr: torrent progress publisher: %w", err)
	}

	var ready k8s.Checks
	if err := ready.Add("torrent-engine.reattach", proxyReadiness(e.HealthzCheck, pcfg)); err != nil {
		_ = rawClient.Close()
		return catalogagent.Registration{}, err
	}
	return catalogagent.Registration{Ready: &ready, Close: rawClient.Close}, nil
}
