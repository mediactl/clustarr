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
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/app/autoscale/extmetrics"
	"github.com/mediactl/clustarr/pkg/agentdomain"
	"github.com/mediactl/clustarr/pkg/events"
)

var observed = time.Date(2026, 10, 6, 20, 0, 0, 0, time.UTC)

func newHandler(t *testing.T, states *fakeStates, now *time.Time) *extmetrics.Handler {
	t.Helper()
	series, err := extmetrics.ServableSeries(agentdomain.Domains(), events.Default())
	require.NoError(t, err)
	return &extmetrics.Handler{Namespace: ns, Series: series, States: states, Now: func() time.Time { return *now }}
}

func do(h http.Handler, method, path string) (int, string, string) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec.Code, rec.Body.String(), rec.Header().Get("Content-Type")
}

const listPath = "/apis/external.metrics.k8s.io/v1beta1/namespaces/" + ns + "/clustarr_consumer_lag"

func TestDiscovery(t *testing.T) {
	now := observed
	code, body, ct := do(newHandler(t, &fakeStates{}, &now), http.MethodGet, "/apis/external.metrics.k8s.io/v1beta1")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, "application/json", ct)
	assert.JSONEq(t, `{"kind":"APIResourceList","apiVersion":"v1","groupVersion":"external.metrics.k8s.io/v1beta1",
			"resources":[{"name":"clustarr_consumer_lag","singularName":"","namespaced":true,"kind":"ExternalMetricValueList","verbs":["get"]}]}`, body)
}

// The list golden of §9.4, and its field names: k8s.io/metrics@v0.36.3
// pkg/apis/external_metrics/v1beta1/types.go:44-59 tags ExternalMetricValue
// metricName, metricLabels, timestamp, window (omitempty) and value.
func TestListGolden(t *testing.T) {
	now := observed
	states := &fakeStates{lag: map[string]uint64{events.ConsumerCaptionFetchHigh: 37}, at: observed}
	code, body, ct := do(newHandler(t, states, &now), http.MethodGet,
		listPath+"?labelSelector="+url.QueryEscape("consumer="+events.ConsumerCaptionFetchHigh))
	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, "application/json", ct)
	assert.JSONEq(t, `{"kind":"ExternalMetricValueList","apiVersion":"external.metrics.k8s.io/v1beta1","metadata":{},
			"items":[{"metricName":"clustarr_consumer_lag","metricLabels":{"stream":"CLUSTARR_WORK_CAPTION","consumer":"caption-fetch-high"},
			"timestamp":"2026-10-06T20:00:00Z","value":"37"}]}`, body)

	var raw struct{ Items []map[string]json.RawMessage }
	require.NoError(t, json.Unmarshal([]byte(body), &raw))
	var keys []string
	for k := range raw.Items[0] {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	assert.Equal(t, []string{"metricLabels", "metricName", "timestamp", "value"}, keys)
}

func TestSelection(t *testing.T) {
	var servable int
	for _, d := range agentdomain.Domains() {
		if d.Autoscaled {
			servable += len(d.Consumers)
		}
	}
	for _, tc := range []struct {
		selector string
		want     []string
	}{
		{"", nil}, // every servable series
		{"consumer=caption-fetch-high", []string{"caption-fetch-high"}},
		// the metadata domain's catalog-metadata and catalog-artwork-fetch
		// share the stream and are not served
		{"stream=CLUSTARR_WORK_CATALOG", []string{"catalog-artwork-render", "catalog-search-high", "catalog-search-normal"}},
		{"consumer in (import-scan,import-list)", []string{"import-list", "import-scan"}},
		{"consumer=catalog-metadata", []string{}}, // a fixed domain's consumer is not served
	} {
		t.Run(tc.selector, func(t *testing.T) {
			now := observed
			code, body, _ := do(newHandler(t, &fakeStates{def: 1, at: observed}, &now), http.MethodGet,
				listPath+"?labelSelector="+url.QueryEscape(tc.selector))
			require.Equal(t, http.StatusOK, code, body)
			var list struct {
				Items []struct {
					MetricLabels map[string]string `json:"metricLabels"`
				} `json:"items"`
			}
			require.NoError(t, json.Unmarshal([]byte(body), &list))
			if tc.want == nil {
				assert.Len(t, list.Items, servable)
				return
			}
			got := []string{}
			for _, it := range list.Items {
				got = append(got, it.MetricLabels["consumer"])
			}
			sort.Strings(got)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestErrors(t *testing.T) {
	failing := &fakeStates{def: 1, at: observed, err: map[string]error{events.ConsumerImportScan: errors.New("nats: timeout")}}
	for _, tc := range []struct {
		name, method, path string
		states             *fakeStates
		code               int
	}{
		{"another metric", http.MethodGet, "/apis/external.metrics.k8s.io/v1beta1/namespaces/" + ns + "/queue_depth", &fakeStates{}, 404},
		{"another namespace", http.MethodGet, "/apis/external.metrics.k8s.io/v1beta1/namespaces/kube-system/clustarr_consumer_lag", &fakeStates{}, 404},
		{"an unparseable selector", http.MethodGet, listPath + "?labelSelector=" + url.QueryEscape("consumer in ("), &fakeStates{}, 400},
		{"POST", http.MethodPost, listPath, &fakeStates{}, 405},
		{"/apis", http.MethodGet, "/apis", &fakeStates{}, 404},
		{"the group without a version", http.MethodGet, "/apis/external.metrics.k8s.io", &fakeStates{}, 404},
		{"/openapi/v2", http.MethodGet, "/openapi/v2", &fakeStates{}, 404},
		{"/openapi/v3", http.MethodGet, "/openapi/v3", &fakeStates{}, 404},
		{"a ConsumerState error holds the HPA", http.MethodGet, listPath, failing, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := observed
			code, body, ct := do(newHandler(t, tc.states, &now), tc.method, tc.path)
			assert.Equal(t, tc.code, code, body)
			assert.Equal(t, "application/json", ct)
			assert.Contains(t, body, `"kind":"Status"`)
			assert.NotContains(t, body, `"items"`, "no error response carries metric data")
		})
	}
}

// Each series is read once per 5 s: the HPA controller asks every 15 s per
// metric, and a burst of discovery and list calls must not become a burst
// of CONSUMER.INFO requests.
func TestStatesAreCachedForFiveSeconds(t *testing.T) {
	now := observed
	states := &fakeStates{def: 1, at: observed}
	h := newHandler(t, states, &now)
	path := listPath + "?labelSelector=" + url.QueryEscape("consumer="+events.ConsumerImportScan)
	code, _, _ := do(h, http.MethodGet, path)
	require.Equal(t, http.StatusOK, code)
	do(h, http.MethodGet, path)
	assert.Equal(t, int64(1), states.calls.Load())
	now = now.Add(6 * time.Second)
	do(h, http.MethodGet, path)
	assert.Equal(t, int64(2), states.calls.Load())
}

func TestServableSeriesRefusesAnUndeclaredConsumer(t *testing.T) {
	_, err := extmetrics.ServableSeries([]agentdomain.Domain{{Name: "x", Autoscaled: true, Consumers: []string{"nope"}}}, events.Default())
	assert.Error(t, err)
}
