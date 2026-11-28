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
// stream's, else the one whose viewer joined longest ago. Viewers get a
// stream in real time by its own clock, Hold behind its upstream, however
// the upstream bursts (pacer), and a viewer whose stream has nothing to
// release is sent padding each KeepAlive, so its reader never times out
// while the upstream recovers. It is xTeVe's own buffer, without the
// ffmpeg and VLC options.
package relay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"slices"
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

// padding is an MPEG-TS null packet (nullPID), which every demuxer reads
// and discards: what a viewer is sent while its stream has nothing to
// release (Options.KeepAlive).
var padding = func() []byte {
	p := bytes.Repeat([]byte{0xff}, packetSize)
	p[0], p[1], p[2], p[3] = 0x47, nullPID>>8, nullPID&0xff, 0x10
	return p
}()

// Options configures a Relay. Zero values take the defaults noted.
type Options struct {
	// Context is the parent of every upstream: it carries the logger, and
	// cancelling it ends every stream. Default context.Background().
	Context context.Context
	// HTTP fetches the upstreams. Default http.DefaultClient.
	HTTP *http.Client
	// Tuners is how many channels may stream at once.
	Tuners int
	// Buffer is each channel's ring size in bytes. Default 64 MiB
	// (clustarrv1.DefaultStreamBuffer).
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
	// StallTimeout is how long an open upstream may send nothing before
	// it is dropped and reopened, as one that closed is. Default 15s. A
	// stalled connection was never reopened before: its viewers starved
	// until PMS's recorder (30 s) or the conversion (30 s) gave the
	// session up (talos, 2026-11-04 02:16Z, "input stalled").
	StallTimeout time.Duration
	// MediaStallTimeout is how long an open upstream that has shown PCRs
	// may send bytes without one before it is reopened, as a stalled one
	// is. Default 25s. tvload.win's CDN fills the silences between its
	// segments with null packets (2026-11-27), so StallTimeout never sees
	// a CDN that keeps padding with no media behind it; its silences ran
	// to 21 s, and 25 s with a reopen's 3 s fits the 30 s DefaultHold.
	MediaStallTimeout time.Duration
	// KeepAlive is how long a viewer may be sent nothing before it is sent
	// a null packet, and another each KeepAlive after, while its stream
	// has nothing to release: a reopened connection still catching up, an
	// upstream with no media. Default 1s; negative sends none. PMS's
	// recorder reads a tuner with -rw_timeout 30 s, and once a stream's
	// cushion ran out it was sent nothing until the upstream had new
	// media: on talos (2026-11-28 07:00Z) BBC World News sent none for a
	// minute and a half, two reopens replayed only what Plex had, and the
	// session ended 37 s after the cushion did. Padding keeps the
	// recorder's connection open however long the upstream takes, as a
	// real HDHomeRun's constant stream does; what then bounds the wait is
	// the conversion's own stall watch (cluster-plex's video worker).
	KeepAlive time.Duration
	// Reclaim is how long a channel whose watched stream a newer tune took
	// may not take a watched stream's tuner in turn. Default 10s.
	Reclaim time.Duration
	// Hold is how much of a stream's media is held back behind the newest
	// its upstream sent, and released to the viewers in real time by the
	// stream's clock (pacer): the cushion that hides the gaps between an
	// upstream's bursts, and the latency it adds. Default DefaultHold;
	// negative passes bytes as they come.
	Hold time.Duration
	// UserAgent is sent upstream.
	UserAgent string
	// Language is the ISO 639-2 code every stream's audio and subtitle
	// tracks are made to declare where they declare none or an
	// undetermined one; empty leaves the program maps as they come.
	Language string
	// OnChange is told the tuners in use whenever that changes.
	OnChange func(inUse int)
	// OnError is told each upstream failure's reason.
	OnError func(reason string)
	// OnDropped is told each viewer cut off for falling a whole buffer
	// behind its upstream.
	OnDropped func()
	// OnDry is told each time a watched stream's cushion runs out: the
	// release caught up with its upstream, and its viewers wait.
	OnDry func()
	// OnBytes is told each chunk of upstream bytes relayed.
	OnBytes func(n int)
	// OnFirstByte is told how long each upstream connection took to its
	// first byte.
	OnFirstByte func(d time.Duration)
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
	// buffer, linger, userAgent and language are Options', changed by
	// Configure for later streams.
	buffer    int
	linger    time.Duration
	userAgent string
	language  string
	// agents is the pool of User-Agents (SetUserAgents): each stream
	// takes one no other stream uses, and keeps it.
	agents []string
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
	lang      Language // declared in the program map's audio and subtitle tracks
	// slot is the User-Agent it took from the pool, kept across reopens;
	// -1 without a pool.
	slot int
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
		o.Buffer = 64 << 20
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
	if o.StallTimeout <= 0 {
		o.StallTimeout = 15 * time.Second
	}
	if o.MediaStallTimeout <= 0 {
		o.MediaStallTimeout = 25 * time.Second
	}
	switch {
	case o.KeepAlive == 0:
		o.KeepAlive = time.Second
	case o.KeepAlive < 0:
		o.KeepAlive = 0
	}
	if o.Reclaim <= 0 {
		o.Reclaim = 10 * time.Second
	}
	switch {
	case o.Hold == 0:
		o.Hold = DefaultHold
	case o.Hold < 0:
		o.Hold = 0
	}
	return &Relay{o: o, streams: map[string]*stream{}, taken: map[string]time.Time{}, tuners: o.Tuners, buffer: o.Buffer, linger: o.Linger, userAgent: o.UserAgent, language: o.Language}
}

// SetUserAgents gives each later stream its own User-Agent from pool: a
// new stream takes the first no live stream holds and sends it on every
// reopen (owner, 2026-11-28: argon-tv allows one stream per public IP and
// User-Agent, and "make the streams recycle a pool of 3 distinct user
// agents"). The relay then streams at most one channel per User-Agent.
// An empty pool sends every stream the relay's one User-Agent. A stream
// already open keeps the one it has.
func (r *Relay) SetUserAgents(pool []string) {
	r.mu.Lock()
	r.agents = slices.Clone(pool)
	r.mu.Unlock()
}

// limit is how many channels may stream at once: tuners, capped by the
// pool of User-Agents when there is one; r.mu is held.
func (r *Relay) limit() int {
	if len(r.agents) == 0 {
		return r.tuners
	}
	return min(r.tuners, len(r.agents))
}

// freeSlot is the first User-Agent in the pool no live stream holds, -1
// for none (or no pool); r.mu is held.
func (r *Relay) freeSlot() int {
	used := make([]bool, len(r.agents))
	for _, s := range r.streams {
		if !s.dead && s.slot >= 0 && s.slot < len(used) {
			used[s.slot] = true
		}
	}
	for i, u := range used {
		if !u {
			return i
		}
	}
	return -1
}

// agentFor is the User-Agent of a new stream in slot: the pool's, else the
// relay's own; r.mu is held.
func (r *Relay) agentFor(slot int) string {
	if slot >= 0 && slot < len(r.agents) {
		return r.agents[slot]
	}
	return r.userAgent
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

// Configure sets the limit, buffer, linger, user agent and language for
// later streams; no stream is ended.
func (r *Relay) Configure(tuners, buffer int, linger time.Duration, userAgent, language string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tuners, r.linger, r.userAgent, r.language = tuners, linger, userAgent, language
	if buffer > 0 {
		r.buffer = buffer
	}
}

// streamLanguage is the language a new stream declares: the channel's own,
// forced, else the relay's default, filled in; r.mu is held.
func (r *Relay) streamLanguage(l Language) Language {
	if l.Code == "" {
		return Fill(r.language)
	}
	return l
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
	r.ServeWith(w, req, channel, upstream, Language{})
}

// ServeWith is Serve with the language the channel's stream declares for
// its audio and subtitle tracks: the channel's own, replacing the stream's,
// or with an empty Code the relay's default, which fills a missing or
// undetermined one.
func (r *Relay) ServeWith(w http.ResponseWriter, req *http.Request, channel, upstream string, lang Language) {
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
		if r.inUse >= r.limit() {
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
		// The upstream outlives this viewer, so it takes Options.Context for
		// its lifetime, and the viewer's logger: the tuner serves viewers
		// under the manager's, while its own context carries none.
		ctx, cancel := context.WithCancel(logging.NewContext(r.o.Context, logging.FromContext(req.Context())))
		slot := r.freeSlot()
		s = &stream{channel: channel, ring: newRing(r.buffer), ready: make(chan struct{}), cancel: cancel, userAgent: r.agentFor(slot), lang: r.streamLanguage(lang), slot: slot}
		s.ring.onSplice = func(dropped int64, aligned bool) {
			logging.FromContext(ctx).Info("livetv: upstream reopened and spliced", "channel", channel, "droppedBytes", dropped, "aligned", aligned)
		}
		s.ring.onSyncLost = func(at int64) {
			logging.FromContext(ctx).Warn("livetv: upstream lost MPEG-TS sync; the rest of its connection is not indexed",
				"channel", channel, "bytesIn", at)
		}
		s.ring.onResume = func(dry time.Duration) {
			logging.FromContext(ctx).Info("livetv: stream resumed; its viewers waited for the upstream", "channel", channel, "waited", dry)
		}
		s.ring.setHold(r.o.Hold, func() {
			logging.FromContext(ctx).Info("livetv: stream ran dry; its viewers wait for the upstream", "channel", channel)
			if r.o.OnDry != nil {
				r.o.OnDry()
			}
		})
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
				logging.FromContext(req.Context()).Info("livetv: a new tune took a watched channel's tuner",
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
	cur, tables := s.ring.Join()
	defer s.ring.Leave(cur)
	if failed != "" && s.ring.Total() == 0 {
		http.Error(w, "upstream: "+failed, http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "video/mp2t")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	if len(tables) > 0 {
		_ = rc.SetWriteDeadline(time.Now().Add(writeTimeout))
		if _, err := w.Write(tables); err != nil {
			return
		}
	}
	buf := make([]byte, 64<<10)
	for {
		n, err := s.ring.ReadIdle(req.Context(), cur, buf, r.o.KeepAlive)
		if n > 0 {
			_ = rc.SetWriteDeadline(time.Now().Add(writeTimeout))
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			_ = rc.Flush()
		}
		if errors.Is(err, errIdle) {
			// Nothing to release: padding, between whole packets only,
			// keeps the viewer's connection open (Options.KeepAlive).
			if s.ring.boundary(cur) {
				_ = rc.SetWriteDeadline(time.Now().Add(writeTimeout))
				if _, werr := w.Write(padding); werr != nil {
					return
				}
				_ = rc.Flush()
			}
			continue
		}
		if err != nil {
			if errors.Is(err, errSlow) {
				r.dropped(req.Context(), channel, s.ring.size())
			}
			return
		}
	}
}

// dropped records a viewer cut off for falling a whole buffer behind. Its
// response ends cleanly, which a player reads as the stream's end: PMS's
// grab fails as "Recording failed. Please check your tuner or antenna"
// (talos, 2026-11-03: argon-tv opens a stream with about 60 s, 34 MB, of
// backlog in 2-3 s, and PMS's transcoder on a Pi fell an 8 MiB ring behind
// in every fresh tune). A bigger spec.stream.buffer holds such a burst.
func (r *Relay) dropped(ctx context.Context, channel string, buffer int) {
	logging.FromContext(ctx).Warn("livetv: a viewer fell a whole buffer behind its upstream and was cut off",
		"channel", channel, "buffer", buffer)
	if r.o.OnDropped != nil {
		r.o.OnDropped()
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

// watchMedia ends the connection ctx belongs to once s has had no PCR for
// MediaStallTimeout, checking a few times a timeout; it returns with ctx.
func (r *Relay) watchMedia(ctx context.Context, s *stream, cancel context.CancelCauseFunc) {
	t := time.NewTicker(max(r.o.MediaStallTimeout/10, 10*time.Millisecond))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if silent, ok := s.ring.mediaSilence(now); ok && silent > r.o.MediaStallTimeout {
				cancel(errNoMedia)
				return
			}
		}
	}
}

// errStalled ends an upstream connection that sent nothing for
// StallTimeout; errNoMedia one that sent no PCR for MediaStallTimeout.
var (
	errStalled = errors.New("relay: upstream stalled")
	errNoMedia = errors.New("relay: upstream sends no media")
)

// fetch copies one upstream connection into s's ring. It returns the reason
// the connection ended, whether that is final (an HLS answer), and whether
// it delivered any bytes. A connection that sends nothing for StallTimeout
// is dropped (ReasonTimeout) for the pump to reopen.
func (r *Relay) fetch(ctx context.Context, s *stream, upstream string) (reason string, final, delivered bool) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	began := time.Now()
	trace := newUpstreamTrace(began) // where the wait for the first byte goes
	req, err := http.NewRequestWithContext(httptrace.WithClientTrace(ctx, trace.hooks()), http.MethodGet, upstream, nil)
	if err != nil {
		return ReasonRefused, true, false
	}
	if s.userAgent != "" {
		req.Header.Set("User-Agent", s.userAgent)
	}
	log := logging.FromContext(ctx)
	resp, err := r.o.HTTP.Do(req)
	if err != nil {
		// The URL carries the account's credentials: only the cause is logged.
		cause := err
		var ue *url.Error
		if errors.As(err, &ue) {
			cause = ue.Err
		}
		if ctx.Err() == nil {
			log.Warn("livetv: upstream failed", "channel", s.channel, "after", time.Since(began), "path", trace.String(), "err", cause.Error())
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return ReasonTimeout, false, false
		}
		return ReasonRefused, false, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 400 {
		log.Warn("livetv: upstream refused", "channel", s.channel, "after", time.Since(began), "status", resp.StatusCode, "path", trace.String())
		return ReasonRefused, false, false
	}
	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "mpegurl") {
		return ReasonUnsupportedStream, true, false
	}
	buf := make([]byte, 32<<10)
	first := true
	rw := newTSRewriter(s.lang) // each connection's packets start afresh
	lastByte := time.Now()
	stall := time.AfterFunc(r.o.StallTimeout, func() { cancel(errStalled) })
	defer stall.Stop()
	go r.watchMedia(ctx, s, cancel)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			lastByte = time.Now()
			stall.Reset(r.o.StallTimeout)
			if first && bytes.HasPrefix(buf[:n], []byte("#EXTM3U")) {
				return ReasonUnsupportedStream, true, delivered
			}
			first = false
			s.ring.Write(rw.write(buf[:n]))
			if r.o.OnBytes != nil {
				r.o.OnBytes(n)
			}
			if !delivered {
				delivered = true
				took := time.Since(began)
				if r.o.OnFirstByte != nil {
					r.o.OnFirstByte(took)
				}
				log.Info("livetv: upstream answered", "channel", s.channel, "firstByte", took, "agent", s.slot+1, "path", trace.String())
				s.readyOnce.Do(func() { close(s.ready) })
			}
		}
		if err != nil {
			if errors.Is(context.Cause(ctx), errStalled) {
				log.Warn("livetv: upstream stalled; reopening", append([]any{"channel", s.channel, "silentFor", time.Since(lastByte)},
					silenceAttrs(s.ring.silence())...)...)
				return ReasonTimeout, false, delivered
			}
			if errors.Is(context.Cause(ctx), errNoMedia) {
				silent, _ := s.ring.mediaSilence(time.Now())
				log.Warn("livetv: upstream sends no media; reopening", append([]any{"channel", s.channel, "noMediaFor", silent},
					silenceAttrs(s.ring.silence())...)...)
				return ReasonTimeout, false, delivered
			}
			return ReasonEOF, false, delivered
		}
	}
}

// silenceAttrs are a reopen's log attributes for what its connection sent
// since its newest PCR (quietTally) and, if it was a reopen still being
// caught up, how far its replay got: a replay that ends behind what the
// viewers had is an upstream with nothing newer to send.
func silenceAttrs(sl silence) []any {
	a := []any{"paddingPackets", sl.padding, "videoPackets", sl.video, "otherPackets", sl.other, "garbledPackets", sl.garbled}
	if sl.catching {
		a = append(a, "catchingUp", true, "droppedBytes", sl.dropped)
		if sl.replayed {
			a = append(a, "replayBehind", sl.behind)
		}
	}
	return a
}
