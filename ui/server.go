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

// Package ui is the server-rendered web UI (§A3): templ for markup, htmx and
// SSE for live updates, never a field manager and never a CRD of its own.
// Every user action either patches a spec field or creates a short-lived
// resource (a Search, a LibraryScan, ...), so anything this service does,
// kubectl can do too.
package ui

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/mediactl/clustarr/pkg/pipeline"
)

// DefaultBindAddress is what [Run] listens on when Options.BindAddress is
// empty.
const DefaultBindAddress = ":8080"

// authWarning is logged once, at startup, because it is the one thing an
// operator deploying this service must not miss: it ships with no login of
// its own (§A3.5) and must sit behind whatever ingress authentication the
// cluster already runs.
const authWarning = "ui service has no built-in authentication; it must sit behind ingress " +
	"authentication and must never be exposed directly " +
	"(design amendment §A3.5, docs/superpowers/specs/2026-09-18-clustarr-design-amendment-1.md:390-398)"

// Options configures a [Server].
type Options struct {
	// BindAddress is the address the HTTP server listens on, e.g. ":8080".
	// [Run] defaults it to [DefaultBindAddress] when empty.
	BindAddress string

	// Entries returns the current pipeline projection for the Pipeline page
	// and its SSE stream. Production wiring backs this with a
	// controller-runtime cache; tests inject a fixture directly, which is
	// the whole point of the indirection: the page is testable without a
	// cluster. A nil Entries behaves as if it always returned no rows.
	Entries func(context.Context) []pipeline.Entry

	// Logger receives the startup warning and handler error logs. Defaults
	// to slog.Default().
	//
	// pkg/obs/logging is being written by a different task in this same
	// phase; ui does not depend on it and uses slog directly.
	Logger *slog.Logger
}

// Server is the ui service's whole surface: an HTTP handler and nothing
// else. It holds no client, no cache and no field manager -- Options.Entries
// is the only way it ever sees cluster state, and it is deliberately just a
// function, not an interface, so a test can supply cluster state without a
// cluster.
type Server struct {
	opts   Options
	logger *slog.Logger
}

// NewServer builds a Server from opts and logs [authWarning]. Construction
// never fails and never touches the network; call [Server.Handler] to get
// something an http.Server (or httptest) can serve.
func NewServer(opts Options) *Server {
	if opts.Entries == nil {
		opts.Entries = func(context.Context) []pipeline.Entry { return nil }
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.Warn(authWarning)
	return &Server{opts: opts, logger: logger}
}

// Handler returns the composed HTTP handler for every route this service
// exposes.
func (s *Server) Handler() http.Handler {
	return s.routes()
}
