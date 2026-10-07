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

package extmetrics

import (
	"context"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// DefaultGaugeInterval is QueueGauge's period.
const DefaultGaugeInterval = 30 * time.Second

// QueueGauge sets metrics.WorkQueuePending{stream,consumer} to every work
// consumer's lag (§9.4), the four counters behind it (ConsumerPending,
// ConsumerAckPending, ConsumerWaiting, ConsumerMaxAckPending), and, with
// Streams, metrics.StreamFillRatio{stream} to every byte-limited stream's
// fill (S10). Leader-only: one replica asks the broker.
type QueueGauge struct {
	States ConsumerStater
	// Cache, when set, is the read path shared with the External Metrics
	// API, so the two never double-poll (split §9.4 as amended 2026-10-07);
	// nil reads States directly.
	Cache    *StateCache
	Topology events.Topology
	// Streams reads each stream's fill; nil skips the series.
	Streams  events.StreamStater
	Interval time.Duration // 0 means DefaultGaugeInterval
	Timeout  time.Duration // 0 means 5 s
	// Dispatch, when set, is the manager's dispatch ledger (app/dispatch):
	// Update exports clustarr_dispatch_waiting and
	// clustarr_dispatch_unattended for every Dispatched consumer from it
	// (ADR-0019 §5.4). Never an HPA metric.
	Dispatch DispatchStats
}

// DispatchStats is what QueueGauge reads of the dispatch ledger.
type DispatchStats interface {
	WaitingCount(durable string) int
	Unattended(durable string) bool
}

// NeedLeaderElection makes it a cluster singleton.
func (q *QueueGauge) NeedLeaderElection() bool { return true }

// Start updates at once and every Interval until ctx ends.
func (q *QueueGauge) Start(ctx context.Context) error {
	interval := q.Interval
	if interval <= 0 {
		interval = DefaultGaugeInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		q.Update(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// consumerGauges are the five per-consumer series Update sets together and
// drops together.
var consumerGauges = []*prometheus.GaugeVec{
	metrics.WorkQueuePending, metrics.ConsumerPending, metrics.ConsumerAckPending,
	metrics.ConsumerWaiting, metrics.ConsumerMaxAckPending,
}

// read is c's state through the shared Cache, else straight from States.
func (q *QueueGauge) read(ctx context.Context, c events.ConsumerSpec, timeout time.Duration) (events.ConsumerState, error) {
	if q.Cache != nil {
		return q.Cache.Get(ctx, Series{Stream: c.Stream, Consumer: c.Name})
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return q.States.ConsumerState(cctx, c.Stream, c.Name)
}

// Update reads every non-advisory consumer once. A consumer it cannot read
// loses its series.
func (q *QueueGauge) Update(ctx context.Context) {
	timeout := q.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	for _, c := range q.Topology.Consumers {
		if c.Stream == events.StreamAdvisories {
			continue
		}
		st, err := q.read(ctx, c, timeout)
		if err != nil {
			for _, g := range consumerGauges {
				g.DeleteLabelValues(c.Stream, c.Name)
			}
			logging.FromContext(ctx).Debug("extmetrics: queue gauge could not read a consumer", "stream", c.Stream, "consumer", c.Name, "error", err)
			continue
		}
		metrics.WorkQueuePending.WithLabelValues(c.Stream, c.Name).Set(float64(st.Lag()))
		metrics.ConsumerPending.WithLabelValues(c.Stream, c.Name).Set(float64(st.Pending))
		metrics.ConsumerAckPending.WithLabelValues(c.Stream, c.Name).Set(float64(st.AckPending))
		metrics.ConsumerWaiting.WithLabelValues(c.Stream, c.Name).Set(float64(st.Waiting))
		metrics.ConsumerMaxAckPending.WithLabelValues(c.Stream, c.Name).Set(float64(st.MaxAckPending))
	}
	if q.Dispatch != nil {
		for _, c := range q.Topology.Consumers {
			if !c.Dispatched || c.Stream == events.StreamAdvisories {
				continue
			}
			metrics.DispatchWaiting.WithLabelValues(c.Name).Set(float64(q.Dispatch.WaitingCount(c.Name)))
			unattended := 0.0
			if q.Dispatch.Unattended(c.Name) {
				unattended = 1
			}
			metrics.DispatchUnattended.WithLabelValues(c.Name).Set(unattended)
		}
	}
	if q.Streams != nil {
		for _, s := range q.Topology.Streams {
			if s.MaxBytes <= 0 {
				metrics.StreamFillRatio.DeleteLabelValues(s.Name)
				continue
			}
			cctx, cancel := context.WithTimeout(ctx, timeout)
			f, err := q.Streams.StreamFill(cctx, s.Name)
			cancel()
			if err != nil || f.MaxBytes == 0 {
				metrics.StreamFillRatio.DeleteLabelValues(s.Name)
				continue
			}
			metrics.StreamFillRatio.WithLabelValues(s.Name).Set(float64(f.Bytes) / float64(f.MaxBytes))
		}
	}
}
