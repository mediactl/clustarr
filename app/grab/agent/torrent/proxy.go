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

package torrent

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/healthz"

	downloadv1alpha1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	dltorrent "github.com/mediactl/clustarr/pkg/download/torrent"
	"github.com/mediactl/clustarr/pkg/socks5"
)

// torrentProxy builds the torrent engine's proxy from
// dc.spec.torrent.proxy, or nil without one. The Secret it names is read
// once, here, by name (the engine rolls when its data changes); a missing
// one fails the start rather than running unauthenticated. A proxy named by
// host is resolved now, before net.DefaultResolver may be pointed through
// the proxy itself. warn receives what the engine runs without.
func torrentProxy(ctx context.Context, reader client.Reader, dc *downloadv1alpha1.DownloadClient, warn func(reason, message string)) (*dltorrent.ProxyConfig, error) {
	if dc.Spec.Torrent == nil || dc.Spec.Torrent.Proxy == nil {
		return nil, nil
	}
	spec := dc.Spec.Torrent.Proxy
	host := spec.Host
	if net.ParseIP(host) == nil {
		// IPv4 first: a SOCKS proxy is nearly always reached over it.
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
		if err != nil || len(ips) == 0 {
			ips, err = net.DefaultResolver.LookupIP(ctx, "ip", host)
		}
		if err != nil || len(ips) == 0 {
			return nil, fmt.Errorf("grabarr: resolve proxy host %q: %w", host, err)
		}
		host = ips[0].String()
	}
	p := socks5.Proxy{Addr: net.JoinHostPort(host, strconv.Itoa(int(spec.Port)))}
	if spec.SecretRef != nil {
		var s corev1.Secret
		if err := reader.Get(ctx, types.NamespacedName{Namespace: dc.Namespace, Name: spec.SecretRef.Name}, &s); err != nil {
			return nil, fmt.Errorf("grabarr: read proxy Secret %s/%s: %w", dc.Namespace, spec.SecretRef.Name, err)
		}
		p.Username, p.Password = string(s.Data["username"]), string(s.Data["password"])
	}
	return &dltorrent.ProxyConfig{
		Proxy:           p,
		PeerConnections: spec.PeerConnectionsOrDefault(),
		UDP:             spec.UDPOrDefault(),
		Warn:            warn,
	}, nil
}

// proxyReadiness is the engine's readiness with a proxy: its own check,
// then a SOCKS5 greeting, so an unreachable proxy reads as EngineReady
// False on the DownloadClient rather than as a healthy engine moving
// nothing.
func proxyReadiness(engine healthz.Checker, pcfg *dltorrent.ProxyConfig) healthz.Checker {
	if pcfg == nil {
		return engine
	}
	return func(req *http.Request) error {
		if err := engine(req); err != nil {
			return err
		}
		return pcfg.Proxy.Ping(req.Context())
	}
}

// proxyHTTPClient is the engine's client for .torrent fetches through the
// proxy, or nil (http.DefaultClient) without one.
func proxyHTTPClient(pcfg *dltorrent.ProxyConfig) *http.Client {
	if pcfg == nil {
		return nil
	}
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(pcfg.Proxy.URL())}}
}
