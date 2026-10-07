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

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// DefaultGaugeInterval is QueueGauge's period.
const DefaultGaugeInterval = 30 * time.Second

// QueueGauge sets metrics.WorkQueuePending{stream,consumer} to every work
// consumer's lag (§9.4), and, with Streams, metrics.StreamFillRatio{stream}
// to every byte-limited stream's fill (S10). Leader-only: one replica asks
// the broker.
type QueueGauge struct {
	States   ConsumerStater
	Topology events.Topology
	// Streams reads each stream's fill; nil skips the series.
	Streams  events.StreamStater
	Interval time.Duration // 0 means DefaultGaugeInterval
	Timeout  time.Duration // 0 means 5 s
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
		cctx, cancel := context.WithTimeout(ctx, timeout)
		st, err := q.States.ConsumerState(cctx, c.Stream, c.Name)
		cancel()
		if err != nil {
			metrics.WorkQueuePending.DeleteLabelValues(c.Stream, c.Name)
			logging.FromContext(ctx).Debug("extmetrics: queue gauge could not read a consumer", "stream", c.Stream, "consumer", c.Name, "error", err)
			continue
		}
		metrics.WorkQueuePending.WithLabelValues(c.Stream, c.Name).Set(float64(st.Lag()))
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
