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
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	idxclients "github.com/mediactl/clustarr/app/indexer/clients"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/cardigann"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/ratelimit"
	"github.com/mediactl/clustarr/pkg/torznab"
)

func cardigannFixture(t *testing.T, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "test", "data", "cardigann", name))
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

// The watch must carry an alias too: an Indexer naming a retired id is
// re-reconciled when the definition that replaces it changes, not only on
// the one-minute DefinitionNotFound retry.
func TestIndexersForDefinitionFollowsReplacedIDs(t *testing.T) {
	def := idxDefinition("renamed", "", nil, "new-tracker")
	def.Status.Replaces = []string{"old-tracker"}
	byID := func(name, id string) *indexv1alpha1.Indexer {
		return &indexv1alpha1.Indexer{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media"},
			Spec:       indexv1alpha1.IndexerSpec{Definition: ptr.To(id)},
		}
	}
	c := fakeClient(t, byID("by-old", "old-tracker"), byID("by-new", "new-tracker"), byID("unrelated", "other"))
	r := &Reconciler{Client: c}
	var names []string
	for _, req := range r.indexersForDefinition(context.Background(), def) {
		names = append(names, req.Name)
	}
	require.ElementsMatch(t, []string{"by-old", "by-new"}, names)

	// And the predicate sees a status.replaces change as a change.
	moved := def.DeepCopy()
	moved.Status.Replaces = []string{"old-tracker", "oldest-tracker"}
	require.NotEqual(t, definitionIDs(def), definitionIDs(moved))
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

	cli, err := buildWireClient(context.Background(), c, idx, nil, idxclients.NewSessionStore(c, nil))
	require.NoError(t, err)
	rels, err := cli.Search(context.Background(), torznab.Query{Type: torznab.ModeSearch, Q: "movie"})
	require.NoError(t, err)
	require.Len(t, rels, 2)
	require.Equal(t, 1, proxied)
	require.Zero(t, direct, "the search bypassed the proxy and reached the tracker directly")

	// A proxy that does not exist fails closed.
	idx.Spec.ProxyRef = ptr.To("absent")
	_, err = buildWireClient(context.Background(), c, idx, nil, idxclients.NewSessionStore(c, nil))
	require.ErrorIs(t, err, idxclients.ErrProxyUnavailable)
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
	_, isCardigann := cli.(*idxclients.CardigannClient)
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
