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
// StatefulSet replica's embedded anacrolix client, its command handler,
// its transfer and engine records, and its progress publisher (ADR-0019
// §6.7). It reads its DownloadClient and proxy Secret at start and writes
// no Kubernetes object.
package torrent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"

	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"

	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	catalogagent "github.com/mediactl/clustarr/app/catalog/agent"
	"github.com/mediactl/clustarr/app/grab/engine"
	torrentengine "github.com/mediactl/clustarr/app/grab/engine/torrent"
	dltorrent "github.com/mediactl/clustarr/pkg/download/torrent"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
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
// DownloadClient's spec.torrent, re-attaches it synchronously from its
// descriptors (R4; journals included, so the import survives a restart),
// and registers the engine runtime: the reporter (the re-attach report,
// then the engine record and transfer records), the command handler bound
// to the engine's durable, and the progress publisher. It returns the
// torrent-engine.reattach readiness check -- the reporter's -- and the
// client's Close.
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
	// to cluster DNS, so NATS and the apiserver are reached as before. The
	// proxy's warning is the engine record's proxyUDP now: the engine
	// records no Event (ADR-0019 §7.7).
	var proxyUDP atomic.Value
	proxyUDP.Store(schema.ProxyUDPNotApplicable)
	pcfg, err := torrentProxy(ctx, mgr.GetAPIReader(), &dc, func(reason, message string) {
		logging.FromContext(ctx).WarnContext(ctx, "torrent engine proxy", "reason", reason, "message", message)
		proxyUDP.Store(schema.ProxyUDPUnavailable)
	})
	if err != nil {
		return catalogagent.Registration{}, err
	}
	if pcfg != nil {
		if proxyUDP.Load() == schema.ProxyUDPNotApplicable {
			proxyUDP.Store(schema.ProxyUDPAvailable)
		}
		if dc.Spec.Torrent.Proxy.HostnameLookupOrDefault() {
			net.DefaultResolver = pcfg.Proxy.Resolver(dc.Spec.Torrent.Proxy.DNSServerOrDefault(), nil)
		}
	}

	enableDHT := dc.Spec.Torrent.EnableDHT == nil || *dc.Spec.Torrent.EnableDHT
	rawClient, err := dltorrent.New(dltorrent.Config{
		Proxy:      pcfg,
		DataDir:    torrentDataDir,
		ScratchDir: scratchDir,
		ListenPort: int(dc.Spec.Torrent.ListenPort),
		NoDHT:      !enableDHT,
		// No stall verdict in the engine: the manager judges a stall on
		// the transfer record's lastProgressAt (ADR-0019 §3.5 P111).
		StallTimeout: 0,
		// Seed keeps a completed torrent uploading once finished; the
		// manager's command carries the seed criteria that govern it.
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

	pod, _ := PodName(os.Getenv, os.Hostname)
	rt, err := engine.NewRuntime(bus, &torrentengine.Transfers{
		Engine: e, HTTPClient: proxyHTTPClient(pcfg), Resolver: torrentengine.NewBusIndexerResolver(bus),
	}, engine.RuntimeOptions{
		Client: schema.Ref{Namespace: dc.Namespace, Name: dc.Name, UID: string(dc.UID)},
		Engine: o.Engine, Pod: pod,
		Proxy:      func() string { return proxyUDP.Load().(string) },
		ScratchDir: scratchDir, PublishDir: torrentDataDir,
	})
	if err != nil {
		_ = rawClient.Close()
		return catalogagent.Registration{}, err
	}
	if err := rt.Add(mgr, bus); err != nil {
		_ = rawClient.Close()
		return catalogagent.Registration{}, err
	}

	var ready k8s.Checks
	if err := ready.Add("torrent-engine.reattach", proxyReadiness(rt.Reporter.HealthzCheck, pcfg)); err != nil {
		_ = rawClient.Close()
		return catalogagent.Registration{}, err
	}
	return catalogagent.Registration{Ready: &ready, Close: rawClient.Close}, nil
}
