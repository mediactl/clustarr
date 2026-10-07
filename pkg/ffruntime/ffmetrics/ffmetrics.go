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

// Package ffmetrics exports pkg/ffruntime's abandoned-call count. It is
// separate so pkg/ffruntime stays standard library plus ffgo.
package ffmetrics

import (
	"errors"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/mediactl/clustarr/pkg/ffruntime"
)

// Register adds clustarr_ffgo_abandoned_calls to r. A registry that already
// has it is not an error (an envtest registers several domains in one process).
func Register(r prometheus.Registerer) error {
	err := r.Register(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "clustarr_ffgo_abandoned_calls",
		Help: "In-process FFmpeg calls that outlived their context and are still running; at ffruntime.MaxAbandoned the process reports itself wedged.",
	}, func() float64 { return float64(ffruntime.Abandoned()) }))
	var are prometheus.AlreadyRegisteredError
	if errors.As(err, &are) {
		return nil
	}
	return err
}
