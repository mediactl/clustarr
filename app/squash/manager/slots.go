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

package manager

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Hardware classes a transcode slot can be budgeted against, from §12's
// `--slots cpu=2,nvidia=1,intel=1`.
const (
	// HardwareCPU is a libx265 software encode.
	HardwareCPU = "cpu"

	// HardwareNVIDIA is an hevc_nvenc encode on an nvidia.com/gpu.
	HardwareNVIDIA = "nvidia"

	// HardwareIntel is a QSV/VAAPI encode on gpu.intel.com/i915 or /xe.
	HardwareIntel = "intel"
)

// DefaultJobWindow and DefaultJobRetention are --job-window's and
// --job-retention's defaults.
const (
	DefaultJobWindow    = 32
	DefaultJobRetention = 24 * time.Hour
)

// DefaultSlots is §12's default budget: two concurrent CPU encodes and one per
// GPU vendor. The scheduler dispatches a Planned TranscodeJob to its pool
// only while a slot of its hardware class is free.
func DefaultSlots() map[string]int32 {
	return map[string]int32{HardwareCPU: 2, HardwareNVIDIA: 1, HardwareIntel: 1}
}

// ParseSlots reads the --slots flag, "cpu=2,nvidia=1,intel=1".
//
// An empty string returns [DefaultSlots]. A zero budget is legal and means
// "never admit this hardware class", which is how a cluster with no GPUs is
// configured; a negative one is not.
func ParseSlots(s string) (map[string]int32, error) {
	if strings.TrimSpace(s) == "" {
		return DefaultSlots(), nil
	}
	out := map[string]int32{}
	for _, pair := range strings.Split(s, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		hardware, budget, ok := strings.Cut(pair, "=")
		if !ok {
			return nil, fmt.Errorf("squasharr: --slots entry %q is not <hardware>=<count>", pair)
		}
		hardware = strings.TrimSpace(hardware)
		switch hardware {
		case HardwareCPU, HardwareNVIDIA, HardwareIntel:
		default:
			return nil, fmt.Errorf("squasharr: --slots names unknown hardware %q, want one of %s, %s, %s",
				hardware, HardwareCPU, HardwareNVIDIA, HardwareIntel)
		}
		if _, dup := out[hardware]; dup {
			return nil, fmt.Errorf("squasharr: --slots names %q twice", hardware)
		}
		n, err := strconv.ParseInt(strings.TrimSpace(budget), 10, 32)
		if err != nil {
			return nil, fmt.Errorf("squasharr: --slots budget for %q: %w", hardware, err)
		}
		if n < 0 {
			return nil, fmt.Errorf("squasharr: --slots budget for %q is negative", hardware)
		}
		out[hardware] = int32(n)
	}
	if len(out) == 0 {
		return DefaultSlots(), nil
	}
	return out, nil
}

// FormatSlots renders a budget back into the --slots syntax, in a stable order
// so it can be logged and compared.
func FormatSlots(slots map[string]int32) string {
	keys := make([]string, 0, len(slots))
	for k := range slots {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+strconv.FormatInt(int64(slots[k]), 10))
	}
	return strings.Join(parts, ",")
}
