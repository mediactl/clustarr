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

package worker

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// The worker's metrics, on the default registry the binary serves. No
// title, path or release label.
var (
	analyzedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "clustarr_segments_analyzed_total",
		Help: "Files analyzed, by result and by the source of their strongest segment.",
	}, []string{"result", "source"})
	stageSeconds = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "clustarr_segments_stage_seconds",
		Help:    "Time spent per analysis stage.",
		Buckets: prometheus.ExponentialBuckets(0.1, 2, 12),
	}, []string{"stage"})
	// DNNAvailable is 1 when the text detector loaded.
	DNNAvailable = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "clustarr_segments_dnn_available",
		Help: "1 when the text-detection model loaded on ONNX Runtime, else 0.",
	})
)
