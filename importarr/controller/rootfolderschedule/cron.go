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

package rootfolderschedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule answers "when does this cron expression next fire after t?".
type Schedule interface {
	// Next returns the first firing time strictly after t, in UTC, or the
	// zero time when the expression can never fire again (for example
	// "0 0 30 2 *", a 30th of February).
	Next(t time.Time) time.Time
}

// ParseSchedule parses a standard five-field cron expression -- minute,
// hour, day-of-month, month, day-of-week -- plus the usual @-descriptors
// (@yearly, @annually, @monthly, @weekly, @daily, @midnight, @hourly and
// @every <duration>).
//
// # Why this is not github.com/robfig/cron/v3
//
// This parser is a deliberate choice, not a stopgap, and swapping
// robfig/cron/v3 in would regress behaviour in three ways this controller
// depends on. (It began as a workaround -- the dependency the plan promised
// was never added to go.mod, and a worker must not run `go get` -- but the
// review that followed established it should stay.)
//
//  1. Next is strictly-after-MINUTE here, strictly-after-SECOND in robfig.
//     The controller stores a minute-truncated tick in
//     AnnotationLastTick, so robfig would answer Next(tick) with
//     tick+1s: due immediately, fired, re-stamped, and due again --
//     a scan loop rather than a schedule.
//  2. Times here are UTC. robfig's ParseStandard binds time.Local, so the
//     same expression would mean different instants on differently
//     configured nodes, and a RootFolder carries no timezone to
//     disambiguate with.
//  3. Day-of-week 7 is Sunday here, as in Vixie cron. robfig bounds dow at
//     {0,6} and returns an error for 7, so "0 3 * * 7" -- a perfectly
//     ordinary expression that works today -- would become a
//     reconcile.TerminalError on somebody's existing RootFolder.
//
// Semantics follow Vixie cron:
//
//   - a field is a comma-separated list of "*", "?", "n", "a-b", "n/step",
//     "*/step" or "a-b/step";
//   - "?" is accepted as a synonym for "*", which is what robfig and the
//     Quartz-flavoured expressions people paste in both do;
//   - months accept jan..dec and days-of-week sun..sat, case-insensitively,
//     including in ranges (jan-mar, mon-fri);
//   - day-of-week 7 is Sunday, the same day as 0;
//   - when day-of-month and day-of-week are BOTH restricted, a time matches
//     if it satisfies EITHER, not both. When one is "*", the other simply
//     applies.
//
// # Deliberate divergences, pinned by tests
//
// Two behaviours are this parser's own and are asserted in cron_test.go so
// that a later reader does not "fix" them into something else:
//
//   - "n/1" means exactly n, not "n to the end of the range". A step of 1
//     adds nothing to a bare value, and reading "5/1" as 5,6,7,...,59 is a
//     surprise nobody writes on purpose. "n/step" with step > 1 does run to
//     the end of the range, the way Vixie reads it.
//   - "*/1" counts as RESTRICTED for the day-of-month/day-of-week OR rule,
//     where a bare "*" counts as unrestricted. The two select the same set,
//     but only the literal "*" carries Vixie's "this field is not
//     specified" meaning.
//
// # Not supported, and rejected rather than guessed
//
// Wrapping ranges ("22-2"), sub-minute @every intervals, non-standard field
// counts and unknown descriptors all return an error naming the problem.
// Nothing here silently reinterprets an expression it does not understand:
// a schedule that quietly means something other than what was written is
// worse than one that refuses to load.
//
// Times are evaluated in UTC. Cluster workloads run on UTC clocks and a
// RootFolder carries no timezone, so a local-time schedule would silently
// mean different things on different nodes.
func ParseSchedule(spec string) (Schedule, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, fmt.Errorf("cron: empty expression")
	}

	if strings.HasPrefix(spec, "@") {
		return parseDescriptor(spec)
	}

	fields := strings.Fields(spec)
	if len(fields) != 5 {
		return nil, fmt.Errorf(
			"cron: %q has %d fields, want 5 (minute hour day-of-month month day-of-week)",
			spec, len(fields))
	}

	var (
		s   cronSchedule
		err error
	)
	if s.minute, err = parseField(fields[0], 0, 59, nil); err != nil {
		return nil, fmt.Errorf("cron: minute field %q: %w", fields[0], err)
	}
	if s.hour, err = parseField(fields[1], 0, 23, nil); err != nil {
		return nil, fmt.Errorf("cron: hour field %q: %w", fields[1], err)
	}
	if s.dom, err = parseField(fields[2], 1, 31, nil); err != nil {
		return nil, fmt.Errorf("cron: day-of-month field %q: %w", fields[2], err)
	}
	if s.month, err = parseField(fields[3], 1, 12, monthNames); err != nil {
		return nil, fmt.Errorf("cron: month field %q: %w", fields[3], err)
	}
	if s.dow, err = parseField(fields[4], 0, 7, dayNames); err != nil {
		return nil, fmt.Errorf("cron: day-of-week field %q: %w", fields[4], err)
	}
	// 7 and 0 are both Sunday.
	if s.dow&(1<<7) != 0 {
		s.dow |= 1
		s.dow &^= 1 << 7
	}

	s.domRestricted = !isWildcard(fields[2])
	s.dowRestricted = !isWildcard(fields[4])
	return &s, nil
}

var monthNames = map[string]uint{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12,
}

var dayNames = map[string]uint{
	"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6,
}

func parseDescriptor(spec string) (Schedule, error) {
	if rest, ok := strings.CutPrefix(spec, "@every "); ok {
		d, err := time.ParseDuration(strings.TrimSpace(rest))
		if err != nil {
			return nil, fmt.Errorf("cron: @every duration %q: %w", strings.TrimSpace(rest), err)
		}
		if d < time.Minute {
			// A scan schedule is evaluated to whole minutes throughout,
			// so a sub-minute interval cannot be honoured. Saying so
			// beats silently rounding it to a minute or to zero.
			return nil, fmt.Errorf(
				"cron: @every interval %s is shorter than a minute; the smallest supported interval is 1m", d)
		}
		return everySchedule{d: d.Truncate(time.Minute)}, nil
	}

	equivalents := map[string]string{
		"@yearly":   "0 0 1 1 *",
		"@annually": "0 0 1 1 *",
		"@monthly":  "0 0 1 * *",
		"@weekly":   "0 0 * * 0",
		"@daily":    "0 0 * * *",
		"@midnight": "0 0 * * *",
		"@hourly":   "0 * * * *",
	}
	equivalent, ok := equivalents[strings.ToLower(spec)]
	if !ok {
		return nil, fmt.Errorf("cron: unknown descriptor %q", spec)
	}
	return ParseSchedule(equivalent)
}

// isAny reports whether a single list element selects the whole range.
func isAny(part string) bool { return part == "*" || part == "?" }

// isWildcard reports whether a whole field is unrestricted in Vixie's sense,
// which is what drives the day-of-month/day-of-week OR rule. Only a bare "*"
// or "?" counts: "*/1" selects the same set but carries a step, and is
// treated as restricted (see the package-level note on divergences).
func isWildcard(field string) bool {
	for _, part := range strings.Split(field, ",") {
		if isAny(strings.TrimSpace(part)) {
			return true
		}
	}
	return false
}

// parseField turns one cron field into a bitmask over [min, max].
func parseField(field string, minValue, maxValue uint, names map[string]uint) (uint64, error) {
	var bits uint64
	for _, part := range strings.Split(field, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return 0, fmt.Errorf("empty list element")
		}

		step := uint(1)
		if base, stepStr, ok := strings.Cut(part, "/"); ok {
			parsed, err := strconv.ParseUint(strings.TrimSpace(stepStr), 10, 32)
			if err != nil || parsed == 0 {
				return 0, fmt.Errorf("bad step %q", stepStr)
			}
			step = uint(parsed)
			part = strings.TrimSpace(base)
		}

		// "*" (and its "?" synonym) is the whole range; anything else is a
		// value or an a-b range.
		low, high := minValue, maxValue
		if !isAny(part) {
			lowStr, highStr, isRange := strings.Cut(part, "-")
			var err error
			if low, err = parseValue(lowStr, names); err != nil {
				return 0, err
			}
			high = low
			if isRange {
				if high, err = parseValue(highStr, names); err != nil {
					return 0, err
				}
			} else if step > 1 {
				// "5/15" means "from 5 to the end of the range, every
				// 15", the way Vixie cron reads it.
				high = maxValue
			}
		}

		switch {
		case low > high:
			// Vixie cron does not wrap a range, and neither does this:
			// "22-2" is far more likely to be a mistake than a request
			// for a nightly window, and guessing which would be the
			// worst of both.
			return 0, fmt.Errorf(
				"range start %d is after range end %d; wrapping ranges such as 22-2 are not supported", low, high)
		case low < minValue || high > maxValue:
			return 0, fmt.Errorf("range %d-%d is outside %d-%d", low, high, minValue, maxValue)
		}
		for v := low; v <= high; v += step {
			bits |= 1 << v
		}
	}
	return bits, nil
}

func parseValue(s string, names map[string]uint) (uint, error) {
	s = strings.TrimSpace(s)
	if names != nil {
		if v, ok := names[strings.ToLower(s)]; ok {
			return v, nil
		}
	}
	v, err := strconv.ParseUint(s, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("bad value %q", s)
	}
	return uint(v), nil
}

// cronSchedule is a parsed five-field expression.
type cronSchedule struct {
	minute, hour, dom, month, dow uint64

	// domRestricted and dowRestricted drive the Vixie OR rule: when both
	// day fields name specific days, either one matching is enough.
	domRestricted, dowRestricted bool
}

// maxLookahead bounds the search so an expression that can never fire -- a
// 30th of February, say -- returns the zero time instead of spinning.
const maxLookahead = 5 * 365 * 24 * time.Hour

func (s *cronSchedule) Next(t time.Time) time.Time {
	t = t.UTC().Truncate(time.Minute).Add(time.Minute)
	limit := t.Add(maxLookahead)

	for t.Before(limit) {
		if !s.matchesDay(t) {
			// Skip the whole day rather than its 1440 minutes.
			t = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
			continue
		}
		if s.hour&(1<<uint(t.Hour())) == 0 {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, time.UTC).Add(time.Hour)
			continue
		}
		if s.minute&(1<<uint(t.Minute())) != 0 {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{}
}

func (s *cronSchedule) matchesDay(t time.Time) bool {
	if s.month&(1<<uint(t.Month())) == 0 {
		return false
	}
	domMatch := s.dom&(1<<uint(t.Day())) != 0
	dowMatch := s.dow&(1<<uint(t.Weekday())) != 0
	if s.domRestricted && s.dowRestricted {
		return domMatch || dowMatch
	}
	return domMatch && dowMatch
}

// everySchedule is "@every <duration>": a fixed interval rather than a
// calendar expression.
type everySchedule struct{ d time.Duration }

func (s everySchedule) Next(t time.Time) time.Time {
	return t.UTC().Truncate(time.Minute).Add(s.d)
}
