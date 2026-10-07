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

package advisory

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// Advisory kinds and outcomes (clustarr_task_events_total's labels).
const (
	kindNak  = "nak"
	kindTerm = "term"

	outcomeRecorded    = "recorded"
	outcomeStale       = "stale"
	outcomeUnresolved  = "unresolved"
	outcomeMetricsOnly = "metricsOnly"
)

func count(consumer, kind, outcome string) {
	if consumer == "" {
		consumer = "unknown"
	}
	metrics.TaskEventsTotal.WithLabelValues(consumer, kind, outcome).Inc()
}

// ackMetric is JetStream's JSConsumerAckMetric (nats-server
// jetstream_events.go): ack_time is nanoseconds from delivery to ack.
type ackMetric struct {
	Stream     string `json:"stream"`
	Consumer   string `json:"consumer"`
	StreamSeq  uint64 `json:"stream_seq"`
	Delay      int64  `json:"ack_time"`
	Deliveries uint64 `json:"deliveries"`
}

// ackSubjects are the ack-metric subjects of every Dispatched consumer of
// t, plus a "<prefix>.<stream>.*" for each stream whose dispatched durables
// are dynamic.
func ackSubjects(t events.Topology) []string {
	var out []string
	for _, c := range t.Consumers {
		if c.Dispatched {
			out = append(out, events.AckMetricSubject(c.Stream, c.Name))
		}
	}
	if events.TranscodeTaskConsumer("", "").Dispatched {
		out = append(out, events.AckMetricSubject(events.StreamWorkTranscode, "*"))
	}
	if events.EngineConsumer("", 0).Dispatched {
		out = append(out, events.AckMetricSubject(events.StreamWorkEngine, "*"))
	}
	return out
}

// subscribeAcks opens one core subscription per ack-metric subject (ruling
// R16). Ack sampling is metrics only: a WorkQueue stream deletes an acked
// message, so nothing here can reach a CR. A bus with no core subscriptions
// (membus) samples nothing.
func (i *Intake) subscribeAcks(ctx context.Context) (stop func()) {
	cs, ok := i.Bus.(events.CoreSubscriber)
	if !ok {
		return func() {}
	}
	log := logging.FromContext(ctx)
	var stops []func()
	for _, subj := range ackSubjects(i.Topology) {
		s, err := cs.SubscribeCore(ctx, subj, observeAck)
		if err != nil {
			log.Warn("advisory: could not subscribe to an ack metric subject; its samples are lost", "subject", subj, "error", err)
			continue
		}
		stops = append(stops, s)
	}
	return func() {
		for _, s := range stops {
			s()
		}
	}
}

// observeAck records one sampled ack.
func observeAck(subject string, data []byte) {
	var m ackMetric
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	consumer := m.Consumer
	if consumer == "" {
		consumer = subject[strings.LastIndex(subject, ".")+1:]
	}
	metrics.TaskAckDelay.WithLabelValues(consumer).Observe(float64(m.Delay) / 1e9)
	metrics.TaskDeliveries.WithLabelValues(consumer).Observe(float64(m.Deliveries))
}
