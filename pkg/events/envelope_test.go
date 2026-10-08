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

package events_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
)

// TestEnvelopeFromHeadersKeepsTheIDOfAFiredSchedule: the copy JetStream's
// scheduler hands a consumer (scheduler.go:214-231) -- the producer's headers
// without Nats-Msg-Id, plus Nats-Scheduler and Nats-Schedule-Next -- decodes
// to the producer's ID and carries neither broker header (M2, E7).
func TestEnvelopeFromHeadersKeepsTheIDOfAFiredSchedule(t *testing.T) {
	h := (&events.Envelope{ID: "search-movie-a", Type: "catalog.search", Schema: "catalog.SearchTask.v1"}).ToHeaders()
	require.Equal(t, "search-movie-a", h[events.HeaderID])
	require.Equal(t, "search-movie-a", h[events.HeaderMsgID])
	delete(h, events.HeaderMsgID)
	h["Nats-Scheduler"] = "clustarr.work.catalog.sched.search.normal.movie-a"
	h["Nats-Schedule-Next"] = "purge"
	e := events.EnvelopeFromHeaders(h, nil)
	assert.Equal(t, "search-movie-a", e.ID)
	assert.Equal(t, "catalog.search", e.Type)
	assert.Empty(t, e.Headers, "a transport header in the envelope rides on every copy, and CLUSTARR_DLQ refuses it (10188)")
	assert.Equal(t, "search-movie-a", e.ToHeaders()[events.HeaderMsgID], "a dead-letter copy carries the ID again")
}

func TestEnvelopeFromHeadersDropsTransportHeaders(t *testing.T) {
	for _, name := range []string{
		"Nats-Schedule", "Nats-Schedule-Target", "Nats-Schedule-Next", "Nats-Schedule-TTL", "Nats-Scheduler",
		"nats-schedule-time-zone", "Nats-Expected-Stream", "Nats-Expected-Last-Subject-Sequence", "Nats-TTL", "Nats-Rollup",
	} {
		e := events.EnvelopeFromHeaders(map[string]string{name: "x", "Clustarr-Other": "kept"}, nil)
		assert.Equal(t, map[string]string{"Clustarr-Other": "kept"}, e.Headers, name)
	}
	e := events.EnvelopeFromHeaders(map[string]string{events.HeaderMsgID: "a", events.HeaderID: "b"}, nil)
	assert.Equal(t, "a", e.ID, "Nats-Msg-Id wins when both are present")
	e = events.EnvelopeFromHeaders(map[string]string{events.HeaderID: "b"}, nil)
	assert.Equal(t, "b", e.ID, "Clustarr-Id stands in when a fired schedule stripped Nats-Msg-Id")
}
