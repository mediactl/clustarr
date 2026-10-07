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

package extmetrics

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// The APIService the installers render and the manager injects into.
const APIServiceName = "v1beta1.external.metrics.k8s.io"

// APIServiceGVK is read as unstructured, so k8s.io/kube-aggregator is not
// linked.
var APIServiceGVK = schema.GroupVersionKind{Group: "apiregistration.k8s.io", Version: "v1", Kind: "APIService"}

// DefaultReload is how old the loaded TLS material may get before the next
// handshake re-reads it.
const DefaultReload = 60 * time.Second

// ServerOptions configures the API server.
type ServerOptions struct {
	Namespace     string               // the manager's; the only one served
	BindAddress   string               // --external-metrics-bind-address; "0" means disabled
	SecretName    string               // --external-metrics-secret
	AuthConfigMap types.NamespacedName // zero means kube-system/extension-apiserver-authentication
	Series        []Series
	Reload        time.Duration // 0 means DefaultReload
	Now           func() time.Time
	// Cache is the read path shared with QueueGauge; nil builds the
	// handler's own (split §9.4 as amended 2026-10-07).
	Cache *StateCache
}

// Server is the TLS listener for external.metrics.k8s.io. It needs only
// an API reader (the manager's APIReader, never the cache) and the
// ConsumerStater, so it can serve before the caches sync.
type Server struct {
	opts    ServerOptions
	reader  client.Reader
	handler http.Handler

	mu       sync.Mutex // serialises reloads
	current  atomic.Pointer[material]
	lastTry  time.Time
	listener net.Listener
}

type material struct {
	cert     tls.Certificate
	auth     AuthConfig
	loadedAt time.Time
}

// NewServer builds the server; Runnable binds it.
func NewServer(o ServerOptions, reader client.Reader, states ConsumerStater) (*Server, error) {
	switch {
	case o.Namespace == "":
		return nil, errors.New("extmetrics: a namespace is required")
	case o.BindAddress == "" || o.BindAddress == "0":
		return nil, errors.New("extmetrics: the External Metrics API is disabled (--external-metrics-bind-address)")
	case o.SecretName == "":
		return nil, errors.New("extmetrics: a TLS Secret name is required")
	}
	if o.AuthConfigMap == (types.NamespacedName{}) {
		o.AuthConfigMap = types.NamespacedName{Namespace: AuthConfigMapNamespace, Name: AuthConfigMapName}
	}
	s := &Server{opts: o, reader: reader}
	api := &Handler{Namespace: o.Namespace, Series: o.Series, Cache: o.Cache, States: states, Now: o.Now}
	s.handler = FrontProxyAuth(func() *AuthConfig {
		m := s.current.Load()
		if m == nil {
			return nil
		}
		return &m.auth
	}, api)
	return s, nil
}

// Runnable binds the TLS listener and returns the server as a
// *manager.Server: controller-runtime's runnables.Add puts exactly that type
// in the HTTP-servers group, which starts before the caches
// (pkg/manager/runnable_group.go:86-90, internal.go:426-446). Any other
// runnable type would wait out the cache sync.
func (s *Server) Runnable() (*manager.Server, error) {
	if s.listener == nil {
		ln, err := net.Listen("tcp", s.opts.BindAddress)
		if err != nil {
			return nil, fmt.Errorf("extmetrics: listen on %s: %w", s.opts.BindAddress, err)
		}
		s.listener = tls.NewListener(ln, s.TLSConfig())
	}
	shutdown := 10 * time.Second
	return &manager.Server{
		Name: "external-metrics",
		Server: &http.Server{
			Handler:           s.handler,
			ReadHeaderTimeout: 10 * time.Second,
			IdleTimeout:       90 * time.Second,
			ErrorLog:          slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug),
		},
		Listener:        s.listener,
		ShutdownTimeout: &shutdown,
	}, nil
}

// Addr is the bound address, once Runnable has run.
func (s *Server) Addr() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

// TLSConfig resolves the certificate and the client CAs per handshake, so
// a reload takes effect on the next connection. Session tickets are off,
// so a rotated front-proxy CA is enforced on every connection.
func (s *Server) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			m, err := s.material(hello.Context())
			if err != nil {
				return nil, err
			}
			return &tls.Config{
				MinVersion:             tls.VersionTLS12,
				NextProtos:             []string{"http/1.1"},
				Certificates:           []tls.Certificate{m.cert},
				ClientAuth:             tls.VerifyClientCertIfGiven,
				ClientCAs:              m.auth.ClientCAs,
				SessionTicketsDisabled: true,
			}, nil
		},
	}
}

// material returns the loaded serving pair and front-proxy trust,
// re-reading both when they are older than Reload. A failed reload keeps
// the previous material; with none loaded the handshake is refused, at
// most one read per second.
func (s *Server) material(ctx context.Context) (*material, error) {
	now := s.now()
	if m := s.current.Load(); m != nil && now.Sub(m.loadedAt) < s.reload() {
		return m, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	m := s.current.Load()
	if m != nil && now.Sub(m.loadedAt) < s.reload() {
		return m, nil
	}
	if m == nil && !s.lastTry.IsZero() && now.Sub(s.lastTry) < time.Second {
		return nil, errors.New("extmetrics: the serving certificate and front-proxy CA are not loaded yet")
	}
	s.lastTry = now
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	fresh, err := s.load(ctx)
	if err != nil {
		if m != nil {
			logging.FromContext(ctx).Warn("extmetrics: reloading the TLS material failed; serving the previous one", "error", err)
			kept := *m
			kept.loadedAt = now
			s.current.Store(&kept)
			return &kept, nil
		}
		return nil, err
	}
	fresh.loadedAt = now
	s.current.Store(fresh)
	return fresh, nil
}

func (s *Server) load(ctx context.Context) (*material, error) {
	var sec corev1.Secret
	if err := s.reader.Get(ctx, types.NamespacedName{Namespace: s.opts.Namespace, Name: s.opts.SecretName}, &sec); err != nil {
		return nil, fmt.Errorf("extmetrics: read Secret %s/%s: %w", s.opts.Namespace, s.opts.SecretName, err)
	}
	cert, err := tls.X509KeyPair(sec.Data[corev1.TLSCertKey], sec.Data[corev1.TLSPrivateKeyKey])
	if err != nil {
		return nil, fmt.Errorf("extmetrics: Secret %s holds no serving pair: %w", s.opts.SecretName, err)
	}
	var cm corev1.ConfigMap
	if err := s.reader.Get(ctx, s.opts.AuthConfigMap, &cm); err != nil {
		return nil, fmt.Errorf("extmetrics: read ConfigMap %s: %w", s.opts.AuthConfigMap, err)
	}
	auth, err := ParseAuthConfig(cm.Data)
	if err != nil {
		return nil, err
	}
	return &material{cert: cert, auth: auth}, nil
}

func (s *Server) now() time.Time {
	if s.opts.Now != nil {
		return s.opts.Now()
	}
	return time.Now()
}

func (s *Server) reload() time.Duration {
	if s.opts.Reload > 0 {
		return s.opts.Reload
	}
	return DefaultReload
}
