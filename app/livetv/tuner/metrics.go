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

package tuner

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/apimachinery/pkg/types"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// The Live TV metrics (spec §5.5), labelled by provider ("ns/name") and a
// bounded reason or source; never by channel, since a channel name is a
// title.
var (
	tunersInUse = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "clustarr_livetv_tuners_in_use", Help: "Tuners streaming now, per provider.",
	}, []string{"provider"})
	viewers = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Name: "clustarr_livetv_viewers", Help: "Viewers attached now, per provider.",
	}, []string{"provider"})
	streamBytes = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "clustarr_livetv_stream_bytes_total", Help: "Bytes relayed from upstreams, per provider.",
	}, []string{"provider"})
	upstreamErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "clustarr_livetv_upstream_errors_total", Help: "Upstream failures, per provider and reason.",
	}, []string{"provider", "reason"})
	fetchDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Name: "clustarr_livetv_fetch_duration_seconds", Help: "Playlist and guide fetch time, per provider and source.",
		Buckets: prometheus.ExponentialBuckets(0.1, 2, 12),
	}, []string{"provider", "source"})

	metricsOnce sync.Once
)

func registerMetrics() {
	metricsOnce.Do(func() {
		ctrlmetrics.Registry.MustRegister(tunersInUse, viewers, streamBytes, upstreamErrors, fetchDuration)
	})
}

// forgetMetrics drops a removed provider's series.
func forgetMetrics(key types.NamespacedName) {
	l := prometheus.Labels{"provider": label(key)}
	tunersInUse.DeletePartialMatch(l)
	viewers.DeletePartialMatch(l)
	streamBytes.DeletePartialMatch(l)
	upstreamErrors.DeletePartialMatch(l)
	fetchDuration.DeletePartialMatch(l)
}
