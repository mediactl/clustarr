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
	"time"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/metrics"
)

// HandlerBudget is how long a candidate is held for its owner's decision
// (§8.4), catalogarr-intake-candidate's HandlerTimeout; past it the
// candidate is naked with RetryDelay.
const HandlerBudget = 60 * time.Second

// RetryDelay is the nak delay of a candidate no pass settled in time, or
// one the full inbox refused.
const RetryDelay = 10 * time.Second

// errTimedOut is a candidate no owner pass settled within HandlerBudget.
var errTimedOut = errors.New("intake: no decision on the candidate within the handler budget")

// CandidateConsumer is the leader-only catalogarr-intake-candidate consumer.
type CandidateConsumer struct {
	Bus      events.Bus
	Inbox    *Inbox
	Topology events.Topology
	// Now is a seam for tests; nil is time.Now.
	Now func() time.Time
}

// NeedLeaderElection makes the consumer leader-only (§8.1).
func (c *CandidateConsumer) NeedLeaderElection() bool { return true }

// Start binds the durable the manager's topology created and parks every
// candidate until ctx ends.
func (c *CandidateConsumer) Start(ctx context.Context) error {
	if c.Bus == nil || c.Inbox == nil {
		return errors.New("intake: the candidate consumer needs the bus and the inbox")
	}
	spec, ok := c.Topology.Consumer(events.ConsumerIntakeCandidate)
	if !ok {
		return fmt.Errorf("intake: %s is not in the topology", events.ConsumerIntakeCandidate)
	}
	stop, err := c.Bus.Subscribe(ctx, spec.Subscription(), c.Handle)
	if err != nil {
		return fmt.Errorf("intake: subscribe %s: %w", events.ConsumerIntakeCandidate, err)
	}
	defer stop()
	<-ctx.Done()
	return nil
}

// Handle parks one candidate under its owner, wakes the owner, and waits for
// the owner's pass to settle it: nil (ack) once settled, a 10 s retry when
// the budget lapses or the inbox is full, a discard when the payload names
// no owner or cannot be decoded. The bus heartbeats the delivery while
// Handle waits (the durable's HandlerTimeout).
func (c *CandidateConsumer) Handle(ctx context.Context, m events.Message) error {
	env := m.Envelope()
	var cand schema.Candidate
	if err := schema.Decode(env.Schema, env.Data, &cand); err != nil {
		return events.Discard("undecodable candidate", err)
	}
	if cand.Owner.Kind == "" || cand.Owner.Name == "" {
		return events.Discard("the candidate names no owner", nil)
	}
	if env.ID == "" {
		return events.Discard("the candidate carries no Msg-Id", nil)
	}
	p, err := c.Inbox.park(OwnerOf(cand.Owner), Parked{MsgID: env.ID, Candidate: cand, At: c.now()})
	if err != nil {
		return events.Retry(RetryDelay, err)
	}
	c.Inbox.wake(cand.Owner)
	timer := time.NewTimer(HandlerBudget)
	defer timer.Stop()
	select {
	case o, ok := <-p.done:
		if !ok {
			// Superseded by a redelivery of the same message: that handler
			// owns the delivery now, so this one must not ack it.
			return events.Retry(RetryDelay, errSuperseded)
		}
		metrics.IntakeCandidatesTotal.WithLabelValues(string(o)).Inc()
		return nil
	case <-timer.C:
	case <-ctx.Done():
	}
	c.Inbox.drop(p)
	metrics.IntakeCandidatesTotal.WithLabelValues(string(TimedOut)).Inc()
	logging.FromContext(ctx).Debug("intake: no decision on a candidate in time; it is redelivered",
		"owner", cand.Owner.Kind+"/"+cand.Owner.Namespace+"/"+cand.Owner.Name, "msgID", env.ID)
	return events.Retry(RetryDelay, errTimedOut)
}

func (c *CandidateConsumer) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
