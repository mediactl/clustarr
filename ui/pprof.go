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
	"log/slog"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// DefaultPprofBindAddress is `--pprof-bind-address`'s default: loopback, as
// the manager's (ctrlflags.ManagerPprofAddress), reached through
// `kubectl port-forward pod/<ui> 6060`. The ui had none, and a ui at 750 MB
// of heap could not say what held it (2026-11-28).
const DefaultPprofBindAddress = "127.0.0.1:6060"

// servePprof serves net/http/pprof on addr until ctx ends, on its own
// listener and its own mux: never a route of the ui's, whose address is
// behind a Gateway, and never http.DefaultServeMux. An address it cannot
// bind is logged and the ui serves on: profiling is a tool, not a duty.
func servePprof(ctx context.Context, addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          logging.StdLogger(ctx, "ui", slog.LevelWarn),
	}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logging.FromContext(ctx).Warn("ui: profiling is not served", "address", addr, "err", err)
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownGrace)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()
}
