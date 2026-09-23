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

package natsbus

import (
	"testing"
	"time"
)

// TestNakDelaySubtractsTheServersBackoffAddition pins the arithmetic behind
// natsbus's delayed nak; WorkQueueNakRedeliveryFollowsBackoff in the contract
// suite measures the result against a real server.
func TestNakDelaySubtractsTheServersBackoffAddition(t *testing.T) {
	s, m := time.Second, time.Minute
	search := []time.Duration{30 * s, 2 * m, 10 * m, 60 * m}
	for _, tc := range []struct {
		name    string
		want    time.Duration
		backoff []time.Duration
		attempt uint64
		ask     time.Duration
	}{
		{"no backoff asks for the wait itself", 5 * s, nil, 3, 5 * s},
		{"first attempt: nothing is added", 30 * s, search, 1, 30 * s},
		{"settle's schedule comes out as BackOff[0]", 2 * m, search, 2, 30 * s},
		{"fourth attempt of catalogarr-search-normal", 60 * m, search, 4, 30 * s},
		{"past the end reuses the last entry", 60 * m, search, 9, 30 * s},
		{"a longer explicit retry keeps its excess", 3 * m, search, 2, 90 * s},
		{"shorter than the addition floors at a delayed nak", 10 * s, search, 3, time.Nanosecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := nakDelay(tc.want, tc.backoff, tc.attempt); got != tc.ask {
				t.Errorf("nakDelay(%v, %v, %d) = %v, want %v", tc.want, tc.backoff, tc.attempt, got, tc.ask)
			}
		})
	}
}
