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

// Package dispatch is the manager's admission for every task it publishes
// to an agent (ADR-0019 §5.4, §5.5, §8.3, §8.4): the dispatch Ledger, its
// budgets and unattended detection, and the DeliveryBook of nak and term
// advisories that planners render into a CR's Dispatch block.
//
// The Ledger is leader-local and rebuilt, never trusted: at leader start it
// reads every registered Source's outstanding dispatches from the cache
// (seq > answeredSeq) and refuses with Rebuilding until it has. A planner
// asks Admit before it issues a seq, reports Published once the publish
// effect landed and Answered once it incorporated (or closed) the answer;
// above a durable's Budget (2 × MaxAckPending) Admit refuses with the
// reason and message the planner renders as delivery.state waiting. A
// reservation not confirmed within ReservationTTL lapses, so a failed apply
// costs nothing. Kinds with no status dispatch block (metadata, artwork
// fetch, overlay render: ruling R23) have no Source and lapse after
// AckWait × MaxDeliver.
//
// Nothing here is the truth of what was published: records and status are
// (Review Focus 2). The book and the ledger are lost with the leader and
// rebuilt from status, or, for delivery state, left stale until the next
// transition.
//
// Only cmd/manager links this package; no agent root may (A7, §9.2).
package dispatch
