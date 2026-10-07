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
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/obinnaokechukwu/ffgo"
)

// logRouter is the one FFmpeg log callback a process installs (FFmpeg's
// log callback is process-wide; spec §7.4). Lines at LogError or worse go to
// slog at Warn as "ffmpeg", rate-limited unless muted; every line goes to
// each Capture sink.
type logRouter struct {
	mu        sync.Mutex
	installed bool
	logger    *slog.Logger
	muted     int
	sinks     map[int]func(ffgo.LogLevel, string)
	next      int
	bucket    tokenBucket
}

func newLogRouter(now func() time.Time) *logRouter {
	return &logRouter{sinks: map[int]func(ffgo.LogLevel, string){}, bucket: tokenBucket{rate: 10, burst: 20, tokens: 20, now: now}}
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
	r.mu.Lock()
	sinks := make([]func(ffgo.LogLevel, string), 0, len(r.sinks))
	for _, s := range r.sinks {
		sinks = append(sinks, s)
	}
	logger := r.logger
	toSlog := logger != nil && r.muted == 0 && level <= ffgo.LogError && r.bucket.allow()
	r.mu.Unlock()
	for _, s := range sinks {
		s(level, msg)
	}
	if toSlog {
		logger.Warn("ffmpeg", "level", level.String(), "message", strings.TrimRight(msg, "\n"))
	}
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
