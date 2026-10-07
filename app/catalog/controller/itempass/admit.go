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

package itempass

import "time"

// Admitter is the dispatch ledger bound to one item key (app/dispatch):
// the admission of a task a kind publishes itself, the metadata refresh
// (ADR-0019 §5.4, ruling R23). The item path builds it; the kinds never
// import app/dispatch.
type Admitter interface {
	// Admit reserves a slot of durable at seq, or says how long to wait.
	Admit(durable string, seq int64) (admitted bool, retryAfter time.Duration)
	// Published confirms the reservation once the publish landed.
	Published(durable string, seq int64)
}

// Admit asks p's Admitter whether the kind may publish to durable at seq.
// Outside a staged pass, or with no ledger, everything is admitted. A
// refusal is remembered on p, which the item path folds into the pass's
// requeue (RetryAfter), so the kind needs no requeue path of its own.
func Admit(p *Pass, durable string, seq int64) bool {
	if p == nil || p.Admitter == nil {
		return true
	}
	ok, after := p.Admitter.Admit(durable, seq)
	if !ok {
		p.mu.Lock()
		if after <= 0 {
			after = time.Second
		}
		if p.retry == 0 || after < p.retry {
			p.retry = after
		}
		p.mu.Unlock()
	}
	return ok
}

// Published confirms an admitted publish on p's Admitter.
func Published(p *Pass, durable string, seq int64) {
	if p == nil || p.Admitter == nil {
		return
	}
	p.Admitter.Published(durable, seq)
}

// RetryAfter is the shortest wait a refused Admit asked for this pass; zero
// when none was refused.
func (p *Pass) RetryAfter() time.Duration {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.retry
}
