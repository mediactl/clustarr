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
		{name: "@midnight is @daily", spec: "@midnight", from: "2026-09-18T01:00:00Z", want: "2026-09-19T00:00:00Z"},
		{name: "@annually is @yearly", spec: "@annually", from: "2026-09-18T01:00:00Z", want: "2027-01-01T00:00:00Z"},
		{name: "descriptors are case-insensitive", spec: "@DAILY", from: "2026-09-18T01:00:00Z", want: "2026-09-19T00:00:00Z"},

		// n/step: "from n to the end of the range, every step".
		{name: "n/step starts at n", spec: "5/15 * * * *", from: "2026-09-18T03:00:00Z", want: "2026-09-18T03:05:00Z"},
		{name: "n/step walks to the end of the range", spec: "5/15 * * * *", from: "2026-09-18T03:36:00Z", want: "2026-09-18T03:50:00Z"},
		{name: "n/step wraps into the next hour, not past 59", spec: "5/15 * * * *", from: "2026-09-18T03:51:00Z", want: "2026-09-18T04:05:00Z"},

		// a-b/step: stepped, and bounded by b.
		{name: "a-b/step inside the range", spec: "0-30/10 * * * *", from: "2026-09-18T03:01:00Z", want: "2026-09-18T03:10:00Z"},
		{name: "a-b/step stops at b", spec: "0-30/10 * * * *", from: "2026-09-18T03:31:00Z", want: "2026-09-18T04:00:00Z"},
		{name: "a-b/step on hours", spec: "0 9-17/4 * * *", from: "2026-09-18T10:00:00Z", want: "2026-09-18T13:00:00Z"},

		// Named ranges, in both name fields.
		{name: "a weekday-name range skips the weekend", spec: "0 6 * * mon-fri", from: "2026-09-19T00:00:00Z", want: "2026-09-21T06:00:00Z"},
		{name: "a weekday-name range matches inside itself", spec: "0 6 * * mon-fri", from: "2026-09-22T07:00:00Z", want: "2026-09-23T06:00:00Z"},
		{name: "a month-name range", spec: "0 0 1 jan-mar *", from: "2026-09-18T00:00:00Z", want: "2027-01-01T00:00:00Z"},
		{name: "a stepped month-name range", spec: "0 0 1 jan-dec/3 *", from: "2026-09-18T00:00:00Z", want: "2026-10-01T00:00:00Z"},
		{name: "names are case-insensitive", spec: "0 4 * * SUN", from: "2026-09-18T00:00:00Z", want: "2026-09-20T04:00:00Z"},

		// "?" is a synonym for "*", as robfig and Quartz-flavoured
		// expressions both treat it.
		{name: "? in day-of-week", spec: "0 3 * * ?", from: "2026-09-18T01:15:00Z", want: "2026-09-18T03:00:00Z"},
		{name: "? in day-of-month", spec: "0 3 ? * *", from: "2026-09-18T01:15:00Z", want: "2026-09-18T03:00:00Z"},
		{name: "? in both day fields", spec: "0 3 ? * ?", from: "2026-09-18T04:00:00Z", want: "2026-09-19T03:00:00Z"},
		{name: "? takes a step, being a plain synonym for *", spec: "0 3 ?/2 * *", from: "2026-09-18T04:00:00Z", want: "2026-09-19T03:00:00Z"},

		// A list containing "*" is the whole range, whatever else it names.
		{name: "a list containing * is the whole range", spec: "1,* * * * *", from: "2026-09-18T03:00:30Z", want: "2026-09-18T03:01:00Z"},
		{name: "a plain list of minutes", spec: "5,20,50 * * * *", from: "2026-09-18T03:21:00Z", want: "2026-09-18T03:50:00Z"},
		{name: "a list mixing a value and a range", spec: "0,30-32 * * * *", from: "2026-09-18T03:01:00Z", want: "2026-09-18T03:30:00Z"},
		{name: "a list mixing a name and a number", spec: "0 4 * * sun,3", from: "2026-09-21T00:00:00Z", want: "2026-09-23T04:00:00Z"},
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

// The two things this parser deliberately does not support must say so, not
// merely fail. A schedule that refuses to load with "bad value" sends the
// operator hunting; one that names the unsupported construct does not.
func TestUnsupportedConstructsAreNamedInTheError(t *testing.T) {
	tests := []struct{ name, spec, wantSubstring string }{
		{
			name: "a wrapping range names wrapping",
			spec: "0 22-2 * * *", wantSubstring: "wrapping ranges",
		},
		{
			name: "a sub-minute @every names the smallest interval",
			spec: "@every 30s", wantSubstring: "smallest supported interval is 1m",
		},
		{
			name: "an unknown descriptor names the descriptor",
			spec: "@fortnightly", wantSubstring: "@fortnightly",
		},
		{
			name: "a wrong field count names the expected fields",
			spec: "0 3 * *", wantSubstring: "minute hour day-of-month month day-of-week",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := rootfolderschedule.ParseSchedule(tc.spec)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantSubstring)
		})
	}
}

// Two behaviours are this parser's own rather than inherited, and are pinned
// here so a later reader does not "fix" them by accident. See the divergence
// note in cron.go's package documentation.
func TestDocumentedDivergencesFromVixie(t *testing.T) {
	// "n/1" is exactly n. Vixie would read it as n..max step 1.
	sched, err := rootfolderschedule.ParseSchedule("5/1 * * * *")
	require.NoError(t, err)
	assert.Equal(t, at("2026-09-18T04:05:00Z"), sched.Next(at("2026-09-18T03:05:00Z")),
		`"5/1" selects minute 5 only, not 5 through 59`)

	// "*/1" selects every value, but counts as RESTRICTED for the
	// day-of-month/day-of-week OR rule, where a bare "*" does not. With dom
	// "*/1" and dow "fri", the OR rule makes every day match; with a bare
	// "*" for dom, only Fridays would.
	ored, err := rootfolderschedule.ParseSchedule("0 0 */1 * fri")
	require.NoError(t, err)
	assert.Equal(t, at("2026-09-19T00:00:00Z"), ored.Next(at("2026-09-18T01:00:00Z")),
		`"*/1" is restricted, so the dom-or-dow rule matches every day`)

	anded, err := rootfolderschedule.ParseSchedule("0 0 * * fri")
	require.NoError(t, err)
	assert.Equal(t, at("2026-09-25T00:00:00Z"), anded.Next(at("2026-09-18T01:00:00Z")),
		`a bare "*" is unrestricted, so only Fridays match`)
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
		{name: "a wrapping hour range", spec: "0 22-2 * * *"},
		{name: "a wrapping weekday-name range", spec: "0 6 * * fri-mon"},
		{name: "a wrapping minute range", spec: "50-10 * * * *"},
		{name: "a range with a bad step", spec: "0-30/x * * * *"},
		{name: "a negative value", spec: "-5 * * * *"},
		{name: "a bare step with no field", spec: "/5 * * * *"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := rootfolderschedule.ParseSchedule(tc.spec)
			assert.Error(t, err)
		})
	}
}
