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

// Package importlist is importarr's ImportList sync worker: the
// clustarr.work.importarr.list.<importlist> handler that wires an
// ImportList's spec to a concrete pkg/importlist provider (BuildProvider,
// the job pkg/importlist/config.go:112-115 names this task's owner for),
// fetches it, dedupes, drops entries an ImportExclusion blocks, resolves
// the id each catalog kind requires through the metadata gateway, and
// creates or updates the resulting Movie and Series items (amendment
// §A1.3, §A1.6, design spec §8.7; task G1-3).
//
// Non-video kinds a spec.kinds entry may name (album, book, audiobook,
// comic) are G2's controllers, not this task's, and are skipped per kind --
// never guessed at -- with a logged reason; see syncKind's doc comment.
//
// # The status split
//
// This worker is never the writer of ImportList.status: k8s.ManagerImportarr
// (the ImportList controller's field manager, importarr/controller/importlist)
// owns it in full, per that constant's own doc comment. Instead this worker
// checkpoints its outcome -- Result, at ResultKey(uid) in the
// clustarr-progress bucket -- for the controller to poll and project into
// status, the same split importarr/worker/rescan uses against
// importarr/controller/libraryscan for LibraryScan.status.
//
// Catalog writes are spec-only, under [FieldManager]
// (k8s.ManagerImportarrWorker): this worker creates or updates a Movie or
// Series' spec fields and unmonitors or deletes one spec.syncLevel decides
// has fallen off the list, but it never touches MovieStatus or
// SeriesStatus, both of which catalogarr owns in full. See FieldManager's
// doc comment for why importarr/worker/rescan shares this same manager name
// rather than getting its own, and why that is deliberate rather than the
// hazard CLAUDE.md otherwise warns two writers sharing one manager name
// into.
//
// # Respecting ImportExclusion
//
// Every fetched, deduped entry is checked against the
// clustarr-import-exclusions bucket (isExcluded, in sync.go) before it is
// ever turned into a catalog write -- the same KV lookup contract
// events.ExclusionEntry documents, and the reason the ImportExclusion
// controller (importarr/controller/importexclusion) indexes that bucket in
// the first place rather than this worker listing every ImportExclusion
// per candidate.
//
// # Trakt's device-code flow
//
// The device-code handshake itself (minting and polling a user code) is
// the ImportList controller's job, not this worker's -- see
// importarr/controller/importlist's doc comment. This package's role is
// narrower: [SecretTokenStore] persists the resulting access/refresh token
// pair (and, in the controller, the in-flight device code) in an owned
// Secret, so a token survives a pod restart the way
// pkg/importlist.MemoryTokenStore does not, and
// pkg/importlist/trakt.List.Fetch's own refresh-on-401 logic calls it
// transparently mid-sync when a token is near enough to expiry.
//
// # Registration
//
// Nothing here registers itself. Task G1-5 wires it into importarr-worker's
// setupWorkers, alongside the rescan and fileimport consumers already
// there, with exactly the shape those two already use:
//
//	spec, ok := o.BusTopology().Consumer(events.ConsumerImportList)
//	if !ok {
//	        return fmt.Errorf("importarr: consumer %s missing from topology", events.ConsumerImportList)
//	}
//	worker := importlist.NewWorker(mgr.GetClient(), bus)
//
//	if err := mgr.Add(k8s.EveryReplica(func(ctx context.Context) error {
//	        stop, err := bus.Subscribe(ctx, spec.Subscription(), worker.Handle)
//	        if err != nil {
//	                return fmt.Errorf("importarr: subscribe %s: %w", events.ConsumerImportList, err)
//	        }
//	        <-ctx.Done()
//	        stop()
//	        return nil
//	})); err != nil {
//	        return err
//	}
//
// k8s.EveryReplica, not manager.RunnableFunc, for the same reason rescan
// and fileimport use it: manager.RunnableFunc has no NeedLeaderElection
// method, so controller-runtime would put this behind the leader lease on
// any service that elects, and every importarr-worker replica must be able
// to pick up a sync task.
package importlist
