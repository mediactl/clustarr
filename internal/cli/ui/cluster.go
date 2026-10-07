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

package uicli

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
)

// buildCluster attempts to build ui's two seams onto the cluster -- the
// informer-backed reader every page reads through, and the *actions.Actions
// every button writes through -- and reports whether it succeeded through
// the returned values themselves, never through an error: a cluster is
// optional for ui (Task D3-0's brief: "Do not make a cluster connection
// mandatory"), so the ordinary case for a developer running `ui` with no
// kubeconfig is not a failure at all. Any problem here is logged and
// swallowed; the nils it returns on that path are exactly what
// ui.Options.Reader, WaitForSync and Actions being unset already mean -- see
// ui.NewServer's defaulting, and actions.ErrNoWriter.
//
// The writer is a plain client.Client over the same config and ui's own
// scheme (ui.NewReaderScheme: corev1 plus the five Clustarr groups, which
// covers every kind ui/actions creates or patches). It goes straight into
// actions.New and is never handed to ui by any other route: ui.Options.Reader
// stays a client.Reader, and *actions.Actions holds its writer unexported, so
// the only writes ui can make are the actions ui/actions defines
// (ui/guard_test.go, ruling R2). It is uncached on purpose: ui/actions only
// creates and patches, and a create or a merge patch goes to the apiserver
// whatever client sends it.
//
// It uses controller-runtime's pkg/log rather than pkg/obs/logging: it runs
// before ui.Run's own obs.Bootstrap has installed a logger on ctx.
func buildCluster(ctx context.Context) (client.Reader, func(context.Context) bool, *actions.Actions) {
	logger := log.FromContext(ctx).WithName("ui")

	cfg, err := config.GetConfig()
	if err != nil {
		logger.Info("no cluster reachable; ui will serve empty pages and refuse every action",
			"error", err.Error())
		return nil, nil, nil
	}

	scheme, err := ui.NewReaderScheme()
	if err != nil {
		logger.Error(err, "build ui reader scheme")
		return nil, nil, nil
	}

	reader, waitForSync, err := ui.NewClusterReader(ctx, cfg, scheme)
	if err != nil {
		logger.Error(err, "build ui cluster reader")
		return nil, nil, nil
	}

	writer, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		logger.Error(err, "build ui action writer; ui will serve its pages and refuse every action")
		return reader, waitForSync, nil
	}
	return reader, waitForSync, actions.New(writer)
}

// buildProjection builds and starts Task D3-1's shared pipeline projection
// loop (ui/projection.Projection) over reader and returns it so the caller
// can wire every page's accessor and every stream's Subscribe* in
// ui.Options to the same instance -- one list round feeding the initial
// render of each page and every open SSE connection (design plan ruling R4).
//
// Starting it unconditionally, even over a nil reader, is safe: Projection
// already treats a nil reader as "project nothing"
// (ui/projection/projection.go), the same as ui.Options.Entries being unset
// ever meant.
func buildProjection(ctx context.Context, reader client.Reader, history int) *projection.Projection {
	proj := projection.New(reader, projection.DefaultInterval, projection.WithPipelineHistory(history))
	go func() {
		if err := proj.Run(ctx); err != nil && ctx.Err() == nil {
			log.FromContext(ctx).WithName("ui").Error(err, "pipeline projection loop stopped")
		}
	}()
	return proj
}
