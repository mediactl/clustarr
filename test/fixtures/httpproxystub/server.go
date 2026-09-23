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

// Package httpproxystub is a minimal plain-HTTP forward proxy, for scenario
// 10's "an IndexerProxy on the HTTP path" (docs/superpowers/plans/
// 2026-09-18-remaining-work.md, scenario 10).
//
// indexarr/controller/indexer/proxy.go's resolveProxy builds an
// IndexerProxyTypeHTTP proxy as plain net/http.Transport.Proxy =
// http.ProxyURL(u) -- Go's own standard forward-proxy client behaviour. For
// an http:// target (test/fixtures/cardigannstub is plain HTTP; the e2e
// cluster has no TLS-terminating egress to tunnel through anyway) that
// means the proxy receives an ordinary request whose request line carries
// the ABSOLUTE target URI (RFC 7230 §5.3.2), which net/http's own server
// parses into a fully-populated r.URL (scheme, host and path all set) --
// no CONNECT tunnelling is involved, so this fixture does not implement
// CONNECT.
//
// It never talks to the Internet outside the cluster; every request this
// stub forwards stays inside it (Cardigann fixture, other in-cluster stubs).
package httpproxystub

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync"
)

// hopByHopHeaders are stripped before forwarding, per RFC 7230 §6.1 -- the
// same list net/http/httputil.ReverseProxy strips, restated here because
// this fixture does not use ReverseProxy (it needs the absolute-URI request
// line, which ReverseProxy's Director model does not fit as directly as a
// plain RoundTrip call).
var hopByHopHeaders = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate",
	"Proxy-Authorization", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// Server is a forward proxy that records every target URL it forwards, for
// GET /_proxied to report back to a test.
type Server struct {
	logger *slog.Logger

	mu      sync.Mutex
	proxied []string
}

// NewHandler builds the stub.
func NewHandler(logger *slog.Logger) http.Handler {
	s := &Server{logger: logger}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_proxied", s.handleProxied)
	mux.HandleFunc("/", s.handleForward)
	return mux
}

// handleForward re-issues r against its own absolute URL and copies the
// response back verbatim -- the whole of what a plain-HTTP forward proxy
// does.
func (s *Server) handleForward(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		s.logger.Warn("httpproxystub: CONNECT is not supported by this fixture", "host", r.Host)
		http.Error(w, "CONNECT not supported by this fixture", http.StatusNotImplemented)
		return
	}
	if !r.URL.IsAbs() {
		s.logger.Warn("httpproxystub: request is not proxy-shaped (no absolute URI)", "path", r.URL.Path)
		http.Error(w, "not a proxy request: no absolute URI", http.StatusBadRequest)
		return
	}

	target := r.URL.String()
	s.mu.Lock()
	s.proxied = append(s.proxied, target)
	s.mu.Unlock()
	s.logger.Info("httpproxystub: forwarding", "target", target)

	outReq := r.Clone(r.Context())
	outReq.RequestURI = "" // http.Client refuses a request with RequestURI set
	for _, h := range hopByHopHeaders {
		outReq.Header.Del(h)
	}

	resp, err := http.DefaultTransport.RoundTrip(outReq)
	if err != nil {
		s.logger.Warn("httpproxystub: forward failed", "target", target, "error", err)
		http.Error(w, "proxy: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// handleProxied answers a JSON array of every target URL forwarded so far,
// in order -- the request log an e2e test polls to prove the Indexer's
// login and search traffic actually left through this proxy rather than
// dialling cardigann-stub directly.
func (s *Server) handleProxied(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	proxied := append([]string{}, s.proxied...)
	s.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(proxied)
}
