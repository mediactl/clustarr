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

package intake

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// NoApplierDelay is the nak delay of an observation while no ScanApplier is
// wired (until A6.1).
const NoApplierDelay = 30 * time.Second

// errNoApplier naks every observation until the scan applier exists.
var errNoApplier = errors.New("intake: no scan applier is wired yet")

// Scan intake outcomes (clustarr_intake_scan_total's outcome label).
const (
	scanApplied   = "applied"
	scanRetried   = "retried"
	scanDiscarded = "discarded"
	scanUnapplied = "unapplied"
)

// ScanApplier decides one observation and writes; A6.1 implements it
// (app/import/manager/scanapply). Apply is idempotent: a crash between it
// and the ack re-applies.
type ScanApplier interface {
	Apply(ctx context.Context, obs schema.ScanObservation) error
}

// ScanConsumer is the leader-only importarr-intake-scan consumer.
type ScanConsumer struct {
	Bus events.Bus
	// Applier is nil until A6.1: every observation is naked with 30 s.
	Applier  ScanApplier
	Topology events.Topology
}

// NeedLeaderElection makes the consumer leader-only (§8.1).
func (c *ScanConsumer) NeedLeaderElection() bool { return true }

// Start binds the durable the manager's topology created and applies every
// observation until ctx ends.
func (c *ScanConsumer) Start(ctx context.Context) error {
	if c.Bus == nil {
		return errors.New("intake: the scan consumer needs the bus")
	}
	spec, ok := c.Topology.Consumer(events.ConsumerIntakeScan)
	if !ok {
		return fmt.Errorf("intake: %s is not in the topology", events.ConsumerIntakeScan)
	}
	stop, err := c.Bus.Subscribe(ctx, spec.Subscription(), c.Handle)
	if err != nil {
		return fmt.Errorf("intake: subscribe %s: %w", events.ConsumerIntakeScan, err)
	}
	defer stop()
	<-ctx.Done()
	return nil
}

// Handle applies one observation, then acks (§8.4: write, then ack). A
// transient failure retries on the durable's backoff; any other is
// dead-lettered with the error as its reason.
func (c *ScanConsumer) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	var obs schema.ScanObservation
	if err := schema.Decode(env.Schema, env.Data, &obs); err != nil {
		metrics.IntakeScanTotal.WithLabelValues("unknown", scanDiscarded).Inc()
		return events.Discard("undecodable scan observation", err)
	}
	kind := scanKind(obs.Kind)
	if c.Applier == nil {
		metrics.IntakeScanTotal.WithLabelValues(kind, scanUnapplied).Inc()
		return events.Retry(NoApplierDelay, errNoApplier)
	}
	if err := c.Applier.Apply(ctx, obs); err != nil {
		if Transient(err) {
			metrics.IntakeScanTotal.WithLabelValues(kind, scanRetried).Inc()
			return err
		}
		metrics.IntakeScanTotal.WithLabelValues(kind, scanDiscarded).Inc()
		return events.Discard(err.Error(), err)
	}
	metrics.IntakeScanTotal.WithLabelValues(kind, scanApplied).Inc()
	return nil
}

// scanKind bounds the kind label to the four observation kinds.
func scanKind(k string) string {
	switch k {
	case schema.ScanObservationPresent, schema.ScanObservationMissing,
		schema.ScanObservationUnmatched, schema.ScanObservationOrphanPart:
		return k
	}
	return "unknown"
}

// Transient is remediation.IsTransient's rule restated (the intake must not
// link app/remediation's loop), plus a write conflict: a deadline, a
// cancelled context, EIO, a closed or full bus, no responders, a missing
// stream or bucket, the apiserver's timeout, throttling, unavailability,
// internal error or conflict, and any network error.
func Transient(err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled),
		errors.Is(err, syscall.EIO), errors.Is(err, events.ErrClosed), errors.Is(err, events.ErrQueueFull),
		errors.Is(err, events.ErrNoResponders), errors.Is(err, events.ErrStreamNotFound), errors.Is(err, events.ErrBucketNotFound),
		apierrors.IsServerTimeout(err), apierrors.IsTimeout(err), apierrors.IsTooManyRequests(err),
		apierrors.IsServiceUnavailable(err), apierrors.IsInternalError(err), apierrors.IsConflict(err):
		return true
	}
	var ne net.Error
	return errors.As(err, &ne)
}
