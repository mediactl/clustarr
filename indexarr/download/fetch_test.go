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
	"bytes"
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/yaml"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/ratelimit"
)

func TestSameOrigin(t *testing.T) {
	for _, tc := range []struct {
		base, target string
		want         bool
	}{
		{"tr.example", "tr.example", true},
		{"tr.example", "TR.Example", true},
		{"tr.example", "dl.tr.example", true},   // a download subdomain
		{"dl.tr.example", "tr.example", true},   // and back
		{"tr.example", "tr.example:8443", true}, // ports are ignored
		{"tr.example", "cdn.example.net", false},
		{"tr.example", "eviltr.example", false}, // NOT a dot-suffix
		{"tr.example", "", false},
		{"", "tr.example", false},
	} {
		t.Run(tc.base+"->"+tc.target, func(t *testing.T) {
			require.Equal(t, tc.want, sameOrigin(tc.base, tc.target))
		})
	}
}

func testFetcher(t *testing.T, base string) *fetcher {
	t.Helper()
	u, err := url.Parse(base)
	require.NoError(t, err)
	f := &fetcher{base: u.Host, scrub: func(s string) string { return s }}
	f.hc = &http.Client{Timeout: 5 * time.Second, CheckRedirect: f.checkRedirect}
	return f
}

func TestFetchFollowsSameOriginAndStopsElsewhere(t *testing.T) {
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hop":
			http.Redirect(w, r, srv.URL+"/file", http.StatusFound)
		case "/file":
			w.Header().Set("Content-Type", "application/x-bittorrent")
			_, _ = w.Write([]byte("d8:announce5:hello e"))
		case "/magnet":
			http.Redirect(w, r, "magnet:?xt=urn:btih:abc", http.StatusFound)
		case "/away":
			http.Redirect(w, r, "https://cdn.elsewhere.invalid/x?passkey=s3cret", http.StatusFound)
		case "/evil":
			http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, srv.URL+"/loop", http.StatusFound)
		}
	}))
	t.Cleanup(srv.Close)
	f := testFetcher(t, srv.URL)

	t.Run("same origin is followed", func(t *testing.T) {
		res, err := f.Fetch(context.Background(), srv.URL+"/hop")
		require.NoError(t, err)
		t.Cleanup(func() { _ = res.Body.Close() })
		require.Equal(t, http.StatusOK, res.Status)
		b, err := readPayload(res.Body)
		require.NoError(t, err)
		require.Equal(t, "d8:announce5:hello e", string(b))
	})
	t.Run("magnet redirect becomes MagnetURL", func(t *testing.T) {
		res, err := f.Fetch(context.Background(), srv.URL+"/magnet")
		require.NoError(t, err)
		require.Equal(t, "magnet:?xt=urn:btih:abc", res.MagnetURL)
		require.Nil(t, res.Body)
	})
	t.Run("cross origin becomes OffHostURL, intact", func(t *testing.T) {
		res, err := f.Fetch(context.Background(), srv.URL+"/away")
		require.NoError(t, err)
		require.Equal(t, "https://cdn.elsewhere.invalid/x?passkey=s3cret", res.OffHostURL,
			"grabarr needs the URL intact; only the LOG is redacted")
		require.Nil(t, res.Body)
	})
	t.Run("a non-http scheme is refused by name", func(t *testing.T) {
		_, err := f.Fetch(context.Background(), srv.URL+"/evil")
		require.ErrorContains(t, err, "file")
		require.NotContains(t, err.Error(), "/etc/passwd")
	})
	t.Run("a redirect loop stops at the hop limit", func(t *testing.T) {
		_, err := f.Fetch(context.Background(), srv.URL+"/loop")
		require.ErrorIs(t, err, errTooManyRedirects)
	})
}

// A magnet: URL handed straight to Fetch must never reach the network: it is
// already the payload, and Go's client rejects the scheme outright.
func TestFetchShortCircuitsAMagnetURL(t *testing.T) {
	f := &fetcher{base: "tr.example", scrub: func(s string) string { return s }}
	res, err := f.Fetch(context.Background(), "magnet:?xt=urn:btih:abc&dn=x")
	require.NoError(t, err)
	require.Equal(t, "magnet:?xt=urn:btih:abc&dn=x", res.MagnetURL)
	require.Nil(t, res.Body)
}

func TestFetchRefusesANonHTTPDownloadURL(t *testing.T) {
	f := testFetcher(t, "https://tr.example")
	_, err := f.Fetch(context.Background(), "file:///etc/passwd")
	require.ErrorContains(t, err, "file")
	require.NotContains(t, err.Error(), "/etc/passwd")
}

func TestFetchReportsAnOversizeContentLengthWithoutReadingTheBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(MaxPayloadBytes+1))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bytes.Repeat([]byte("x"), MaxPayloadBytes+1))
	}))
	t.Cleanup(srv.Close)
	res, err := testFetcher(t, srv.URL).Fetch(context.Background(), srv.URL+"/big")
	require.NoError(t, err)
	t.Cleanup(func() { _ = res.Body.Close() })
	require.Equal(t, int64(MaxPayloadBytes+1), res.ContentLen,
		"the handler decides on ContentLen alone; the body is never read")
}

func TestSessionCookieIsScopedToTheIndexerOrigin(t *testing.T) {
	var gotCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCookie = r.Header.Get("Cookie")
		_, _ = w.Write([]byte("d1:xe"))
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)

	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	seedCookies(jar, u, "sess=abc123; other=zz")
	f := testFetcher(t, srv.URL)
	f.hc.Jar = jar

	res, err := f.Fetch(context.Background(), srv.URL+"/dl")
	require.NoError(t, err)
	_ = res.Body.Close()
	require.Contains(t, gotCookie, "sess=abc123")

	// A different origin must see nothing.
	other, err := url.Parse("https://cdn.elsewhere.invalid/")
	require.NoError(t, err)
	require.Empty(t, jar.Cookies(other))
}

func TestSeedCookiesIgnoresRubbish(t *testing.T) {
	jar, err := cookiejar.New(nil)
	require.NoError(t, err)
	u, err := url.Parse("https://tr.example/")
	require.NoError(t, err)
	require.NotPanics(t, func() {
		seedCookies(jar, u, "")
		seedCookies(jar, u, "   ")
	})
	require.Empty(t, jar.Cookies(u))
}

// A kubebuilder default fills an ABSENT field, and metav1.Duration is a
// struct that `omitempty` cannot omit, so a typed create sends "0s" and is
// never defaulted. http.Client reads a zero Timeout as NO timeout, so the
// fetcher floors it -- at the CRD's own default, pinned by the test below so
// the two cannot drift.
func TestNewFetcherForFloorsAZeroTimeoutAtTheCRDDefault(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "tr"},
		Spec:       indexv1alpha1.IndexerSpec{BaseURL: "https://tr.example"},
	}

	f, err := NewFetcherFor(c, nil)(context.Background(), idx)
	require.NoError(t, err)
	require.Equal(t, defaultTimeout, f.(*fetcher).hc.Timeout)

	idx.Spec.Timeout = metav1.Duration{Duration: 7 * time.Second}
	f, err = NewFetcherFor(c, nil)(context.Background(), idx)
	require.NoError(t, err)
	require.Equal(t, 7*time.Second, f.(*fetcher).hc.Timeout)
}

func TestFetcherTimeoutMatchesTheGeneratedCRD(t *testing.T) {
	raw, err := os.ReadFile("../../config/crd/bases/index.clustarr.io_indexers.yaml")
	require.NoError(t, err)

	var crd struct {
		Spec struct {
			Versions []struct {
				Schema struct {
					OpenAPIV3Schema struct {
						Properties struct {
							Spec struct {
								Properties map[string]struct {
									Default string `json:"default"`
								} `json:"properties"`
							} `json:"spec"`
						} `json:"properties"`
					} `json:"openAPIV3Schema"`
				} `json:"schema"`
			} `json:"versions"`
		} `json:"spec"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &crd))
	require.NotEmpty(t, crd.Spec.Versions, "the CRD was not parsed; run `make manifests`")

	props := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties.Spec.Properties
	require.Equal(t, defaultTimeout.String(), props["timeout"].Default,
		"fetch.go's defaultTimeout no longer mirrors spec.timeout's +kubebuilder:default")
}

// The scrubber is built from the indexer's OWN secret values, and the session
// cookie is seeded onto the jar so a redirect off-host cannot carry it away.
func TestNewFetcherForSeedsTheSessionAndTheScrubber(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "tr-creds"},
			Data: map[string][]byte{
				"apikey":  []byte("apikey-deadbeef"),
				"passkey": []byte("passkey-cafebabe"),
			},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "tr-session"},
			Data:       map[string][]byte{"cookie": []byte("sess=sessionvalue123")},
		},
	).Build()
	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "tr"},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL:   "https://tr.example",
			SecretRef: &corev1.LocalObjectReference{Name: "tr-creds"},
		},
		Status: indexv1alpha1.IndexerStatus{SessionSecretRef: "tr-session"},
	}

	f, err := NewFetcherFor(c, nil)(context.Background(), idx)
	require.NoError(t, err)
	require.NotContains(t, f.Scrub("denied for passkey-cafebabe"), "passkey-cafebabe")
	require.NotContains(t, f.Scrub("denied for apikey-deadbeef"), "apikey-deadbeef")

	base, err := url.Parse("https://tr.example/dl")
	require.NoError(t, err)
	require.Len(t, f.(*fetcher).hc.Jar.Cookies(base), 1)

	// And nowhere else.
	other, err := url.Parse("https://cdn.elsewhere.invalid/x")
	require.NoError(t, err)
	require.Empty(t, f.(*fetcher).hc.Jar.Cookies(other))
}

// A missing session Secret is not an error: the indexer may be public, or the
// login may not have run yet. A missing spec.secretRef Secret is, because the
// operator named something that is not there.
func TestNewFetcherForToleratesAMissingSessionButNotAMissingSecret(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "tr"},
		Spec:       indexv1alpha1.IndexerSpec{BaseURL: "https://tr.example"},
		Status:     indexv1alpha1.IndexerStatus{SessionSecretRef: "absent"},
	}
	_, err := NewFetcherFor(c, nil)(context.Background(), idx)
	require.NoError(t, err)

	idx.Spec.SecretRef = &corev1.LocalObjectReference{Name: "absent"}
	_, err = NewFetcherFor(c, nil)(context.Background(), idx)
	require.ErrorContains(t, err, "media/absent")
}

func TestNewFetcherForRefusesAnUnusableBaseURL(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	for _, base := range []string{"", "tr.example", "://nope", "ht tp://x"} {
		idx := &indexv1alpha1.Indexer{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "tr"},
			Spec:       indexv1alpha1.IndexerSpec{BaseURL: base},
		}
		_, err := NewFetcherFor(c, nil)(context.Background(), idx)
		require.ErrorContains(t, err, "spec.baseURL", "baseURL %q", base)
	}
}

// TestTheFetcherKeysItsLimiterWithRatelimitHostKey pins the DOWNLOAD half of
// a convention that has two halves. The other half is
// indexarr/controller/indexer's TestTheLimiterKeyIsRatelimitHostKey, and both
// anchor on ratelimit.HostKey so neither can drift on its own (ruling R38).
//
// The reconciler is the only writer of a key's Config and this package only
// Waits on it. Spell the key differently here and the Wait lands on a key
// with NO Config, ratelimit falls back to the Limiter's defaults, and D1-8
// builds the Limiter as ratelimit.New(ratelimit.Config{}) -- whose RPS of 0
// is rate.Inf. This verb would be completely unpaced against a private
// tracker: a ban, not a slowdown, and nothing would log it.
func TestTheFetcherKeysItsLimiterWithRatelimitHostKey(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).Build()
	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "tr"},
		// A port and a path, because both are places a hand-rolled key
		// drifts: u.Hostname() drops the port, u.String() keeps the path.
		Spec: indexv1alpha1.IndexerSpec{BaseURL: "https://tracker.invalid:8443/prowlarr/1"},
	}

	f, err := NewFetcherFor(c, ratelimit.New(ratelimit.Config{}))(context.Background(), idx)
	require.NoError(t, err)
	require.Equal(t, ratelimit.HostKey(idx.Spec.BaseURL), f.(*fetcher).key,
		"the reconciler's SetConfig and this Wait must address one bucket")
	require.Equal(t, "tracker.invalid:8443", f.(*fetcher).key,
		"the port is part of the budget; two services on one machine are two budgets")

	// The error behaviour this call site had before the shared helper is
	// unchanged: NewFetcherFor refuses an unusable spec.baseURL rather than
	// quietly pacing everything through HostKey's "" bucket.
	idx.Spec.BaseURL = "::not a url"
	_, err = NewFetcherFor(c, nil)(context.Background(), idx)
	require.ErrorContains(t, err, "unusable spec.baseURL")
}

// The generic fetcher routes through the Indexer's IndexerProxies like every
// other path: an HTTP proxy receives the download in absolute form and the
// tracker receives nothing -- the property an operator names a proxy for,
// and the request that carries the passkey. A proxy that cannot be resolved
// fails the fetch rather than going direct.
func TestNewFetcherForRoutesThroughTheIndexersProxy(t *testing.T) {
	var direct, proxied int
	tracker := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { direct++ }))
	defer tracker.Close()
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied++
		w.Header().Set("Content-Type", "application/x-bittorrent")
		_, _ = w.Write([]byte("d8:announce0:e"))
	}))
	defer proxySrv.Close()
	pu, err := url.Parse(proxySrv.URL)
	require.NoError(t, err)
	port, err := strconv.Atoi(pu.Port())
	require.NoError(t, err)

	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(&indexv1alpha1.IndexerProxy{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "egress"},
		Spec: indexv1alpha1.IndexerProxySpec{
			Type: indexv1alpha1.IndexerProxyTypeHTTP, Host: pu.Hostname(), Port: int32(port),
			Selector: metav1.LabelSelector{MatchLabels: map[string]string{"egress": "vpn"}},
		},
	}).Build()
	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "tr", Labels: map[string]string{"egress": "vpn"}},
		Spec:       indexv1alpha1.IndexerSpec{BaseURL: tracker.URL},
	}

	f, err := NewFetcherFor(c, nil)(context.Background(), idx)
	require.NoError(t, err)
	res, err := f.Fetch(context.Background(), tracker.URL+"/dl/1.torrent?passkey=x")
	require.NoError(t, err)
	if res.Body != nil {
		_ = res.Body.Close()
	}
	require.Equal(t, 1, proxied, "the download did not go through the selected proxy")
	require.Zero(t, direct, "the download reached the tracker directly, around the proxy")

	idx.Spec.ProxyRef = ptrTo("absent")
	_, err = NewFetcherFor(c, nil)(context.Background(), idx)
	require.Error(t, err, "an unresolvable proxy must fail the fetch, not go direct")
}

func ptrTo[T any](v T) *T { return &v }
