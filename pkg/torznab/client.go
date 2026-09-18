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

package torznab

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// defaultTimeout matches Indexer.spec.timeout's default in
// api/index/v1alpha1.
const defaultTimeout = 30 * time.Second

// defaultRequestInterval matches Prowlarr's RateLimit default
// (docs/research/indexers.md §6).
const defaultRequestInterval = 2 * time.Second

// ClientOption configures a Client built by NewClient.
type ClientOption func(*Client)

// WithTimeout overrides the HTTP client's per-request timeout.
func WithTimeout(d time.Duration) ClientOption {
	return func(c *Client) { c.hc.Timeout = d }
}

// WithRateLimit overrides the per-host rate limit.
func WithRateLimit(r rate.Limit, burst int) ClientOption {
	return func(c *Client) { c.limiter = rate.NewLimiter(r, burst) }
}

// WithProxy routes every request through dial. It is a no-op -- fails
// closed rather than panicking -- when the client's http.Client.Transport
// (set by an earlier WithHTTPClient) is not an *http.Transport, since only
// that concrete type exposes DialContext. NewClient's own default
// constructs an *http.Transport explicitly, so this is never reached from
// the default configuration.
func WithProxy(dial func(ctx context.Context, network, addr string) (net.Conn, error)) ClientOption {
	return func(c *Client) {
		t, ok := c.hc.Transport.(*http.Transport)
		if !ok {
			return
		}
		clone := t.Clone()
		clone.DialContext = dial
		c.hc.Transport = clone
	}
}

// WithHTTPClient replaces the underlying *http.Client outright.
func WithHTTPClient(hc *http.Client) ClientOption {
	return func(c *Client) { c.hc = hc }
}

// Client is a Torznab/Newznab HTTP client for one indexer host: context
// aware, rate limited per host (2s/request default, matching Prowlarr's
// RateLimit default from §6), API-key authenticated, with an optional
// caller-supplied dial function for proxying.
type Client struct {
	baseURL *url.URL
	apikey  string
	hc      *http.Client
	limiter *rate.Limiter
}

// NewClient builds a Client for baseURL, authenticated with apikey.
func NewClient(baseURL, apikey string, opts ...ClientOption) (*Client, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("torznab: parse base URL %q: %w", baseURL, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("torznab: base URL %q must be absolute", baseURL)
	}

	c := &Client{
		baseURL: u,
		apikey:  apikey,
		hc: &http.Client{
			Timeout:   defaultTimeout,
			Transport: &http.Transport{},
		},
		limiter: rate.NewLimiter(rate.Every(defaultRequestInterval), 1),
	}
	for _, opt := range opts {
		opt(c)
	}
	return c, nil
}

// do rate-limits, traces and issues one GET request against c.baseURL with
// values as its query string. The caller must close the returned
// response's Body.
func (c *Client) do(ctx context.Context, values url.Values) (*http.Response, error) {
	ctx, span := tracing.Start(ctx, "torznab.request")
	defer span.End()

	logging.FromContext(ctx).Debug("torznab: request", "url", c.baseURL.String(), "t", values.Get("t"))

	// Wait returns ctx.Err() immediately when ctx is already cancelled or
	// its deadline has passed, without ever issuing the request -- this is
	// what makes context cancellation propagate out of Search/Caps without
	// a separate manual check.
	if err := c.limiter.Wait(ctx); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	u := *c.baseURL
	u.RawQuery = values.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	return resp, nil
}

// Caps fetches and parses t=caps.
func (c *Client) Caps(ctx context.Context) (Caps, error) {
	v := url.Values{"t": {"caps"}}
	if c.apikey != "" {
		v.Set("apikey", c.apikey)
	}

	resp, err := c.do(ctx, v)
	if err != nil {
		return Caps{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := readOrError(resp)
	if err != nil {
		return Caps{}, err
	}
	return ParseCaps(bytes.NewReader(body))
}

// Search runs q and parses the resulting item feed.
func (c *Client) Search(ctx context.Context, q Query) ([]Release, error) {
	resp, err := c.do(ctx, q.Values(c.apikey))
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := readOrError(resp)
	if err != nil {
		return nil, err
	}
	return ParseResults(bytes.NewReader(body))
}

// readOrError reads resp's body fully and, when resp represents a
// Torznab/Newznab failure, returns that failure as *Error instead of the
// body. A failure is either HTTP-level (410 disabled, 429 rate limited --
// per §4.5 these never carry an XML body at all) or an XML <error> element
// in the body itself, since Newznab conventionally answers every error with
// plain HTTP 200 (§4.5). Any other non-2xx status with no <error> body
// becomes a generic error.
func readOrError(resp *http.Response) ([]byte, error) {
	if err := httpStatusError(resp); err != nil {
		return nil, err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if e, perr := ParseError(bytes.NewReader(body)); perr == nil && e != nil {
		e.HTTPStatus = resp.StatusCode
		return nil, e
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("torznab: unexpected status %d", resp.StatusCode)
	}

	return body, nil
}

// httpStatusError maps the two HTTP-level failures that never carry an XML
// body (Prowlarr's convention, §4.5/§6) to an *Error. Every other status,
// including other non-2xx statuses, returns nil so the caller inspects the
// body for a Newznab <error> element instead.
func httpStatusError(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusGone:
		return &Error{HTTPStatus: http.StatusGone, Description: "indexer disabled"}
	case http.StatusTooManyRequests:
		return &Error{HTTPStatus: http.StatusTooManyRequests, Description: "rate limited", RetryAfter: retryAfter(resp)}
	default:
		return nil
	}
}

// retryAfter parses the Retry-After header as a whole number of seconds,
// returning 0 when it is absent or not a valid integer (Torznab/Newznab
// never send the HTTP-date form of Retry-After).
func retryAfter(resp *http.Response) time.Duration {
	s := resp.Header.Get("Retry-After")
	if s == "" {
		return 0
	}
	secs, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return time.Duration(secs) * time.Second
}
