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

package metadata

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// gatewayRebuildEvery is how often the gateway's MetadataProviders are
// compared with the set its registry was built from. Below the work
// handler's 30 s retry for ErrNoProvider, so a task that met an empty
// registry is retried against the rebuilt one.
const gatewayRebuildEvery = 15 * time.Second

// superviseGateway runs the metadata gateway and rebuilds it whenever the
// MetadataProviders it was built from change. catalogmetadata.Setup reads
// them once, so a provider added after the agent started -- a fresh
// install's TMDB and TVDB, applied a minute after the chart (talos,
// 2026-10-15) -- was never asked, and every movie and series task was
// discarded as "no such record". fingerprint names the current provider set;
// start builds a gateway from it and returns its stop. A List that fails
// keeps the running gateway; a gateway that cannot be rebuilt ends the
// runnable, and with it the agent, as a first Setup failure does.
//
// Provider Secrets are not in the fingerprint: they are read by name,
// uncached, and a changed key still needs the agent restarted.
func superviseGateway(ctx context.Context, every time.Duration,
	fingerprint func(context.Context) (string, error),
	start func(context.Context) (func(), error),
) error {
	fp, err := fingerprint(ctx)
	if err != nil {
		return fmt.Errorf("catalog: list the metadata gateway's providers: %w", err)
	}
	stop, err := start(ctx)
	if err != nil {
		return err
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			stop()
			return nil
		case <-tick.C:
		}
		next, err := fingerprint(ctx)
		if err != nil {
			logging.FromContext(ctx).Warn("catalog: could not list the metadata gateway's providers; keeping the running gateway", "err", err)
			continue
		}
		if next == fp {
			continue
		}
		logging.FromContext(ctx).Info("catalog: the MetadataProviders changed; rebuilding the metadata gateway")
		stop()
		if stop, err = start(ctx); err != nil {
			return err
		}
		fp = next
	}
}

// providerFingerprint names what BuildRegistry reads: every MetadataProvider
// and its spec, by generation. A status write leaves it alone.
func providerFingerprint(providers []catalogv1alpha1.MetadataProvider) string {
	keys := make([]string, 0, len(providers))
	for _, p := range providers {
		keys = append(keys, fmt.Sprintf("%s/%s@%d", p.Namespace, p.Name, p.Generation))
	}
	slices.Sort(keys)
	return strings.Join(keys, ",")
}
