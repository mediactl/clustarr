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
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
)

// ErrProxyUnavailable is returned when spec.proxyRef names a proxy this
// process cannot route through: one that does not exist, whose credentials
// are missing, or whose type has no transport yet.
//
// It is an ERROR, deliberately, never a silent fall back to a direct
// connection. An operator names a proxy to keep the cluster's real address
// away from a tracker; quietly bypassing it on a misconfiguration is the one
// outcome worse than failing the search.
var ErrProxyUnavailable = errors.New("indexer: indexer proxy unavailable")

// resolveProxy builds the transport for spec.proxyRef, or returns nil for an
// Indexer that names no proxy.
//
// http and socks5 are routed through net/http's own proxy support
// (http.ProxyURL understands both schemes), with the IndexerProxy Secret's
// username/password keys as the proxy credentials. socks4 and flaresolverr
// are refused with ErrProxyUnavailable: net/http has no socks4 dialer and
// the FlareSolverr client does not exist yet, and refusing is the only
// answer that does not leak the direct address.
//
// It is called by the ONE builder shared by the caps probe, the search
// fan-out, the RSS poll and the download verb, so the proxy is applied to
// all four or to none -- a probe that honoured the proxy while searches
// bypassed it would report the proxy healthy while leaking the real IP.
//
// spec.selector-based matching (an IndexerProxy selecting Indexers by label,
// "at most one FlareSolverr, applied last") is NOT implemented: only the
// explicit spec.proxyRef is. See this package's doc.
func resolveProxy(ctx context.Context, c client.Client, idx *indexv1alpha1.Indexer) (http.RoundTripper, error) {
	if idx.Spec.ProxyRef == nil || *idx.Spec.ProxyRef == "" {
		return nil, nil
	}
	var p indexv1alpha1.IndexerProxy
	key := types.NamespacedName{Namespace: idx.Namespace, Name: *idx.Spec.ProxyRef}
	if err := c.Get(ctx, key, &p); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: IndexerProxy %s does not exist", ErrProxyUnavailable, key)
		}
		return nil, err
	}
	creds, err := readSecret(ctx, c, p.Namespace, p.Spec.SecretRef)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrProxyUnavailable, err)
	}
	u, err := proxyURL(p.Spec, creds)
	if err != nil {
		return nil, err
	}
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("%w: the default transport is not an *http.Transport", ErrProxyUnavailable)
	}
	tr := base.Clone()
	tr.Proxy = http.ProxyURL(u)
	return tr, nil
}

// proxyURL renders an IndexerProxy as the URL http.ProxyURL wants.
func proxyURL(spec indexv1alpha1.IndexerProxySpec, creds map[string][]byte) (*url.URL, error) {
	var scheme string
	switch spec.Type {
	case indexv1alpha1.IndexerProxyTypeHTTP:
		scheme = "http"
	case indexv1alpha1.IndexerProxyTypeSocks5:
		scheme = "socks5"
	default:
		return nil, fmt.Errorf("%w: proxy type %q has no transport yet", ErrProxyUnavailable, spec.Type)
	}
	if spec.Host == "" {
		return nil, fmt.Errorf("%w: proxy has no host", ErrProxyUnavailable)
	}
	host := spec.Host
	if spec.Port > 0 {
		host = net.JoinHostPort(spec.Host, strconv.Itoa(int(spec.Port)))
	}
	u := &url.URL{Scheme: scheme, Host: host}
	if user := string(creds["username"]); user != "" {
		u.User = url.UserPassword(user, string(creds["password"]))
	}
	return u, nil
}
