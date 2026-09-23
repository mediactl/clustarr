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

package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

const (
	// maxBodyBytes caps every body this package buffers: a request body it
	// may have to replay, the slice of a response it inspects for a
	// challenge, and FlareSolverr's own answer. It matches pkg/torznab's and
	// pkg/cardigann's 8 MiB.
	maxBodyBytes = 8 << 20

	// peekBytes is how much of a Cloudflare-served response is read to look
	// for a challenge page's title. Challenge pages are small; the rest of
	// the body is handed on untouched.
	peekBytes = 64 << 10

	// defaultMaxTimeout backs up IndexerProxy.spec.requestTimeout's CRD
	// default of 60s, which is also FlareSolverr's own maxTimeout default.
	defaultMaxTimeout = 60 * time.Second
)

var (
	// ErrFlareSolverr is what every failed solve matches: FlareSolverr
	// unreachable, refusing, or answering without the clearance cookies a
	// solve exists to produce.
	ErrFlareSolverr = errors.New("indexarr/proxy: flaresolverr could not solve the challenge")

	// ErrResponseTooLarge is returned when a body this package must buffer
	// exceeds maxBodyBytes.
	ErrResponseTooLarge = errors.New("indexarr/proxy: body exceeds size limit")
)

// FlareSolverr is an http.RoundTripper that answers a Cloudflare or
// DDoS-Guard challenge through a FlareSolverr service, the way Prowlarr's
// FlareSolverr indexer proxy does
// (src/NzbDrone.Core/IndexerProxies/FlareSolverr/FlareSolverr.cs):
//
//   - Every request first goes out as normal, through Next, carrying any
//     clearance this host already earned (its cookies, and the user agent
//     they are bound to).
//   - A response that is a challenge -- Prowlarr's
//     CloudFlareDetectionService.IsCloudflareProtected, ported in
//     [IsChallenge] -- is not returned. FlareSolverr is asked to fetch the
//     same URL (request.get, or request.post for a form body), and the
//     clearance it comes back with is cached for the host.
//   - The ORIGINAL request is then sent again, through Next, with that
//     clearance. FlareSolverr's own copy of the page is never used as the
//     answer, so what the caller parses always came through the caller's
//     own route -- its proxy, its limiter's pacing and its size caps.
//
// It is applied LAST, outermost (IndexerProxy.spec.selector's "at most one
// FlareSolverr proxy may match an Indexer; it is applied last"): Next is the
// Indexer's HTTP or SOCKS route, or a direct transport.
type FlareSolverr struct {
	// Endpoint is FlareSolverr's API URL, http://host:port/v1.
	Endpoint string

	// MaxTimeout is how long FlareSolverr may spend on one solve
	// (spec.requestTimeout). Zero means 60s.
	MaxTimeout time.Duration

	// Proxy, when set, is the route FlareSolverr's browser must fetch the
	// tracker through: the same proxy the Indexer's own requests use, so a
	// solve does not reveal the address the proxy hides. FlareSolverr takes
	// a schema-prefixed URL and supports no proxy credentials.
	Proxy string

	// Next sends the tracker requests. nil means http.DefaultTransport.
	Next http.RoundTripper

	// Client talks to FlareSolverr. nil means a client with no proxy and a
	// timeout of MaxTimeout plus a margin.
	Client *http.Client

	// Clearances holds what each host has earned. nil means the
	// process-wide [DefaultClearances], which is what lets a clearance
	// outlive the client that earned it: clients are rebuilt on every
	// Indexer edit and every few minutes by the client cache.
	Clearances *ClearanceCache

	// solveMu single-flights solves, so concurrent requests that all hit a
	// challenge cost one FlareSolverr run, not one each.
	solveMu sync.Mutex
}

// Clearance is what a solve earns for one host: the cookies (cf_clearance and
// friends) and the user agent they were issued to. Cloudflare binds the
// clearance to the user agent, so both are replayed together.
type Clearance struct {
	UserAgent string
	Cookies   []*http.Cookie
}

// ClearanceCache is a concurrency-safe map of host to Clearance, keyed by the
// FlareSolverr endpoint as well so two FlareSolverr services never share one.
type ClearanceCache struct {
	mu sync.Mutex
	m  map[string]Clearance
}

// DefaultClearances is the process-wide cache FlareSolverr uses when none is
// injected.
var DefaultClearances = &ClearanceCache{}

func (c *ClearanceCache) get(key string) (Clearance, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cl, ok := c.m[key]
	return cl, ok
}

func (c *ClearanceCache) set(key string, cl Clearance) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]Clearance{}
	}
	c.m[key] = cl
}

func (f *FlareSolverr) next() http.RoundTripper {
	if f.Next != nil {
		return f.Next
	}
	return http.DefaultTransport
}

func (f *FlareSolverr) clearances() *ClearanceCache {
	if f.Clearances != nil {
		return f.Clearances
	}
	return DefaultClearances
}

func (f *FlareSolverr) maxTimeout() time.Duration {
	if f.MaxTimeout > 0 {
		return f.MaxTimeout
	}
	return defaultMaxTimeout
}

func (f *FlareSolverr) cacheKey(host string) string { return f.Endpoint + "\x00" + host }

// RoundTrip implements http.RoundTripper.
func (f *FlareSolverr) RoundTrip(req *http.Request) (*http.Response, error) {
	body, err := bufferBody(req)
	if err != nil {
		return nil, err
	}
	key := f.cacheKey(req.URL.Host)
	before, _ := f.clearances().get(key)

	resp, err := f.next().RoundTrip(withClearance(req, body, before))
	if err != nil {
		return nil, err
	}
	challenged, err := IsChallenge(resp)
	if err != nil || !challenged {
		return resp, err
	}
	_ = resp.Body.Close()

	if err := f.solve(req, body, key, before); err != nil {
		return nil, err
	}
	after, _ := f.clearances().get(key)
	return f.next().RoundTrip(withClearance(req, body, after))
}

// solve asks FlareSolverr for a clearance for req's host, unless another
// request solved it while this one waited.
func (f *FlareSolverr) solve(req *http.Request, body []byte, key string, seen Clearance) error {
	ctx, span := tracing.Start(req.Context(), "indexarr.proxy.flaresolverr")
	defer span.End()

	f.solveMu.Lock()
	defer f.solveMu.Unlock()
	if cur, ok := f.clearances().get(key); ok && !sameClearance(cur, seen) {
		return nil
	}

	cmd := map[string]any{
		"cmd":        "request.get",
		"url":        req.URL.String(),
		"maxTimeout": f.maxTimeout().Milliseconds(),
	}
	if f.Proxy != "" {
		cmd["proxy"] = map[string]string{"url": f.Proxy}
	}
	if req.Method == http.MethodPost {
		// FlareSolverr's request.post takes an application/x-www-form-
		// urlencoded body and nothing else; Prowlarr refuses the rest too.
		if ct := req.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/x-www-form-urlencoded") {
			err := fmt.Errorf("%w: a %q POST cannot be sent through FlareSolverr", ErrFlareSolverr, ct)
			tracing.RecordError(span, err)
			return err
		}
		cmd["cmd"] = "request.post"
		cmd["postData"] = string(body)
	}
	payload, err := json.Marshal(cmd)
	if err != nil {
		return err
	}

	sol, err := f.call(ctx, payload)
	if err != nil {
		tracing.RecordError(span, err)
		return err
	}
	f.clearances().set(key, sol)
	logging.FromContext(ctx).Info("indexarr/proxy: FlareSolverr solved a challenge", "host", req.URL.Host)
	return nil
}

// flareSolverrAnswer is FlareSolverr's reply to request.get/request.post
// (README, "Response").
type flareSolverrAnswer struct {
	Status   string `json:"status"`
	Message  string `json:"message"`
	Solution struct {
		Status    int    `json:"status"`
		UserAgent string `json:"userAgent"`
		Cookies   []struct {
			Name     string  `json:"name"`
			Value    string  `json:"value"`
			Domain   string  `json:"domain"`
			Path     string  `json:"path"`
			Expires  float64 `json:"expires"`
			HTTPOnly bool    `json:"httpOnly"`
			Secure   bool    `json:"secure"`
		} `json:"cookies"`
	} `json:"solution"`
}

// call posts one command to FlareSolverr and returns the clearance it earned.
// Like Prowlarr, a 500 is read too (FlareSolverr reports a failed solve in
// the body with status "error"), and an answer without cookies is a failure:
// a clearance with nothing to replay would challenge again at once.
func (f *FlareSolverr) call(ctx context.Context, payload []byte) (Clearance, error) {
	hc := f.Client
	if hc == nil {
		hc = &http.Client{Timeout: f.maxTimeout() + 10*time.Second, Transport: &http.Transport{}}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.Endpoint, bytes.NewReader(payload))
	if err != nil {
		return Clearance{}, fmt.Errorf("%w: %w", ErrFlareSolverr, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return Clearance{}, fmt.Errorf("%w: %w", ErrFlareSolverr, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusInternalServerError {
		return Clearance{}, fmt.Errorf("%w: FlareSolverr answered HTTP %d", ErrFlareSolverr, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes+1))
	if err != nil {
		return Clearance{}, fmt.Errorf("%w: %w", ErrFlareSolverr, err)
	}
	if len(raw) > maxBodyBytes {
		return Clearance{}, fmt.Errorf("%w: FlareSolverr's answer: %w", ErrFlareSolverr, ErrResponseTooLarge)
	}
	var ans flareSolverrAnswer
	if err := json.Unmarshal(raw, &ans); err != nil {
		return Clearance{}, fmt.Errorf("%w: undecodable answer: %w", ErrFlareSolverr, err)
	}
	if ans.Status != "ok" {
		return Clearance{}, fmt.Errorf("%w: %s", ErrFlareSolverr, clip(ans.Message))
	}
	if len(ans.Solution.Cookies) == 0 {
		return Clearance{}, fmt.Errorf("%w: FlareSolverr returned no cookies", ErrFlareSolverr)
	}
	cl := Clearance{UserAgent: ans.Solution.UserAgent}
	for _, c := range ans.Solution.Cookies {
		cl.Cookies = append(cl.Cookies, &http.Cookie{Name: c.Name, Value: c.Value})
	}
	return cl, nil
}

// clip bounds an upstream message before it reaches an error string.
func clip(s string) string {
	const maxMsg = 256
	if len(s) <= maxMsg {
		return s
	}
	return s[:maxMsg]
}

func sameClearance(a, b Clearance) bool {
	if a.UserAgent != b.UserAgent || len(a.Cookies) != len(b.Cookies) {
		return false
	}
	for i := range a.Cookies {
		if a.Cookies[i].Name != b.Cookies[i].Name || a.Cookies[i].Value != b.Cookies[i].Value {
			return false
		}
	}
	return true
}

// bufferBody reads req's body, if any, so it can be sent twice: once before
// the challenge and once after the solve. It is capped like every other body.
func bufferBody(req *http.Request) ([]byte, error) {
	if req.Body == nil || req.Body == http.NoBody {
		return nil, nil
	}
	defer func() { _ = req.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(req.Body, maxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxBodyBytes {
		return nil, fmt.Errorf("indexarr/proxy: request body: %w", ErrResponseTooLarge)
	}
	return body, nil
}

// withClearance clones req with body and cl applied: cl's user agent, and
// each of cl's cookies the request does not already carry. The user agent
// REPLACES any the request set -- a definition's search.headers may name one
// -- because Cloudflare binds cf_clearance to the agent it was issued to, and
// a mismatched one is challenged again on every request (Prowlarr likewise
// sets the solution's agent on the retried request). A RoundTripper must not
// modify the request it was given, so this works on a clone.
func withClearance(req *http.Request, body []byte, cl Clearance) *http.Request {
	out := req.Clone(req.Context())
	if body != nil {
		out.Body = io.NopCloser(bytes.NewReader(body))
		out.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
		out.ContentLength = int64(len(body))
	}
	if cl.UserAgent != "" {
		out.Header.Set("User-Agent", cl.UserAgent)
	}
	for _, c := range cl.Cookies {
		if _, err := out.Cookie(c.Name); err != nil {
			out.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
		}
	}
	return out
}

// challengeServers are the Server header values behind which Prowlarr looks
// for a challenge at all (CloudFlareDetectionService.CloudflareServerNames).
var challengeServers = map[string]bool{"cloudflare": true, "cloudflare-nginx": true, "ddos-guard": true}

// challengeTitles are the challenge pages' markers, from the same method.
var challengeTitles = []string{
	"<title>Just a moment...</title>",
	"<title>Access denied</title>",
	"<title>Attention Required! | Cloudflare</title>",
}

// IsChallenge reports whether resp is a Cloudflare or DDoS-Guard challenge
// rather than the page asked for. It is Prowlarr's
// CloudFlareDetectionService.IsCloudflareProtected
// (src/NzbDrone.Core/Http/CloudFlare/CloudFlareDetectionService.cs), and
// like it only looks further when the Server header names one of the two
// services, so an ordinary response is never read here at all.
//
// A response it does look at has its first peekBytes read and put back in
// front of the rest, so the caller still receives the whole body.
func IsChallenge(resp *http.Response) (bool, error) {
	if resp == nil || !challengeServers[strings.ToLower(resp.Header.Get("Server"))] {
		return false, nil
	}
	head, err := io.ReadAll(io.LimitReader(resp.Body, peekBytes))
	if err != nil {
		return false, err
	}
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head), resp.Body), resp.Body}
	page := string(head)

	if resp.StatusCode == http.StatusServiceUnavailable || resp.StatusCode == http.StatusForbidden {
		for _, t := range challengeTitles {
			if strings.Contains(page, t) {
				return true, nil
			}
		}
		if strings.TrimSpace(page) == "error code: 1020" ||
			strings.Contains(strings.ToLower(page), "<title>ddos-guard</title>") {
			return true, nil
		}
	}
	// Prowlarr's "custom CloudFlare" for a handful of Dutch trackers.
	if resp.Header.Get("Vary") == "Accept-Encoding,User-Agent" &&
		strings.TrimSpace(resp.Header.Get("Content-Encoding")) == "" &&
		strings.Contains(strings.ToLower(page), "ddos") {
		return true, nil
	}
	return false, nil
}
