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
	"context"
	"net/http"
	"net/url"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func pxy(name string, typ indexv1alpha1.IndexerProxyType, sel map[string]string) indexv1alpha1.IndexerProxy {
	return indexv1alpha1.IndexerProxy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media", ResourceVersion: "7"},
		Spec: indexv1alpha1.IndexerProxySpec{
			Type: typ, Host: name + ".proxy", Port: 1080,
			Selector: metav1.LabelSelector{MatchLabels: sel},
		},
	}
}

func idxWith(labels map[string]string, ref string) *indexv1alpha1.Indexer {
	idx := &indexv1alpha1.Indexer{ObjectMeta: metav1.ObjectMeta{Name: "tr", Namespace: "media", Labels: labels}}
	if ref != "" {
		idx.Spec.ProxyRef = ptr.To(ref)
	}
	return idx
}

func TestSelect(t *testing.T) {
	private := map[string]string{"privacy": "private"}
	httpA := pxy("a-http", indexv1alpha1.IndexerProxyTypeHTTP, private)
	socksB := pxy("b-socks", indexv1alpha1.IndexerProxyTypeSocks5, private)
	flareC := pxy("c-flare", indexv1alpha1.IndexerProxyTypeFlareSolverr, private)
	flareD := pxy("d-flare", indexv1alpha1.IndexerProxyTypeFlareSolverr, private)
	everyone := pxy("e-empty", indexv1alpha1.IndexerProxyTypeHTTP, nil)
	other := pxy("f-other", indexv1alpha1.IndexerProxyTypeSocks4, map[string]string{"privacy": "public"})

	name := func(p *indexv1alpha1.IndexerProxy) string {
		if p == nil {
			return ""
		}
		return p.Name
	}
	tests := []struct {
		name         string
		idx          *indexv1alpha1.Indexer
		proxies      []indexv1alpha1.IndexerProxy
		route, flare string
		wantErr      bool
	}{
		{"nothing applies", idxWith(nil, ""), []indexv1alpha1.IndexerProxy{httpA, other}, "", "", false},
		{"an empty selector matches nothing", idxWith(private, ""), []indexv1alpha1.IndexerProxy{everyone}, "", "", false},
		{"a selector match is the route", idxWith(private, ""), []indexv1alpha1.IndexerProxy{httpA, other}, "a-http", "", false},
		{"the route and a flaresolverr together", idxWith(private, ""), []indexv1alpha1.IndexerProxy{httpA, flareC}, "a-http", "c-flare", false},
		{"two routes by selector is ambiguous", idxWith(private, ""), []indexv1alpha1.IndexerProxy{httpA, socksB}, "", "", true},
		{"proxyRef settles two routes", idxWith(private, "b-socks"), []indexv1alpha1.IndexerProxy{httpA, socksB}, "b-socks", "", false},
		{"two flaresolverrs is the CRD's own refusal", idxWith(private, ""), []indexv1alpha1.IndexerProxy{flareC, flareD}, "", "", true},
		{"proxyRef naming a flaresolverr takes that slot", idxWith(nil, "d-flare"), []indexv1alpha1.IndexerProxy{flareD, other}, "", "d-flare", false},
		{"proxyRef wins its slot over a selector match", idxWith(private, "d-flare"), []indexv1alpha1.IndexerProxy{httpA, flareC, flareD}, "a-http", "d-flare", false},
		{"a proxyRef naming nothing fails closed", idxWith(nil, "absent"), []indexv1alpha1.IndexerProxy{httpA}, "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sel, err := Select(tt.idx, tt.proxies)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrUnavailable)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.route, name(sel.Route))
			require.Equal(t, tt.flare, name(sel.FlareSolver))
		})
	}

	bad := pxy("g-bad", indexv1alpha1.IndexerProxyTypeHTTP, nil)
	bad.Spec.Selector.MatchExpressions = []metav1.LabelSelectorRequirement{{Key: "privacy", Operator: "Sometimes"}}
	_, err := Select(idxWith(private, ""), []indexv1alpha1.IndexerProxy{bad})
	require.ErrorIs(t, err, ErrUnavailable, "a selector that cannot be evaluated must fail closed, not match nothing")

	fp1, _ := Select(idxWith(private, ""), []indexv1alpha1.IndexerProxy{httpA})
	httpA.ResourceVersion = "8"
	fp2, _ := Select(idxWith(private, ""), []indexv1alpha1.IndexerProxy{httpA})
	require.NotEqual(t, fp1.Fingerprint(), fp2.Fingerprint(), "an edited proxy must change the fingerprint")
}

func TestRouteTransport(t *testing.T) {
	base := &http.Transport{}
	spec := indexv1alpha1.IndexerProxySpec{Type: indexv1alpha1.IndexerProxyTypeHTTP, Host: "proxy.local", Port: 3128}
	tr, plain, err := RouteTransport(base, spec, map[string][]byte{"username": []byte("u"), "password": []byte("p")})
	require.NoError(t, err)
	u, err := tr.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "t.example"}})
	require.NoError(t, err)
	require.Equal(t, "http://u:p@proxy.local:3128", u.String())
	require.Empty(t, plain, "a credentialled route cannot be handed to FlareSolverr")

	spec = indexv1alpha1.IndexerProxySpec{Type: indexv1alpha1.IndexerProxyTypeSocks5, Host: "10.0.0.1", Port: 1080}
	tr, plain, err = RouteTransport(base, spec, nil)
	require.NoError(t, err)
	u, err = tr.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "t.example"}})
	require.NoError(t, err)
	require.Equal(t, "socks5://10.0.0.1:1080", u.String())
	require.Equal(t, "socks5://10.0.0.1:1080", plain)

	spec = indexv1alpha1.IndexerProxySpec{Type: indexv1alpha1.IndexerProxyTypeSocks4, Host: "10.0.0.2", Port: 1080}
	tr, plain, err = RouteTransport(base, spec, nil)
	require.NoError(t, err)
	require.Nil(t, tr.Proxy, "socks4 is a dialer, not an http.ProxyURL")
	require.NotNil(t, tr.DialContext)
	require.Equal(t, "socks4://10.0.0.2:1080", plain)

	_, _, err = RouteTransport(base, indexv1alpha1.IndexerProxySpec{Type: indexv1alpha1.IndexerProxyTypeFlareSolverr, Host: "h", Port: 1}, nil)
	require.ErrorIs(t, err, ErrUnavailable)
}

func TestBuild(t *testing.T) {
	ctx := context.Background()
	creds := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "creds", Namespace: "media"},
		Data:       map[string][]byte{"username": []byte("u"), "password": []byte("p")},
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(creds).Build()

	rt, err := Build(ctx, c, Selection{})
	require.NoError(t, err)
	require.Nil(t, rt, "no proxy is a direct connection, which is the caller's own default")

	route := pxy("route", indexv1alpha1.IndexerProxyTypeSocks5, nil)
	flare := pxy("flare", indexv1alpha1.IndexerProxyTypeFlareSolverr, nil)
	flare.Spec.Port = 8191
	rt, err = Build(ctx, c, Selection{Route: &route, FlareSolver: &flare})
	require.NoError(t, err)
	fs, ok := rt.(*FlareSolverr)
	require.True(t, ok, "FlareSolverr is applied last, outermost")
	require.Equal(t, "http://flare.proxy:8191/v1", fs.Endpoint)
	require.Equal(t, "socks5://route.proxy:1080", fs.Proxy)
	_, ok = fs.Next.(*http.Transport)
	require.True(t, ok, "and wraps the route")

	route.Spec.SecretRef = &corev1.LocalObjectReference{Name: "creds"}
	_, err = Build(ctx, c, Selection{Route: &route, FlareSolver: &flare})
	require.ErrorIs(t, err, ErrUnavailable, "a solve must not go direct around a credentialled route")

	route.Spec.SecretRef = &corev1.LocalObjectReference{Name: "absent"}
	_, err = Build(ctx, c, Selection{Route: &route})
	require.ErrorIs(t, err, ErrUnavailable)
}
