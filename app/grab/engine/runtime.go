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

package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/fsops"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/records"
	"github.com/mediactl/clustarr/pkg/records/agentrecords"
	"github.com/mediactl/clustarr/pkg/version"
)

// RuntimeOptions is one engine instance's identity and what it reports.
type RuntimeOptions struct {
	// Client is the DownloadClient (namespace, name and uid, read through
	// the APIReader at start).
	Client schema.Ref
	// Engine is "<client>-<ordinal>".
	Engine string
	// Pod is this pod's name, for the engine record and the records'
	// writer stamp.
	Pod string
	// Proxy reports the proxy's UDP state; nil is n/a.
	Proxy func() string
	// ScratchDir and PublishDir are statfs'd for the engine record's free
	// bytes.
	ScratchDir, PublishDir string
}

// Runtime is one engine instance's reporter, command handler and progress
// publisher, wired to the bus.
type Runtime struct {
	Reporter *Reporter
	Handler  *Handler
	Progress *ProgressPublisher
	ordinal  int32
	client   string
}

// NewRuntime builds an engine instance's runtime over t.
func NewRuntime(bus events.Bus, t Transfers, o RuntimeOptions) (*Runtime, error) {
	client, ordinal, err := SplitEngine(o.Engine)
	if err != nil {
		return nil, err
	}
	pod := o.Pod
	if pod == "" {
		pod, _ = os.Hostname()
	}
	free := func() (int64, int64) {
		s, _ := fsops.FreeBytes(o.ScratchDir)
		p, _ := fsops.FreeBytes(o.PublishDir)
		return s, p
	}
	rep := &Reporter{
		Transfers: records.NewWriter(bus.KV(events.BucketTransfers), agentrecords.Transfers(), pod, version.String()),
		Engines:   records.NewWriter(bus.KV(events.BucketEngines), agentrecords.Engines(), pod, version.String()),
		Source:    t,
		Client:    schema.ItemRef{Kind: "DownloadClient", Ref: o.Client},
		Ordinal:   ordinal,
		Engine:    o.Engine,
		Pod:       pod,
		Version:   version.String(),
		Proxy:     o.Proxy,
		Free:      free,
	}
	return &Runtime{
		Reporter: rep,
		Handler:  &Handler{Transfers: t, Reporter: rep, Engine: o.Engine},
		Progress: &ProgressPublisher{Transfers: t, EngineID: o.Engine, KV: bus.KV(events.BucketProgress), Ready: rep.Ready},
		ordinal:  ordinal,
		client:   client,
	}, nil
}

// Add registers the runtime's runnables on every replica: the reporter, the
// command subscription -- bind-only, on the durable the manager's
// DownloadClient controller created (§5.1) -- and the progress publisher.
func (rt *Runtime) Add(mgr ctrl.Manager, bus events.Bus) error {
	if err := mgr.Add(rt.Reporter); err != nil {
		return fmt.Errorf("engine: add the reporter: %w", err)
	}
	sub := events.EngineConsumer(rt.client, rt.ordinal).Subscription()
	if err := mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
		stop, err := bus.Subscribe(ctx, sub, rt.Handler.Handle)
		if err != nil {
			return fmt.Errorf("engine: subscribe %s: %w", sub.Durable, err)
		}
		defer stop()
		<-ctx.Done()
		return nil
	})); err != nil {
		return fmt.Errorf("engine: add the command subscription: %w", err)
	}
	if err := mgr.Add(k8s.EveryReplica(rt.Progress.Start)); err != nil {
		return fmt.Errorf("engine: add the progress publisher: %w", err)
	}
	return nil
}

// SplitEngine reads "<client>-<ordinal>", splitting at the last hyphen
// because a client's own name may hold hyphens.
func SplitEngine(engine string) (string, int32, error) {
	i := strings.LastIndex(engine, "-")
	if i <= 0 || i == len(engine)-1 {
		return "", 0, fmt.Errorf("engine %q is not \"<client>-<ordinal>\"", engine)
	}
	n, err := strconv.ParseInt(engine[i+1:], 10, 32)
	if err != nil || n < 0 {
		return "", 0, errors.Join(fmt.Errorf("engine %q has no ordinal", engine), err)
	}
	return engine[:i], int32(n), nil
}
