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
// repair and extraction), its command handler, its transfer and engine
// records, and its progress publisher (ADR-0019 §6.7). It reads its
// DownloadClient and provider Secrets at start and writes no Kubernetes
// object.
package usenet

import (
	"context"
	"errors"
	"fmt"
	"os"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogagent "github.com/mediactl/clustarr/app/catalog/agent"
	"github.com/mediactl/clustarr/app/grab/engine"
	usenetengine "github.com/mediactl/clustarr/app/grab/engine/usenet"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
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
// re-attaches synchronously inside usenet.BuildClient from its scratch
// manifests, journals included (R4) -- and registers the engine runtime:
// the reporter, the command handler bound to the engine's durable, and the
// progress publisher. It returns the usenet-engine.reattach readiness check
// (the reporter's) and the client's Close.
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
	// see [directClient]'s doc comment. It only reads.
	direct, err := directClient(mgr)
	if err != nil {
		return catalogagent.Registration{}, err
	}
	cl, dc, err := usenetengine.BuildClient(ctx, direct, o.Namespace, o.Client, o.DataDir, o.ScratchDir, o.PublishDir)
	if err != nil {
		return catalogagent.Registration{}, fmt.Errorf("grabarr: build usenet client: %w", err)
	}

	publish := o.PublishDir
	if publish == "" {
		publish = o.DataDir
	}
	rt, err := engine.NewRuntime(bus, &usenetengine.Transfers{
		Client: cl, Resolver: &usenetengine.Resolver{RPC: bus},
	}, engine.RuntimeOptions{
		Client: schema.Ref{Namespace: dc.Namespace, Name: dc.Name, UID: string(dc.UID)},
		Engine: o.Engine, Pod: os.Getenv("POD_NAME"),
		ScratchDir: o.ScratchDir, PublishDir: publish,
	})
	if err != nil {
		_ = cl.Close()
		return catalogagent.Registration{}, err
	}
	if err := rt.Add(mgr, bus); err != nil {
		_ = cl.Close()
		return catalogagent.Registration{}, err
	}

	var ready k8s.Checks
	if err := ready.Add("usenet-engine.reattach", rt.Reporter.HealthzCheck); err != nil {
		_ = cl.Close()
		return catalogagent.Registration{}, err
	}
	return catalogagent.Registration{Ready: &ready, Close: cl.Close}, nil
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
