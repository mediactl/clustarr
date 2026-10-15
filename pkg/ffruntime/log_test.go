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

package ffruntime

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/obinnaokechukwu/ffgo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newRouterForTest(t *testing.T, now func() time.Time) (*logRouter, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	r := newLogRouter(now)
	r.logger = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return r, &buf
}

// distinct is the i-th of many different FFmpeg errors: letters, since the
// repeat collapse reads digits as one.
func distinct(i int) string {
	return "an error in " + string(rune('A'+i/26%26)) + string(rune('a'+i%26)) + string(rune('a'+i/676)) + "\n"
}

// Errors reach slog at Warn as "ffmpeg", at most a burst of 20 and then
// 10 a second; warnings and below never do (spec §7.4).
func TestErrorsReachSlogRateLimited(t *testing.T) {
	now := time.Unix(0, 0)
	r, buf := newRouterForTest(t, func() time.Time { return now })
	r.dispatch(ffgo.LogWarning, "a warning\n")
	for i := range 100 {
		r.dispatch(ffgo.LogError, distinct(i))
	}
	assert.Equal(t, 20, strings.Count(buf.String(), "msg=ffmpeg"))
	assert.Equal(t, 20, strings.Count(buf.String(), "level=WARN"), "FFmpeg's errors are slog warnings")
	assert.NotContains(t, buf.String(), "a warning")
	now = now.Add(time.Second)
	for i := range 100 {
		r.dispatch(ffgo.LogError, distinct(100+i))
	}
	assert.Equal(t, 30, strings.Count(buf.String(), "msg=ffmpeg"), "10 more after one second")
}

// A decode that starts at a seek point -- every markers window -- makes
// FFmpeg's HEVC and H.264 decoders complain at error level about
// references from before it: benign, so Debug, never a warning. A healthy
// markers pod logged little else (2026-10-15). Capture sinks still see
// them.
func TestKnownDecodeNoiseIsDebug(t *testing.T) {
	r, buf := newRouterForTest(t, time.Now)
	var captured int
	defer r.capture(func(ffgo.LogLevel, string) { captured++ })()
	noise := []string{
		"Duplicate POC in a sequence: -1242.\n",
		"Could not find ref with POC 7\n",
		"Error constructing the frame RPS.\n",
		"co located POCs unavailable\n",
		"reference picture missing during reorder\n",
		"Missing reference picture, default is 65530\n",
		"number of reference frames (0+5) exceeds max (4; probably corrupt input), discarding one\n",
		"non-existing PPS 0 referenced\n",
	}
	for _, m := range noise {
		r.dispatch(ffgo.LogError, m)
	}
	assert.Equal(t, len(noise), captured)
	assert.NotContains(t, buf.String(), "level=WARN")
	assert.Equal(t, len(noise), strings.Count(buf.String(), "level=DEBUG"))
	r.dispatch(ffgo.LogError, "Invalid data found when processing input\n")
	assert.Equal(t, 1, strings.Count(buf.String(), "level=WARN"), "anything else is still a warning")
}

// The same message again -- its numbers aside -- is logged once a minute:
// the first time, then with the count of its repeats in between.
func TestARepeatedMessageLogsOnceThenItsCount(t *testing.T) {
	now := time.Unix(0, 0)
	r, buf := newRouterForTest(t, func() time.Time { return now })
	for i := range 50 {
		r.dispatch(ffgo.LogError, fmt.Sprintf("Invalid NAL unit size (%d > 512).\n", 1000+i))
		now = now.Add(100 * time.Millisecond)
	}
	assert.Equal(t, 1, strings.Count(buf.String(), "msg=ffmpeg"), "once within the minute")
	r.dispatch(ffgo.LogError, "a different error\n")
	assert.Equal(t, 2, strings.Count(buf.String(), "msg=ffmpeg"), "another message is its own")
	now = now.Add(time.Minute)
	r.dispatch(ffgo.LogError, "Invalid NAL unit size (77 > 512).\n")
	assert.Equal(t, 3, strings.Count(buf.String(), "msg=ffmpeg"))
	assert.Contains(t, buf.String(), "repeated=49")
}

// FFmpeg's own level is an attribute beside slog's, never a second "level":
// a JSON reader keeps the last of two keys, so every one of these Warn
// lines read as an error on talos (2026-10-15).
func TestFFmpegsLevelDoesNotShadowSlogsLevel(t *testing.T) {
	var buf bytes.Buffer
	r := newLogRouter(time.Now)
	r.logger = slog.New(slog.NewJSONHandler(&buf, nil))
	r.dispatch(ffgo.LogError, "Invalid data found when processing input\n")
	var rec map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rec))
	assert.Equal(t, "WARN", rec["level"])
	assert.Equal(t, "error", rec["ffmpegLevel"])
	assert.Equal(t, "Invalid data found when processing input", rec["message"])
}

func TestCaptureSeesEveryLineUntilReleased(t *testing.T) {
	r, _ := newRouterForTest(t, time.Now)
	var got []string
	release := r.capture(func(_ ffgo.LogLevel, m string) { got = append(got, m) })
	r.dispatch(ffgo.LogWarning, "w")
	r.dispatch(ffgo.LogError, "e")
	release()
	r.dispatch(ffgo.LogError, "after")
	assert.Equal(t, []string{"w", "e"}, got)
}

func TestMuteSilencesSlogButNotCaptures(t *testing.T) {
	r, buf := newRouterForTest(t, time.Now)
	var got int
	defer r.capture(func(ffgo.LogLevel, string) { got++ })()
	unmute := r.mute()
	r.dispatch(ffgo.LogError, "muted")
	unmute()
	r.dispatch(ffgo.LogError, "heard")
	assert.Equal(t, 2, got)
	assert.NotContains(t, buf.String(), "muted")
	assert.Contains(t, buf.String(), "heard")
}
