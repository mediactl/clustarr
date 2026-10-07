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

// DeadLetterWatcherPrefix begins every dead-letter watcher's durable name.
const DeadLetterWatcherPrefix = "clustarr-dlq-watch-"

// A dead-letter watcher's tuning, unchanged from the watcher natsbus created
// per subscription before the watchers became topology objects (spec §5.9).
// A failed copy is retried for as long as it takes (MaxDeliver -1): the
// message an advisory names is otherwise never dead-lettered. AckWait covers
// the MaxAckPending advisories one pull holds, each allowed the bus's 10 s
// dead-letter timeout and handled one at a time, twice over.
const (
	DeadLetterWatcherMaxAckPending = 4
	DeadLetterWatcherAckWait       = 80 * time.Second
)

// DeadLetterWatcherName is the durable on StreamAdvisories that dead-letters
// stream's durable's lapsed final deliveries. It names the stream as well as
// the durable, so two durables of one name on different streams do not share,
// and fight over, one watcher's filter. It takes strings because
// StreamAdmin.DeleteSubscription deletes a watcher by stream and durable alone.
func DeadLetterWatcherName(stream, durable string) string {
	return DeadLetterWatcherPrefix + stream + "-" + durable
}

// DeadLetterWatcherSpec is c's MAX_DELIVERIES watcher: a durable on
// StreamAdvisories filtered to c's advisory subject, which every process
// consuming c shares, and the manager too as a backstop (spec §5.9). The
// manager's EnsureTopology creates it; Subscribe binds it.
func DeadLetterWatcherSpec(c ConsumerSpec) ConsumerSpec {
	return ConsumerSpec{
		Name:          DeadLetterWatcherName(c.Stream, c.Name),
		Stream:        StreamAdvisories,
		Description:   "Dead-letters the lapsed final deliveries of " + c.Name + ".",
		Filters:       []string{MaxDeliveriesAdvisorySubject(c.Stream, c.Name)},
		AckWait:       DeadLetterWatcherAckWait,
		MaxDeliver:    -1,
		MaxAckPending: DeadLetterWatcherMaxAckPending,
		Slots:         DeadLetterWatcherMaxAckPending,
	}
}

// withDeadLetterWatchers returns cs followed by one watcher per consumer.
func withDeadLetterWatchers(cs []ConsumerSpec) []ConsumerSpec {
	out := make([]ConsumerSpec, 0, 2*len(cs))
	out = append(out, cs...)
	for _, c := range cs {
		out = append(out, DeadLetterWatcherSpec(c))
	}
	return out
}
