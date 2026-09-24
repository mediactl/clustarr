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

package facade

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/torznab"
)

// torznabContentType is what every successful Torznab/Newznab response --
// caps, results or a standalone error document -- is served as. WriteCaps,
// WriteResults and WriteError all write an XML document with this shape;
// this package sets the header, they write the body.
const torznabContentType = "application/rss+xml; charset=utf-8"

// DefaultSearchTimeout bounds a live, per-indexer search or an aggregate
// local-index read this facade makes on behalf of an inbound HTTP request.
// It is comfortably under the search fan-out's own "min(deadline, 45s)"
// (§6.2), so this facade asking for DefaultSearchTimeout as its
// SearchRequest.DeadlineMillis never contradicts indexarr's own cap.
const DefaultSearchTimeout = 30 * time.Second

// shutdownGrace is how long Run waits for an in-flight request to finish
// once its context is cancelled, before forcing the listener closed. Mirrors
// ui.Run's own constant and reasoning: cancelling ctx also cancels every
// request's context (see the BaseContext wiring below), so this only bounds
// requests already past that point.
const shutdownGrace = 5 * time.Second

// SearchFunc is clustarr.rpc.indexarr.search's own body: indexarr/search's
// Service.Search method value satisfies it directly.
type SearchFunc func(ctx context.Context, req schema.SearchRequest) schema.SearchResponse

// QueryFunc is clustarr.rpc.indexarr.query's own body: indexarr/query's
// Service.Handle method value satisfies it directly.
type QueryFunc func(ctx context.Context, req schema.QueryRequest) schema.QueryResponse

// DownloadFunc is clustarr.rpc.indexarr.download's own body: indexarr/download's
// Service.Handle method value satisfies it directly.
type DownloadFunc func(ctx context.Context, req schema.DownloadRequest) schema.DownloadResponse

// Config is everything New needs to serve the facade. See doc.go for the
// routes each field backs and for why Search/Query/Download are plain
// function values rather than a bus client.
type Config struct {
	// Client resolves Indexer objects by name: t=caps reads status.caps
	// directly, and every per-indexer route uses it to scope a request to
	// exactly one Indexer. Read-only -- the facade never writes to the
	// cluster.
	Client client.Reader

	// Namespace is the namespace Indexer objects live in. Empty falls back
	// to a cluster-wide List filtered by name, which errors if more than
	// one Indexer shares that name across namespaces; setting it makes a
	// per-indexer route a single Get instead. In production this is
	// indexarr's own k8s.Options.Namespace -- there is exactly one indexarr
	// replica per cluster (§3), and every chart value and e2e fixture puts
	// Indexers in that same namespace.
	Namespace string

	// Search answers a live, per-indexer federated search
	// (/{indexer}/api?t=search|tvsearch|...). Required.
	Search SearchFunc

	// Query answers an aggregate read against the local release index
	// (/search/api). Required.
	Query QueryFunc

	// Download resolves a release payload using the indexer's own session
	// (/{indexer}/download). Required.
	Download DownloadFunc

	// APIKeys is the set of keys accepted on Torznab's own `apikey` query
	// parameter or an `X-Api-Key` header. Required and must contain at
	// least one non-blank key -- see doc.go's Auth section for why New
	// refuses to build a Server otherwise.
	APIKeys []string

	// SearchTimeout bounds every outbound Search/Query/Download call this
	// facade makes on behalf of one HTTP request. Zero means
	// DefaultSearchTimeout.
	SearchTimeout time.Duration
}

// Server serves the Torznab facade. Build one with New.
type Server struct {
	addr string
	cfg  Config
	keys []string
	mux  *http.ServeMux
}

// New builds a Server bound to addr, or returns (nil, nil) when addr equals
// pkg/k8s.DisabledBindAddress -- see doc.go's Disabling section. Every other
// invalid Config is a returned error: a facade that fails closed at
// construction can never accidentally serve unauthenticated or with a nil
// backend.
func New(addr string, cfg Config) (*Server, error) {
	if addr == k8s.DisabledBindAddress {
		return nil, nil //nolint:nilnil // the documented "disabled" contract; see doc.go and New's own doc comment.
	}

	var keys []string
	for _, k := range cfg.APIKeys {
		k = strings.TrimSpace(k)
		if k == "" {
			continue
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return nil, errors.New("facade: at least one non-blank API key is required; " +
			"a Torznab facade with none would serve private-tracker results, passkeys included, " +
			"to anything that can reach the port")
	}
	if cfg.Client == nil {
		return nil, errors.New("facade: Client is required")
	}
	if cfg.Search == nil {
		return nil, errors.New("facade: Search is required")
	}
	if cfg.Query == nil {
		return nil, errors.New("facade: Query is required")
	}
	if cfg.Download == nil {
		return nil, errors.New("facade: Download is required")
	}
	if cfg.SearchTimeout <= 0 {
		cfg.SearchTimeout = DefaultSearchTimeout
	}

	s := &Server{addr: addr, cfg: cfg, keys: keys}
	s.mux = s.buildMux()
	return s, nil
}

// Handler returns the facade's http.Handler, for a test that wants to drive
// it with httptest without going through Run's real listener.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) buildMux() *http.ServeMux {
	mux := http.NewServeMux()
	// Registered before the wildcard routes so net/http's own precedence
	// rule (a literal segment beats a wildcard segment in the same
	// position) gives it priority; see doc.go on the one real Indexer name
	// this would shadow.
	mux.HandleFunc("GET /search/api", s.withAuth(s.handleAggregate))
	mux.HandleFunc("GET /{indexer}/api", s.withAuth(s.handleIndexerAPI))
	mux.HandleFunc("GET /{indexer}/download", s.withAuth(s.handleIndexerDownload))
	return mux
}

// Run starts the facade's HTTP server and blocks until ctx is cancelled or
// the server fails to serve. Its shape is [pkg/k8s.EveryReplica]'s
// underlying func type (func(context.Context) error), so a caller wires it
// with mgr.Add(k8s.EveryReplica(srv.Run)) exactly like indexarr's other
// every-replica runnables -- the facade has nothing that needs a leader.
func (s *Server) Run(ctx context.Context) error {
	httpSrv := &http.Server{
		Addr:              s.addr,
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
		// Tying every request's context to ctx, not just the listener's
		// lifetime, means cancelling ctx cancels in-flight handlers too --
		// see ui.Run's identical wiring for why Shutdown alone is not
		// enough.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("facade: serve %s: %w", s.addr, err)
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
		defer cancel()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("facade: graceful shutdown: %w", err)
		}
		return nil
	}
}

// withAuth gates next on a valid API key. It is the first thing every route
// runs: doc.go's Auth section is the whole reason this package exists as a
// gated server rather than a bare mux.
func (s *Server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("apikey")
		if key == "" {
			key = r.Header.Get("X-Api-Key")
		}
		if !s.validKey(key) {
			// r.URL.Path only -- never the query string or r.URL.String():
			// the query string is where a WRONG apikey (or, on a valid
			// request, the right one) lives, and neither belongs in a log
			// line.
			logging.FromContext(r.Context()).Warn("facade: rejected a request with a missing or invalid apikey",
				"path", r.URL.Path, "remoteAddr", r.RemoteAddr)
			s.writeTorznabError(w, http.StatusUnauthorized, torznab.ErrIncorrectCredentials, "invalid or missing apikey")
			return
		}
		next(w, r)
	}
}

// validKey reports whether key matches one of s.keys, in time independent of
// which key it is or how much of it matches -- constant-time comparison is
// cheap here (a handful of short strings per request) and this endpoint is
// reachable by anything that can route to the Service, so there is no reason
// to skip it.
func (s *Server) validKey(key string) bool {
	if key == "" {
		return false
	}
	kb := []byte(key)
	ok := false
	for _, want := range s.keys {
		// Every comparison runs, win or lose already found, so the loop's
		// own length does not leak which key (if any) matched.
		if subtle.ConstantTimeCompare(kb, []byte(want)) == 1 {
			ok = true
		}
	}
	return ok
}
