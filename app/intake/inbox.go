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
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/mediactl/clustarr/pkg/events/schema"
)

// Outcome is how an owner's pass settled a candidate (§8.4), and the
// outcome label of clustarr_intake_candidates_total.
type Outcome string

// Outcomes.
const (
	// Incorporated: the candidate became (or updated) an entry.
	Incorporated Outcome = "incorporated"
	// Pended: the candidate is held in the owner's pending candidates.
	Pended Outcome = "pended"
	// Refused: the owner's planner decided not to take it.
	Refused Outcome = "refused"
	// TimedOut is the handler's own outcome when no pass settled the
	// candidate within HandlerBudget; never a Settle argument.
	TimedOut Outcome = "timedOut"
)

// OwnerKey is the item a candidate's grab would live on.
type OwnerKey struct{ Kind, Namespace, Name string }

// OwnerOf is ref's owner key.
func OwnerOf(ref schema.ItemRef) OwnerKey {
	return OwnerKey{Kind: ref.Kind, Namespace: ref.Namespace, Name: ref.Name}
}

// Parked is one candidate held for its owner's next pass.
type Parked struct {
	MsgID     string
	Candidate schema.Candidate
	At        time.Time
}

// errInboxFull refuses a candidate past the inbox's bound; the handler
// naks it with 10 s.
var errInboxFull = errors.New("intake: the candidate inbox is full")

// errSuperseded releases a handler whose candidate a redelivery of the same
// message parked again: the newer handler waits for the decision.
var errSuperseded = errors.New("intake: the candidate was redelivered and parked again")

// parked is an inbox entry: the candidate and its waiting handler.
type parked struct {
	Parked
	owner OwnerKey
	// done receives the settlement, or is closed when superseded.
	done chan Outcome
}

// Inbox is leader-local: a leader crash loses it and the server redelivers
// (§8.4). Owners' passes Take their candidates and Settle each by Msg-Id.
type Inbox struct {
	max   int
	mu    sync.Mutex
	order map[OwnerKey][]*parked
	byID  map[string]*parked
	wakes chan schema.ItemRef
}

// NewInbox is an inbox of at most limit parked candidates: the consumer's
// MaxAckPending (64).
func NewInbox(limit int) *Inbox {
	limit = max(limit, 1)
	return &Inbox{
		max:   limit,
		order: map[OwnerKey][]*parked{},
		byID:  map[string]*parked{},
		wakes: make(chan schema.ItemRef, limit),
	}
}

// Take returns every unsettled candidate of owner, oldest first. They stay
// parked until Settled: a pass that fails before it settles leaves them for
// the next.
func (in *Inbox) Take(owner OwnerKey) []Parked {
	in.mu.Lock()
	defer in.mu.Unlock()
	ps := in.order[owner]
	out := make([]Parked, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.Parked)
	}
	return out
}

// Len is how many candidates are parked.
func (in *Inbox) Len() int {
	in.mu.Lock()
	defer in.mu.Unlock()
	return len(in.byID)
}

// Settle releases the handler waiting on msgID with o, after the owner's
// decision landed. An unknown msgID (settled, timed out, or parked by an
// earlier leader) is ignored.
func (in *Inbox) Settle(msgID string, o Outcome) {
	in.mu.Lock()
	p, ok := in.byID[msgID]
	if ok {
		in.removeLocked(p)
	}
	in.mu.Unlock()
	if ok {
		p.done <- o
	}
}

// Wakes is S30: each parked candidate's owner, to enqueue at PriorityUser.
// The send never blocks: a full channel drops the wake, and the candidate
// waits for the owner's next pass or its budget.
func (in *Inbox) Wakes() <-chan schema.ItemRef { return in.wakes }

// park holds p under owner. A candidate already parked under the same
// Msg-Id (a redelivery while the earlier handler still waits) supersedes
// it; past max the inbox refuses.
func (in *Inbox) park(owner OwnerKey, p Parked) (*parked, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if old, ok := in.byID[p.MsgID]; ok {
		in.removeLocked(old)
		close(old.done)
	}
	if len(in.byID) >= in.max {
		return nil, errInboxFull
	}
	e := &parked{Parked: p, owner: owner, done: make(chan Outcome, 1)}
	in.byID[p.MsgID] = e
	in.order[owner] = append(in.order[owner], e)
	return e, nil
}

// drop removes p unsettled: its handler gave up.
func (in *Inbox) drop(p *parked) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if cur, ok := in.byID[p.MsgID]; ok && cur == p {
		in.removeLocked(p)
	}
}

func (in *Inbox) removeLocked(p *parked) {
	delete(in.byID, p.MsgID)
	ps := slices.DeleteFunc(in.order[p.owner], func(q *parked) bool { return q == p })
	if len(ps) == 0 {
		delete(in.order, p.owner)
	} else {
		in.order[p.owner] = ps
	}
}

// wake sends ref on Wakes without blocking.
func (in *Inbox) wake(ref schema.ItemRef) {
	select {
	case in.wakes <- ref:
	default:
	}
}
