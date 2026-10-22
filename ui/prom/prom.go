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

// Package prom reads range queries from a Prometheus-compatible HTTP API
// (Prometheus, or Mimir under its /prometheus prefix) for the dashboard's
// charts: clustarr's own metrics and cluster-plex's Plex metrics, as the
// cluster's scraper stored them. It only reads, and the browser never talks
// to the metrics store: the ui queries it and renders the answer.
package prom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MaxResponseBytes caps one answer: a dashboard query is a handful of
// series at a few hundred points each, well under a megabyte.
const MaxResponseBytes = 4 << 20

// CacheTTL is how long an answer is reused for the same query and range,
// so every open dashboard refreshing at once costs the store one query.
const CacheTTL = 30 * time.Second

// Timeout bounds one query.
const Timeout = 10 * time.Second

var (
	// ErrNotConfigured is every query's answer when no URL was given.
	ErrNotConfigured = errors.New("prom: no metrics URL configured")
	// ErrResponseTooLarge is an answer over MaxResponseBytes.
	ErrResponseTooLarge = errors.New("prom: response too large")
)

// Point is one sample.
type Point struct {
	T time.Time
	V float64
}

// Series is one labelled series of a range query's matrix.
type Series struct {
	Labels map[string]string
	Points []Point
}

// Querier runs range queries; *Client is the one implementation.
type Querier interface {
	Configured() bool
	QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]Series, error)
}

// Client is a Querier over one base URL.
type Client struct {
	base string
	http *http.Client
	now  func() time.Time

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	at     time.Time
	series []Series
}

// New returns a Client for base, the API's root without /api/v1 (e.g.
// http://mimir.observability.svc:8080/prometheus). An empty base is a
// client that answers every query with ErrNotConfigured.
func New(base string) *Client {
	return &Client{
		base:  strings.TrimRight(base, "/"),
		http:  &http.Client{Timeout: Timeout},
		now:   time.Now,
		cache: map[string]cached{},
	}
}

// Configured reports whether the client has a URL to query.
func (c *Client) Configured() bool { return c.base != "" }

// QueryRange runs query over [start, end] at step.
func (c *Client) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) ([]Series, error) {
	if !c.Configured() {
		return nil, ErrNotConfigured
	}
	v := url.Values{}
	v.Set("query", query)
	v.Set("start", strconv.FormatInt(start.Unix(), 10))
	v.Set("end", strconv.FormatInt(end.Unix(), 10))
	v.Set("step", strconv.FormatInt(int64(step/time.Second), 10))
	key := v.Encode()

	now := c.now()
	c.mu.Lock()
	if hit, ok := c.cache[key]; ok && now.Sub(hit.at) < CacheTTL {
		c.mu.Unlock()
		return hit.series, nil
	}
	c.mu.Unlock()

	series, err := c.fetch(ctx, key)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	for k, e := range c.cache {
		if now.Sub(e.at) >= CacheTTL {
			delete(c.cache, k)
		}
	}
	c.cache[key] = cached{at: now, series: series}
	c.mu.Unlock()
	return series, nil
}

// answer is the API's envelope around a matrix.
type answer struct {
	Status    string `json:"status"`
	ErrorType string `json:"errorType"`
	Error     string `json:"error"`
	Data      struct {
		ResultType string `json:"resultType"`
		Result     []struct {
			Metric map[string]string `json:"metric"`
			Values [][2]any          `json:"values"`
		} `json:"result"`
	} `json:"data"`
}

func (c *Client) fetch(ctx context.Context, query string) ([]Series, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/v1/query_range?"+query, nil)
	if err != nil {
		return nil, fmt.Errorf("prom: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("prom: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("prom: read answer: %w", err)
	}
	if len(body) > MaxResponseBytes {
		return nil, ErrResponseTooLarge
	}
	var a answer
	if err := json.Unmarshal(body, &a); err != nil {
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("prom: %s", resp.Status)
		}
		return nil, fmt.Errorf("prom: decode answer: %w", err)
	}
	if a.Status != "success" {
		return nil, fmt.Errorf("prom: %s (%s): %s", resp.Status, a.ErrorType, a.Error)
	}
	if a.Data.ResultType != "matrix" {
		return nil, fmt.Errorf("prom: answer is a %q, not a matrix", a.Data.ResultType)
	}
	out := make([]Series, 0, len(a.Data.Result))
	for _, r := range a.Data.Result {
		s := Series{Labels: r.Metric, Points: make([]Point, 0, len(r.Values))}
		for _, sample := range r.Values {
			ts, ok := sample[0].(float64)
			text, isText := sample[1].(string)
			if !ok || !isText {
				continue
			}
			v, err := strconv.ParseFloat(text, 64)
			if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			s.Points = append(s.Points, Point{T: time.Unix(0, int64(ts*float64(time.Second))), V: v})
		}
		out = append(out, s)
	}
	return out, nil
}
