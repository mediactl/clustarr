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

package clients

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
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
	idxstatus "github.com/mediactl/clustarr/app/indexer/status"
	"github.com/mediactl/clustarr/pkg/cardigann"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/ratelimit"
	"github.com/mediactl/clustarr/pkg/torznab"
)

func cardigannFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "test", "data", "cardigann", name))
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
	require.Equal(t, string(indexv1alpha1.DefinitionTypePublic), DefinitionPrivacy["public"])
	require.Equal(t, string(indexv1alpha1.DefinitionTypeSemiPrivate), DefinitionPrivacy["semi-private"])
	require.Equal(t, string(indexv1alpha1.DefinitionTypePrivate), DefinitionPrivacy["private"])
	require.Len(t, DefinitionPrivacy, 3)
}

// Cardigann's mode names must reach status.caps as Torznab's wire values,
// read back through the SAME predicate the fan-out gates on. A "tv-search"
// key would match nothing and the indexer would never be queried.
func TestDefinitionCapsUseTheTorznabModeVocabulary(t *testing.T) {
	def, err := cardigann.Load([]byte(cardigannFixture(t, "search-error.yml")))
	require.NoError(t, err)
	caps := DefinitionCaps(def)

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
	caps := DefinitionCaps(def)
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

	def, err := ResolveDefinition(ctx, c, indexv1alpha1.IndexerSpec{DefinitionRef: ptr.To("by-name")})
	require.NoError(t, err)
	require.Equal(t, "synthetic-search-error", def.ID)

	// spec.definition resolves through spec.replaces first ...
	def, err = ResolveDefinition(ctx, c, indexv1alpha1.IndexerSpec{Definition: ptr.To("1337x")})
	require.NoError(t, err)
	require.Equal(t, "synthetic-search-error", def.ID)

	// ... and through a parsed status.id second.
	def, err = ResolveDefinition(ctx, c, indexv1alpha1.IndexerSpec{Definition: ptr.To("synthetic-search-error")})
	require.NoError(t, err)
	require.NotNil(t, def)

	_, err = ResolveDefinition(ctx, c, indexv1alpha1.IndexerSpec{Definition: ptr.To("unknown")})
	require.ErrorIs(t, err, ErrDefinitionNotFound)
	_, err = ResolveDefinition(ctx, c, indexv1alpha1.IndexerSpec{DefinitionRef: ptr.To("missing")})
	require.ErrorIs(t, err, ErrDefinitionNotFound)
	_, err = ResolveDefinition(ctx, c, indexv1alpha1.IndexerSpec{DefinitionRef: ptr.To("broken")})
	require.ErrorIs(t, err, ErrDefinitionInvalid)
	require.LessOrEqual(t, len(err.Error()), maxDefinitionErr+200, "a schema error must fit a condition message")
}

// X15: a definition's Cardigann `replaces` ids (status.replaces) are aliases.
// An Indexer written against a tracker's retired id resolves to the
// definition that replaced it, as Jackett's GetIndexer resolves a renamed
// indexer -- and only as a last resort: a definition whose own id or
// spec.replaces claims the id keeps it, and between two alias claims the
// first by name wins, on every replica.
func TestResolveDefinitionThroughReplacedIDs(t *testing.T) {
	yaml := cardigannFixture(t, "search-error.yml")
	renamed := idxDefinition("b-renamed", yaml, nil, "synthetic-search-error")
	renamed.Status.Replaces = []string{"old-tracker", "older-tracker"}
	second := idxDefinition("c-also-claims", "id: nope\n", nil, "another-id")
	second.Status.Replaces = []string{"old-tracker"}
	ctx := context.Background()

	c := fakeClient(t, renamed, second)
	for _, old := range []string{"old-tracker", "older-tracker"} {
		def, err := ResolveDefinition(ctx, c, indexv1alpha1.IndexerSpec{Definition: ptr.To(old)})
		require.NoError(t, err, "the retired id %q must resolve", old)
		require.Equal(t, "synthetic-search-error", def.ID, "%q resolved to the wrong definition", old)
	}

	// A definition that IS the id outranks one that claims to replace it.
	live := idxDefinition("z-live", "id: nope\n", nil, "old-tracker")
	d, err := definitionByID(ctx, fakeClient(t, renamed, live), "old-tracker")
	require.NoError(t, err)
	require.Equal(t, "z-live", d.Name, "status.id must win over status.replaces")

	// So does an explicit override.
	override := idxDefinition("z-override", "id: nope\n", ptr.To("older-tracker"), "x")
	d, err = definitionByID(ctx, fakeClient(t, renamed, override), "older-tracker")
	require.NoError(t, err)
	require.Equal(t, "z-override", d.Name, "spec.replaces must win over status.replaces")
}

// Every IndexerProxy type now yields a route rather than a refusal: socks4
// through app/indexer/proxy's own dialer and flaresolverr as the outer
// challenge-solving layer. Before, both were ErrProxyUnavailable.
func TestEveryProxyTypeResolves(t *testing.T) {
	for _, typ := range []indexv1alpha1.IndexerProxyType{
		indexv1alpha1.IndexerProxyTypeHTTP, indexv1alpha1.IndexerProxyTypeSocks4,
		indexv1alpha1.IndexerProxyTypeSocks5, indexv1alpha1.IndexerProxyTypeFlareSolverr,
	} {
		c := fakeClient(t, &indexv1alpha1.IndexerProxy{
			ObjectMeta: metav1.ObjectMeta{Name: "egress", Namespace: "media"},
			Spec:       indexv1alpha1.IndexerProxySpec{Type: typ, Host: "proxy.local", Port: 1080},
		})
		idx := &indexv1alpha1.Indexer{
			ObjectMeta: metav1.ObjectMeta{Name: "t", Namespace: "media"},
			Spec:       indexv1alpha1.IndexerSpec{ProxyRef: ptr.To("egress")},
		}
		rt, err := ResolveProxy(context.Background(), c, idx)
		require.NoError(t, err, "%s", typ)
		require.NotNil(t, rt, "%s must route, never be bypassed", typ)
	}
}

// The limiter is read onto the engine under ratelimit.HostKey(spec.baseURL),
// the key ApplyRateLimit writes. A different spelling is not "paced twice as
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
	ApplyRateLimit(spec, def, lim, 0)
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
	ApplyRateLimit(spec, nil, lim, DefinitionDelay(5))
	key := ratelimit.HostKey(spec.BaseURL)
	require.True(t, lim.Allow(key))
	require.False(t, lim.Allow(key), "a definition's 5s requestDelay did not pace a 0s spec")
}

func TestClassifyLogin(t *testing.T) {
	require.Equal(t, ProbeOutcome{}, ClassifyLogin(nil))
	o := ClassifyLogin(&cardigann.LoginError{Message: "bad password"})
	require.True(t, o.AuthFailed)
	require.Equal(t, ReasonCredentialsRejected, o.Reason)
	o = ClassifyLogin(&cardigann.CaptchaRequiredError{Type: "image", Selector: "img.captcha"})
	require.True(t, o.AuthFailed)
	require.Equal(t, ReasonCaptchaRequired, o.Reason)
	require.Contains(t, o.Message, `an image captcha ("img.captcha")`, "the condition names the captcha")
	require.Contains(t, o.Message, `Indexer Secret's "cookie" key`, "and the manual-cookie workaround")
	o = ClassifyLogin(errors.New("dial tcp: refused"))
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
