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

package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/cardigann"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/ratelimit"
)

// maxRedirects bounds the same-origin hop chain. Go's default is ten; five is
// generous for "the tracker bounced us to its download host" and stops a loop
// from holding a request open for the whole timeout.
const maxRedirects = 5

// defaultTimeout mirrors spec.timeout's +kubebuilder:default="30s", and
// TestFetcherTimeoutMatchesTheGeneratedCRD reads the generated schema so the
// two cannot drift.
//
// The CRD default does NOT cover a spec built in Go: an apiserver default
// fills a field ABSENT from the submitted JSON, metav1.Duration is a struct,
// and `omitempty` does nothing to a struct field -- so a typed client always
// marshals `"timeout":"0s"` and is never defaulted. Verified against a real
// apiserver: an unstructured create yields 30s, a typed create yields 0s.
// http.Client treats a zero Timeout as NO timeout, so an indexer that accepts
// the connection and never answers would hold the RPC until grabarr's own
// deadline. Unlike spec.requestDelay -- where ratelimit.Config documents
// RPS <= 0 as a supported "do not pace me" -- a zero timeout has no reading
// anyone wants, so it is floored. This mirrors indexarr/controller/indexer's
// timeoutFor for exactly the same reason; the two are deliberately separate
// because that one is unexported and in a package this task does not own.
const defaultTimeout = 30 * time.Second

// errTooManyRedirects is returned through *url.Error by the redirect policy.
var errTooManyRedirects = errors.New("indexarr/download: too many redirects")

// FetchResult is one authenticated GET, with the redirect chain ALREADY
// classified by the fetcher. Exactly one of Body, MagnetURL and OffHostURL is
// set on a non-error result.
type FetchResult struct {
	// Status is the HTTP status of the response that ended the chain.
	Status int

	// Header is that response's header.
	Header http.Header

	// Body is nil unless the chain ended in a response worth reading; the
	// caller closes it.
	Body io.ReadCloser

	// ContentLen is -1 when the server sent no Content-Length.
	ContentLen int64

	// FinalURL is the URL that produced this response.
	FinalURL *url.URL

	// MagnetURL is set when the chain pointed at a magnet: URI.
	MagnetURL string

	// OffHostURL is set when the chain left the indexer's origin.
	OffHostURL string
}

// Fetcher performs one authenticated GET against one indexer.
type Fetcher interface {
	// Fetch performs the GET and classifies the redirect chain.
	Fetch(ctx context.Context, rawURL string) (*FetchResult, error)

	// Scrub removes this indexer's secret VALUES from a diagnostic string.
	// It is the last line of defence before a message reaches
	// DownloadResponse.Error and, through grabarr, a Download's status
	// condition.
	Scrub(s string) string
}

// FetcherFor builds the Fetcher for one Indexer. D1-8 supplies it; this
// package ships NewFetcherFor as the production implementation.
type FetcherFor func(ctx context.Context, idx *indexv1alpha1.Indexer) (Fetcher, error)

type fetcher struct {
	hc      *http.Client
	base    string // the indexer host, for the same-origin test
	limiter *ratelimit.Limiter
	key     string
	scrub   func(string) string
}

func (f *fetcher) Scrub(s string) string {
	if f.scrub == nil {
		return s
	}
	return f.scrub(s)
}

// sameOrigin reports whether target may be reached with this indexer's
// session. Hostnames only, case-insensitive, ports ignored; a dot-suffix in
// either direction counts, so tr.example -> dl.tr.example is followed. The
// "."+host is load-bearing: a bare suffix test would accept eviltr.example,
// which is exactly how a look-alike domain harvests a session cookie.
func sameOrigin(base, target string) bool {
	b, t := strings.ToLower(hostOnly(base)), strings.ToLower(hostOnly(target))
	if b == "" || t == "" {
		return false
	}
	return b == t || strings.HasSuffix(t, "."+b) || strings.HasSuffix(b, "."+t)
}

// hostOnly drops a :port, leaving a bracketed IPv6 literal intact.
func hostOnly(h string) string {
	if i := strings.LastIndexByte(h, ':'); i > 0 && !strings.Contains(h[i:], "]") {
		return h[:i]
	}
	return h
}

// checkRedirect classifies the next hop instead of blindly following it. Go's
// default policy follows ten hops and ERRORS on a magnet: target with
// "unsupported protocol scheme" -- which is exactly the shape a torrent site
// uses, so the default would turn the most common success into a failure.
//
// Returning http.ErrUseLastResponse hands the 3xx back to Fetch, which then
// reads Location. Cross-origin hops stop because our session cookie would not
// be sent there anyway (the jar is per-origin), so proxying buys nothing but
// memory -- and because a look-alike host must never see the cookie.
func (f *fetcher) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > maxRedirects {
		return errTooManyRedirects
	}
	switch req.URL.Scheme {
	case "http", "https":
		if !sameOrigin(f.base, req.URL.Host) {
			return http.ErrUseLastResponse
		}
		return nil
	case "magnet":
		return http.ErrUseLastResponse
	default:
		// The scheme is named; the URL is not, because a data: URL can
		// carry anything and a file: URL names a path.
		return fmt.Errorf("indexarr/download: refusing to follow a %q redirect", req.URL.Scheme)
	}
}

// Fetch performs the GET. The magnet short circuit comes first, so a magnet
// link never touches the network.
func (f *fetcher) Fetch(ctx context.Context, rawURL string) (*FetchResult, error) {
	ctx, span := tracing.Start(ctx, "indexarr.download.fetch")
	defer span.End()

	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("indexarr/download: parse download URL: %w", cardigann.RedactErr(err))
	}
	// A magnet link is already the payload. Never fetch it.
	if u.Scheme == "magnet" {
		return &FetchResult{MagnetURL: rawURL, FinalURL: u}, nil
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("indexarr/download: refusing a %q download URL", u.Scheme)
	}

	// The caller owns rate limiting (CLAUDE.md); this waits on the limiter
	// D1-3 built, keyed by HOST, so two Indexers pointing at one tracker
	// share one bucket.
	if f.limiter != nil {
		if err := f.limiter.Wait(ctx, f.key); err != nil {
			return nil, cardigann.RedactErr(err)
		}
	}

	logging.FromContext(ctx).Debug("indexarr/download: fetching", "url", cardigann.RedactURL(u))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, cardigann.RedactErr(err)
	}
	resp, err := f.hc.Do(req)
	if err != nil {
		tracing.RecordError(span, err)
		return nil, fmt.Errorf("indexarr/download: get %s: %w", cardigann.RedactURL(u), cardigann.RedactErr(err))
	}

	res := &FetchResult{
		Status:     resp.StatusCode,
		Header:     resp.Header,
		ContentLen: resp.ContentLength,
		FinalURL:   resp.Request.URL,
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		defer func() { _ = resp.Body.Close() }()
		loc, lerr := resp.Location()
		if lerr != nil {
			return nil, fmt.Errorf("indexarr/download: %d with no usable Location: %w",
				resp.StatusCode, cardigann.RedactErr(lerr))
		}
		if loc.Scheme == "magnet" {
			res.MagnetURL = loc.String()
		} else {
			res.OffHostURL = loc.String()
		}
		return res, nil
	}
	res.Body = resp.Body
	return res, nil
}

// seedCookies installs a raw Cookie header value into the jar, scoped to
// origin. A jar is used rather than a request header on purpose: net/http
// will not send jar cookies to a different origin, so a redirect that leaves
// the indexer cannot carry the session away with it.
func seedCookies(jar http.CookieJar, origin *url.URL, raw string) {
	if strings.TrimSpace(raw) == "" {
		return
	}
	header := http.Header{}
	header.Add("Cookie", raw)
	cookies := (&http.Request{Header: header}).Cookies()
	if len(cookies) > 0 {
		jar.SetCookies(origin, cookies)
	}
}

// NewFetcherFor is the production FetcherFor. It reads spec.secretRef and
// status.sessionSecretRef, seeds a per-origin cookie jar, floors spec.timeout
// at the CRD default, and paces through the injected limiter keyed by HOST.
//
// It does NOT rewrite the download URL. The link came out of the indexer's own
// feed and already embeds whatever credential that indexer signs with;
// appending our apikey can duplicate a parameter or break an HMAC, and the
// failure reads like an auth error. Credentials are applied only as cookies.
//
// The limiter is injected, never constructed: D1-3's reconciler is the only
// writer of a key's Config, and this package only ever Waits on it.
func NewFetcherFor(c client.Client, lim *ratelimit.Limiter) FetcherFor {
	return func(ctx context.Context, idx *indexv1alpha1.Indexer) (Fetcher, error) {
		base, err := url.Parse(idx.Spec.BaseURL)
		if err != nil || base.Host == "" || base.Scheme == "" {
			return nil, fmt.Errorf("indexarr/download: indexer %s/%s has an unusable spec.baseURL",
				idx.Namespace, idx.Name)
		}
		secret, err := readSecretData(ctx, c, idx.Namespace, idx.Spec.SecretRef)
		if err != nil {
			return nil, err
		}
		session, err := readSessionData(ctx, c, idx.Namespace, idx.Status.SessionSecretRef)
		if err != nil {
			return nil, err
		}
		jar, err := cookiejar.New(nil)
		if err != nil {
			return nil, err
		}
		seedCookies(jar, base, string(secret["cookie"]))
		seedCookies(jar, base, string(session["cookie"]))

		timeout := idx.Spec.Timeout.Duration
		if timeout <= 0 {
			timeout = defaultTimeout
		}
		f := &fetcher{
			base:    base.Host,
			limiter: lim,
			// The same string indexarr/controller/indexer's limiterKeyFor
			// produces for this Indexer -- url.Parse(spec.baseURL).Host --
			// so the reconciler's SetConfig and this Wait address one
			// bucket. That helper is unexported in a package this task
			// does not own; a shared key helper is a carried item.
			key: base.Host,
			scrub: scrubber([]string{
				string(secret["apikey"]), string(secret["passkey"]),
				string(secret["rss_key"]), string(secret["password"]),
				string(secret["cookie"]), string(session["cookie"]),
			}),
		}
		f.hc = &http.Client{Timeout: timeout, Jar: jar, CheckRedirect: f.checkRedirect}
		return f, nil
	}
}

// readSecretData reads spec.secretRef. Recognised keys are apikey, username,
// password, cookie, passkey and rss_key (indexer_types.go:116). A missing
// Secret is an error naming the Secret, never its contents.
func readSecretData(
	ctx context.Context, c client.Client, ns string, ref *corev1.LocalObjectReference,
) (map[string][]byte, error) {
	if ref == nil || ref.Name == "" {
		return nil, nil
	}
	var s corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: ref.Name}, &s); err != nil {
		return nil, fmt.Errorf("indexarr/download: read secret %s/%s: %w", ns, ref.Name, err)
	}
	return s.Data, nil
}

// readSessionData reads status.sessionSecretRef. A missing session Secret is
// NOT an error: the indexer may be public, or the login may not have run yet,
// and the fetch should be attempted either way.
func readSessionData(
	ctx context.Context, c client.Client, ns, name string,
) (map[string][]byte, error) {
	if name == "" {
		return nil, nil
	}
	var s corev1.Secret
	if err := c.Get(ctx, types.NamespacedName{Namespace: ns, Name: name}, &s); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("indexarr/download: read session secret %s/%s: %w", ns, name, err)
	}
	return s.Data, nil
}
