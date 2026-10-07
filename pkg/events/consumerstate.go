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

package events

import "time"

// ConsumerState is one durable's backlog as the broker reports it (spec §9.2),
// from $JS.API.CONSUMER.INFO, which only the consumer's leader answers. Its
// Lag is the autoscaling metric (split §9.0 as amended 2026-10-07): a
// WithScheduleAt hold on a .sched. subject counts only once it fires; a
// message past MaxDeliver waiting for its dead-letter copy counts in neither
// Pending nor AckPending; and a stream's message count is never used -- on
// 2026-10-07 CLUSTARR_WORK_SEGMENTARR held 18,897 messages against a lag of
// 16,563, the gap exactly its 2,334 scheduled TheIntroDB holds, and a limits
// stream's count is its retention window, not work.
type ConsumerState struct {
	// Pending is JetStream's NumPending: matching messages not yet delivered.
	// A message a schedule still holds is on a subject no filter matches and
	// counts nowhere.
	Pending uint64
	// AckPending is NumAckPending: delivered and not settled -- in a handler,
	// waiting out a delayed nak, or lapsed.
	AckPending uint64
	// Waiting is NumWaiting: pull requests open on the durable, idle
	// capacity -- the inverse of demand, so never part of Lag.
	Waiting int
	// MaxAckPending is the durable's cap across every process.
	MaxAckPending int
	// ObservedAt is when the broker gathered the numbers.
	ObservedAt time.Time
}

// Lag is the work the durable still has, delivered or not: the
// clustarr_consumer_lag metric (spec §9.0).
func (s ConsumerState) Lag() uint64 { return s.Pending + s.AckPending }
