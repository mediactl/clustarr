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

package rootfolderschedule_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/importarr/controller/rootfolderschedule"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t.UTC()
}

func TestParseScheduleNext(t *testing.T) {
	tests := []struct {
		name string
		spec string
		from string
		want string
	}{
		{name: "daily at 3am, later the same day", spec: "0 3 * * *", from: "2026-09-18T01:15:00Z", want: "2026-09-18T03:00:00Z"},
		{name: "daily at 3am, already past", spec: "0 3 * * *", from: "2026-09-18T04:00:00Z", want: "2026-09-19T03:00:00Z"},
		{name: "exactly on the tick still moves forward", spec: "0 3 * * *", from: "2026-09-18T03:00:00Z", want: "2026-09-19T03:00:00Z"},
		{name: "sub-minute precision is ignored", spec: "0 3 * * *", from: "2026-09-18T02:59:30Z", want: "2026-09-18T03:00:00Z"},
		{name: "every minute", spec: "* * * * *", from: "2026-09-18T02:59:30Z", want: "2026-09-18T03:00:00Z"},
		{name: "step minutes", spec: "*/15 * * * *", from: "2026-09-18T03:02:00Z", want: "2026-09-18T03:15:00Z"},
		{name: "a list of hours", spec: "30 2,14 * * *", from: "2026-09-18T03:00:00Z", want: "2026-09-18T14:30:00Z"},
		{name: "an hour range", spec: "0 9-17 * * *", from: "2026-09-18T20:00:00Z", want: "2026-09-19T09:00:00Z"},
		{name: "a weekday name", spec: "0 4 * * sun", from: "2026-09-18T00:00:00Z", want: "2026-09-20T04:00:00Z"},
		{name: "day-of-week 7 is Sunday too", spec: "0 4 * * 7", from: "2026-09-18T00:00:00Z", want: "2026-09-20T04:00:00Z"},
		{name: "a month name", spec: "0 0 1 jan *", from: "2026-09-18T00:00:00Z", want: "2027-01-01T00:00:00Z"},
		{name: "a day of month", spec: "0 0 1 * *", from: "2026-09-18T00:00:00Z", want: "2026-10-01T00:00:00Z"},
		// Vixie's OR rule: with both day fields restricted, either matches.
		{name: "restricted dom or dow", spec: "0 0 13 * fri", from: "2026-09-18T01:00:00Z", want: "2026-09-25T00:00:00Z"},
		{name: "@daily", spec: "@daily", from: "2026-09-18T01:00:00Z", want: "2026-09-19T00:00:00Z"},
		{name: "@hourly", spec: "@hourly", from: "2026-09-18T01:20:00Z", want: "2026-09-18T02:00:00Z"},
		{name: "@weekly is Sunday midnight", spec: "@weekly", from: "2026-09-18T01:00:00Z", want: "2026-09-20T00:00:00Z"},
		{name: "@monthly", spec: "@monthly", from: "2026-09-18T01:00:00Z", want: "2026-10-01T00:00:00Z"},
		{name: "@yearly", spec: "@yearly", from: "2026-09-18T01:00:00Z", want: "2027-01-01T00:00:00Z"},
		{name: "@every interval", spec: "@every 6h", from: "2026-09-18T01:00:00Z", want: "2026-09-18T07:00:00Z"},
		// A local-time input is evaluated in UTC, so the same expression
		// means the same instant on every node.
		{name: "a non-UTC input is normalised", spec: "0 3 * * *", from: "2026-09-18T01:15:00+02:00", want: "2026-09-18T03:00:00Z"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sched, err := rootfolderschedule.ParseSchedule(tc.spec)
			require.NoError(t, err)
			assert.Equal(t, at(tc.want), sched.Next(at(tc.from)))
		})
	}
}

// A calendar date that never occurs must terminate with the zero time rather
// than search forever.
func TestParseScheduleNextGivesUpOnAnImpossibleDate(t *testing.T) {
	sched, err := rootfolderschedule.ParseSchedule("0 0 30 2 *")
	require.NoError(t, err)
	assert.True(t, sched.Next(at("2026-09-18T00:00:00Z")).IsZero())
}

func TestParseScheduleRejectsBadExpressions(t *testing.T) {
	tests := []struct{ name, spec string }{
		{name: "empty", spec: ""},
		{name: "prose", spec: "not a cron expression"},
		{name: "too few fields", spec: "0 3 * *"},
		{name: "too many fields", spec: "0 3 * * * *"},
		{name: "minute out of range", spec: "60 3 * * *"},
		{name: "hour out of range", spec: "0 24 * * *"},
		{name: "day of month out of range", spec: "0 0 32 * *"},
		{name: "month out of range", spec: "0 0 1 13 *"},
		{name: "day of week out of range", spec: "0 0 * * 8"},
		{name: "inverted range", spec: "0 17-9 * * *"},
		{name: "zero step", spec: "*/0 * * * *"},
		{name: "unknown name", spec: "0 0 1 smarch *"},
		{name: "unknown descriptor", spec: "@fortnightly"},
		{name: "bad @every duration", spec: "@every never"},
		{name: "@every below a minute", spec: "@every 30s"},
		{name: "empty list element", spec: "0,,5 * * * *"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := rootfolderschedule.ParseSchedule(tc.spec)
			assert.Error(t, err)
		})
	}
}
