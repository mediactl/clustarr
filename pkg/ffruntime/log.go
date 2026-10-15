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
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/obinnaokechukwu/ffgo"
)

// logRouter is the one FFmpeg log callback a process installs (FFmpeg's
// log callback is process-wide; spec §7.4). Lines at LogError or worse go to
// slog as "ffmpeg" unless muted: known decode noise at Debug (decodeNoise),
// anything else at Warn, each message once per repeatWindow and then with
// the count of its repeats, under a rate limit. Every line goes to each
// Capture sink.
type logRouter struct {
	mu        sync.Mutex
	installed bool
	logger    *slog.Logger
	muted     int
	sinks     map[int]func(ffgo.LogLevel, string)
	next      int
	bucket    tokenBucket
	now       func() time.Time
	repeats   map[string]*repeat
}

// repeat is one message's logging: when it was last logged and how often it
// came again since.
type repeat struct {
	logged     time.Time
	suppressed int
}

// repeatWindow is how long a message logged once is not logged again: its
// repeats are counted, and the count rides on its next line a window later
// ("repeated"). maxRepeats bounds the messages remembered.
const (
	repeatWindow = time.Minute
	maxRepeats   = 256
)

// decodeNoise are what FFmpeg's HEVC and H.264 decoders print, at error
// level, when a decode starts at a seek point rather than at the stream's
// start -- as every markers window does, from the keyframe before it,
// often an open-GOP one whose references precede it. They say nothing
// about the file, so they go to slog at Debug: a healthy markers pod logged
// little but "Duplicate POC in a sequence" warnings (2026-10-15). Capture
// sinks see them as every other line.
var decodeNoise = []string{
	"Duplicate POC in a sequence",              // hevc refs.c
	"Could not find ref with POC",              // hevc refs.c
	"Error constructing the frame RPS",         // hevc hevcdec.c
	"co located POCs unavailable",              // h264_direct.c
	"reference picture missing during reorder", // h264_refs.c
	"Missing reference picture",                // h264_refs.c
	"number of reference frames",               // h264_slice.c: "... exceeds max ..., discarding one"
	"non-existing PPS",                         // h264: slices before the first PPS
}

func newLogRouter(now func() time.Time) *logRouter {
	return &logRouter{
		sinks: map[int]func(ffgo.LogLevel, string){}, now: now, repeats: map[string]*repeat{},
		bucket: tokenBucket{rate: 10, burst: 20, tokens: 20, now: now},
	}
}

var router = newLogRouter(time.Now)

// RouteLog sends FFmpeg's errors to l from now on.
func RouteLog(l *slog.Logger) {
	router.mu.Lock()
	router.logger = l
	router.mu.Unlock()
	router.install()
}

// Capture hands every FFmpeg line to sink until release is called.
func Capture(sink func(ffgo.LogLevel, string)) (release func()) {
	router.install()
	return router.capture(sink)
}

// Mute stops FFmpeg's errors reaching slog until release is called; Capture
// sinks still see them.
func Mute() (release func()) {
	router.install()
	return router.mute()
}

func (r *logRouter) install() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.installed {
		return
	}
	if err := ffgo.SetLogCallback(r.dispatch); err == nil {
		r.installed = true
	}
}

func (r *logRouter) capture(sink func(ffgo.LogLevel, string)) func() {
	r.mu.Lock()
	id := r.next
	r.next++
	r.sinks[id] = sink
	r.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { r.mu.Lock(); delete(r.sinks, id); r.mu.Unlock() }) }
}

func (r *logRouter) mute() func() {
	r.mu.Lock()
	r.muted++
	r.mu.Unlock()
	var once sync.Once
	return func() { once.Do(func() { r.mu.Lock(); r.muted--; r.mu.Unlock() }) }
}

func (r *logRouter) dispatch(level ffgo.LogLevel, msg string) {
	text := strings.TrimRight(msg, "\n")
	r.mu.Lock()
	sinks := make([]func(ffgo.LogLevel, string), 0, len(r.sinks))
	for _, s := range r.sinks {
		sinks = append(sinks, s)
	}
	logger := r.logger
	slogLevel, repeated, toSlog := slog.LevelWarn, 0, false
	if logger != nil && r.muted == 0 && level <= ffgo.LogError {
		if isDecodeNoise(text) {
			slogLevel, toSlog = slog.LevelDebug, true
		} else {
			repeated, toSlog = r.admitLocked(text)
		}
	}
	r.mu.Unlock()
	for _, s := range sinks {
		s(level, msg)
	}
	if !toSlog {
		return
	}
	// ffmpegLevel, not level: a second "level" key shadows slog's own for
	// any reader that keeps the last key (jq, Loki), so a Warn read as an error.
	attrs := []any{"ffmpegLevel", level.String(), "message", text}
	if repeated > 0 {
		attrs = append(attrs, "repeated", repeated)
	}
	logger.Log(context.Background(), slogLevel, "ffmpeg", attrs...)
}

// admitLocked decides whether text goes to slog: not within repeatWindow of
// its last line, whose repeats it counts instead, and not past the rate
// limit, under which it counts as a repeat too. It returns the repeats the
// line reports. The caller holds r.mu.
func (r *logRouter) admitLocked(text string) (repeated int, ok bool) {
	key := repeatKey(text)
	now := r.now()
	rep := r.repeats[key]
	if rep != nil && now.Sub(rep.logged) < repeatWindow {
		rep.suppressed++
		return 0, false
	}
	if !r.bucket.allow() {
		if rep != nil {
			rep.suppressed++
		}
		return 0, false
	}
	if rep == nil {
		if len(r.repeats) >= maxRepeats {
			for k, v := range r.repeats {
				if now.Sub(v.logged) >= repeatWindow {
					delete(r.repeats, k)
				}
			}
			if len(r.repeats) >= maxRepeats {
				clear(r.repeats)
			}
		}
		rep = &repeat{}
		r.repeats[key] = rep
	}
	repeated, rep.logged, rep.suppressed = rep.suppressed, now, 0
	return repeated, true
}

// repeatKey is text with every run of digits read as one: the same message
// about another POC, size or stream is a repeat.
func repeatKey(text string) string {
	var b strings.Builder
	digits := false
	for _, c := range text {
		if c >= '0' && c <= '9' {
			if !digits {
				b.WriteByte('#')
			}
			digits = true
			continue
		}
		digits = false
		b.WriteRune(c)
	}
	return b.String()
}

func isDecodeNoise(text string) bool {
	for _, n := range decodeNoise {
		if strings.Contains(text, n) {
			return true
		}
	}
	return false
}

// tokenBucket is a burst-then-rate limiter; the caller holds the router's lock.
type tokenBucket struct {
	rate, burst float64
	tokens      float64
	last        time.Time
	now         func() time.Time
}

func (b *tokenBucket) allow() bool {
	t := b.now()
	if !b.last.IsZero() {
		b.tokens = min(b.burst, b.tokens+t.Sub(b.last).Seconds()*b.rate)
	}
	b.last = t
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
