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

// Package usenet is the agent's usenet-engine domain (spec §3.5.3): the
// Deployment's embedded usenet pipeline (NNTP pools, yEnc assembly, PAR2
// repair and extraction), its Download reconciler, its orphan reaper and
// its progress publisher.
package usenet

import (
	"context"
	"errors"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogagent "github.com/mediactl/clustarr/app/catalog/agent"
	"github.com/mediactl/clustarr/app/grab/engine"
	usenetengine "github.com/mediactl/clustarr/app/grab/engine/usenet"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// Options is what the usenet engine's Register takes.
type Options struct {
	k8s.Options

	// Client is the DownloadClient's name, read from Namespace.
	Client string

	// Engine is this pod's "<client>-<ordinal>", the Downloads' engine
	// label.
	Engine string

	// DataDir is the RWX media volume.
	DataDir string

	// ScratchDir is the engine's working area: sparse .part files at yEnc
	// offsets, PAR2 repair and extraction.
	ScratchDir string

	// PublishDir is where finished content is published, as
	// <PublishDir>/<category>/<name>; "" means DataDir.
	PublishDir string
}

// Register builds the embedded pkg/download/usenet client -- which
// re-attaches synchronously inside usenet.BuildClient (R4 is satisfied by
// construction; see app/grab/engine/usenet's doc.go) -- and registers
// [usenetengine.Reconciler], [usenetengine.Reaper] -- a manager.Runnable
// that NOTHING else registers (plan task D2-8b, commit d5c01d2) -- and its
// [engine.ProgressPublisher]. It returns the client's Close and no
// readiness check: there is nothing left to gate once BuildClient returns.
// (§6.3, §16 M3; plan tasks D2-6, D2-8)
func Register(ctx context.Context, mgr ctrl.Manager, bus events.Bus, o Options) (catalogagent.Registration, error) {
	if bus == nil {
		return catalogagent.Registration{}, errors.New("usenet engine: Register needs the bus")
	}
	if o.Client == "" || o.Engine == "" || o.DataDir == "" || o.ScratchDir == "" {
		return catalogagent.Registration{}, errors.New("usenet engine: Client, Engine, DataDir and ScratchDir are required")
	}

	// usenet.BuildClient Gets the DownloadClient (and every provider's
	// Secret) before mgr.Start, so it needs a client that talks to the
	// apiserver directly rather than mgr.GetClient()'s cache-backed one --
	// see [directClient]'s doc comment.
	direct, err := directClient(mgr)
	if err != nil {
		return catalogagent.Registration{}, err
	}
	cl, dc, err := usenetengine.BuildClient(ctx, direct, o.Namespace, o.Client, o.DataDir, o.ScratchDir, o.PublishDir)
	if err != nil {
		return catalogagent.Registration{}, fmt.Errorf("grabarr: build usenet client: %w", err)
	}

	r := &usenetengine.Reconciler{
		Client:     mgr.GetClient(),
		Download:   cl,
		Resolver:   &usenetengine.Resolver{RPC: bus},
		Recorder:   mgr.GetEventRecorder("usenet-engine"),
		Engine:     o.Engine,
		Categories: dc.Spec.Categories,
	}
	if err := r.SetupWithManager(mgr); err != nil {
		return catalogagent.Registration{}, fmt.Errorf("grabarr: usenet-engine reconciler: %w", err)
	}

	reaper := &usenetengine.Reaper{
		Client:   mgr.GetClient(),
		Download: cl,
		Engine:   o.Engine,
		Cache:    mgr.GetCache(),
	}
	if err := mgr.Add(reaper); err != nil {
		return catalogagent.Registration{}, fmt.Errorf("grabarr: usenet reaper: %w", err)
	}

	// Design spec §5's 1 Hz telemetry into clustarr-progress. BuildClient has
	// already re-attached, so there is no readiness gate to wait for.
	if err := mgr.Add(&engine.ProgressPublisher{
		Client:   mgr.GetClient(),
		Download: cl,
		EngineID: o.Engine,
		KV:       bus.KV(events.BucketProgress),
		Cache:    mgr.GetCache(),
	}); err != nil {
		return catalogagent.Registration{}, fmt.Errorf("grabarr: usenet progress publisher: %w", err)
	}

	return catalogagent.Registration{Close: cl.Close}, nil
}

// directClient builds a client.Client that talks to the apiserver directly,
// bypassing the manager's informer cache entirely, for the engine's one-off
// pre-mgr.Start reads: mgr.GetClient() is cache-backed and, proven
// empirically (grabarr's own envtest wiring case,
// cmd/clustarr/start_envtest_test.go), does NOT lazily start the one
// informer such a Get would need -- it fails outright with "the cache is not
// started, can not read objects". mgr.GetAPIReader() would work for a plain
// Get, but usenet.BuildClient also needs the full client.Client shape (it
// reads Secrets through the same client), so this builds one from the
// manager's own Config/Scheme/RESTMapper rather than mixing reader types.
func directClient(mgr ctrl.Manager) (client.Client, error) {
	c, err := client.New(mgr.GetConfig(), client.Options{Scheme: mgr.GetScheme(), Mapper: mgr.GetRESTMapper()})
	if err != nil {
		return nil, fmt.Errorf("grabarr: build direct apiserver client: %w", err)
	}
	return c, nil
}
