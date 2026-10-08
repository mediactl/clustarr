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

package extmetrics_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/autoscale/extmetrics"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// consumerGauges are the five per-consumer series QueueGauge sets and drops
// together; each test starts them empty, since they are process-global.
var consumerGauges = []*prometheus.GaugeVec{
	metrics.WorkQueuePending, metrics.ConsumerPending, metrics.ConsumerAckPending,
	metrics.ConsumerWaiting, metrics.ConsumerMaxAckPending,
}

// gaugeValue and seriesCount read the vectors directly rather than through
// prometheus/client_golang's testutil, which needs github.com/kylelemons/godebug,
// a module go.mod does not declare, so `go test` refuses it under
// -mod=readonly (app/catalog/metadata/tieredcache_test.go has the same note).
func gaugeValue(t *testing.T, g prometheus.Gauge) float64 {
	t.Helper()
	var m dto.Metric
	require.NoError(t, g.Write(&m))
	return m.GetGauge().GetValue()
}

func seriesCount(v *prometheus.GaugeVec) int {
	ch := make(chan prometheus.Metric, 1024)
	v.Collect(ch)
	close(ch)
	return len(ch)
}

func resetGauges() {
	for _, g := range consumerGauges {
		g.Reset()
	}
	metrics.StreamFillRatio.Reset()
	metrics.DispatchWaiting.Reset()
	metrics.DispatchUnattended.Reset()
}

// clustarr_work_queue_pending finally has a producer: the manager's
// leader sets it from the same ConsumerInfo the External Metrics API
// serves. A consumer whose state cannot be read loses its series rather
// than keep a stale value.
func TestQueueGaugeSetsTheLagOfEveryWorkConsumer(t *testing.T) {
	resetGauges()
	metrics.WorkQueuePending.WithLabelValues(events.StreamWorkImport, events.ConsumerImportList).Set(9)
	states := &fakeStates{
		def: 1,
		lag: map[string]uint64{events.ConsumerImportScan: 5},
		err: map[string]error{events.ConsumerImportList: errors.New("nats: timeout")},
	}
	q := &extmetrics.QueueGauge{States: states, Topology: events.Default()}
	assert.True(t, q.NeedLeaderElection())
	q.Update(context.Background())

	assert.Equal(t, 5.0, gaugeValue(t, metrics.WorkQueuePending.WithLabelValues(events.StreamWorkImport, events.ConsumerImportScan)))
	assert.Equal(t, 1.0, gaugeValue(t, metrics.WorkQueuePending.WithLabelValues(events.StreamWorkCatalog, events.ConsumerCatalogMetadata)),
		"fixed consumers are reported too")

	work := 0
	for _, c := range events.Default().Consumers {
		if c.Stream != events.StreamAdvisories {
			work++
		}
	}
	assert.Equal(t, work-1, seriesCount(metrics.WorkQueuePending),
		"one series per work consumer, none for advisories, and the failing consumer's stale 9 removed")
}

// fakeFill answers StreamFill from a map; a missing stream is
// events.ErrStreamNotFound.
type fakeFill map[string]events.StreamFill

func (f fakeFill) StreamFill(_ context.Context, stream string) (events.StreamFill, error) {
	v, ok := f[stream]
	if !ok {
		return events.StreamFill{}, events.ErrStreamNotFound
	}
	return v, nil
}

// S10: lag cannot show a single-node memory stream discarding its oldest
// messages, so the gauge exports each byte-limited stream's fill.
func TestQueueGaugeSetsStreamFill(t *testing.T) {
	resetGauges()
	metrics.StreamFillRatio.WithLabelValues("S3").Set(0.5)
	// S2 and S4 are unbounded in the topology (S4's broker reports a limit
	// anyway), S3 is one the broker cannot read, and S5's broker reports no
	// limit (no ratio, never a division by zero).
	top := events.Topology{Streams: []events.StreamSpec{
		{Name: "S1", MaxBytes: 1000}, {Name: "S2"}, {Name: "S3", MaxBytes: 10}, {Name: "S4"}, {Name: "S5", MaxBytes: 10},
	}}
	g := &extmetrics.QueueGauge{States: &fakeStates{}, Topology: top,
		Streams: fakeFill{"S1": {Bytes: 800, MaxBytes: 1000}, "S2": {Bytes: 5}, "S4": {Bytes: 5, MaxBytes: 100}, "S5": {Bytes: 5}}}
	g.Update(context.Background())
	assert.InDelta(t, 0.8, gaugeValue(t, metrics.StreamFillRatio.WithLabelValues("S1")), 1e-9)
	assert.Equal(t, 0, seriesCount(metrics.StreamFillRatio)-1,
		"a stream without MaxBytes has no ratio, and one the broker cannot read loses its stale value")
}

// mapStates answers ConsumerState from a table whose entries may carry an
// error.
type mapStates map[string]struct {
	Pending, AckPending    uint64
	Waiting, MaxAckPending int
	err                    error
}

func (m mapStates) ConsumerState(_ context.Context, _, durable string) (events.ConsumerState, error) {
	v := m[durable]
	if v.err != nil {
		return events.ConsumerState{}, v.err
	}
	return events.ConsumerState{Pending: v.Pending, AckPending: v.AckPending, Waiting: v.Waiting, MaxAckPending: v.MaxAckPending}, nil
}

// TestQueueGaugeExportsEachCounter: the gauge sets the lag and the four
// counters behind it, and drops all five for a consumer it cannot read.
func TestQueueGaugeExportsEachCounter(t *testing.T) {
	resetGauges()
	for _, g := range consumerGauges {
		g.WithLabelValues("S", "c2").Set(42) // a previous tick's stale series
	}
	top := events.Topology{Consumers: []events.ConsumerSpec{{Name: "c1", Stream: "S"}, {Name: "c2", Stream: "S"}}}
	states := mapStates{
		"c1": {Pending: 3, AckPending: 2, Waiting: 1, MaxAckPending: 16},
		"c2": {err: errors.New("unavailable")},
	}
	(&extmetrics.QueueGauge{States: states, Topology: top}).Update(context.Background())
	assert.Equal(t, 5.0, gaugeValue(t, metrics.WorkQueuePending.WithLabelValues("S", "c1")))
	assert.Equal(t, 3.0, gaugeValue(t, metrics.ConsumerPending.WithLabelValues("S", "c1")))
	assert.Equal(t, 2.0, gaugeValue(t, metrics.ConsumerAckPending.WithLabelValues("S", "c1")))
	assert.Equal(t, 1.0, gaugeValue(t, metrics.ConsumerWaiting.WithLabelValues("S", "c1")))
	assert.Equal(t, 16.0, gaugeValue(t, metrics.ConsumerMaxAckPending.WithLabelValues("S", "c1")))
	for _, g := range consumerGauges {
		assert.False(t, g.DeleteLabelValues("S", "c2"), "c2's series must already be gone")
	}
}

// TestQueueGaugeAndTheAPIShareOneRead: with one StateCache, a gauge update
// right after an API read asks the broker nothing more (split §9.4 as
// amended 2026-10-07).
func TestQueueGaugeAndTheAPIShareOneRead(t *testing.T) {
	resetGauges()
	now := observed
	states := &fakeStates{def: 4, at: observed}
	cache := &extmetrics.StateCache{States: states, Now: func() time.Time { return now }}
	s := extmetrics.Series{Stream: events.StreamWorkImport, Consumer: events.ConsumerImportScan}
	h := &extmetrics.Handler{Namespace: ns, Series: []extmetrics.Series{s}, Cache: cache, States: states, Now: func() time.Time { return now }}
	g := &extmetrics.QueueGauge{States: states, Cache: cache,
		Topology: events.Topology{Consumers: []events.ConsumerSpec{{Name: s.Consumer, Stream: s.Stream}}}}

	code, body, _ := do(h, http.MethodGet, listPath+"?labelSelector="+url.QueryEscape("consumer="+s.Consumer))
	require.Equal(t, http.StatusOK, code, body)
	g.Update(context.Background())
	assert.Equal(t, int64(1), states.calls.Load(), "the gauge reads the API's cached state")
	assert.Equal(t, 4.0, gaugeValue(t, metrics.WorkQueuePending.WithLabelValues(s.Stream, s.Consumer)))

	now = now.Add(6 * time.Second)
	g.Update(context.Background())
	do(h, http.MethodGet, listPath)
	assert.Equal(t, int64(2), states.calls.Load(), "and the API the gauge's, once the TTL has passed")
}

// fakeDispatch is the dispatch ledger as QueueGauge reads it.
type fakeDispatch struct {
	waiting    map[string]int
	unattended map[string]bool
}

func (f fakeDispatch) WaitingCount(durable string) int { return f.waiting[durable] }
func (f fakeDispatch) Unattended(durable string) bool  { return f.unattended[durable] }

// ADR-0019 §5.4: the gauge exports clustarr_dispatch_waiting and
// clustarr_dispatch_unattended for every Dispatched consumer, from the
// manager's ledger, and nothing for a consumer the manager does not
// dispatch to.
func TestQueueGaugeExportsDispatchWaitingAndUnattended(t *testing.T) {
	resetGauges()
	top := events.Topology{Consumers: []events.ConsumerSpec{
		{Name: "search", Stream: "W", Dispatched: true},
		{Name: "fetch", Stream: "W", Dispatched: true},
		{Name: "rss-matcher", Stream: "R"},
		{Name: "dlq-watch", Stream: events.StreamAdvisories, Dispatched: true},
	}}
	d := fakeDispatch{
		waiting:    map[string]int{"search": 3, "rss-matcher": 9, "dlq-watch": 2},
		unattended: map[string]bool{"fetch": true, "rss-matcher": true},
	}
	(&extmetrics.QueueGauge{States: &fakeStates{}, Topology: top, Dispatch: d}).Update(context.Background())

	assert.Equal(t, 3.0, gaugeValue(t, metrics.DispatchWaiting.WithLabelValues("search")))
	assert.Equal(t, 0.0, gaugeValue(t, metrics.DispatchUnattended.WithLabelValues("search")))
	assert.Equal(t, 0.0, gaugeValue(t, metrics.DispatchWaiting.WithLabelValues("fetch")))
	assert.Equal(t, 1.0, gaugeValue(t, metrics.DispatchUnattended.WithLabelValues("fetch")))
	assert.Equal(t, 2, seriesCount(metrics.DispatchWaiting), "only the two dispatched work consumers")
	assert.Equal(t, 2, seriesCount(metrics.DispatchUnattended), "only the two dispatched work consumers")

	resetGauges()
	(&extmetrics.QueueGauge{States: &fakeStates{}, Topology: top}).Update(context.Background())
	assert.Equal(t, 0, seriesCount(metrics.DispatchWaiting), "no ledger, no dispatch series")
}
