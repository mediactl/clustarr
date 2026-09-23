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
// SSE for live updates, never a status field manager and never a CRD of its
// own. Every user action either patches a spec field or creates a
// short-lived resource (a Search, a LibraryScan, ...), all of it in
// ui/actions and nothing of it anywhere else, so anything this service does,
// kubectl can do too.
package ui

import (
	"context"
	"net/http"

	"sigs.k8s.io/controller-runtime/pkg/client"

	downloadv1 "github.com/mediactl/clustarr/api/download/v1alpha1"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
	"github.com/mediactl/clustarr/pkg/pipeline"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
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

	// Actions is ui's one write seam, and deliberately a separate field from
	// Reader: Reader stays a client.Reader, so every read path is read-only
	// by type, and the only writes ui can make are the three §A3.2 actions
	// behind this value -- create a Search, create a LibraryScan, patch a
	// catalog item's spec.monitored (ui/actions' package doc). An
	// *actions.Actions exposes those three methods and holds its writer
	// unexported, so nothing here can reach Create or Patch for anything
	// else; ui/guard_test.go bans those calls outside ui/actions and bans
	// every status write everywhere in ui/, ui/actions included.
	//
	// A nil Actions is legal -- no cluster configured, or a test that only
	// reads -- and every method on it returns actions.ErrNoWriter.
	Actions *actions.Actions

	// Subscribe returns a channel that receives the current pipeline
	// projection immediately upon subscribing, and again whenever it
	// changes, plus a func that unsubscribes -- the shape
	// ui/projection.Projection's own Subscribe method has. /events/pipeline
	// (ui/sse.go) reads from it instead of polling Entries itself on its
	// own ticker, so production wiring can back this with ONE shared
	// projection loop instead of one re-projection per open connection
	// (design plan ruling R4).
	//
	// A nil Subscribe -- every test in this package that sets only Entries,
	// and any `clustarr ui` process too short-lived to have wired one --
	// defaults in [NewServer] to a per-connection poll of Entries on a fixed
	// interval, which is exactly what /events/pipeline did before Task
	// D3-1 existed. That keeps Entries the one seam a test needs to inject
	// cluster state, same as before; Subscribe only needs setting in
	// production, where cmd/clustarr wires both it and Entries to the same
	// *projection.Projection.
	Subscribe func() (<-chan []pipeline.Entry, func())

	// SubscribeDownloads is Subscribe's Task D3-3 counterpart for the
	// Downloads page: a channel that receives the current downloads slice
	// immediately upon subscribing, and again whenever it changes, plus a
	// func that unsubscribes. /events/downloads (ui/sse.go) reads from it
	// instead of listing Download itself on its own ticker, so production
	// wiring can back this with the SAME shared *projection.Projection as
	// Subscribe -- one list round feeding both streams (design plan ruling
	// R4).
	//
	// A nil SubscribeDownloads -- every test in this package that sets only
	// Reader (or neither), and any `clustarr ui` process too short-lived to
	// have wired a projection loop yet -- defaults in [NewServer] to a
	// per-connection poll of Reader, mirroring Subscribe's own nil fallback.
	// GET /downloads (ui/routes.go's handleDownloads) does not use this
	// field at all; it lists through Reader directly, exactly as it did
	// before this field existed.
	SubscribeDownloads func() (<-chan []downloadv1.Download, func())

	// Library returns the current library projection for the Library page
	// (Task G3-3) and its SSE stream, mirroring Entries: production wiring
	// backs this with the shared ui/projection.Projection (ruling R4 --
	// Library reuses the same list round Entries already triggers, no
	// separate cluster read of its own), tests inject a fixture directly. A
	// nil Library behaves as if it always returned no rows.
	Library func(context.Context) []projection.LibraryItem

	// SubscribeLibrary is Subscribe's Library-page counterpart: a channel
	// that receives the current library projection immediately upon
	// subscribing, and again whenever it changes, plus a func that
	// unsubscribes. A nil SubscribeLibrary -- every test in this package
	// that sets only Library, and any `clustarr ui` process too short-lived
	// to have wired one -- defaults in [NewServer] to a per-connection poll
	// of Library, exactly as Subscribe's own nil fallback does for Entries.
	SubscribeLibrary func() (<-chan []projection.LibraryItem, func())

	// Unmatched returns the current unmatched-files projection for the
	// Unmatched page (Task G3-3, amendment §A3.4) and its SSE stream:
	// LibraryScan.status.unmatched flattened across every current scan. A
	// nil Unmatched behaves as if it always returned no rows.
	Unmatched func(context.Context) []projection.UnmatchedEntry

	// SubscribeUnmatched is Subscribe's Unmatched-page counterpart, with the
	// same nil-defaults-to-a-per-connection-poll fallback as
	// SubscribeLibrary.
	SubscribeUnmatched func() (<-chan []projection.UnmatchedEntry, func())

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
// else. It holds no client, no cache and no field manager of its own (its
// only writes are Options.Actions', made in ui/actions) --
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
	if opts.Subscribe == nil {
		// Defaulted from the already-defaulted Entries above, so this
		// closure never sees a nil entries func even when a caller built
		// Options directly with neither field set.
		opts.Subscribe = defaultSubscribe(opts.Entries)
	}
	if opts.SubscribeDownloads == nil {
		// opts.Reader may itself be nil here; defaultSubscribeDownloads and
		// the poller behind it treat that exactly like Entries returning no
		// rows -- see pollDownloadsOnly's own doc comment.
		opts.SubscribeDownloads = defaultSubscribeDownloads(opts.Reader)
	}
	if opts.Library == nil {
		opts.Library = func(context.Context) []projection.LibraryItem { return nil }
	}
	if opts.SubscribeLibrary == nil {
		// Defaulted from the already-defaulted Library above, mirroring
		// Subscribe's own fallback for Entries.
		opts.SubscribeLibrary = defaultSubscribeLibrary(opts.Library)
	}
	if opts.Unmatched == nil {
		opts.Unmatched = func(context.Context) []projection.UnmatchedEntry { return nil }
	}
	if opts.SubscribeUnmatched == nil {
		opts.SubscribeUnmatched = defaultSubscribeUnmatched(opts.Unmatched)
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
