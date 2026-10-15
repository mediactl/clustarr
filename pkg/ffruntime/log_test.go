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

// Errors reach slog at Warn as "ffmpeg", at most a burst of 20 and then
// 10 a second; warnings and below never do (spec §7.4).
func TestErrorsReachSlogRateLimited(t *testing.T) {
	now := time.Unix(0, 0)
	r, buf := newRouterForTest(t, func() time.Time { return now })
	r.dispatch(ffgo.LogWarning, "a warning\n")
	for range 100 {
		r.dispatch(ffgo.LogError, "an error\n")
	}
	assert.Equal(t, 20, strings.Count(buf.String(), "msg=ffmpeg"))
	assert.Equal(t, 20, strings.Count(buf.String(), "level=WARN"), "FFmpeg's errors are slog warnings")
	assert.NotContains(t, buf.String(), "a warning")
	now = now.Add(time.Second)
	for range 100 {
		r.dispatch(ffgo.LogError, "an error\n")
	}
	assert.Equal(t, 30, strings.Count(buf.String(), "msg=ffmpeg"), "10 more after one second")
}

// FFmpeg's own level is an attribute beside slog's, never a second "level":
// a JSON reader keeps the last of two keys, so every one of these Warn
// lines read as an error on talos (2026-10-15).
func TestFFmpegsLevelDoesNotShadowSlogsLevel(t *testing.T) {
	var buf bytes.Buffer
	r := newLogRouter(time.Now)
	r.logger = slog.New(slog.NewJSONHandler(&buf, nil))
	r.dispatch(ffgo.LogError, "Duplicate POC in a sequence: 1242.\n")
	var rec map[string]any
	require.NoError(t, json.Unmarshal(buf.Bytes(), &rec))
	assert.Equal(t, "WARN", rec["level"])
	assert.Equal(t, "error", rec["ffmpegLevel"])
	assert.Equal(t, "Duplicate POC in a sequence: 1242.", rec["message"])
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
