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
// slog under the ffmpeg subsystem unless muted: known decode noise at Debug
// (decodeNoise), panic and fatal at FATAL, anything else at Warn, each
// message once per repeatWindow and then with the count of its repeats,
// under a rate limit; with the subsystem at debug (verbose) every line is
// routed the same way. Every line goes to each Capture sink.
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

	// verbose is an ffmpeg: debug (or trace) override in force: FFmpeg's own
	// level is raised to verbose through setLevel and every line it then
	// prints is routed, still through the window and the bucket. Without it
	// the C side stays at warning and only errors and worse are routed.
	verbose  bool
	setLevel func(ffgo.LogLevel) error
}

// The two levels slog does not name, as pkg/obs/logging names them (which
// this package may not link): below DEBUG is TRACE, above ERROR is FATAL.
const (
	levelTrace slog.Level = -8
	levelFatal slog.Level = 12
)

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
		bucket:   tokenBucket{rate: 10, burst: 20, tokens: 20, now: now},
		setLevel: ffgo.SetLogLevel,
	}
}

var router = newLogRouter(time.Now)

// RouteLog sends FFmpeg's lines to l from now on: the caller hands it the
// ffmpeg subsystem's logger and wires SetVerbose to that subsystem's level
// (pkg/ffruntime/ffroute.Log does both), since this package links nothing of
// clustarr's but ffgo (TestFFRuntimeImportsOnlyFFgo).
func RouteLog(l *slog.Logger) {
	router.mu.Lock()
	router.logger = l
	router.mu.Unlock()
	router.install()
}

// SetVerbose raises FFmpeg's own level to verbose and routes every line it
// then prints, or lowers it to warning and routes errors and worse again
// (the 2026-11-10 logging design §4: an ffmpeg: debug override in force).
func SetVerbose(on bool) { router.setVerbose(on) }

// setVerbose raises or lowers FFmpeg's own level and what the router
// forwards; the C side is left alone when it refuses the change.
func (r *logRouter) setVerbose(on bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.verbose == on {
		return
	}
	r.verbose = on
	level := ffgo.LogWarning
	if on {
		level = ffgo.LogVerbose
	}
	if r.setLevel != nil {
		_ = r.setLevel(level)
	}
}

// slogLevelFor is the record's level for an FFmpeg line (design §4): panic
// and fatal FATAL; error WARN, the collapse the router has made since
// 2026-10-15 (an FFmpeg "error" is mostly a recoverable decode complaint,
// and the job's own failure is logged by its worker at ERROR); warning WARN,
// info INFO, verbose DEBUG, debug and trace TRACE.
func slogLevelFor(level ffgo.LogLevel) slog.Level {
	switch {
	case level <= ffgo.LogFatal:
		return levelFatal
	case level <= ffgo.LogWarning:
		return slog.LevelWarn
	case level <= ffgo.LogInfo:
		return slog.LevelInfo
	case level <= ffgo.LogVerbose:
		return slog.LevelDebug
	default:
		return levelTrace
	}
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
	slogLevel, repeated, toSlog := slogLevelFor(level), 0, false
	if logger != nil && r.muted == 0 && (level <= ffgo.LogError || r.verbose) {
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
