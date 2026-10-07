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

// Package intake is the manager's leader-only consumption of what agents
// propose without being asked (ADR-0019 §4.3, §8.1, §8.4): grab candidates
// on catalogarr-intake-candidate and library-scan observations on
// importarr-intake-scan, both on CLUSTARR_INTAKE.
//
// Intake is acked only after its decision lands. A candidate is parked in
// the leader-local Inbox under its owner, the owner's key is woken at
// PriorityUser (loop source S30), and the message is held with InProgress
// heartbeats until that owner's pass Settles it (incorporated, pended or
// refused) or HandlerBudget passes, when it is naked with 10 s. A leader
// crash loses the inbox and the server redelivers; MaxAckPending bounds it.
// A scan observation is applied (the ScanApplier writes), then acked; a
// crash between the two re-applies an idempotent create-or-update.
//
// Subpackage advisory is the third intake: JetStream's nak and term
// advisories of every dispatched task, turned into delivery state.
//
// Only cmd/manager links this package; no agent root may (A7, §9.2).
package intake
