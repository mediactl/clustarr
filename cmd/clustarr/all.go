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

package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/spf13/cobra"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mediactl/clustarr/captionarr"
	"github.com/mediactl/clustarr/catalogarr"
	"github.com/mediactl/clustarr/grabarr"
	"github.com/mediactl/clustarr/indexarr"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/squasharr"
)

// allServices is what `clustarr all` starts, in the order it starts them.
//
// Each entry gets its own port offset because five managers in one process
// would otherwise race for one metrics port and one probe port. grabarr,
// squasharr and captionarr run their controller role only: the engines and the
// transcode worker are separate pods in a real deployment, and running them
// here would need volumes this mode does not have.
func allServices() []struct {
	name string
	run  func(ctx context.Context, o k8s.Options) error
} {
	return []struct {
		name string
		run  func(ctx context.Context, o k8s.Options) error
	}{
		{"catalogarr", func(ctx context.Context, o k8s.Options) error {
			return runCatalogarr(ctx, catalogarr.Options{Options: o, Role: catalogarr.RoleAll})
		}},
		{"indexarr", func(ctx context.Context, o k8s.Options) error {
			d := indexarr.DefaultOptions()
			d.Options = o
			return runIndexarr(ctx, d)
		}},
		{"grabarr", func(ctx context.Context, o k8s.Options) error {
			d := grabarr.DefaultOptions()
			d.Options = o
			return runGrabarr(ctx, d)
		}},
		{"squasharr", func(ctx context.Context, o k8s.Options) error {
			d := squasharr.DefaultOptions()
			d.Options = o
			return runSquasharr(ctx, d)
		}},
		{"captionarr", func(ctx context.Context, o k8s.Options) error {
			d := captionarr.DefaultOptions()
			d.Options = o
			return runCaptionarr(ctx, d)
		}},
	}
}

func newAllCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "all",
		Short: "Run every service in one process, for kind and development",
		Long: "all starts catalogarr, indexarr, grabarr, squasharr and captionarr in a single\n" +
			"process. It is meant for kind and local development, not for a cluster: §3 gives\n" +
			"each service its own Deployment, RBAC and leader election, and the engines and\n" +
			"transcode workers that run as separate pods are not started here.\n\n" +
			"Each service's metrics and probe listeners are offset by one port from the\n" +
			"addresses given, in the order above.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
	}
	common := bindCommonFlags(cmd.Flags())

	// Leader election buys nothing in a single process that already runs one
	// of each controller, and would only add a Lease per service to clean up.
	if err := cmd.Flags().MarkHidden("leader-elect"); err != nil {
		panic(err)
	}

	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		base := *common
		base.LeaderElect = false
		base.BusSingleNode = true

		services := allServices()
		optionsFor := make([]k8s.Options, len(services))
		for i, svc := range services {
			o := base
			var err error
			if o.MetricsBindAddress, err = offsetAddress(base.MetricsBindAddress, i); err != nil {
				return fmt.Errorf("%s: %w", svc.name, err)
			}
			if o.HealthProbeBindAddress, err = offsetAddress(base.HealthProbeBindAddress, i); err != nil {
				return fmt.Errorf("%s: %w", svc.name, err)
			}
			if o.PprofBindAddress, err = offsetAddress(base.PprofBindAddress, i); err != nil {
				return fmt.Errorf("%s: %w", svc.name, err)
			}
			optionsFor[i] = o
		}

		return runAll(cmd.Context(), services, optionsFor)
	}
	return cmd
}

// runAll starts every service and returns when they have all stopped.
//
// The first failure cancels the rest: a half-running stack in kind is worse
// than a clean exit, because the missing service's absence shows up later as
// an unexplained timeout in whatever was being tested.
func runAll(
	ctx context.Context,
	services []struct {
		name string
		run  func(ctx context.Context, o k8s.Options) error
	},
	options []k8s.Options,
) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
	)
	log := ctrl.LoggerFrom(ctx).WithName("all")

	for i, svc := range services {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := svc.run(ctx, options[i])
			if err == nil {
				return
			}
			log.Error(err, "service stopped", "service", svc.name)
			mu.Lock()
			if first == nil {
				first = fmt.Errorf("%s: %w", svc.name, err)
			}
			mu.Unlock()
			cancel()
		}()
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	return first
}
