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

package maintenance

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// DefaultFillInterval is how often every object store's size is read against
// its limit: one stream info a store, so cheap enough to be frequent, and a
// cache store filling at its fastest (a re-analysis of the whole library)
// does not climb from ObjectStoreNearlyFull to its limit inside it.
const DefaultFillInterval = 10 * time.Minute

// nearlyFullEvery is how often a store that stays nearly full is reported
// again.
const nearlyFullEvery = time.Hour

// The reason label of clustarr_objects_evicted_total.
const (
	// EvictCapacity is a cache store nearly full giving up its oldest.
	EvictCapacity = "capacity"
	// EvictUnreferenced is an object nothing the library holds names.
	EvictUnreferenced = "unreferenced"
)

// fillState is what the fill check remembers between passes: when each
// nearly-full store was last reported.
type fillState struct {
	mu       sync.Mutex
	reported map[string]time.Time
}

// fillLoop checks at once and then every FillInterval until ctx ends.
func (m *ObjectStores) fillLoop(ctx context.Context) {
	if m.Stores == nil {
		return
	}
	every := m.FillInterval
	if every <= 0 {
		every = DefaultFillInterval
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := m.CheckFill(ctx, time.Now()); err != nil && ctx.Err() == nil {
			logging.FromContext(ctx).Warn("maintenance: object-store fill check incomplete", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// CheckFill reads every store's size against its byte limit and records both
// (clustarr_object_store_bytes, clustarr_object_store_max_bytes). A bucket's
// stream discards new, so a store at its limit refuses every write, and
// nothing said so: talos's fingerprint store stood at its limit, every
// write refused and ignored (it is a cache), until someone looked
// (2026-11-28). So a store at ObjectStoreNearlyFull of its limit is
//
//   - evicted, when it is a cache (ObjectStoreSpec.Cache): its
//     oldest-written objects are deleted until it is back at
//     ObjectStoreEvictTo;
//   - reported, when it is not: a warning now and every nearlyFullEvery
//     while it lasts, since only its own reaper or a higher limit helps.
//
// Its error joins every failure; the pass carries on past each.
func (m *ObjectStores) CheckFill(ctx context.Context, now time.Time) error {
	if m.Stores == nil {
		return nil
	}
	ctx, span := tracing.Start(ctx, "maintenance.ObjectStores.CheckFill")
	defer span.End()
	log := logging.FromContext(ctx)
	var errs []error
	for _, spec := range m.Topology.ObjectStores {
		store := m.Stores(spec.Name)
		if store == nil {
			continue
		}
		st, err := store.Status(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("maintenance: status of %s: %w", spec.Name, err))
			continue
		}
		metrics.ObjectStoreBytes.WithLabelValues(spec.Name).Set(float64(st.Bytes))
		metrics.ObjectStoreMaxBytes.WithLabelValues(spec.Name).Set(float64(st.MaxBytes))
		if st.MaxBytes == 0 || float64(st.Bytes) < events.ObjectStoreNearlyFull*float64(st.MaxBytes) {
			if m.fill.clear(spec.Name) {
				log.Info("maintenance: the object store has room again", "store", spec.Name,
					"bytes", st.Bytes, "maxBytes", st.MaxBytes)
			}
			continue
		}
		if !spec.Cache {
			if m.fill.due(spec.Name, now) {
				log.Warn("maintenance: the object store is nearly full and refuses every write at its limit",
					"store", spec.Name, "bytes", st.Bytes, "maxBytes", st.MaxBytes)
			}
			continue
		}
		n, freed, err := EvictOldest(ctx, store, st)
		if n > 0 {
			metrics.ObjectsEvictedTotal.WithLabelValues(spec.Name, EvictCapacity).Add(float64(n))
			log.Info("maintenance: evicted the oldest objects of a cache store that was nearly full",
				"store", spec.Name, "objects", n, "bytes", freed, "was", st.Bytes, "maxBytes", st.MaxBytes)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("maintenance: evict from %s: %w", spec.Name, err))
		}
	}
	err := errors.Join(errs...)
	if err != nil {
		tracing.RecordError(span, err)
	}
	return err
}

// maxEvictErrors is how many failed deletes end an eviction: a store that
// refuses one after another is not answering.
const maxEvictErrors = 20

// EvictOldest deletes store's oldest-written objects, oldest first, until
// the bytes they held bring st down to ObjectStoreEvictTo of its limit. It
// returns how many it deleted and the bytes they held. An object's size is
// its content's; its record stays as a delete marker of about the same
// size, so the stream shrinks by about what is returned, and the next check
// takes up any shortfall.
func EvictOldest(ctx context.Context, store events.ObjectStore, st events.ObjectStoreStatus) (n int, freed uint64, err error) {
	target := uint64(events.ObjectStoreEvictTo * float64(st.MaxBytes))
	if st.MaxBytes == 0 || st.Bytes <= target {
		return 0, 0, nil
	}
	need := st.Bytes - target
	objects, err := store.List(ctx, "")
	if err != nil {
		return 0, 0, fmt.Errorf("list: %w", err)
	}
	slices.SortStableFunc(objects, func(a, b events.ObjectInfo) int {
		if c := a.ModTime.Compare(b.ModTime); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	var errs []error
	for _, o := range objects {
		if freed >= need {
			break
		}
		if err := ctx.Err(); err != nil {
			return n, freed, errors.Join(append(errs, err)...)
		}
		if err := store.Delete(ctx, o.Name); err != nil {
			if errors.Is(err, events.ErrObjectNotFound) {
				continue
			}
			errs = append(errs, fmt.Errorf("delete %s: %w", o.Name, err))
			if len(errs) >= maxEvictErrors {
				break
			}
			continue
		}
		n++
		freed += uint64(max(o.Size, 0))
	}
	return n, freed, errors.Join(errs...)
}

// due reports whether store's nearly-full state is to be reported now, and
// records that it was.
func (f *fillState) due(store string, now time.Time) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if last, ok := f.reported[store]; ok && now.Sub(last) < nearlyFullEvery {
		return false
	}
	if f.reported == nil {
		f.reported = map[string]time.Time{}
	}
	f.reported[store] = now
	return true
}

// clear forgets store's nearly-full state and reports whether it had one.
func (f *fillState) clear(store string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.reported[store]
	delete(f.reported, store)
	return ok
}
