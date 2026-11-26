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
	"crypto/sha256"
	"encoding/hex"
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

// OpenAPIPath is where the aggregator asks an APIService for its OpenAPI
// v2 document.
const OpenAPIPath = "/openapi/v2"

// openAPI is the document served at OpenAPIPath, a Swagger 2.0 document
// with no paths, and openAPIETag its entity tag. The API has no schema
// worth publishing; the document is there because the aggregator asks.
// Its downloader skips a 404, but the cache above it (kube-aggregator
// v0.36, openapi/aggregator/downloader.go, cacheableDownloader.get) makes
// one "resource not found", wrapped so that the controller's own test for
// that error misses it: every kube-apiserver logged
//
//	loading OpenAPI spec for "v1beta1.external.metrics.k8s.io" failed with:
//	failed to download v1beta1.external.metrics.k8s.io: resource not found
//
// four times every five minutes (talos, v1.37.0, 2026-11-26). A 200 is
// cached and asked for again each minute with If-None-Match, which the tag
// answers 304. /openapi/v3 stays a 404: the v3 aggregator then reads the
// APIService as a legacy one and converts this document.
const openAPI = `{"swagger":"2.0","info":{"title":"clustarr external metrics","version":"v1beta1"},"paths":{}}`

var openAPIETag = func() string {
	sum := sha256.Sum256([]byte(openAPI))
	return `"` + hex.EncodeToString(sum[:]) + `"`
}()

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
	// Cache is the read path to the broker, shared with QueueGauge (split
	// §9.4 as amended 2026-10-07). Nil: one is built from States, Now, TTL
	// and Timeout on first use.
	Cache   *StateCache
	States  ConsumerStater
	Now     func() time.Time
	TTL     time.Duration // 0 means 5 s; for the cache built when Cache is nil
	Timeout time.Duration // 0 means 5 s; likewise

	once  sync.Once
	built *StateCache
}

// ServeHTTP routes GET only; everything outside the two routes and the
// OpenAPI document is a 404, which the aggregator reads as "no aggregated
// discovery".
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
	if path == OpenAPIPath {
		w.Header().Set("Etag", openAPIETag)
		if r.Header.Get("If-None-Match") == openAPIETag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(openAPI))
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
	return h.cache().Get(ctx, s)
}

// cache is h.Cache, or the one built from h's own fields when it is nil.
func (h *Handler) cache() *StateCache {
	if h.Cache != nil {
		return h.Cache
	}
	h.once.Do(func() {
		h.built = &StateCache{States: h.States, TTL: h.TTL, Timeout: h.Timeout, Now: h.Now}
	})
	return h.built
}

func (h *Handler) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

func clamp(v uint64) int64 {
	if v > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(v)
}
