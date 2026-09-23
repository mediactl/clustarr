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

	"sigs.k8s.io/controller-runtime/pkg/client"

	indexv1alpha1 "github.com/mediactl/clustarr/api/index/v1alpha1"
	"github.com/mediactl/clustarr/indexarr/proxy"
)

// ErrProxyUnavailable is returned when the IndexerProxies that apply to an
// Indexer cannot be routed through: one that does not exist, whose
// credentials are missing, whose selector cannot be evaluated, or two
// competing for one slot. It is indexarr/proxy's ErrUnavailable, re-exported
// so errors.Is works from either name.
//
// It is an ERROR, deliberately, never a silent fall back to a direct
// connection. An operator names a proxy to keep the cluster's real address
// away from a tracker; quietly bypassing it on a misconfiguration is the one
// outcome worse than failing the search.
var ErrProxyUnavailable = proxy.ErrUnavailable

// resolveProxy builds the transport for the proxies that apply to idx --
// spec.proxyRef and every IndexerProxy whose spec.selector matches its labels
// -- or returns nil for an Indexer none apply to. http, socks4 and socks5
// routes and a FlareSolverr applied last are all supported; see
// indexarr/proxy.
//
// It is used by the ONE builder shared by the caps probe, the search
// fan-out, the RSS poll and the download verb (the generic download fetcher
// calls indexarr/proxy.Resolve itself), so the proxy is applied to all four
// or to none -- a probe that honoured the proxy while searches bypassed it
// would report the proxy healthy while leaking the real IP.
func resolveProxy(ctx context.Context, c client.Client, idx *indexv1alpha1.Indexer) (http.RoundTripper, error) {
	return proxy.Resolve(ctx, c, idx)
}
