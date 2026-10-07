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

package busconn

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// Shutdown is every process's one bus shutdown path (ADR-0020 §1): it drains
// bus (events.Bus.Drain: pull subscriptions, then responders, then their
// running handlers), then the connection (nats.go's Conn.Drain: the core
// subscriptions left, then the outbound buffer), and waits for the
// connection's ClosedHandler, all within ctx. Once ctx is spent it falls back
// to Close -- bus.Close and nc.Close, the hard abort -- and reports why.
//
// Either argument may be nil: a process whose bus failed to open still
// shuts its connection down here, so no shutdown path calls nc.Close() or
// bus.Close() itself (TestNoShutdownPathClosesTheConnectionOutsideShutdown).
func Shutdown(ctx context.Context, bus events.Bus, nc *nats.Conn) error {
	var errs []error
	if bus != nil {
		if err := bus.Drain(ctx); err != nil {
			errs = append(errs, fmt.Errorf("busconn: drain the bus: %w", err))
			_ = bus.Close()
		}
	}
	if nc == nil || nc.IsClosed() {
		return errors.Join(errs...)
	}
	if ctx.Err() != nil {
		nc.Close()
		return errors.Join(append(errs, fmt.Errorf("busconn: no budget left to drain the connection: %w", ctx.Err()))...)
	}

	closed := make(chan struct{})
	var once sync.Once
	prev := nc.ClosedHandler()
	nc.SetClosedHandler(func(c *nats.Conn) {
		if prev != nil {
			prev(c)
		}
		once.Do(func() { close(closed) })
	})
	// nats.DrainTimeout is the same budget, so nats.go never cuts its own
	// drain short of ours. The budget is known only now, so the option is
	// set on the connection rather than at Connect: Opts.DrainTimeout is read
	// only by the drain goroutine Conn.Drain starts after this write.
	if dl, ok := ctx.Deadline(); ok {
		nc.Opts.DrainTimeout = max(time.Until(dl), time.Millisecond)
	}
	if err := nc.Drain(); err != nil {
		switch {
		case errors.Is(err, nats.ErrConnectionClosed):
			return errors.Join(errs...)
		case errors.Is(err, nats.ErrConnectionReconnecting):
			// Drain closed it: there was nothing to flush to.
		default:
			nc.Close()
			return errors.Join(append(errs, fmt.Errorf("busconn: drain the connection: %w", err))...)
		}
	}
	select {
	case <-closed:
	case <-ctx.Done():
		nc.Close()
		errs = append(errs, fmt.Errorf("busconn: drain the connection: %w", ctx.Err()))
	}
	return errors.Join(errs...)
}

// ShutdownAfter is Shutdown for a process's defer: called at start, it
// returns the function to defer, which runs Shutdown under a deadline budget
// after ctx ended -- the remainder of the graceful-shutdown timeout once the
// manager's runnables have stopped, which is when a deferred call runs -- or
// budget after it runs, when ctx never ended (a start that failed). A
// failure is logged on ctx's logger.
//
//	defer busconn.ShutdownAfter(ctx, o.GracefulShutdownTimeout, bus, nc)()
func ShutdownAfter(ctx context.Context, budget time.Duration, bus events.Bus, nc *nats.Conn) func() {
	ended := make(chan time.Time, 1)
	stop := context.AfterFunc(ctx, func() { ended <- time.Now() })
	return func() {
		start := time.Now()
		if !stop() {
			// The AfterFunc has started, so its send is coming.
			start = <-ended
		}
		sctx, cancel := context.WithDeadline(context.WithoutCancel(ctx), start.Add(budget))
		defer cancel()
		if err := Shutdown(sctx, bus, nc); err != nil {
			logging.FromContext(ctx).Warn("bus shutdown fell back to closing", "budget", budget, "error", err)
		}
	}
}
