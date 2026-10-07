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

// Package agent is the agent's import domain (spec §3.5.3): importarr-scan,
// importarr-fileimport, importarr-list and importarr-recycle (the
// recycle-bin sweep) on CLUSTARR_WORK_IMPORTARR. Every replica consumes,
// and every replica needs a writable /data.
package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	ctrl "sigs.k8s.io/controller-runtime"

	catalogagent "github.com/mediactl/clustarr/app/catalog/agent"
	"github.com/mediactl/clustarr/app/import/worker/fileimport"
	"github.com/mediactl/clustarr/app/import/worker/rescan"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// Options is what the import domain's Register takes.
type Options struct {
	k8s.Options
	// DataDir is where the RWX library volume is mounted (/data); the
	// import.data readiness check proves it writable.
	DataDir string
	// SampleMaxBytes is the video size floor both the rescan and the import
	// workers apply (fsops.IsSuspectedSample); 0 disables it.
	SampleMaxBytes int64
	// TraktBaseURL and PlexBaseURL point the list worker's providers
	// elsewhere (--trakt-base-url, --plex-base-url).
	TraktBaseURL, PlexBaseURL string
}

// Register adds the four consumers, declares the two MediaFile indexes they
// read, and returns the import.data check.
//
// The consumers are the work.importarr.* queues (amendment §A1.6): scan,
// fileimport, list and recycle. The list worker creates Movie and Series only today;
// a spec.kinds entry naming a kind its provider cannot yield is refused at
// admission (R-10), and one it can yield but no catalog writer exists for
// fails on status (app/import/worker/importlist's syncKind), rather than
// being skipped.
//
// Both file-reading workers get o.SampleMaxBytes through [newScanWorker] and
// [newImportWorker]; see Options.SampleMaxBytes.
func Register(_ context.Context, mgr ctrl.Manager, bus events.Bus, o Options) (catalogagent.Registration, error) {
	if bus == nil {
		return catalogagent.Registration{}, errors.New("import domain: Register needs the bus")
	}
	if strings.TrimSpace(o.DataDir) == "" {
		return catalogagent.Registration{}, errors.New("import domain: a data directory is required")
	}
	topo := o.BusTopology()
	c, api := mgr.GetClient(), mgr.GetAPIReader()
	for _, cons := range []struct {
		durable string
		handle  events.Handler
	}{
		// The library rescan, on ConsumerImportScan ("importarr-scan"). Its
		// incremental fingerprint check reads the spec.path field index
		// (rescan.FieldIndexes), which the process registers before the
		// manager starts.
		{events.ConsumerImportScan, newScanWorker(c, api, bus, o).Handle},
		// The completed-download import worker (amendment §A1.2, §A1.6; plan
		// tasks D2-7/D2-8), on ConsumerImportFile ("importarr-fileimport",
		// R6). It reads the spec.mediaRef.target field index
		// (fileimport.FieldIndexes), which the process registers before the
		// manager starts.
		{events.ConsumerImportFile, newImportWorker(c, api, bus, o).Handle},
		// The import-list sync worker (amendment §A1.3, §A1.6; plan task G1-3),
		// on ConsumerImportList ("importarr-list"): fetch, dedupe, drop what an
		// ImportExclusion blocks, then create or update catalog items under
		// k8s.ManagerImportarrWorker. It never writes ImportList.status -- it
		// checkpoints a Result to clustarr-progress for the ImportList
		// controller (app/import/manager) to project.
		{events.ConsumerImportList, newListWorker(c, bus, o).Handle},
		// The recycle-bin sweep (spec 2026-10-06 §3.5.3): the manager queues
		// one task every 6 h on importarr-recycle, so this domain has no
		// timer and can scale to zero. fileimport.RecycleSweeper (task X7a
		// built it) is the one consumer of RootFolder.spec.recycleBin.
		// cleanupDays, emptying each bin of the dated folders past
		// retention. It reads and deletes under /data, so it runs here in
		// the import domain, whose pods mount it.
		{events.ConsumerImportRecycle, fileimport.NewRecycleSweeper(c).Handle},
	} {
		spec, ok := topo.Consumer(cons.durable)
		if !ok {
			return catalogagent.Registration{}, fmt.Errorf("import domain: consumer %s missing from topology", cons.durable)
		}
		sub, handle := spec.Subscription(), cons.handle
		// k8s.EveryReplica, not manager.RunnableFunc: amendment §A1.6 runs
		// every work.importarr.* consumer on EVERY replica of importarr-worker,
		// and a bare RunnableFunc has no NeedLeaderElection method, so
		// controller-runtime puts it behind the leader lease. importarr-worker
		// does not run leader election, and with it disabled controller-runtime
		// treats the process as elected and starts those runnables anyway --
		// so the subscription did open there. The exposure is any importarr
		// replica that DOES elect: exactly one of them would have opened the
		// subscription.
		if err := mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
			stop, err := bus.Subscribe(ctx, sub, handle)
			if err != nil {
				return fmt.Errorf("import domain: subscribe %s: %w", sub.Durable, err)
			}
			defer stop()
			<-ctx.Done()
			return nil
		})); err != nil {
			return catalogagent.Registration{}, fmt.Errorf("import domain: add %s consumer: %w", cons.durable, err)
		}
	}
	// A scan or import worker that cannot write the library must not accept
	// work (amendment §A1.6).
	var ready k8s.Checks
	if err := ready.Add("import.data", k8s.DataReadyChecker(o.DataDir)); err != nil {
		return catalogagent.Registration{}, err
	}
	return catalogagent.Registration{
		Ready:   &ready,
		Indexes: append(rescan.FieldIndexes(), fileimport.FieldIndexes()...),
	}, nil
}
