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

package downloads

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"k8s.io/client-go/util/workqueue"
	ctrl "sigs.k8s.io/controller-runtime"
	crsource "sigs.k8s.io/controller-runtime/pkg/source"

	"github.com/mediactl/clustarr/app/remediation"
	"github.com/mediactl/clustarr/app/remediation/dlindex"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/records/recordsource"
)

// Sources implements remediation.Watcher (A3.5 step 5):
//
//   - S27, clustarr-transfers: a record wakes its owner (the record's item);
//     a record reading claimed: false is filed in the Unclaimed index under
//     its claim's owner, whose key it wakes too.
//   - S28, clustarr-imports: an import record wakes its owner (A3.8).
//   - clustarr-engines: an engine's new boot wakes every owner with an
//     entry pinned to that engine, which republishes desired state (the
//     resync, §6.7).
func (s *Stage) Sources(mgr ctrl.Manager) ([]remediation.Source, error) {
	reader := mgr.GetClient()
	transfers := recordsource.NewItems(s.o.Bus, events.BucketTransfers, func(item schema.ItemRef, value []byte) []remediation.Key {
		var keys []remediation.Key
		if k, ok := ownerKeyOf(item); ok {
			keys = append(keys, k)
		}
		var rec schema.TransferRecord
		if err := json.Unmarshal(value, &rec); err == nil {
			if claimOwner, unclaimed := s.unclaimed.observe(&rec, 0); unclaimed {
				ref := schema.ItemRef{Kind: claimOwner.Kind, Ref: schema.Ref{Namespace: claimOwner.Namespace, Name: claimOwner.Name}}
				if k, ok := ownerKeyOf(ref); ok && !containsKey(keys, k) {
					keys = append(keys, k)
				}
			}
		}
		return keys
	})
	imports := recordsource.NewItems(s.o.Bus, events.BucketImports, func(item schema.ItemRef, _ []byte) []remediation.Key {
		if k, ok := ownerKeyOf(item); ok {
			return []remediation.Key{k}
		}
		return nil
	})
	engines := recordsource.NewItems(s.o.Bus, events.BucketEngines, func(item schema.ItemRef, value []byte) []remediation.Key {
		var rec schema.EngineRecord
		if err := json.Unmarshal(value, &rec); err != nil || rec.Client == "" {
			return nil
		}
		name := rec.Client + "-" + strconv.Itoa(int(rec.Ordinal))
		if !s.boots.see(name, rec.BootID, rec.Reattached, rec.At) {
			return nil
		}
		owners, err := dlindex.PinnedTo(context.Background(), reader, item.Namespace, name)
		if err != nil {
			logging.FromContext(context.Background()).Warn("downloads: owners pinned to a rebooted engine", "engine", name, "err", err)
			return nil
		}
		keys := make([]remediation.Key, 0, len(owners))
		for _, o := range owners {
			if k, ok := ownerKeyOf(o); ok {
				keys = append(keys, k)
			}
		}
		return keys
	})
	return []remediation.Source{
		{Name: "S27/" + events.BucketTransfers, Raw: transfers},
		{Name: "S28/" + events.BucketImports, Raw: imports},
		{Name: "engines/" + events.BucketEngines, Raw: engines},
		{Name: "seed/" + events.BucketTransfers, Raw: seedSource{s: s}},
	}, nil
}

// ownerKeyOf is an item ref's loop key, for the eight kinds the stage plans.
func ownerKeyOf(ref schema.ItemRef) (remediation.Key, bool) {
	kind := remediation.KeyKind(kindName(ref.Kind))
	switch kind {
	case remediation.KindMovie, remediation.KindSeries, remediation.KindAlbum, remediation.KindBook,
		remediation.KindAudiobook, remediation.KindComic, remediation.KindEpisode, remediation.KindIssue:
	default:
		return remediation.Key{}, false
	}
	if ref.Name == "" {
		return remediation.Key{}, false
	}
	return remediation.Key{Kind: kind, Namespace: ref.Namespace, Name: ref.Name}, true
}

// kindName is an ItemRef kind as an object Kind ("movie" reads "Movie").
func kindName(k string) string {
	if k == "" {
		return k
	}
	return strings.ToUpper(k[:1]) + k[1:]
}

func containsKey(keys []remediation.Key, k remediation.Key) bool {
	for _, x := range keys {
		if x == k {
			return true
		}
	}
	return false
}

// seedTimeout bounds the seed's read of clustarr-transfers.
const seedTimeout = time.Minute

// seedUnclaimed fills the Unclaimed index from clustarr-transfers once per
// leader: the source opens with UpdatesOnly, and a transfer that went
// unclaimed before this leader started is rewritten only daily.
//
// It reads the bucket in one pass. It asked for the keys and then for each
// record in turn, and every item pass waits for it (seedOnce): 2,866
// requests from a manager at its CPU limit held the loop's sixteen workers
// for 165 s after a start on talos, and every candidate parked meanwhile
// lapsed its 60 s budget (2026-11-26).
func (s *Stage) seedUnclaimed(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, seedTimeout)
	defer cancel()
	all, err := s.transfers.All(ctx)
	if errors.Is(err, events.ErrBucketNotFound) {
		// A bucket the topology has not made yet holds no transfer.
		return
	}
	if err != nil {
		logging.FromContext(ctx).Warn("downloads: seed the unclaimed index", "err", err)
		return
	}
	for _, t := range all {
		s.unclaimed.observe(t.Record, t.Revision)
	}
}

// seedSource starts the seed when the loop's sources start, which is when
// this manager takes the lease and before a worker runs: the first passes
// find it done, or wait for the rest of it (seedOnce).
type seedSource struct{ s *Stage }

var _ crsource.TypedSource[remediation.Key] = seedSource{}

// Start implements source.TypedSource. It adds no key.
func (ss seedSource) Start(ctx context.Context, _ workqueue.TypedRateLimitingInterface[remediation.Key]) error {
	ctx = logging.Subsystem(ctx, "remediation.downloads")
	go ss.s.seedOnce.Do(func() { ss.s.seedUnclaimed(ctx) })
	return nil
}

// String implements fmt.Stringer for the controller's logs.
func (seedSource) String() string { return "downloads unclaimed-index seed" }
