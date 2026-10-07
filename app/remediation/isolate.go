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

package remediation

import (
	"context"
	"errors"
	"fmt"
	"net"
	"runtime/debug"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/codes"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// plannerFailedRetry is when a pass after a planner's error or panic runs.
const plannerFailedRetry = 10 * time.Minute

const (
	failTransient = "transient"
	failCASMiss   = "cas_miss" // effects only: a record compare-and-swap another writer won
	failError     = "error"
	failPanic     = "panic"
)

type transientError struct{ err error }

func (e transientError) Error() string { return e.err.Error() }
func (e transientError) Unwrap() error { return e.err }

// Transient marks err as a fault that writes nothing (§3.6): the bus, KV or
// apiserver unavailable, a deadline, EIO from /data, the I/O executor
// saturated or its breaker open. Gather wraps every such error in it.
func Transient(err error) error {
	if err == nil {
		return nil
	}
	return transientError{err: err}
}

// IsTransient reports whether err is a transient fault: marked so, or one of
// the shapes no Gather should have to mark.
func IsTransient(err error) bool {
	if err == nil {
		return false
	}
	var te transientError
	switch {
	case errors.As(err, &te), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled),
		errors.Is(err, syscall.EIO), errors.Is(err, events.ErrClosed), errors.Is(err, events.ErrQueueFull),
		errors.Is(err, events.ErrNoResponders), errors.Is(err, events.ErrStreamNotFound), errors.Is(err, events.ErrBucketNotFound),
		apierrors.IsServerTimeout(err), apierrors.IsTimeout(err), apierrors.IsTooManyRequests(err),
		apierrors.IsServiceUnavailable(err), apierrors.IsInternalError(err):
		return true
	}
	var ne net.Error
	return errors.As(err, &ne)
}

// outcome is one isolated call's classification.
type outcome struct {
	reason  string // "", failTransient, failCASMiss, failError, failPanic
	message string
}

func (o outcome) failed() bool    { return o.reason != "" }
func (o outcome) transient() bool { return o.reason == failTransient }
func (o outcome) casMiss() bool   { return o.reason == failCASMiss }

// isolate runs fn under recover and its own deadline (0: none) and
// classifies the result (§3.6). The stack of a panic goes to the log. Each
// call is its own span, remediation.<planner>.<phase>, a child of
// remediation.Reconcile (§3.19), with an error status when it failed.
func (r *Reconciler) isolate(ctx context.Context, name PlannerName, phase string, timeout time.Duration, fn func(context.Context) error) (o outcome) {
	start := time.Now()
	ctx, span := tracing.Start(ctx, "remediation."+string(name)+"."+phase)
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	defer func() {
		if p := recover(); p != nil {
			logging.FromContext(ctx).Error("remediation: planner panicked", "planner", name, "phase", phase, "panic", p, "stack", string(debug.Stack()))
			o = outcome{reason: failPanic, message: fmt.Sprintf("%s panicked: %v", phase, p)}
		}
		metrics.RemediationPlannerSeconds.WithLabelValues(string(name), phase).Observe(time.Since(start).Seconds())
		if o.failed() {
			metrics.RemediationPlannerFailuresTotal.WithLabelValues(string(name), o.reason).Inc()
			span.SetStatus(codes.Error, o.reason+": "+o.message)
		}
		span.End()
	}()
	err := fn(ctx)
	switch {
	case err == nil:
		return outcome{}
	case IsCASMiss(err):
		return outcome{reason: failCASMiss, message: err.Error()}
	case IsTransient(err):
		return outcome{reason: failTransient, message: err.Error()}
	default:
		return outcome{reason: failError, message: err.Error()}
	}
}
