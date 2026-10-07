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

// Package maintenance is the manager's maintenance loop over the bus's
// object stores (ADR-0019: maintenance is the manager's; ADR-0020 §5,
// ADR-0021 §3). It is leader-only and imports no agent code.
package maintenance

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/mediactl/clustarr/app/catalog/artwork"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Audit is one object store's metadata audit, run on every sweep: it
// backfills, with SetMeta and never by re-uploading chunks, the objects
// written under an older meta-version (ADR-0021 §3). It returns how many
// objects it changed.
type Audit struct {
	// Name labels the audit in logs: the store it audits.
	Name string
	Run  func(ctx context.Context) (changed int, err error)
}

// ObjectStores is the manager's object-store maintenance loop (ADR-0020 §5):
// every Interval it purges the orphaned chunks of every object store of
// Topology through the one shared purge (events.PurgeOrphans), so a store
// added to the topology later is never forgotten, then runs every Audit. Its
// cadence and grace are the artwork reaper's.
type ObjectStores struct {
	// Admin purges; nil purges nothing (membus has no chunks).
	Admin events.ObjectStoreAdmin
	// Topology names the object stores: the manager's ensured topology.
	Topology events.Topology
	// Audits run after the purge, each on its own store.
	Audits []Audit
	// Interval defaults to artwork.DefaultReapInterval, Grace to
	// artwork.DefaultReapGrace: a Put's chunks precede its meta, and the
	// grace covers one in progress.
	Interval, Grace time.Duration
}

// NeedLeaderElection makes the loop a cluster singleton: the stores are
// shared, and two sweepers would only race each other's purges.
func (m *ObjectStores) NeedLeaderElection() bool { return true }

// Start sweeps at once and then every Interval until ctx ends. A failed
// sweep is logged and retried on the next tick.
func (m *ObjectStores) Start(ctx context.Context) error {
	interval := m.Interval
	if interval <= 0 {
		interval = artwork.DefaultReapInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if err := m.Sweep(ctx); err != nil && ctx.Err() == nil {
			logging.FromContext(ctx).Warn("maintenance: object-store sweep incomplete", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// Sweep runs one pass: the purge over every object store, then the audits.
// Its error joins every failure; the pass carries on past each.
func (m *ObjectStores) Sweep(ctx context.Context) error {
	ctx, span := tracing.Start(ctx, "maintenance.ObjectStores.Sweep")
	defer span.End()
	log := logging.FromContext(ctx)
	grace := m.Grace
	if grace <= 0 {
		grace = artwork.DefaultReapGrace
	}
	var errs []error
	if m.Admin != nil {
		purged, err := events.PurgeOrphans(ctx, m.Admin, m.Topology.ObjectStoreNames(), grace)
		for _, p := range purged {
			if p.Subjects > 0 {
				log.Info("maintenance: purged orphaned chunks", "bucket", p.Bucket, "subjects", p.Subjects, "bytes", p.Bytes)
			}
		}
		if err != nil {
			errs = append(errs, err)
		}
	}
	for _, a := range m.Audits {
		if a.Run == nil {
			continue
		}
		n, err := a.Run(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("maintenance: audit %s: %w", a.Name, err))
		}
		if n > 0 {
			log.Info("maintenance: backfilled object metadata", "store", a.Name, "objects", n)
		}
	}
	err := errors.Join(errs...)
	if err != nil {
		tracing.RecordError(span, err)
	}
	return err
}
