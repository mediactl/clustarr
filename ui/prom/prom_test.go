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

package prom

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// matrix is a query_range answer as Mimir sends one.
const matrix = `{"status":"success","data":{"resultType":"matrix","result":[
 {"metric":{"tier":"intel"},"values":[[1792630800,"2"],[1792631100,"3"]]},
 {"metric":{"tier":"cpu"},"values":[[1792630800,"0"],[1792631100,"NaN"]]}]}}`

func server(t *testing.T, h http.HandlerFunc) *httptest.Server {
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func TestQueryRangeReadsAMatrix(t *testing.T) {
	var got *http.Request
	srv := server(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		_, _ = io.WriteString(w, matrix)
	})
	start, end := time.Unix(1792630800, 0), time.Unix(1792631100, 0)

	series, err := New(srv.URL+"/prometheus").QueryRange(context.Background(), "sum by (tier) (x)", start, end, 5*time.Minute)

	require.NoError(t, err)
	assert.Equal(t, "/prometheus/api/v1/query_range", got.URL.Path, "the base URL's path is kept")
	q := got.URL.Query()
	assert.Equal(t, "sum by (tier) (x)", q.Get("query"))
	assert.Equal(t, "1792630800", q.Get("start"))
	assert.Equal(t, "1792631100", q.Get("end"))
	assert.Equal(t, "300", q.Get("step"))
	require.Len(t, series, 2)
	assert.Equal(t, map[string]string{"tier": "intel"}, series[0].Labels)
	assert.Equal(t, []Point{{T: start, V: 2}, {T: end, V: 3}}, series[0].Points)
	assert.Equal(t, []Point{{T: start, V: 0}}, series[1].Points, "a NaN sample is no point")
}

func TestAnErrorAnswerIsAnError(t *testing.T) {
	srv := server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"status":"error","errorType":"bad_data","error":"parse error at char 4"}`)
	})

	_, err := New(srv.URL).QueryRange(context.Background(), "sum(", time.Unix(0, 0), time.Unix(60, 0), time.Minute)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse error at char 4")
}

func TestANonJSONFailureNamesItsStatus(t *testing.T) {
	srv := server(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "upstream connect error", http.StatusBadGateway)
	})

	_, err := New(srv.URL).QueryRange(context.Background(), "up", time.Unix(0, 0), time.Unix(60, 0), time.Minute)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "502")
}

func TestAnOversizedAnswerIsRefused(t *testing.T) {
	srv := server(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"status":"success","data":{"resultType":"matrix","result":[`+strings.Repeat(" ", MaxResponseBytes)+`]}}`)
	})

	_, err := New(srv.URL).QueryRange(context.Background(), "up", time.Unix(0, 0), time.Unix(60, 0), time.Minute)

	assert.ErrorIs(t, err, ErrResponseTooLarge)
}

func TestAnUnconfiguredClientSaysSo(t *testing.T) {
	c := New("")

	_, err := c.QueryRange(context.Background(), "up", time.Unix(0, 0), time.Unix(60, 0), time.Minute)

	assert.ErrorIs(t, err, ErrNotConfigured)
	assert.False(t, c.Configured())
	assert.True(t, New("http://mimir:8080/prometheus").Configured())
}

func TestAnAnswerIsReusedUntilItsTTL(t *testing.T) {
	var hits atomic.Int32
	srv := server(t, func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, matrix)
	})
	now := time.Unix(1792631100, 0)
	c := New(srv.URL)
	c.now = func() time.Time { return now }
	ask := func() {
		_, err := c.QueryRange(context.Background(), "up", time.Unix(0, 0), time.Unix(60, 0), time.Minute)
		require.NoError(t, err)
	}

	ask()
	ask()
	assert.Equal(t, int32(1), hits.Load(), "one viewer's refresh and the next viewer's load share one query")
	now = now.Add(CacheTTL)
	ask()
	assert.Equal(t, int32(2), hits.Load())
}
