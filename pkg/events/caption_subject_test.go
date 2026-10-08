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

	"github.com/mediactl/clustarr/pkg/events"
)

// TestMsgIDForSubtitleFetchCarriesTheSequence (loop spec §6.5): every dispatch
// carries the sequence the loop issued, so a forced search is a new seq and
// the one-hour window absorbs only a republish of one dispatch.
func TestMsgIDForSubtitleFetchCarriesTheSequence(t *testing.T) {
	assert.Equal(t, "subtitle/uid-1/en:forced/1759752000123", events.MsgIDForSubtitleFetch("uid-1", "en:forced", 1759752000123))
	assert.NotEqual(t, events.MsgIDForSubtitleFetch("uid-1", "en", 1), events.MsgIDForSubtitleFetch("uid-1", "en", 2))
	assert.NotEqual(t, events.MsgIDForSubtitleFetch("uid-1", "en", 1), events.MsgIDForSubtitleFetch("uid-1", "fr", 1))
	assert.NotEqual(t, events.MsgIDForSubtitleFetch("uid-1", "en", 1), events.MsgIDForSubtitleFetch("uid-2", "en", 1))
}

// TestWorkFetchSubjectAnyPriorityMatchesEveryLane: the loop's withdrawal
// purges a request with one filter, wherever its dispatch went (loop spec
// §3.8): a forced search and a scheduled one take different lanes.
func TestWorkFetchSubjectAnyPriorityMatchesEveryLane(t *testing.T) {
	filter := events.WorkFetchSubjectAnyPriority("uid-1", "en:forced")
	for _, p := range []events.Priority{events.PriorityHigh, events.PriorityNormal, events.PriorityLow} {
		assert.True(t, events.SubjectMatches(filter, events.WorkFetchSubject(p, "uid-1", "en:forced")), p)
	}
	assert.False(t, events.SubjectMatches(filter, events.WorkFetchSubject(events.PriorityHigh, "uid-1", "en")),
		"another language of the file")
	assert.False(t, events.SubjectMatches(filter, events.WorkFetchSubject(events.PriorityHigh, "uid-2", "en:forced")),
		"another file")
}
