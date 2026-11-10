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

package ui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/mediactl/clustarr/pkg/obs"
)

// shutdownGrace is how long Run waits for in-flight requests -- including an
// open SSE stream -- to finish once ctx is cancelled, before forcing the
// listener closed.
const shutdownGrace = 5 * time.Second

// Run starts the ui HTTP server and blocks until ctx is cancelled or the
// server fails to serve. It is the same shape as the other services'
// Run(ctx, Options) entrypoints (see caption.Run, transcode.Run, ...),
// even though ui needs no controller-runtime manager: it has no CRD of its
// own. Run itself never touches the cluster -- Options.Reader and
// Options.WaitForSync are built by the caller (cmd/ui, via
// [NewClusterReader]) before Run is called, and are already whatever they
// are going to be, nil included, by the time Options reaches here. Run just
// hands them to [NewServer] along with everything else.
func Run(ctx context.Context, o Options) error {
	// Before the logger, the tracer or the listener: a ui started without
	// an explicit authentication mode exits here, having served nothing.
	if err := o.Validate(); err != nil {
		return err
	}
	if o.BindAddress == "" {
		o.BindAddress = DefaultBindAddress
	}

	// One call stands up the logger, the controller-runtime bridge and
	// the TracerProvider; a failure here is a startup failure. ui runs no
	// manager, but it shares the bridge so a library that logs through
	// controller-runtime lands in the same stream.
	ctx, shutdown, err := obs.Bootstrap(ctx, o.Logging, o.Tracing)
	if err != nil {
		return fmt.Errorf("ui: %w", err)
	}
	defer shutdown()

	srv := NewServer(ctx, o)
	httpSrv := &http.Server{
		Addr:              o.BindAddress,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// Every request's context is derived from whatever BaseContext
		// returns (context.WithCancel(baseCtx), per net/http), so tying it
		// to Run's own ctx means cancelling ctx cancels every in-flight
		// request's context too -- including an open /events/pipeline
		// stream's -- not just new ones. Without this, Shutdown below waits
		// out the full shutdownGrace on any open SSE connection: Shutdown
		// only stops accepting new connections and waits for active ones to
		// go idle, it does not itself cancel a handler's context.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	// A connection accepted but silent -- a browser's preconnect, the Go
	// client's spare dial -- is closed at shutdown rather than waited for:
	// net/http's Shutdown counts a StateNew connection as active until it
	// is 5 s old (Go issue 22682), the whole of shutdownGrace, so one open
	// at the cancel had Run return the grace's deadline error after the
	// open stream's handler had long returned (2026-11-10, every fourth
	// run of the shutdown test under load).
	var silent silentConns
	httpSrv.ConnState = silent.track

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- fmt.Errorf("ui: serve %s: %w", o.BindAddress, err)
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
		silent.closeAll()
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("ui: graceful shutdown: %w", err)
		}
		return nil
	}
}

// silentConns is the server's connections that have sent nothing yet
// (net/http's StateNew), kept through the ConnState hook so a shutdown can
// close them instead of waiting net/http's 5 s for each to speak.
type silentConns struct {
	mu    sync.Mutex
	conns map[net.Conn]struct{}
}

// track is the http.Server's ConnState hook: a new connection is silent
// until its first request is read (StateActive), or it is hijacked or
// closed. An idle one has spoken, and Shutdown closes it itself.
func (s *silentConns) track(c net.Conn, state http.ConnState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch state {
	case http.StateNew:
		if s.conns == nil {
			s.conns = map[net.Conn]struct{}{}
		}
		s.conns[c] = struct{}{}
	case http.StateActive, http.StateHijacked, http.StateClosed:
		delete(s.conns, c)
	}
}

// closeAll closes every silent connection: its serve loop fails its read
// and lets go, so Shutdown finds it closed. A client that was about to
// speak reconnects, as to any listener a shutdown has closed.
func (s *silentConns) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for c := range s.conns {
		_ = c.Close()
	}
	clear(s.conns)
}
