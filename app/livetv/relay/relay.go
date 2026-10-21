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

// Package relay is Live TV's stream relay (spec 2026-10-07 §5.2): one
// upstream connection per channel, however many Plex clients watch it,
// admitted against the provider's tuner count. A slow viewer is cut off,
// never waited for; an upstream that fails mid-stream is reopened; the last
// viewer leaving keeps the upstream open for Linger, then frees the tuner.
// With every tuner streaming, the newest tune takes a tuner: an unwatched
// stream's, else the one whose viewer joined longest ago. It is xTeVe's own
// buffer, without the ffmpeg and VLC options.
package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// The upstream error reasons, the metric's bounded label values.
const (
	ReasonRefused           = "refused"
	ReasonTimeout           = "timeout"
	ReasonEOF               = "eof"
	ReasonUnsupportedStream = "unsupportedStream"
	ReasonPanic             = "panic"
)

// writeTimeout bounds one write to a viewer, so a client that stops reading
// cannot hold its goroutine forever; its next read then finds it behind.
const writeTimeout = 30 * time.Second

// Options configures a Relay. Zero values take the defaults noted.
type Options struct {
	// Context is the parent of every upstream: it carries the logger, and
	// cancelling it ends every stream. Default context.Background().
	Context context.Context
	// HTTP fetches the upstreams. Default http.DefaultClient.
	HTTP *http.Client
	// Tuners is how many channels may stream at once.
	Tuners int
	// Buffer is each channel's ring size in bytes. Default 8 MiB.
	Buffer int
	// Linger keeps an unwatched upstream open.
	Linger time.Duration
	// Retries is how often a failed upstream is reopened before its viewers
	// are ended. Default 3.
	Retries int
	// Backoff is the wait before a reopen. Default 1s.
	Backoff time.Duration
	// ReadyTimeout bounds a new viewer's wait for the first byte. Default 15s.
	ReadyTimeout time.Duration
	// Reclaim is how long a channel whose watched stream a newer tune took
	// may not take a watched stream's tuner in turn. Default 10s.
	Reclaim time.Duration
	// UserAgent is sent upstream.
	UserAgent string
	// OnChange is told the tuners in use whenever that changes.
	OnChange func(inUse int)
	// OnError is told each upstream failure's reason.
	OnError func(reason string)
	// OnBytes is told each chunk of upstream bytes relayed.
	OnBytes func(n int)
}

// Relay relays a provider's channels.
type Relay struct {
	o Options

	mu      sync.Mutex
	streams map[string]*stream
	// taken is when each channel's watched stream was last taken for a
	// newer tune, for Reclaim.
	taken   map[string]time.Time
	tuners  int
	inUse   int
	viewers int
	// buffer, linger and userAgent are Options', changed by Configure for
	// later streams.
	buffer    int
	linger    time.Duration
	userAgent string
}

type stream struct {
	channel   string
	ring      *ring
	viewers   int
	joined    time.Time     // when its newest viewer joined
	ready     chan struct{} // closed on the first byte or on failure
	readyOnce sync.Once
	failed    string // the reason, once failed
	dead      bool   // removed from the relay
	cancel    context.CancelFunc
	linger    *time.Timer
	userAgent string
}

// New returns a Relay.
func New(o Options) *Relay {
	if o.Context == nil {
		o.Context = context.Background()
	}
	if o.HTTP == nil {
		o.HTTP = http.DefaultClient
	}
	if o.Buffer <= 0 {
		o.Buffer = 8 << 20
	}
	if o.Retries == 0 {
		o.Retries = 3
	}
	if o.Backoff <= 0 {
		o.Backoff = time.Second
	}
	if o.ReadyTimeout <= 0 {
		o.ReadyTimeout = 15 * time.Second
	}
	if o.Reclaim <= 0 {
		o.Reclaim = 10 * time.Second
	}
	return &Relay{o: o, streams: map[string]*stream{}, taken: map[string]time.Time{}, tuners: o.Tuners, buffer: o.Buffer, linger: o.Linger, userAgent: o.UserAgent}
}

// InUse is the tuners streaming now.
func (r *Relay) InUse() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.inUse
}

// Viewers is the viewers attached now.
func (r *Relay) Viewers() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.viewers
}

// SetTuners changes the limit for later admissions; no stream is ended.
func (r *Relay) SetTuners(n int) {
	r.mu.Lock()
	r.tuners = n
	r.mu.Unlock()
}

// Configure sets the limit, buffer, linger and user agent for later
// streams; no stream is ended.
func (r *Relay) Configure(tuners, buffer int, linger time.Duration, userAgent string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tuners, r.linger, r.userAgent = tuners, linger, userAgent
	if buffer > 0 {
		r.buffer = buffer
	}
}

// Close ends every stream.
func (r *Relay) Close() {
	r.mu.Lock()
	streams := r.streams
	r.streams = map[string]*stream{}
	changed := r.inUse != 0
	r.inUse = 0
	for _, s := range streams {
		s.dead = true
		if s.linger != nil {
			s.linger.Stop()
		}
	}
	r.mu.Unlock()
	for _, s := range streams {
		s.cancel()
		s.ring.close(io.EOF)
		s.readyOnce.Do(func() { close(s.ready) })
	}
	if changed {
		r.changed(0)
	}
}

func (r *Relay) changed(inUse int) {
	if r.o.OnChange != nil {
		r.o.OnChange(inUse)
	}
}

// Serve streams channel to one viewer, from upstream when the channel is not
// streaming yet. With every tuner streaming it takes one (takeable), and
// answers 503, as a real HDHomeRun does, only when none may be taken; it
// answers 502 when the upstream cannot be read.
func (r *Relay) Serve(w http.ResponseWriter, req *http.Request, channel, upstream string) {
	defer func() {
		if p := recover(); p != nil {
			logging.FromContext(req.Context()).Error("livetv: viewer panicked", "channel", channel, "panic", fmt.Sprint(p))
		}
	}()
	r.mu.Lock()
	s, ok := r.streams[channel]
	if !ok {
		var evicted *stream
		took := false
		if r.inUse >= r.tuners {
			// An unwatched stream only lingers in case its viewer comes
			// back: a viewer who changed channel takes its tuner, or a
			// one-connection account answers 503 for the whole linger.
			evicted = r.lingering()
			if evicted == nil {
				evicted = r.takeable(channel, time.Now())
				took = evicted != nil
			}
			if evicted == nil {
				r.mu.Unlock()
				http.Error(w, "all tuners in use", http.StatusServiceUnavailable)
				return
			}
			if evicted.linger != nil {
				evicted.linger.Stop()
				evicted.linger = nil
			}
			if took {
				evicted.failed = "taken by channel " + channel
				r.taken[evicted.channel] = time.Now()
			}
			r.remove(evicted)
		}
		ctx, cancel := context.WithCancel(r.o.Context)
		s = &stream{channel: channel, ring: newRing(r.buffer), ready: make(chan struct{}), cancel: cancel, userAgent: r.userAgent}
		r.streams[channel] = s
		r.inUse++
		inUse := r.inUse
		r.mu.Unlock()
		if evicted != nil {
			// Closed before the new upstream opens: the account's one
			// connection is free when it asks. A taken stream's viewers'
			// responses end.
			evicted.cancel()
			evicted.ring.close(io.EOF)
			evicted.readyOnce.Do(func() { close(evicted.ready) })
			if took {
				logging.FromContext(r.o.Context).Info("livetv: a new tune took a watched channel's tuner",
					"channel", channel, "from", evicted.channel)
			}
		}
		go r.pump(ctx, s, upstream)
		r.changed(inUse)
		r.mu.Lock()
	}
	s.viewers++
	r.viewers++
	s.joined = time.Now()
	if s.linger != nil {
		s.linger.Stop()
		s.linger = nil
	}
	r.mu.Unlock()
	defer r.leave(s)

	timer := time.NewTimer(r.o.ReadyTimeout)
	defer timer.Stop()
	select {
	case <-s.ready:
	case <-req.Context().Done():
		return
	case <-timer.C:
		http.Error(w, "upstream did not answer", http.StatusBadGateway)
		return
	}
	r.mu.Lock()
	failed := s.failed
	r.mu.Unlock()
	cur := s.ring.Join()
	if failed != "" && cur == 0 {
		http.Error(w, "upstream: "+failed, http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	buf := make([]byte, 64<<10)
	for {
		n, err := s.ring.Read(req.Context(), &cur, buf)
		if n > 0 {
			_ = rc.SetWriteDeadline(time.Now().Add(writeTimeout))
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			_ = rc.Flush()
		}
		if err != nil {
			return
		}
	}
}

// leave detaches one viewer; the last one starts the linger timer.
func (r *Relay) leave(s *stream) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s.viewers--
	r.viewers--
	if s.viewers == 0 && !s.dead {
		s.linger = time.AfterFunc(r.linger, func() { r.expire(s) })
	}
}

// lingering is a stream no one watches, or nil;
// r.mu is held.
func (r *Relay) lingering() *stream {
	for _, s := range r.streams {
		if s.viewers == 0 && !s.dead {
			return s
		}
	}
	return nil
}

// takeable is the watched stream a tune of channel takes when every tuner
// streams: the one whose newest viewer joined longest ago, so the channel a
// viewer just left goes first. PMS keeps reading a channel for 30 s to 3 min
// after its client tunes another (talos, 2026-10-21), and on a
// one-connection account each such tune was refused 503, "Could not tune".
// A channel taken less than Reclaim ago takes nothing: its leftover reader
// reopening it must not take the tuner back and start a tug of war. Nil
// when nothing may be taken; r.mu is held.
func (r *Relay) takeable(channel string, now time.Time) *stream {
	for ch, at := range r.taken {
		if now.Sub(at) >= r.o.Reclaim {
			delete(r.taken, ch)
		}
	}
	if _, recent := r.taken[channel]; recent {
		return nil
	}
	var oldest *stream
	for _, s := range r.streams {
		if !s.dead && (oldest == nil || s.joined.Before(oldest.joined)) {
			oldest = s
		}
	}
	return oldest
}

// expire ends an unwatched stream once its linger has passed.
func (r *Relay) expire(s *stream) {
	r.mu.Lock()
	if s.viewers != 0 || s.dead {
		r.mu.Unlock()
		return
	}
	inUse := r.remove(s)
	r.mu.Unlock()
	s.cancel()
	s.ring.close(io.EOF)
	r.changed(inUse)
}

// remove takes s out of the relay; r.mu is held.
func (r *Relay) remove(s *stream) int {
	s.dead = true
	if r.streams[s.channel] == s {
		delete(r.streams, s.channel)
		r.inUse--
	}
	return r.inUse
}

// fail ends s for reason: its viewers' responses end, and its tuner is free.
func (r *Relay) fail(s *stream, reason string) {
	r.mu.Lock()
	s.failed = reason
	wasDead := s.dead
	inUse := r.remove(s)
	r.mu.Unlock()
	s.ring.close(io.EOF)
	s.readyOnce.Do(func() { close(s.ready) })
	s.cancel()
	if !wasDead {
		r.changed(inUse)
	}
}

func (r *Relay) report(reason string) {
	if r.o.OnError != nil {
		r.o.OnError(reason)
	}
}

// pump feeds s from upstream until s is cancelled, reopening a failed
// upstream up to Retries times in a row.
func (r *Relay) pump(ctx context.Context, s *stream, upstream string) {
	defer func() {
		if p := recover(); p != nil {
			logging.FromContext(ctx).Error("livetv: upstream panicked", "channel", s.channel, "panic", fmt.Sprint(p))
			r.report(ReasonPanic)
			r.fail(s, ReasonPanic)
		}
	}()
	failures := 0
	for {
		reason, final, delivered := r.fetch(ctx, s, upstream)
		if ctx.Err() != nil {
			return
		}
		r.report(reason)
		if delivered {
			failures = 0
		}
		failures++
		if final || failures > r.o.Retries {
			r.fail(s, reason)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.o.Backoff):
		}
		s.ring.Realign()
	}
}

// fetch copies one upstream connection into s's ring. It returns the reason
// the connection ended, whether that is final (an HLS answer), and whether
// it delivered any bytes.
func (r *Relay) fetch(ctx context.Context, s *stream, upstream string) (reason string, final, delivered bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, upstream, nil)
	if err != nil {
		return ReasonRefused, true, false
	}
	if s.userAgent != "" {
		req.Header.Set("User-Agent", s.userAgent)
	}
	resp, err := r.o.HTTP.Do(req)
	if err != nil {
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return ReasonTimeout, false, false
		}
		return ReasonRefused, false, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		return ReasonRefused, false, false
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "mpegurl") {
		return ReasonUnsupportedStream, true, false
	}
	buf := make([]byte, 32<<10)
	first := true
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if first && bytes.HasPrefix(buf[:n], []byte("#EXTM3U")) {
				return ReasonUnsupportedStream, true, delivered
			}
			first = false
			s.ring.Write(buf[:n])
			if r.o.OnBytes != nil {
				r.o.OnBytes(n)
			}
			if !delivered {
				delivered = true
				s.readyOnce.Do(func() { close(s.ready) })
			}
		}
		if err != nil {
			return ReasonEOF, false, delivered
		}
	}
}
