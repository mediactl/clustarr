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

package native

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/internal/testmedia"
)

// The probe reads a stream's own pixel aspect, so a DVD-shaped 720x480
// frame is recorded as the 16:9 or the 4:3 picture it shows, and a
// square-pixel frame as its own shape (ProbeVersion 5).
func TestProbeRecordsTheDisplayAspectRatio(t *testing.T) {
	for _, c := range []struct {
		name, source string
		sar          [2]int
		want         int32
	}{
		{"a 16:9 DVD frame", "testsrc2=size=720x480:rate=24", [2]int{32, 27}, 1778},
		{"a 4:3 DVD frame", "testsrc2=size=720x480:rate=24", [2]int{8, 9}, 1333},
		{"square pixels, 4:3", "testsrc2=size=320x240:rate=24", [2]int{}, 1333},
		{"square pixels, 16:9", "testsrc2=size=320x180:rate=24", [2]int{}, 1778},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := testmedia.Make(t, "clip.mkv", testmedia.Spec{
				Duration: time.Second,
				Video:    []testmedia.Video{{Source: c.source, SampleAspect: c.sar, Options: map[string]string{"preset": "ultrafast"}}},
			})
			mi, _, err := testProber(t).Probe(context.Background(), path)
			require.NoError(t, err)
			assert.Equal(t, c.want, mi.DisplayAspectMilli)
		})
	}
}
