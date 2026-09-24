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

// Package proxy routes an Indexer's requests through the IndexerProxies that
// apply to it.
//
// [Select] decides which apply -- spec.proxyRef explicitly, then every proxy
// whose spec.selector matches the Indexer's labels, at most one route and at
// most one FlareSolverr -- and [Resolve] builds the one http.RoundTripper
// every path uses: the caps probe and login, the search fan-out, the RSS poll
// and both download fetchers. A route is an http or socks5 proxy through
// net/http's own proxy support, or socks4 through [Socks4Dialer], which
// net/http lacks. A FlareSolverr proxy wraps whatever the route produced as
// [FlareSolverr], applied last, which answers Cloudflare and DDoS-Guard
// challenges the way Prowlarr's FlareSolverr indexer proxy does.
//
// Every failure fails CLOSED with [ErrUnavailable]: no path in this package
// ever falls back to a direct connection when a proxy was meant to apply.
//
// It lives in its own package, not in indexarr/controller/indexer where the
// proxyRef half used to be, because indexarr/download needs it too and that
// controller imports indexarr/download: the generic download fetcher had no
// way to reach the proxy at all, and fetched every .torrent directly.
//
// The RBAC markers below are package-level, which is the only place
// controller-gen collects them from. Reading IndexerProxies (cached) and
// their credential Secrets (live Gets; indexarr disables the Secret cache)
// is all this package does.
//
// +kubebuilder:rbac:groups=index.clustarr.io,resources=indexerproxies,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
package proxy
