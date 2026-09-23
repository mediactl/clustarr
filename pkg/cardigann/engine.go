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
	"crypto/sha1" //nolint:gosec // Certificates are SHA-1 fingerprints, the definition format's own choice (Jackett's GetCertHashString).
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
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
//
// Limiter paces every outbound request (login, search, download, and each
// of their sub-requests). It is injected and NEVER defaulted on: the caller
// owns rate limiting, holding one bucket per indexer host shared across
// every verb, and a library-side default would sit in series underneath it
// and silently change the effective rate. A nil Limiter is "unpaced", which
// is what a test wants. RateKey is the bucket key to Wait on -- indexarr
// passes ratelimit.HostKey(spec.baseURL), the same spelling its reconciler
// writes the bucket's Config under -- and an empty RateKey falls back to
// each request's own host, so a definition whose download links live on
// another host is still paced per host rather than not at all.
type Engine struct {
	HTTP    *http.Client
	Proxy   http.RoundTripper
	Now     func() time.Time
	Limiter RateLimiter
	RateKey string
}

// RateLimiter is the one method Engine needs from a limiter.
// *ratelimit.Limiter satisfies it; an interface rather than that concrete
// type so a test can count the waits.
type RateLimiter interface {
	Wait(ctx context.Context, key string) error
}

// wait blocks on e.Limiter for req, or returns at once when none is set.
func (e Engine) wait(ctx context.Context, req *http.Request) error {
	if e.Limiter == nil {
		return nil
	}
	key := e.RateKey
	if key == "" {
		key = req.URL.Host
	}
	if err := e.Limiter.Wait(ctx, key); err != nil {
		return fmt.Errorf("cardigann: rate limit wait for %s: %w", RedactURL(req.URL), err)
	}
	return nil
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

// exchange is how one request is sent: the definition it runs for (whose
// Certificates may relax TLS verification for the site host), the site it
// belongs to, whether redirects are followed, and the cookie jar a login
// flow collects every Set-Cookie into.
//
// Redirects follow Prowlarr's per-request choices, not Go's default of
// always following: a search request follows only when its path sets
// followredirect, a login landing page only when the definition does, and
// the login submit and the download fetch always do. A redirect that is not
// followed comes back as the 3xx response itself, which is what lets
// searchOnePath report "redirected to the login page" instead of parsing
// that page as a search with no results.
type exchange struct {
	def    *Definition
	site   string
	follow bool
	jar    http.CookieJar
}

// httpClient returns the client for one exchange: e.HTTP (or
// http.DefaultClient when unset) with e.Proxy as its transport when one is
// configured, redirects followed only when x.follow, x.jar as its cookie jar
// when set, and x.def's Certificates trusted for the site host. It never
// mutates a caller-owned *http.Client or transport; every change is on a
// copy.
func (e Engine) httpClient(x exchange) *http.Client {
	base := e.HTTP
	if base == nil {
		base = http.DefaultClient
	}
	clone := *base
	if e.Proxy != nil {
		clone.Transport = e.Proxy
	}
	if !x.follow {
		clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	if x.jar != nil {
		clone.Jar = x.jar
	}
	if t := trustingTransport(clone.Transport, x.def, x.site); t != nil {
		clone.Transport = t
	}
	return &clone
}

// trustingTransport honours Definition.Certificates, Jackett's semantics
// (CardigannIndexer adds each to WebClient.AddTrustedCertificate for the
// site host; HttpWebClient2.ValidateCertificate accepts a leaf whose SHA-1
// thumbprint is listed for the request's host even when chain validation
// fails). The corpus uses it for trackers with an expired or self-signed
// certificate. Prowlarr decodes the field and ignores it, so such a tracker
// simply fails there.
//
// It returns nil -- keep rt -- when def lists no certificates, and when rt is
// not an *http.Transport: a FlareSolverr round tripper does its own TLS, and
// a transport this package cannot see into keeps its own policy. The clone
// disables keep-alives because it lives for one request; a pooled idle
// connection on a transport nothing will reuse would outlive it.
func trustingTransport(rt http.RoundTripper, def *Definition, site string) http.RoundTripper {
	if def == nil || len(def.Certificates) == 0 {
		return nil
	}
	if rt == nil {
		rt = http.DefaultTransport
	}
	t, ok := rt.(*http.Transport)
	if !ok {
		return nil
	}
	u, err := url.Parse(site)
	if err != nil || u.Hostname() == "" {
		return nil
	}
	clone := t.Clone()
	clone.DisableKeepAlives = true
	clone.TLSClientConfig = trustConfig(clone.TLSClientConfig, u.Hostname(), def.Certificates)
	return clone
}

// trustConfig returns base with verification replaced by one that first
// verifies normally and, only when that fails, accepts a leaf certificate
// whose SHA-1 fingerprint is in fingerprints and whose server name is host.
// A base that already skips verification is returned as it was.
func trustConfig(base *tls.Config, host string, fingerprints []string) *tls.Config {
	var cfg *tls.Config
	if base == nil {
		cfg = &tls.Config{MinVersion: tls.VersionTLS12}
	} else {
		cfg = base.Clone()
	}
	if cfg.InsecureSkipVerify {
		return cfg
	}
	trusted := make(map[string]bool, len(fingerprints))
	for _, f := range fingerprints {
		trusted[strings.ToLower(f)] = true
	}
	roots := cfg.RootCAs
	// Verification moves into VerifyConnection, which still runs on every
	// handshake; InsecureSkipVerify only turns off the built-in check that
	// would reject the pinned certificate before VerifyConnection sees it.
	cfg.InsecureSkipVerify = true
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 {
			return errors.New("cardigann: tls: server presented no certificate")
		}
		leaf := cs.PeerCertificates[0]
		opts := x509.VerifyOptions{Roots: roots, DNSName: cs.ServerName, Intermediates: x509.NewCertPool()}
		for _, c := range cs.PeerCertificates[1:] {
			opts.Intermediates.AddCert(c)
		}
		_, verr := leaf.Verify(opts)
		if verr == nil {
			return nil
		}
		sum := sha1.Sum(leaf.Raw) //nolint:gosec // fingerprint comparison, see the import.
		if isSiteConnection(cs.ServerName, host) && trusted[hex.EncodeToString(sum[:])] {
			return nil
		}
		return verr
	}
	return cfg
}

// isSiteConnection reports whether a handshake whose SNI name is serverName
// is a connection to host. Go sends no SNI for an IP literal, so
// ConnectionState.ServerName is empty exactly when the dialled host was an
// IP; that is the site only when the site itself is one. (The pin is on one
// exact certificate, whose private key the handshake has just proved the
// server holds; the host check is Jackett's defence in depth on top.)
func isSiteConnection(serverName, host string) bool {
	if serverName == "" {
		return net.ParseIP(strings.Trim(host, "[]")) != nil
	}
	return strings.EqualFold(serverName, host)
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
func (e Engine) templateContext(def *Definition, cfg Config) *TemplateContext {
	now := e.now()
	tc := &TemplateContext{
		Config: cfg.Values,
		Result: map[string]string{},
		True:   "true",
		False:  "",
		Now:    now,
	}
	tc.Today.Year = now.Year()
	if def != nil {
		// Load has already refused an encoding it cannot resolve; a
		// hand-built Definition with a bad one falls back to UTF-8.
		tc.enc, _ = def.textEncoding()
	}
	return tc
}

// looksLikeCloudflare reports whether h carries a Cloudflare/DDoS-Guard
// marker: the cf-mitigated header, or a Server header naming either
// service.
func looksLikeCloudflare(h http.Header) bool {
	server := strings.ToLower(h.Get("Server"))
	return h.Get("cf-mitigated") != "" || strings.Contains(server, "cloudflare") || strings.Contains(server, "ddos-guard")
}

// maxResponseBodyBytes bounds how much of an indexer's response Engine
// will buffer into memory, mirroring pkg/torznab.Client's own cap
// (confirmed via `go doc ./pkg/torznab`: "Client reads bodies with an
// 8 MiB cap"). Without a bound, one misbehaving (or malicious) indexer's
// oversized response — on any of login, search or download, all of which
// go through do() — could exhaust memory in a fan-out search across many
// indexers.
const maxResponseBodyBytes = 8 << 20 // 8 MiB

// ErrResponseTooLarge is returned by do() — and therefore by every Engine
// outbound call (login page fetch/submit, search request, download
// fetch, and any redirect Go's http.Client follows along the way) — when
// a response body exceeds maxResponseBodyBytes.
var ErrResponseTooLarge = errors.New("cardigann: response body exceeds size limit")

// RedactURL renders u for a diagnostic message -- an error or a log line
// -- with everything secret stripped: the query string, the fragment and
// any userinfo. Scheme, host and path survive, which is what makes the
// message diagnosable.
//
// Definition inputs are rendered from .Config.<setting> and
// SettingsField.Type supports "password", so real definitions put apikey,
// passkey and rsskey in the query string; those errors reach
// Indexer.status and the logs. pkg/torznab.Client makes the same choice
// deliberately, logging only its base URL and the `t` parameter.
//
// Exported because it is the one place an indexer passkey gets stripped
// before it reaches a log, an error string or a status condition; a
// second, independent implementation elsewhere is how that secret
// eventually leaks -- the two copies drift and nobody notices until a
// passkey is on someone's screen. Other packages needing the same
// redaction (captionarr/indexarr diagnostics, per Ruling R26) must call
// this one rather than writing their own.
func RedactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	safe := *u
	safe.RawQuery = ""
	safe.ForceQuery = false
	safe.Fragment = ""
	safe.RawFragment = ""
	safe.User = nil
	return safe.String()
}

// redactRawURL is RedactURL for a URL still in string form, including one
// url.Parse rejects: everything from the first "?" on is dropped, so an
// unparseable URL cannot leak its query either.
func redactRawURL(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return RedactURL(u)
	}
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		return raw[:i]
	}
	return raw
}

// RedactErr strips the URL net/url puts in *url.Error's own message
// (http.Client.Do and url.Parse both return one, carrying the full URL,
// query string included) while keeping the underlying cause -- so
// errors.Is still finds context.Canceled, syscall errors and the rest
// through it.
//
// Exported alongside RedactURL for the same reason (Ruling R26): a
// second, independent copy of secret redaction is the worst kind of
// technical debt, because the copies drift silently.
func RedactErr(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) && ue.Err != nil {
		return ue.Err
	}
	return err
}

// do is the one shared low-level request function every outbound call
// (login page fetch, submit, search request, download fetch) goes
// through, so Cloudflare detection, the size cap and tracing all apply
// everywhere for free.
func (e Engine) do(ctx context.Context, req *http.Request, x exchange) (*http.Response, []byte, error) {
	ctx, span := tracing.Start(ctx, "cardigann."+strings.ToLower(req.Method))
	defer span.End()
	if err := e.wait(ctx, req); err != nil {
		tracing.RecordError(span, err)
		return nil, nil, err
	}
	client := e.httpClient(x)
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		tracing.RecordError(span, err)
		return nil, nil, fmt.Errorf("cardigann: request %s: %w", RedactURL(req.URL), RedactErr(err))
	}
	defer func() { _ = resp.Body.Close() }()

	// Read one byte past the limit so a body that is exactly at the limit
	// is accepted while anything larger is detected without ever
	// buffering more than maxResponseBodyBytes+1 bytes.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodyBytes+1))
	if err != nil {
		tracing.RecordError(span, err)
		return nil, nil, fmt.Errorf("cardigann: read response %s: %w", RedactURL(req.URL), RedactErr(err))
	}
	if len(body) > maxResponseBodyBytes {
		sizeErr := fmt.Errorf("%w: at least %d bytes: %s", ErrResponseTooLarge, len(body), RedactURL(req.URL))
		tracing.RecordError(span, sizeErr)
		return resp, nil, sizeErr
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
		return "", fmt.Errorf("cardigann: base url %q: %w", redactRawURL(baseURL), RedactErr(err))
	}
	ref, err := url.Parse(path)
	if err != nil {
		return "", fmt.Errorf("cardigann: path %q: %w", redactRawURL(path), RedactErr(err))
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

// postForm issues a POST with an application/x-www-form-urlencoded body,
// encoded in tc's charset, to path (resolved against cfg.BaseURL).
func (e Engine) postForm(ctx context.Context, cfg Config, tc *TemplateContext, path string, form url.Values, x exchange) (*http.Response, []byte, error) {
	u, err := resolveURL(cfg.BaseURL, path)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(encodeValues(form, tc.enc, "")))
	if err != nil {
		return nil, nil, fmt.Errorf("cardigann: build request: %w", RedactErr(err))
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	attachSession(req, cfg.Session)
	return e.do(ctx, req, x)
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
