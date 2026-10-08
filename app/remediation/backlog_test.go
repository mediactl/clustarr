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

package remediation

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/events"
)

type fakeAdmin struct {
	events.StreamAdmin
	state events.ConsumerState
	err   error
}

func (a *fakeAdmin) ConsumerState(_ context.Context, stream, durable string) (events.ConsumerState, error) {
	if stream != events.StreamWorkCaption || durable != events.ConsumerCaptionFetchNormal {
		return events.ConsumerState{}, fmt.Errorf("read %s/%s", stream, durable)
	}
	return a.state, a.err
}

// TestTheCaptionBacklogGate is §6.4.6's gate: scheduled subtitle dispatches
// wait while the normal fetch durable is 500 deep, or its reading is two
// minutes old; a failed read keeps the last good reading until it is stale.
func TestTheCaptionBacklogGate(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	clock := at
	admin := &fakeAdmin{state: events.ConsumerState{Pending: 300, AckPending: 199}}
	b := NewCaptionBacklog(admin, func() time.Time { return clock })
	assert.True(t, b.NeedLeaderElection())
	assert.False(t, b.Open(at), "no reading yet")

	b.read(t.Context())
	assert.True(t, b.Open(at), "499 is under the ceiling")
	admin.state.AckPending = 200
	b.read(t.Context())
	assert.False(t, b.Open(at), "500 is the ceiling (§6.4.6)")

	admin.state.AckPending = 0
	b.read(t.Context())
	assert.True(t, b.Open(at.Add(119*time.Second)))
	assert.False(t, b.Open(at.Add(2*time.Minute)), "a reading two minutes old closes the gate")

	clock = at.Add(time.Minute)
	admin.err = errors.New("nats: timeout")
	b.read(t.Context())
	assert.True(t, b.Open(at.Add(time.Minute)), "a failed read keeps the last good reading until it is stale")
	assert.False(t, b.Open(at.Add(2*time.Minute)), "and only until then")
}

// Start reads at once and then on its tick, until its context ends.
func TestTheCaptionBacklogGateReadsOnStart(t *testing.T) {
	admin := &fakeAdmin{state: events.ConsumerState{Pending: 1}}
	b := NewCaptionBacklog(admin, nil)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- b.Start(ctx) }()
	assert.Eventually(t, func() bool { return b.Open(time.Now()) }, 5*time.Second, 10*time.Millisecond)
	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return when its context ended")
	}
}
