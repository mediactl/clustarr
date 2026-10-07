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

// ConsumerState is one durable's backlog as the broker reports it (spec §9.2).
type ConsumerState struct {
	// Pending is JetStream's NumPending: matching messages not yet delivered.
	// A message a schedule still holds is on a subject no filter matches and
	// counts nowhere.
	Pending uint64
	// AckPending is NumAckPending: delivered and not settled -- in a handler,
	// waiting out a delayed nak, or lapsed.
	AckPending uint64
	// MaxAckPending is the durable's cap across every process.
	MaxAckPending int
	// ObservedAt is when the broker gathered the numbers.
	ObservedAt time.Time
}

// Lag is the work the durable still has, delivered or not: the
// clustarr_consumer_lag metric (spec §9.0).
func (s ConsumerState) Lag() uint64 { return s.Pending + s.AckPending }
