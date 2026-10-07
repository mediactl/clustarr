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

import (
	"fmt"
	"strconv"
	"strings"
)

// SlotsEnv overrides consumers' per-pod slots for one Deployment, as
// "segmentarr-analyze=3,catalogarr-grab=8" (spec §9.2, §10.2.2).
const SlotsEnv = "CLUSTARR_CONSUMER_SLOTS"

// ParseSlotOverrides reads SlotsEnv's value. An empty value overrides
// nothing; a malformed entry, a count below one or a durable named twice is
// an error naming SlotsEnv.
func ParseSlotOverrides(v string) (map[string]int, error) {
	out := map[string]int{}
	if strings.TrimSpace(v) == "" {
		return out, nil
	}
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		name, count, ok := strings.Cut(part, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" {
			return nil, fmt.Errorf("events: %s: %q is not <durable>=<slots>", SlotsEnv, part)
		}
		n, err := strconv.Atoi(strings.TrimSpace(count))
		if err != nil || n < 1 {
			return nil, fmt.Errorf("events: %s: %q: slots must be a whole number of at least 1", SlotsEnv, part)
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("events: %s: %s is named twice", SlotsEnv, name)
		}
		out[name] = n
	}
	return out, nil
}

// SlotsFor is c's per-pod slots: the override for its name, else c.Slots.
func SlotsFor(c ConsumerSpec, overrides map[string]int) int {
	if n, ok := overrides[c.Name]; ok {
		return n
	}
	return c.Slots
}
