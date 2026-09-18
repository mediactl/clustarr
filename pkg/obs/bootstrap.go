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

// Package obs is the one call every Clustarr service makes to stand up its
// observability: [Bootstrap] builds the logger, bridges it to
// controller-runtime, installs the TracerProvider and hands back the shutdown.
//
// The subpackages are the pieces it assembles -- pkg/obs/logging (the
// *slog.Logger carried through context), pkg/obs/tracing (OpenTelemetry spans
// and W3C propagation) and pkg/obs/metrics (the clustarr_ collectors, which
// are registered once per process by cmd/clustarr rather than here, because
// they go into controller-runtime's process-wide registry and `clustarr all`
// would otherwise register them seven times).
package obs

import (
	"context"
	"fmt"
	"sync"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// tracingShutdownTimeout bounds how long the shutdown closure waits for the
// OpenTelemetry exporter to flush and close. It is not tied to
// GracefulShutdownTimeout: that governs the controller-runtime manager's own
// drain, which has already completed by the time this runs.
const tracingShutdownTimeout = 5 * time.Second

// setLoggerOnce guards ctrl.SetLogger, which is process-wide AND
// single-shot.
//
// controller-runtime's delegating log sink fulfils its promise exactly once:
// loggerPromise.Fulfill sets promise = nil, and delegatingLogSink.Fulfill is
// guarded by `if l.promise != nil`, so the SECOND SetLogger call in a process
// is silently a no-op. That is not a race to avoid, it is a correctness
// requirement: whoever calls first owns controller-runtime's output for the
// life of the process, and every later caller is thrown away without a word.
//
// This Once is what makes Bootstrap the only SetLogger call site in the
// binary. `clustarr all` runs seven services in one process, each calling
// Bootstrap; without it, six of the seven calls would be the no-op and the
// only thing deciding the winner would be goroutine scheduling. cmd/clustarr
// must not call SetLogger itself -- it used to install a zap logger in
// PersistentPreRunE, which ran before every RunE and therefore won, so the
// slog bridge here was dead code and controller-runtime logged zap JSON
// alongside the services' slog JSON.
var setLoggerOnce sync.Once

// Bootstrap builds the process's observability from opts and returns the
// context every later call should descend from.
//
// It does three things, in this order:
//
//   - builds the root *slog.Logger from lo and installs it on ctx, so
//     logging.FromContext works everywhere below;
//   - bridges that logger to controller-runtime with ctrl.SetLogger, exactly
//     once per process, so ctrl.LoggerFrom and controller-runtime's own
//     output join the same stream in the same format (amendment §A2.1);
//   - installs the process-wide TracerProvider via tracing.Setup.
//
// The returned shutdown flushes and closes the tracing exporter, bounded by
// [tracingShutdownTimeout], and logs a failure at Warn rather than returning
// it: it runs from a defer on the way out, where there is no caller left to
// handle an error but an operator still needs to know spans were dropped. It
// is never nil, and is safe to call more than once.
//
// A non-nil error means startup failed and the service must not continue;
// Run returns it.
func Bootstrap(ctx context.Context, lo logging.Options, to tracing.Options) (context.Context, func(), error) {
	logger := logging.New(lo)
	setLoggerOnce.Do(func() { ctrl.SetLogger(logging.LogrBridge(logger)) })
	ctx = logging.NewContext(ctx, logger)

	shutdown, err := tracing.Setup(ctx, to)
	if err != nil {
		return ctx, func() {}, fmt.Errorf("tracing: %w", err)
	}

	return ctx, func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), tracingShutdownTimeout)
		defer cancel()
		if err := shutdown(shutdownCtx); err != nil {
			logging.FromContext(ctx).Warn("tracing shutdown", "err", err)
		}
	}, nil
}
