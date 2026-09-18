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
// The plan called for robfig/cron v3.0.1, and this task's brief states it was
// pre-added to go.mod by an earlier task. It was not: neither go.mod nor
// go.sum mentions it, and the module cache holds only the unrelated v1. A
// worker must never run `go get` (parallel agents corrupt go.mod that way),
// so the alternative to the ~150 lines below was leaving the whole scan
// schedule unimplemented. This parser is deliberately a drop-in for the one
// call site it has -- ParseSchedule/Schedule.Next mirror robfig's
// ParseStandard/Schedule.Next -- so swapping the dependency back in later is
// a two-line change plus deleting this file.
//
// Semantics follow Vixie cron, as robfig's ParseStandard does:
//
//   - a field is a comma-separated list of "*", "n", "a-b", "*/step" or
//     "a-b/step";
//   - months accept jan..dec and days-of-week sun..sat, case-insensitively;
//   - day-of-week 7 is Sunday, the same day as 0;
//   - when day-of-month and day-of-week are BOTH restricted, a time matches
//     if it satisfies EITHER, not both. When one is "*", the other simply
//     applies.
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
			return nil, fmt.Errorf("cron: @every interval %s is shorter than a minute", d)
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

func isWildcard(field string) bool {
	for _, part := range strings.Split(field, ",") {
		if strings.TrimSpace(part) == "*" {
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

		low, high := minValue, maxValue
		switch {
		case part == "*":
			// the full range
		default:
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

		if low < minValue || high > maxValue || low > high {
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
