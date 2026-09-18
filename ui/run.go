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
	"time"
)

// shutdownGrace is how long Run waits for in-flight requests -- including an
// open SSE stream -- to finish once ctx is cancelled, before forcing the
// listener closed.
const shutdownGrace = 5 * time.Second

// Run starts the ui HTTP server and blocks until ctx is cancelled or the
// server fails to serve. It is the same shape as the other services'
// Run(ctx, Options) entrypoints (see captionarr.Run, squasharr.Run, ...) so
// a future cmd/clustarr wiring task can add `clustarr ui` the same way it
// added every other service, even though ui needs no controller-runtime
// manager: it has no CRD of its own and reads the cluster only through
// Options.Entries.
func Run(ctx context.Context, o Options) error {
	if o.BindAddress == "" {
		o.BindAddress = DefaultBindAddress
	}

	srv := NewServer(o)
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
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("ui: graceful shutdown: %w", err)
		}
		return nil
	}
}
