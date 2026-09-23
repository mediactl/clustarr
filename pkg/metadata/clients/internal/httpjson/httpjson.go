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

// Package httpjson is the request half every in-house metadata client
// added for the eight MetadataProvider types that shipped without one
// (coverart, fanart, hardcover, metron, mangadex, anilist, kitsu,
// animelists) and for pkg/metadata/scenemap's TheXEM client shares: wait on
// the caller's limiter, send, read the body through pkg/metadata's cap, map
// the status onto pkg/metadata's sentinel errors, decode.
//
// Those conventions are CLAUDE.md's ("the caller owns rate limiting",
// "every HTTP response body is read through a cap", "provider errors
// expose sentinels"), and writing them once is what keeps nine clients from
// drifting apart on them.
package httpjson

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/version"
)

// DefaultUserAgent identifies Clustarr to a provider when the caller
// configured no contact string of its own. MangaDex requires a User-Agent
// on every request (docs/research/metadata.md §2.5) and Hardcover asks for
// one (hardcover-docs, api/Getting-Started.mdx: "it is recommended to
// include a user-agent header"), so none of these clients ever sends Go's
// default one.
var DefaultUserAgent = "Clustarr/" + version.Version + " (+https://github.com/mediactl/clustarr)"

// StatusError is a response whose HTTP status none of pkg/metadata's
// sentinels describes (a 5xx, a 400). It carries the status so a caller can
// still branch on it, and names the provider and the request path -- never
// the query string, which is where fanart.tv's api_key travels.
type StatusError struct {
	Provider string
	Code     int
	Path     string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("metadata: %s: unexpected status %d for %s", e.Provider, e.Code, e.Path)
}

// Client sends requests for one provider.
type Client struct {
	// Provider names the provider in errors and in RateLimitedError.
	Provider string
	// HTTP is the client requests go through; nil means
	// http.DefaultClient.
	HTTP *http.Client
	// Limiter is waited on before every request. Nil means no client-side
	// limiting at all: the caller owns rate limiting (CLAUDE.md), so this
	// package never invents a default.
	Limiter *rate.Limiter
	// UserAgent is sent on every request; empty means DefaultUserAgent.
	UserAgent string
	// MaxBody caps how many bytes of one response are read; zero means
	// metadata.MaxResponseBytes.
	MaxBody int64
	// Now is the clock Retry-After dates are measured against; nil means
	// time.Now.
	Now func() time.Time
}

// Request is one outbound call.
type Request struct {
	Method string
	URL    string
	Header http.Header
	Body   []byte
}

// Response is a complete, capped response.
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// Do waits on the limiter, sends req and reads the whole body through the
// cap. It returns a Response for every status, so a caller that treats
// some non-2xx status as an answer (a 304, TheXEM's failure envelope) can
// see it; Check maps the rest. An error here is a transport failure, a
// cancelled wait, or metadata.ErrResponseTooLarge.
func (c *Client) Do(ctx context.Context, req Request) (*Response, error) {
	if c.Limiter != nil {
		if err := c.Limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("metadata: %s: wait for rate limiter: %w", c.Provider, err)
		}
	}
	method := req.Method
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if req.Body != nil {
		body = bytes.NewReader(req.Body)
	}
	hreq, err := http.NewRequestWithContext(ctx, method, req.URL, body)
	if err != nil {
		return nil, fmt.Errorf("metadata: %s: build request for %s: %w", c.Provider, redact(req.URL), err)
	}
	for k, vs := range req.Header {
		for _, v := range vs {
			hreq.Header.Add(k, v)
		}
	}
	ua := c.UserAgent
	if ua == "" {
		ua = DefaultUserAgent
	}
	hreq.Header.Set("User-Agent", ua)

	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(hreq)
	if err != nil {
		// net/http's *url.Error prints the full URL, query string and all;
		// fanart.tv authenticates with ?api_key=, so the inner error is
		// re-wrapped under a redacted URL instead. errors.Is still reaches
		// context.Canceled, a net.Error or metadata.ErrResponseTooLarge
		// (a capped transport's) through the inner error.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return nil, fmt.Errorf("metadata: %s: %s %s: %w", c.Provider, method, redact(req.URL), err)
	}
	defer func() { _ = resp.Body.Close() }()

	limit := c.MaxBody
	if limit <= 0 {
		limit = metadata.MaxResponseBytes
	}
	b, err := metadata.ReadBody(resp.Body, limit)
	if err != nil {
		return nil, fmt.Errorf("metadata: %s: %s %s: %w", c.Provider, method, redact(req.URL), err)
	}
	return &Response{StatusCode: resp.StatusCode, Header: resp.Header, Body: b}, nil
}

// Check maps resp's status onto pkg/metadata's vocabulary: nil for 2xx,
// ErrAuth for 401/403, ErrNotFound for 404, a *RateLimitedError (which
// unwraps to ErrRateLimited) for 429, and a *StatusError for anything else.
func (c *Client) Check(resp *Response, rawURL string) error {
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("metadata: %s: status %d for %s: %w", c.Provider, resp.StatusCode, redact(rawURL), metadata.ErrAuth)
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("metadata: %s: %s: %w", c.Provider, redact(rawURL), metadata.ErrNotFound)
	case resp.StatusCode == http.StatusTooManyRequests:
		now := time.Now
		if c.Now != nil {
			now = c.Now
		}
		return &metadata.RateLimitedError{Provider: c.Provider, RetryAfter: ParseRetryAfter(resp.Header.Get("Retry-After"), now())}
	default:
		return &StatusError{Provider: c.Provider, Code: resp.StatusCode, Path: redact(rawURL)}
	}
}

// Decode unmarshals body into out, wrapping a failure -- an empty body
// included -- in metadata.ErrDecode.
func (c *Client) Decode(body []byte, out any) error {
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("metadata: %s: %w: %w", c.Provider, metadata.ErrDecode, err)
	}
	return nil
}

// GetJSON is Do, Check and Decode for a GET.
func (c *Client) GetJSON(ctx context.Context, rawURL string, header http.Header, out any) error {
	resp, err := c.Do(ctx, Request{Method: http.MethodGet, URL: rawURL, Header: header})
	if err != nil {
		return err
	}
	if err := c.Check(resp, rawURL); err != nil {
		return err
	}
	return c.Decode(resp.Body, out)
}

// PostJSON is Do, Check and Decode for a POST whose body is in, marshalled
// to JSON with a JSON Content-Type.
func (c *Client) PostJSON(ctx context.Context, rawURL string, header http.Header, in, out any) error {
	body, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("metadata: %s: encode request: %w", c.Provider, err)
	}
	h := header.Clone()
	if h == nil {
		h = http.Header{}
	}
	h.Set("Content-Type", "application/json")
	if h.Get("Accept") == "" {
		h.Set("Accept", "application/json")
	}
	resp, err := c.Do(ctx, Request{Method: http.MethodPost, URL: rawURL, Header: h, Body: body})
	if err != nil {
		return err
	}
	if err := c.Check(resp, rawURL); err != nil {
		return err
	}
	return c.Decode(resp.Body, out)
}

// ParseRetryAfter reads a Retry-After header in either of the forms RFC
// 9110 §10.2.3 allows: delay-seconds or an HTTP-date, measured against
// now. It returns zero when the header is absent, malformed or already in
// the past, which pkg/metadata's callers read as "no hint".
func ParseRetryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil {
		if secs < 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// redact renders rawURL for an error message without its query string or
// userinfo: every secret these providers take in the URL (fanart.tv's
// api_key and client_key) travels in the query.
func redact(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "<unparseable url>"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// Redact is redact, exported for a client that builds its own error
// message around a URL.
func Redact(rawURL string) string { return redact(rawURL) }
