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

// Package ffmetrics exports pkg/ffruntime's abandoned-call and panic counts. It is
// separate so pkg/ffruntime stays standard library plus ffgo.
package ffmetrics

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/mediactl/clustarr/pkg/ffruntime"
)

// Register adds clustarr_ffgo_abandoned_calls and clustarr_ffgo_panics_total
// to r. A registry that already has them is not an error (an envtest
// registers several domains in one process).
func Register(r prometheus.Registerer) error {
	for _, c := range []prometheus.Collector{
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "clustarr_ffgo_abandoned_calls",
			Help: "In-process FFmpeg calls that outlived their context and are still running; at ffruntime.MaxAbandoned the process reports itself wedged.",
		}, func() float64 { return float64(ffruntime.Abandoned()) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "clustarr_ffgo_panics_total",
			Help: "In-process FFmpeg calls that panicked; ffruntime.Do recovered each and returned it as the call's error.",
		}, func() float64 { return float64(ffruntime.Panics()) }),
	} {
		err := r.Register(c)
		var are prometheus.AlreadyRegisteredError
		if err != nil && !errors.As(err, &are) {
			return err
		}
	}
	return nil
}
