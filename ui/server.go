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
	"net/http"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
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

	// Reader is ui's one seam onto the cluster: an informer-backed
	// [client.Reader], normally built by [NewClusterReader]. It is
	// client.Reader and never client.Client or client.Writer on purpose --
	// CLAUDE.md: "The UI never writes status and owns no CRD" -- so the type
	// itself is the guard against ui ever reaching for Create, Update,
	// Patch or Delete.
	//
	// Reader is nil whenever `clustarr ui` could not reach a cluster (no
	// kubeconfig, no in-cluster config) or when a test builds Options
	// directly. That is legal and stays legal: nothing in this package
	// requires it, and a nil Reader behaves exactly as ui does today --
	// pages render with no rows.
	//
	// Nothing in Task D3-0 reads Reader directly; it exists so cmd/clustarr
	// can hand it to a later projection (Task D3-1) without changing this
	// struct again.
	Reader client.Reader

	// WaitForSync reports whether Reader's cache has completed its initial
	// sync -- typically [NewClusterReader]'s own WaitForCacheSync. The
	// /readyz handler polls it: 503 while it returns false, 200 once it
	// returns true. A nil WaitForSync defaults to a function that always
	// returns true, matching a nil Reader: nothing to sync means nothing to
	// wait for, and a ui with no cluster configured must still become
	// Ready.
	WaitForSync func(context.Context) bool

	// Logging configures the root logger [Run] builds and installs on the
	// context every request descends from. The zero value is a reasonable
	// default: JSON to stderr at info level.
	//
	// There is no Logger field, and [Server] has no logger of its own:
	// handlers log through logging.FromContext(r.Context()), which in
	// production is the logger Run put on the server's BaseContext and in
	// httptest is the discard logger. A test that wants the lines injects a
	// context, not a struct field.
	Logging logging.Options

	// Tracing configures the OpenTelemetry SDK. The zero value is a valid,
	// sampling TracerProvider that exports nowhere -- see
	// pkg/obs/tracing.Setup. ui has no controller-runtime manager of its
	// own, so [Run], not [NewServer], is what calls tracing.Setup.
	Tracing tracing.Options
}

// Validate exists so ui.Options satisfies the same shape every other
// service's Options does (cmd/clustarr's deploy-manifest test executes every
// Deployment's argv and calls Validate() on whatever it produced). There is
// nothing to check yet: ui takes no --role, and reaching a cluster is always
// optional (Reader may be nil).
func (o Options) Validate() error { return nil }

// Server is the ui service's whole surface: an HTTP handler and nothing
// else. It holds no client, no cache and no field manager of its own --
// Options.Entries is the only way it ever sees cluster state for the
// Pipeline page, and Options.WaitForSync is the only way /readyz does. Both
// are deliberately just functions, not interfaces, so a test can supply
// cluster state, or its absence, without a cluster.
type Server struct {
	opts Options
}

// NewServer builds a Server from opts and logs [authWarning] through the
// logger ctx carries. Construction never fails and never touches the
// network; call [Server.Handler] to get something an http.Server (or
// httptest) can serve.
//
// ctx is taken for the warning alone and is not retained: CLAUDE.md's
// logging invariant is that the logger travels in the context, never on a
// struct, and the warning is the one line this service logs outside a
// request. Everything else logs through
// logging.FromContext(r.Context()).
func NewServer(ctx context.Context, opts Options) *Server {
	if opts.Entries == nil {
		opts.Entries = func(context.Context) []pipeline.Entry { return nil }
	}
	if opts.WaitForSync == nil {
		opts.WaitForSync = func(context.Context) bool { return true }
	}
	logging.FromContext(ctx).Warn(authWarning)
	return &Server{opts: opts}
}

// Handler returns the composed HTTP handler for every route this service
// exposes.
func (s *Server) Handler() http.Handler {
	return s.routes()
}
