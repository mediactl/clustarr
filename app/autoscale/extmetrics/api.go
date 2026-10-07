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
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/mediactl/clustarr/pkg/agentdomain"
	"github.com/mediactl/clustarr/pkg/events"
)

// The served group-version.
const (
	GroupVersion = "external.metrics.k8s.io/v1beta1"
	APIPrefix    = "/apis/" + GroupVersion
)

const (
	defaultTTL     = 5 * time.Second
	defaultTimeout = 5 * time.Second
)

// valueList and value carry k8s.io/metrics external_metrics/v1beta1's
// JSON tags (types.go:28-59), defined here so the manager does not link
// k8s.io/metrics.
type valueList struct {
	Kind       string   `json:"kind"`
	APIVersion string   `json:"apiVersion"`
	Metadata   struct{} `json:"metadata"`
	Items      []value  `json:"items"`
}

type value struct {
	MetricName   string            `json:"metricName"`
	MetricLabels map[string]string `json:"metricLabels"`
	Timestamp    metav1.Time       `json:"timestamp"`
	Window       *int64            `json:"window,omitempty"`
	Value        resource.Quantity `json:"value"`
}

var discovery = metav1.APIResourceList{
	TypeMeta:     metav1.TypeMeta{Kind: "APIResourceList", APIVersion: "v1"},
	GroupVersion: GroupVersion,
	APIResources: []metav1.APIResource{{
		Name: agentdomain.MetricConsumerLag, Namespaced: true,
		Kind: "ExternalMetricValueList", Verbs: metav1.Verbs{"get"},
	}},
}

// Handler serves external.metrics.k8s.io/v1beta1: discovery and
// clustarr_consumer_lag in Namespace, for Series only. Wrap it in
// FrontProxyAuth.
type Handler struct {
	Namespace string
	Series    []Series
	States    ConsumerStater
	Now       func() time.Time
	TTL       time.Duration // 0 means 5 s
	Timeout   time.Duration // 0 means 5 s

	mu    sync.Mutex
	cache map[Series]cached
}

type cached struct {
	state events.ConsumerState
	at    time.Time
}

// ServeHTTP routes GET only; everything outside the two routes is a 404,
// which the aggregator reads as "no aggregated discovery, no OpenAPI".
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeStatus(w, http.StatusMethodNotAllowed, metav1.StatusReasonMethodNotAllowed, "only GET is served")
		return
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	if path == APIPrefix {
		writeJSON(w, http.StatusOK, discovery)
		return
	}
	rest, ok := strings.CutPrefix(path, APIPrefix+"/namespaces/")
	namespace, metric, ok2 := strings.Cut(rest, "/")
	if !ok || !ok2 || strings.Contains(metric, "/") || metric != agentdomain.MetricConsumerLag || namespace != h.Namespace {
		writeStatus(w, http.StatusNotFound, metav1.StatusReasonNotFound, "not found")
		return
	}
	sel, err := labels.Parse(r.URL.Query().Get("labelSelector"))
	if err != nil {
		writeStatus(w, http.StatusBadRequest, metav1.StatusReasonBadRequest, fmt.Sprintf("labelSelector: %v", err))
		return
	}
	list := valueList{Kind: "ExternalMetricValueList", APIVersion: GroupVersion, Items: []value{}}
	for _, s := range h.Series {
		if !sel.Matches(labels.Set{"stream": s.Stream, "consumer": s.Consumer}) {
			continue
		}
		st, err := h.state(r.Context(), s)
		if err != nil {
			// 503 holds the HPA's scale rather than act on partial data.
			writeStatus(w, http.StatusServiceUnavailable, metav1.StatusReasonServiceUnavailable,
				fmt.Sprintf("the backlog of %s on %s could not be read", s.Consumer, s.Stream))
			return
		}
		at := st.ObservedAt
		if at.IsZero() {
			at = h.now()
		}
		list.Items = append(list.Items, value{
			MetricName:   agentdomain.MetricConsumerLag,
			MetricLabels: map[string]string{"stream": s.Stream, "consumer": s.Consumer},
			Timestamp:    metav1.NewTime(at.UTC()),
			Value:        *resource.NewQuantity(clamp(st.Lag()), resource.DecimalSI),
		})
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handler) state(ctx context.Context, s Series) (events.ConsumerState, error) {
	now := h.now()
	h.mu.Lock()
	if c, ok := h.cache[s]; ok && now.Sub(c.at) < h.ttl() {
		h.mu.Unlock()
		return c.state, nil
	}
	h.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, h.timeout())
	defer cancel()
	st, err := h.States.ConsumerState(ctx, s.Stream, s.Consumer)
	if err != nil {
		return events.ConsumerState{}, err
	}
	h.mu.Lock()
	if h.cache == nil {
		h.cache = map[Series]cached{}
	}
	h.cache[s] = cached{state: st, at: now}
	h.mu.Unlock()
	return st, nil
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func (h *Handler) ttl() time.Duration {
	if h.TTL > 0 {
		return h.TTL
	}
	return defaultTTL
}

func (h *Handler) timeout() time.Duration {
	if h.Timeout > 0 {
		return h.Timeout
	}
	return defaultTimeout
}

func clamp(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}
