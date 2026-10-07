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

package mediafile

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	clustarrevents "github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/probestore"
)

// probeWatchRetry is how long probeRecordsSource waits before it opens again
// a watch that ended while the controller runs.
var probeWatchRetry = 5 * time.Second

// probeRecordsSource wakes the reconciler for every answered probe: a KV watch
// on clustarr-probes that enqueues the MediaFile a probed or failed record
// names and skips requests, deletes and undecodable values. It writes nothing:
// Reconcile reads the record itself, so an event never stands in for the
// record (spec 2026-10-06 §6.5.3; the R3 amendment). Controller sources start
// only on the leader, and the watch replays every record when it opens, so an
// answer that landed while no leader ran is incorporated at the next start.
func (r *Reconciler) probeRecordsSource() source.Source {
	return source.Func(func(ctx context.Context, q workqueue.TypedRateLimitingInterface[reconcile.Request]) error {
		ch, err := r.watchProbes(ctx)
		if err != nil {
			return fmt.Errorf("mediafile: watch %s: %w", clustarrevents.BucketProbes, err)
		}
		go r.forwardProbeRecords(ctx, ch, q)
		return nil
	})
}

func (r *Reconciler) watchProbes(ctx context.Context) (<-chan clustarrevents.Entry, error) {
	if r.watchProbeRecords != nil {
		return r.watchProbeRecords(ctx)
	}
	return r.Probes.Watch(ctx)
}

// forwardProbeRecords enqueues from ch until ctx ends, opening the watch again
// probeWatchRetry after it closes.
func (r *Reconciler) forwardProbeRecords(ctx context.Context, ch <-chan clustarrevents.Entry, q workqueue.TypedRateLimitingInterface[reconcile.Request]) {
	for {
		for e := range ch {
			rec, ok := probestore.Decode(e)
			if !ok || rec.MediaFile.Name == "" || (rec.State != schema.ProbeProbed && rec.State != schema.ProbeFailed) {
				continue
			}
			q.Add(reconcile.Request{NamespacedName: types.NamespacedName{Namespace: rec.MediaFile.Namespace, Name: rec.MediaFile.Name}})
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(probeWatchRetry):
		}
		next, err := r.watchProbes(ctx)
		if err != nil {
			logging.FromContext(ctx).Warn("mediafile: could not open the probe records watch again; retrying", "error", err)
			closed := make(chan clustarrevents.Entry)
			close(closed)
			next = closed
		}
		ch = next
	}
}
