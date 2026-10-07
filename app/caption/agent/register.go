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

// Package agent is the agent's caption domain (spec §3.5.3):
// captionarr-fetch-high and captionarr-fetch-normal, bounded by the shared
// KV token bucket clustarr-provider-throttle rather than by replicas.
package agent

import (
	"context"
	"errors"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mediactl/clustarr/app/caption/providerset/build"
	"github.com/mediactl/clustarr/app/caption/worker/fetch"
	catalogagent "github.com/mediactl/clustarr/app/catalog/agent"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/subtitles/providers/embedded/execextract"
)

// Options is what the caption domain's Register takes.
type Options struct {
	k8s.Options
	// DataDir is where the RWX media volume is mounted; sidecars are written
	// beside their video file through it, and caption.data proves it writable.
	DataDir string
}

// Register registers the fetch worker (task F-5) on both fetch consumers,
// captionarr-fetch-high and captionarr-fetch-normal. The worker's runnables
// are k8s.EveryReplica: fetch workers are never leader-elected (§6.5), so
// every replica consumes, bounded by the shared KV token bucket rather than
// by how many pods run.
//
// The provider builder lives as long as the process: its client cache is
// what keeps an OpenSubtitles login across fetch tasks, and its KV -- the
// clustarr-provider-throttle bucket -- is what shares that login across
// worker replicas (build.TokenCache, throttle.SetAuth). It reads
// Secrets, and the worker re-reads each SubtitleRequest before its status
// apply, through the API reader.
//
// The domain's one check is caption.data: the fetch worker writes sidecars
// into o.DataDir, which had no readiness gate before the split (spec §3.3).
func Register(_ context.Context, mgr ctrl.Manager, bus events.Bus, o Options) (catalogagent.Registration, error) {
	if bus == nil {
		return catalogagent.Registration{}, errors.New("caption domain: Register needs the bus")
	}
	if o.DataDir == "" {
		return catalogagent.Registration{}, errors.New("caption domain: a data directory is required")
	}
	providers := build.NewBuilder(mgr.GetClient(), mgr.GetAPIReader())
	providers.KV = bus.KV(events.BucketProviderThrottle)
	providers.Extract = execextract.New("")
	worker := fetch.NewWorker(mgr.GetClient(), mgr.GetAPIReader(), bus, providers, o.DataDir)
	if err := worker.SetupWithManager(mgr, o.BusTopology()); err != nil {
		return catalogagent.Registration{}, fmt.Errorf("captionarr: fetch worker: %w", err)
	}
	var ready k8s.Checks
	if err := ready.Add("caption.data", k8s.DataReadyChecker(o.DataDir)); err != nil {
		return catalogagent.Registration{}, err
	}
	return catalogagent.Registration{Ready: &ready}, nil
}
