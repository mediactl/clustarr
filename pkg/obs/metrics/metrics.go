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

// Package metrics is Clustarr's Prometheus catalogue: one collector per
// series in the amendment's §A2.3 table, registered into
// controller-runtime's registry so a single /metrics endpoint serves both
// the controller-runtime built-ins and Clustarr's own domain metrics.
//
// Every series lives in [all], built once at package init time by the
// domain.go variable block through the newCounterVec, newGaugeVec and
// newHistogramVec helpers below. [Register] and the cardinality-guard tests
// in metrics_test.go both walk that one list, so a metric added to domain.go
// is automatically registered and automatically checked — there is nothing
// else to wire up.
//
// Naming follows the Prometheus conventions the amendment requires: a
// clustarr_ prefix, base units (bytes, seconds), "_total" on counters, and
// labels of bounded cardinality only. Never add a label carrying media
// title, file path, release name or any other dimension an attacker or a
// large library could grow without bound — see
// TestNoMetricIsLabelledByAnUnboundedDimension.
package metrics

import "github.com/prometheus/client_golang/prometheus"

// all holds every Clustarr collector. It is populated exclusively by the
// newCounterVec, newGaugeVec and newHistogramVec helpers so that Register
// and the guard tests always see the same set.
var all []prometheus.Collector

// Register adds every Clustarr collector in [all] to r. Call it once per
// process, typically with controller-runtime's metrics.Registry so the
// domain series share the manager's existing /metrics endpoint.
//
// Registering the same collector set with the same r a second time returns
// a *prometheus.AlreadyRegisteredError; callers that only ever call Register
// once at startup (the expected use) never see it.
func Register(r prometheus.Registerer) error {
	for _, c := range all {
		if err := r.Register(c); err != nil {
			return err
		}
	}
	return nil
}

// newCounterVec builds a CounterVec, appends it to [all] and returns it.
func newCounterVec(name, help string, labels ...string) *prometheus.CounterVec {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: name,
		Help: help,
	}, labels)
	all = append(all, c)
	return c
}

// newGaugeVec builds a GaugeVec, appends it to [all] and returns it.
func newGaugeVec(name, help string, labels ...string) *prometheus.GaugeVec {
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: name,
		Help: help,
	}, labels)
	all = append(all, g)
	return g
}

// newHistogramVec builds a HistogramVec, appends it to [all] and returns it.
// A nil buckets uses prometheus.DefBuckets.
func newHistogramVec(name, help string, buckets []float64, labels ...string) *prometheus.HistogramVec {
	opts := prometheus.HistogramOpts{
		Name:    name,
		Help:    help,
		Buckets: buckets,
	}
	h := prometheus.NewHistogramVec(opts, labels)
	all = append(all, h)
	return h
}
