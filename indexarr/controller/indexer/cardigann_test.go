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

package indexer

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	idxstatus "github.com/mediactl/clustarr/indexarr/status"
	"github.com/mediactl/clustarr/pkg/cardigann"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/ratelimit"
	"github.com/mediactl/clustarr/pkg/torznab"
)

func cardigannFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "cardigann", name))
	require.NoError(t, err)
	return string(raw)
}

func fakeClient(t *testing.T, objs ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, k8s.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
}

func idxDefinition(name, yaml string, replaces *string, id string) *indexv1alpha1.IndexerDefinition {
	return &indexv1alpha1.IndexerDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       indexv1alpha1.IndexerDefinitionSpec{YAML: yaml, Replaces: replaces},
		Status:     indexv1alpha1.IndexerDefinitionStatus{ID: id},
	}
}

// The schema spells the middle privacy class "semi-private", the CRD
// "semiPrivate". status.privacy carries no enum, so a wrong spelling would be
// ACCEPTED by the apiserver and silently disagree with the IndexerDefinition.
// Held against the CRD's own constants, not against restated strings.
func TestDefinitionPrivacyMapsOntoTheCRDSpelling(t *testing.T) {
	require.Equal(t, string(indexv1alpha1.DefinitionTypePublic), definitionPrivacy["public"])
	require.Equal(t, string(indexv1alpha1.DefinitionTypeSemiPrivate), definitionPrivacy["semi-private"])
	require.Equal(t, string(indexv1alpha1.DefinitionTypePrivate), definitionPrivacy["private"])
	require.Len(t, definitionPrivacy, 3)
}

// Cardigann's mode names must reach status.caps as Torznab's wire values,
// read back through the SAME predicate the fan-out gates on. A "tv-search"
// key would match nothing and the indexer would never be queried.
func TestDefinitionCapsUseTheTorznabModeVocabulary(t *testing.T) {
	def, err := cardigann.Load([]byte(cardigannFixture(t, "search-error.yml")))
	require.NoError(t, err)
	caps := definitionCaps(def)

	require.True(t, idxstatus.SupportsMode(caps, string(torznab.ModeSearch)))
	require.True(t, idxstatus.SupportsMode(caps, string(torznab.ModeMovieSearch)))
	require.True(t, idxstatus.SupportsMode(caps, string(torznab.ModeTVSearch)))
	require.NotContains(t, caps.Modes, "tv-search")
	require.NotContains(t, caps.Modes, "movie-search")
	require.Equal(t, []string{"ep", "q", "season"}, caps.Modes["tvsearch"], "params are sorted for a stable apply")

	// Movies (2000) and TV (5000), each a parent with no leaf.
	require.Len(t, caps.Categories, 2)
	require.Equal(t, int32(2000), caps.Categories[0].ID)
	require.Equal(t, "Movies", caps.Categories[0].Name)
	require.Equal(t, int32(5000), caps.Categories[1].ID)
}

func TestDefinitionCapsGroupLeavesUnderTheirParent(t *testing.T) {
	def, err := cardigann.Load([]byte(cardigannFixture(t, "1337x.yml")))
	require.NoError(t, err)
	caps := definitionCaps(def)
	for _, c := range caps.Categories {
		require.Zero(t, c.ID%1000, "a leaf %d was projected as a parent", c.ID)
		for _, s := range c.Sub {
			require.Equal(t, c.ID, (s.ID/1000)*1000, "leaf %d is under the wrong parent %d", s.ID, c.ID)
		}
	}
	require.NotEmpty(t, caps.Categories)
}

func TestResolveDefinition(t *testing.T) {
	yaml := cardigannFixture(t, "search-error.yml")
	c := fakeClient(t,
		idxDefinition("by-name", yaml, nil, "synthetic-search-error"),
		idxDefinition("overrides-1337x", yaml, ptr.To("1337x"), "synthetic-search-error"),
		idxDefinition("broken", "id: nope\n", nil, ""),
	)
	ctx := context.Background()

	def, err := resolveDefinition(ctx, c, indexv1alpha1.IndexerSpec{DefinitionRef: ptr.To("by-name")})
	require.NoError(t, err)
	require.Equal(t, "synthetic-search-error", def.ID)

	// spec.definition resolves through spec.replaces first ...
	def, err = resolveDefinition(ctx, c, indexv1alpha1.IndexerSpec{Definition: ptr.To("1337x")})
	require.NoError(t, err)
	require.Equal(t, "synthetic-search-error", def.ID)

	// ... and through a parsed status.id second.
	def, err = resolveDefinition(ctx, c, indexv1alpha1.IndexerSpec{Definition: ptr.To("synthetic-search-error")})
	require.NoError(t, err)
	require.NotNil(t, def)

	_, err = resolveDefinition(ctx, c, indexv1alpha1.IndexerSpec{Definition: ptr.To("unknown")})
	require.ErrorIs(t, err, ErrDefinitionNotFound)
	_, err = resolveDefinition(ctx, c, indexv1alpha1.IndexerSpec{DefinitionRef: ptr.To("missing")})
	require.ErrorIs(t, err, ErrDefinitionNotFound)
	_, err = resolveDefinition(ctx, c, indexv1alpha1.IndexerSpec{DefinitionRef: ptr.To("broken")})
	require.ErrorIs(t, err, errDefinitionInvalid)
	require.LessOrEqual(t, len(err.Error()), maxDefinitionErr+200, "a schema error must fit a condition message")
}

func TestProxyURL(t *testing.T) {
	u, err := proxyURL(indexv1alpha1.IndexerProxySpec{Type: indexv1alpha1.IndexerProxyTypeHTTP, Host: "proxy.local", Port: 3128},
		map[string][]byte{"username": []byte("u"), "password": []byte("p")})
	require.NoError(t, err)
	require.Equal(t, "http://u:p@proxy.local:3128", u.String())

	u, err = proxyURL(indexv1alpha1.IndexerProxySpec{Type: indexv1alpha1.IndexerProxyTypeSocks5, Host: "10.0.0.1", Port: 1080}, nil)
	require.NoError(t, err)
	require.Equal(t, "socks5://10.0.0.1:1080", u.String())

	for _, typ := range []indexv1alpha1.IndexerProxyType{indexv1alpha1.IndexerProxyTypeSocks4, indexv1alpha1.IndexerProxyTypeFlareSolverr} {
		_, err = proxyURL(indexv1alpha1.IndexerProxySpec{Type: typ, Host: "h", Port: 1}, nil)
		require.ErrorIs(t, err, ErrProxyUnavailable, "%s must be refused, never bypassed", typ)
	}
}

// spec.proxyRef reaches the wire. An HTTP proxy receives the request in
// absolute form; the tracker itself receives nothing, which is the property
// an operator names a proxy for.
func TestTheProxyRoutesBothSourceKinds(t *testing.T) {
	var direct, proxied int
	tracker := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { direct++ }))
	defer tracker.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied++
		_, _ = w.Write([]byte(cardigannFixture(t, "search-error-results.html")))
	}))
	defer proxy.Close()
	proxyHost, proxyPort := splitHostPort(t, proxy.URL)

	yaml := cardigannFixture(t, "search-error.yml")
	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: "t", Namespace: "media", UID: "u1", ResourceVersion: "1"},
		Spec: indexv1alpha1.IndexerSpec{
			BaseURL:       tracker.URL,
			DefinitionRef: ptr.To("def"),
			ProxyRef:      ptr.To("egress"),
		},
	}
	c := fakeClient(t, idxDefinition("def", yaml, nil, ""), &indexv1alpha1.IndexerProxy{
		ObjectMeta: metav1.ObjectMeta{Name: "egress", Namespace: "media"},
		Spec:       indexv1alpha1.IndexerProxySpec{Type: indexv1alpha1.IndexerProxyTypeHTTP, Host: proxyHost, Port: proxyPort},
	})

	cli, err := buildWireClient(context.Background(), c, idx, nil, NewSessionStore(c, nil))
	require.NoError(t, err)
	rels, err := cli.Search(context.Background(), torznab.Query{Type: torznab.ModeSearch, Q: "movie"})
	require.NoError(t, err)
	require.Len(t, rels, 2)
	require.Equal(t, 1, proxied)
	require.Zero(t, direct, "the search bypassed the proxy and reached the tracker directly")

	// A proxy that does not exist fails closed.
	idx.Spec.ProxyRef = ptr.To("absent")
	_, err = buildWireClient(context.Background(), c, idx, nil, NewSessionStore(c, nil))
	require.ErrorIs(t, err, ErrProxyUnavailable)
}

func splitHostPort(t *testing.T, raw string) (string, int32) {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	return u.Hostname(), int32(port)
}

// R5 at the factory: a definition-backed Indexer gets a client behind the
// SAME interface as a Torznab one, and it runs the definition -- with the
// definition's search.error surfacing as an error (R6).
func TestClientCacheBuildsTheCardigannEngineForADefinition(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(cardigannFixture(t, "search-error.html")))
	}))
	defer srv.Close()

	idx := &indexv1alpha1.Indexer{
		ObjectMeta: metav1.ObjectMeta{Name: "t", Namespace: "media", UID: "u1", ResourceVersion: "1"},
		Spec:       indexv1alpha1.IndexerSpec{BaseURL: srv.URL, DefinitionRef: ptr.To("def")},
	}
	c := fakeClient(t, idxDefinition("def", cardigannFixture(t, "search-error.yml"), nil, ""))
	cc := NewClientCache(c, ratelimit.New(ratelimit.Config{}))

	cli, err := cc.For(context.Background(), idx)
	require.NoError(t, err)
	_, isCardigann := cli.(*cardigannClient)
	require.True(t, isCardigann)

	_, err = cli.Search(context.Background(), torznab.Query{Type: torznab.ModeSearch, Q: "x"})
	require.ErrorIs(t, err, cardigann.ErrSearchFailed)

	f, err := cc.DefinitionFetcherFor(context.Background(), idx)
	require.NoError(t, err)
	require.NotNil(t, f)

	// A generic Indexer has no definition fetcher.
	gen := idx.DeepCopy()
	gen.UID, gen.Spec.DefinitionRef = "u2", nil
	gen.Spec.Generic = &indexv1alpha1.GenericNewznab{Protocol: "torrent"}
	_, err = cc.DefinitionFetcherFor(context.Background(), gen)
	require.Error(t, err)
}

// The limiter is read onto the engine under ratelimit.HostKey(spec.baseURL),
// the key applyRateLimit writes. A different spelling is not "paced twice as
// fast", it is unpaced.
func TestTheEngineWaitsOnTheReconcilersBucket(t *testing.T) {
	spec := indexv1alpha1.IndexerSpec{BaseURL: "https://tracker.example:8443/sub"}
	def := &cardigann.Definition{Links: []string{"https://tracker.example:8443/sub/"}}
	eng := newEngine(spec, def, ratelimit.New(ratelimit.Config{}), nil)
	require.Equal(t, ratelimit.HostKey(spec.BaseURL), eng.RateKey)
	require.NotNil(t, eng.Limiter)

	// A nil limiter stays a nil INTERFACE; a typed nil would panic on Wait.
	eng = newEngine(spec, def, nil, nil)
	require.Nil(t, eng.Limiter)
}

// An Indexer still configured with one of the definition's legacylinks sends
// every request to links[0] (cardigann.NewConfig's SiteLink), so the bucket
// must be keyed there -- by the writer and the engine alike. Keyed on the
// configured legacy host, the engine's real host would read the Limiter's
// default and the operator's requestDelay would pace nothing.
func TestTheBucketFollowsTheDefinitionsSiteLink(t *testing.T) {
	def := &cardigann.Definition{
		Links:       []string{"https://new-tracker.example/"},
		LegacyLinks: []string{"https://old-tracker.example/"},
	}
	spec := indexv1alpha1.IndexerSpec{
		BaseURL: "https://old-tracker.example", RequestDelay: &metav1.Duration{Duration: time.Hour},
	}
	eng := newEngine(spec, def, ratelimit.New(ratelimit.Config{}), nil)
	require.Equal(t, "new-tracker.example", eng.RateKey)

	lim := ratelimit.New(ratelimit.Config{})
	applyRateLimit(spec, def, lim, 0)
	require.True(t, lim.Allow("new-tracker.example"))
	require.False(t, lim.Allow("new-tracker.example"), "the SiteLink host was not paced")

	// A current link, and a generic Indexer, keep their own host.
	spec.BaseURL = "https://mirror.example"
	require.Equal(t, "mirror.example", rateKey(spec, def))
	require.Equal(t, "mirror.example", rateKey(spec, nil))
}

func TestApplyRateLimitIsRaisedToTheDefinitionsDelay(t *testing.T) {
	lim := ratelimit.New(ratelimit.Config{})
	spec := indexv1alpha1.IndexerSpec{BaseURL: "https://t.example", RequestDelay: &metav1.Duration{Duration: 0}}
	applyRateLimit(spec, nil, lim, definitionDelay(5))
	key := ratelimit.HostKey(spec.BaseURL)
	require.True(t, lim.Allow(key))
	require.False(t, lim.Allow(key), "a definition's 5s requestDelay did not pace a 0s spec")
}

func TestClassifyLogin(t *testing.T) {
	require.Equal(t, probeOutcome{}, classifyLogin(nil))
	o := classifyLogin(&cardigann.LoginError{Message: "bad password"})
	require.True(t, o.AuthFailed)
	require.Equal(t, ReasonCredentialsRejected, o.Reason)
	o = classifyLogin(&cardigann.CaptchaRequiredError{Type: "image"})
	require.True(t, o.AuthFailed)
	require.Equal(t, ReasonCaptchaRequired, o.Reason)
	o = classifyLogin(errors.New("dial tcp: refused"))
	require.False(t, o.AuthFailed)
	require.Equal(t, ReasonProbeFailed, o.Reason)
}

func TestSessionStoreIgnoresASecretOwnedByAnotherIndexer(t *testing.T) {
	sess, err := cardigann.MarshalSession(&cardigann.Session{Cookies: []*http.Cookie{{Name: "a", Value: "b"}}})
	require.NoError(t, err)
	idx := &indexv1alpha1.Indexer{ObjectMeta: metav1.ObjectMeta{Name: "t", Namespace: "media", UID: "new-uid"}}
	c := fakeClient(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "t-session", Namespace: "media",
			OwnerReferences: []metav1.OwnerReference{{UID: "old-uid", Name: "t", Kind: "Indexer", APIVersion: "index.clustarr.io/v1alpha1"}},
		},
		Data: map[string][]byte{SessionSecretKeySession: sess},
	})
	got, err := NewSessionStore(c, nil).Load(context.Background(), idx)
	require.NoError(t, err)
	require.Nil(t, got, "a recreated Indexer inherited its predecessor's login")

	idx.UID = "old-uid"
	got, err = NewSessionStore(c, nil).Load(context.Background(), idx)
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, "a=b", got.CookieHeader())
}
