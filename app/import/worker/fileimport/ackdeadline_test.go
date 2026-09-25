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

package fileimport

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
)

// importSlack is the file loop's own work between its bounded steps -- a
// cache read, an apply -- which no timeout bounds.
const importSlack = 2 * time.Second

// ackDeadline is the shortest acknowledgement deadline any delivery on
// consumer gets, read from the topology the services install: a BackOff
// replaces AckWait as the deadline (events.Subscription.Backoff), delivery
// n getting BackOff[n-1], so it is the BackOff's smallest entry when one is
// set and the AckWait otherwise. app/import/worker/rescan has the same
// helper.
func ackDeadline(t *testing.T, consumer string) time.Duration {
	t.Helper()
	c, ok := events.Default().Consumer(consumer)
	require.True(t, ok, "consumer %s is in the default topology", consumer)
	if len(c.BackOff) > 0 {
		return slices.Min(c.BackOff)
	}
	return c.AckWait
}

// Between two in-progress acks the file loop may wait out
// heartbeatInterval and probe a file, each bounded, plus its own work. All
// of it must land inside ConsumerImportFile's acknowledgement deadline, or
// the import is redelivered while it is still running and a second replica
// imports the same Download. The loop also beats immediately before each
// probe, so this sum is a bound that holds without that beat. A change to
// the consumer's BackOff or AckWait, or to either bound, trips it.
func TestTheImportFitsTheFileConsumersAckDeadline(t *testing.T) {
	deadline := ackDeadline(t, events.ConsumerImportFile)
	worst := heartbeatInterval + videoProbeTimeout + importSlack
	assert.LessOrEqual(t, worst, deadline,
		"heartbeatInterval %s + videoProbeTimeout %s + slack %s must fit inside %s's ack deadline %s",
		heartbeatInterval, videoProbeTimeout, importSlack, events.ConsumerImportFile, deadline)
}
