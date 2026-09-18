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

package cardigann

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Engine executes a Definition against the network. The zero value is
// usable with http.DefaultClient; the indexer controller sets HTTP
// (timeouts, cookie jar per Indexer) and, when an IndexerProxy selects
// this Indexer, Proxy (a RoundTripper that speaks SOCKS5 or FlareSolverr —
// built and owned entirely outside this package, which never dials a
// proxy itself). Now, when set, replaces time.Now for every
// TemplateContext this Engine builds (timeago/reltime/fuzzytime filters
// and .Today.Year) — tests set it to a fixed instant so date-relative
// fixtures ("12:25am", "Yesterday 12:25") assert an exact result instead
// of one that depends on the day the suite happens to run.
type Engine struct {
	HTTP  *http.Client
	Proxy http.RoundTripper
	Now   func() time.Time
}

// Caps derives Capabilities via Definition.Capabilities. It takes no
// context and does no I/O — kept on Engine (rather than only on
// Definition) so the indexer controller can hold one Engine value and
// reach all four operations the spec names: Caps, Login, Search, Download.
func (e Engine) Caps(def *Definition) (Capabilities, error) {
	return def.Capabilities(), nil
}

// CloudflareChallengeError signals a 403/503 response carrying a
// Cloudflare/DDoS-Guard marker (cf-mitigated header, or Server:
// cloudflare|ddos-guard). pkg/cardigann never solves the challenge itself
// (that is IndexerProxy's FlareSolverr client, a later indexarr task); it
// only detects and reports so the caller can retry through a proxy.
type CloudflareChallengeError struct{ StatusCode int }

func (e *CloudflareChallengeError) Error() string {
	return fmt.Sprintf("cardigann: cloudflare/ddos-guard challenge (HTTP %d)", e.StatusCode)
}

// httpClient returns e.HTTP (or http.DefaultClient when unset), with its
// Transport swapped for e.Proxy when one is configured. It never mutates a
// caller-owned *http.Client.
func (e Engine) httpClient() *http.Client {
	base := e.HTTP
	if base == nil {
		base = http.DefaultClient
	}
	if e.Proxy == nil {
		return base
	}
	clone := *base
	clone.Transport = e.Proxy
	return &clone
}

// now returns e.Now(), defaulting to time.Now when unset.
func (e Engine) now() time.Time {
	if e.Now == nil {
		return time.Now()
	}
	return e.Now()
}

// templateContext builds the shared *TemplateContext every one of
// Login/Search/Download renders against: Config from cfg.Values, the
// True/False sentinels, and Now from e.now() — the one place Engine.Now is
// read, so every date-relative filter and .Today.Year a definition's
// templates use is deterministic under test whenever the caller sets
// Engine.Now. Query/Keywords/Categories/Result are populated by Search
// itself per-request, not here.
func (e Engine) templateContext(_ *Definition, cfg Config) *TemplateContext {
	now := e.now()
	tc := &TemplateContext{
		Config: cfg.Values,
		Result: map[string]string{},
		True:   "true",
		False:  "",
		Now:    now,
	}
	tc.Today.Year = now.Year()
	return tc
}

// looksLikeCloudflare reports whether h carries a Cloudflare/DDoS-Guard
// marker: the cf-mitigated header, or a Server header naming either
// service.
func looksLikeCloudflare(h http.Header) bool {
	server := strings.ToLower(h.Get("Server"))
	return h.Get("cf-mitigated") != "" || strings.Contains(server, "cloudflare") || strings.Contains(server, "ddos-guard")
}

// do is the one shared low-level request function every outbound call
// (login page fetch, submit, search request, download fetch) goes
// through, so Cloudflare detection and tracing apply everywhere for free.
func (e Engine) do(ctx context.Context, req *http.Request) (*http.Response, []byte, error) {
	ctx, span := tracing.Start(ctx, "cardigann."+strings.ToLower(req.Method))
	defer span.End()
	client := e.httpClient()
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		tracing.RecordError(span, err)
		return nil, nil, fmt.Errorf("cardigann: request %s: %w", req.URL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, nil, fmt.Errorf("cardigann: read response %s: %w", req.URL, err)
	}
	if (resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusServiceUnavailable) &&
		looksLikeCloudflare(resp.Header) {
		cfErr := &CloudflareChallengeError{StatusCode: resp.StatusCode}
		tracing.RecordError(span, cfErr)
		return resp, body, cfErr
	}
	return resp, body, nil
}

// resolveURL resolves path against baseURL: an absolute path (already
// carrying a scheme) passes through unchanged, otherwise it is resolved as
// an RFC 3986 reference against baseURL (which every Config carries with a
// trailing "/", so a bare "login" or "api/torrents/filter" lands directly
// under the site root).
func resolveURL(baseURL, path string) (string, error) {
	if path == "" {
		return baseURL, nil
	}
	if strings.Contains(path, "://") {
		return path, nil
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("cardigann: base url %q: %w", baseURL, err)
	}
	ref, err := url.Parse(path)
	if err != nil {
		return "", fmt.Errorf("cardigann: path %q: %w", path, err)
	}
	return base.ResolveReference(ref).String(), nil
}

// attachSession adds sess's cookies and headers to req; a nil sess is a
// no-op (public trackers, or a caller that hasn't logged in yet on a
// definition whose login method doesn't require a session — see
// loginRequiresSession).
func attachSession(req *http.Request, sess *Session) {
	if sess == nil {
		return
	}
	for _, c := range sess.Cookies {
		req.AddCookie(c)
	}
	for k, vals := range sess.Headers {
		for _, v := range vals {
			req.Header.Add(k, v)
		}
	}
}

// stringValue reads a resolved setting (or a pass-through raw value — see
// ResolveSettings) out of cfg.Values as a string.
func (cfg Config) stringValue(name string) (string, bool) {
	v, ok := cfg.Values[name]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

// get issues a GET to path (resolved against cfg.BaseURL) and returns the
// response body.
func (e Engine) get(ctx context.Context, cfg Config, path string) ([]byte, error) {
	u, err := resolveURL(cfg.BaseURL, path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("cardigann: build request: %w", err)
	}
	attachSession(req, cfg.Session)
	_, body, err := e.do(ctx, req)
	return body, err
}

// postForm issues a POST with an application/x-www-form-urlencoded body to
// path (resolved against cfg.BaseURL).
func (e Engine) postForm(ctx context.Context, cfg Config, path string, form url.Values) (*http.Response, []byte, error) {
	u, err := resolveURL(cfg.BaseURL, path)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, nil, fmt.Errorf("cardigann: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	attachSession(req, cfg.Session)
	return e.do(ctx, req)
}

// renderHeaders renders every value of headers (search.headers,
// login.headers, download.headers all share this shape) as a template and
// sets them on req.
func renderHeaders(req *http.Request, headers map[string][]string, tc *TemplateContext) error {
	for name, values := range headers {
		for _, v := range values {
			rendered, err := render(v, tc)
			if err != nil {
				return fmt.Errorf("cardigann: header %q: %w", name, err)
			}
			req.Header.Add(name, rendered)
		}
	}
	return nil
}
