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

package relay

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pcrStep is how often the test streams carry a PCR.
const pcrStep = 40 * time.Millisecond

// feed gives p a PCR every pcrStep from from to to, each at the stream
// position equal to its own time, so a release position reads as the media
// time released.
func feed(p *pacer, from, to time.Duration) {
	for t := from; t <= to; t += pcrStep {
		p.pcr(ticks(t), ticks(t))
	}
}

// releasedAt is the media time p releases up to at now, of a stream fed up
// to written.
func releasedAt(p *pacer, now time.Time, written time.Duration) time.Duration {
	return span(p.release(now, ticks(written)+1, 0))
}

func TestThePacerSendsTheBacklogBeyondItsHoldAtOnceThenRealTime(t *testing.T) {
	p := newPacer(30 * time.Second)
	t0 := time.Now()
	feed(&p, 0, 60*time.Second) // the opening backlog, in one go

	assert.InDelta(t, 30*time.Second, releasedAt(&p, t0, 60*time.Second), float64(200*time.Millisecond), "all but the hold at once")
	assert.InDelta(t, 35*time.Second, releasedAt(&p, t0.Add(5*time.Second), 60*time.Second), float64(200*time.Millisecond), "then at real time")
	assert.InDelta(t, 25*time.Second, p.cushion(t0.Add(5*time.Second)), float64(100*time.Millisecond))
}

func TestAQuietUpstreamDrainsTheCushionThenTheStreamRunsDryOnce(t *testing.T) {
	p := newPacer(30 * time.Second)
	dry := 0
	p.ranDry = func() { dry++ }
	t0 := time.Now()
	feed(&p, 0, 60*time.Second)
	releasedAt(&p, t0, 60*time.Second)

	assert.InDelta(t, 59*time.Second, releasedAt(&p, t0.Add(29*time.Second), 60*time.Second), float64(200*time.Millisecond))
	assert.Zero(t, dry, "the cushion hid 29 s of silence")
	assert.Equal(t, 60*time.Second+span(1), releasedAt(&p, t0.Add(31*time.Second), 60*time.Second), "dry: all that came")
	releasedAt(&p, t0.Add(40*time.Second), 60*time.Second)
	assert.Equal(t, 1, dry, "one drought, counted once")
}

// TestABurstAfterRunningDryIsPacedFromItsArrival: the clock waits at the
// head, so the burst that ends a drought is not let out at once.
func TestABurstAfterRunningDryIsPacedFromItsArrival(t *testing.T) {
	p := newPacer(30 * time.Second)
	t0 := time.Now()
	feed(&p, 0, 60*time.Second)
	releasedAt(&p, t0, 60*time.Second)
	releasedAt(&p, t0.Add(31*time.Second), 60*time.Second) // dry

	feed(&p, 60*time.Second+pcrStep, 70*time.Second) // 9 s later
	at := t0.Add(40 * time.Second)

	assert.InDelta(t, 60*time.Second, releasedAt(&p, at, 70*time.Second), float64(200*time.Millisecond), "not 9 s of it at once")
	assert.InDelta(t, 65*time.Second, releasedAt(&p, at.Add(5*time.Second), 70*time.Second), float64(200*time.Millisecond))
}

func TestTheCushionNeverExceedsTheHold(t *testing.T) {
	p := newPacer(30 * time.Second)
	t0 := time.Now()
	feed(&p, 0, 60*time.Second)
	releasedAt(&p, t0, 60*time.Second)

	feed(&p, 60*time.Second+pcrStep, 80*time.Second) // 20 s more, a second later

	assert.InDelta(t, 50*time.Second, releasedAt(&p, t0.Add(time.Second), 80*time.Second), float64(200*time.Millisecond), "the newest less the hold, ahead of the clock's 31 s")
}

func TestADiscontinuityPacesAsNoTime(t *testing.T) {
	p := newPacer(30 * time.Second)
	for t := time.Duration(0); t <= 10*time.Second; t += pcrStep {
		p.pcr(ticks(t), ticks(time.Hour+t))
	}
	for t := time.Duration(0); t <= 5*time.Second; t += pcrStep { // the encoder restarted
		p.pcr(ticks(10*time.Second+pcrStep+t), ticks(t))
	}
	for t := time.Duration(0); t <= 5*time.Second; t += pcrStep { // and jumped an hour on
		p.pcr(ticks(16*time.Second+t), ticks(2*time.Hour+t))
	}

	assert.Equal(t, ticks(20*time.Second), p.head, "10 s, 5 s and 5 s of the stream's own time")
}

func TestThePCRWrapIsContinuous(t *testing.T) {
	p := newPacer(30 * time.Second)
	start := ptsModulus - ticks(time.Second)
	for t := time.Duration(0); t <= 2*time.Second; t += pcrStep {
		p.pcr(ticks(t), (start+ticks(t))&(ptsModulus-1))
	}

	assert.Equal(t, ticks(2*time.Second), p.head)
}

func TestThePacerNeverHoldsBytesBelowTheFloor(t *testing.T) {
	p := newPacer(30 * time.Second)
	feed(&p, 0, 60*time.Second)

	got := p.release(time.Now(), ticks(60*time.Second)+1, ticks(45*time.Second))

	assert.GreaterOrEqual(t, got, ticks(45*time.Second))
	assert.Less(t, got, ticks(45*time.Second+200*time.Millisecond))
}

func TestWithoutAPCROrAHoldBytesPassAsTheyCome(t *testing.T) {
	off := newPacer(0)
	feed(&off, 0, 60*time.Second)
	assert.Equal(t, int64(1234), off.release(time.Now(), 1234, 0), "no hold")
	assert.Empty(t, off.marks)

	none := newPacer(30 * time.Second)
	assert.Equal(t, int64(1234), none.release(time.Now(), 1234, 0), "no PCR")
	_, timed := none.due()
	assert.False(t, timed)
}

func TestDueIsWhenTheClockReachesTheNextMark(t *testing.T) {
	p := newPacer(30 * time.Second)
	t0 := time.Now()
	feed(&p, 0, 60*time.Second)
	rel := releasedAt(&p, t0, 60*time.Second)

	due, timed := p.due()

	require.True(t, timed)
	assert.Equal(t, t0.Add(rel-30*time.Second), due)
	assert.Positive(t, due.Sub(t0))
}

func TestAStreamsStartIsNoDrySpell(t *testing.T) {
	p := newPacer(30 * time.Second)
	dry := 0
	p.ranDry = func() { dry++ }
	t0 := time.Now()

	p.pcr(0, 0) // the first PCR: no cushion yet
	releasedAt(&p, t0, 0)
	feed(&p, pcrStep, 2*time.Second)
	releasedAt(&p, t0.Add(10*time.Second), 2*time.Second)

	assert.Zero(t, dry, "a stream that never had a cushion did not run out of one")
}

// pcrPacket is an adaptation-only packet on pid carrying the PCR base.
func pcrPacket(pid int, base int64) []byte {
	p := make([]byte, packetSize)
	p[0], p[1], p[2], p[3] = 0x47, byte(pid>>8)&0x1f, byte(pid), 0x20
	p[4], p[5] = packetSize-5, 0x10
	p[6], p[7], p[8], p[9] = byte(base>>25), byte(base>>17), byte(base>>9), byte(base>>1)
	p[10] = byte(base&1)<<7 | 0x7e
	for i := 12; i < packetSize; i++ {
		p[i] = 0xff
	}
	return p
}

func TestTheIndexReadsThePCRsOnThePMTsPCRPID(t *testing.T) {
	x := newTSIndex(0)
	x.write(cat(pcrPacket(testVideoPID, 7), patPacket(), pmtPacket(0x1b), pcrPacket(testVideoPID, ptsModulus-1), pcrPacket(testAudioPID, 99)))

	assert.Equal(t, []pcrMark{{pos: 3 * packetSize, base: ptsModulus - 1}}, x.pcrs, "only the PCR PID's, and only once the PMT names it")
}

// pacedStream is a test stream of the media time [from, to]: its tables,
// then every pcrStep a PCR on the video PID and fills packets of video,
// with a keyframe every keyEvery. at maps each PCR's offset in the stream
// to its media time.
func pacedStream(from, to, keyEvery time.Duration, fills int) (b []byte, at map[int64]time.Duration) {
	at = map[int64]time.Duration{}
	b = cat(patPacket(), pmtPacket(0x1b))
	for t := from; t <= to; t += pcrStep {
		at[int64(len(b))] = t
		b = append(b, pcrPacket(testVideoPID, ticks(t))...)
		if t%keyEvery == 0 {
			b = append(b, keyAt(ticks(t))...)
		} else {
			b = append(b, frameAt(ticks(t))...)
		}
		b = append(b, fill(testVideoPID, fills)...)
	}
	return b, at
}

// mediaAt is the media time of the last PCR before pos.
func mediaAt(at map[int64]time.Duration, pos int64) time.Duration {
	best, bestPos := time.Duration(-1), int64(-1)
	for p, t := range at {
		if p < pos && p > bestPos {
			best, bestPos = t, p
		}
	}
	return best
}

// fakeClock is a ring's clock, set by the test.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func pacedRing(t *testing.T, size int, hold time.Duration) (*ring, *fakeClock) {
	r := newRing(size)
	c := &fakeClock{t: time.Now()}
	r.now = c.now
	r.setHold(hold, nil)
	t.Cleanup(func() { r.close(io.EOF) })
	return r, c
}

func TestAPacedRingHoldsItsViewersBehindTheUpstream(t *testing.T) {
	r, clock := pacedRing(t, 4<<20, 2*time.Second)
	v, _ := r.Join() // the first viewer, at the connection's first byte
	b, at := pacedStream(0, 10*time.Second, time.Second, 4)
	writeChunked(r, b)
	buf := make([]byte, len(b))

	_, err := r.Read(t.Context(), v, buf)
	require.NoError(t, err)
	assert.InDelta(t, 8*time.Second, mediaAt(at, v.pos), float64(200*time.Millisecond), "all but the hold")

	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	_, err = r.Read(ctx, v, buf)
	assert.ErrorIs(t, err, context.DeadlineExceeded, "nothing more until the clock moves")

	clock.t = clock.t.Add(time.Second)
	_, err = r.Read(t.Context(), v, buf)
	require.NoError(t, err)
	assert.InDelta(t, 9*time.Second, mediaAt(at, v.pos), float64(200*time.Millisecond))
}

// TestALaterViewerOfAPacedRingStartsBehindTheReleasePoint: at the latest
// keyframe it would get every gap in the upstream as its own.
func TestALaterViewerOfAPacedRingStartsBehindTheReleasePoint(t *testing.T) {
	r, _ := pacedRing(t, 8<<20, 3*time.Second)
	b, at := pacedStream(0, 10*time.Second, time.Second, 40) // past connectionStart
	writeChunked(r, b)

	v, tables := r.Join()

	assert.Equal(t, 7*time.Second, mediaAt(at, v.pos+1), "the keyframe at the release point, not the newest at 10 s")
	assert.Equal(t, cat(patPacket(), pmtPacket(0x1b)), tables)
}

func TestAClosedPacedRingDrainsAtOnce(t *testing.T) {
	r, _ := pacedRing(t, 4<<20, 2*time.Second)
	v, _ := r.Join()
	b, _ := pacedStream(0, 10*time.Second, time.Second, 4)
	writeChunked(r, b)
	r.close(io.EOF)

	got, err := io.ReadAll(readerFunc(func(p []byte) (int, error) { return r.Read(t.Context(), v, p) }))

	require.NoError(t, err)
	assert.Equal(t, b, got)
}

type readerFunc func(p []byte) (int, error)

func (f readerFunc) Read(p []byte) (int, error) { return f(p) }

func TestThePacedRingGivesBackThePagesNoViewerCanRead(t *testing.T) {
	r, clock := pacedRing(t, 16<<20, time.Second)
	require.True(t, r.mapped)
	v, _ := r.Join() // at the connection's first byte
	b, _ := pacedStream(0, 30*time.Second, time.Second, 40)
	writeChunked(r, b)
	assert.Zero(t, r.trimmed, "the first viewer has read nothing")

	_, err := r.Read(t.Context(), v, make([]byte, len(b))) // up to the release point
	require.NoError(t, err)
	clock.t = clock.t.Add(10 * time.Millisecond)
	writeChunked(r, fill(testAudioPID, 1)) // any write trims

	page := int64(os.Getpagesize())
	assert.Greater(t, r.trimmed, int64(len(b))*9/10, "all but the second's hold and a GOP given back")
	assert.LessOrEqual(t, r.trimmed, v.pos&^(page-1), "never what the viewer reads next")
	assert.Zero(t, r.buf[r.trimmed-page], "a given-back page reads as zeroes")
	assert.Equal(t, b[v.pos], r.buf[v.pos], "what the viewer reads next is resident")

	r.Leave(v)
	writeChunked(r, fill(testAudioPID, 1))
	assert.Less(t, int64(len(b))-r.trimmed, int64(len(b))/10, "with no viewer, back to where a new one would start")
}

// TestABurstyUpstreamReachesItsViewerSteadily is the owner's sputter
// (2026-11-27): tvload.win sends whole segments with silences between,
// and the relay passed the silences on to PMS. Paced, the viewer's bytes
// arrive at least every few hundred milliseconds.
func TestABurstyUpstreamReachesItsViewerSteadily(t *testing.T) {
	const segment = 600 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		backlog, _ := pacedStream(0, 1200*time.Millisecond, 200*time.Millisecond, 2)
		if _, err := w.Write(backlog); err != nil {
			return
		}
		w.(http.Flusher).Flush()
		for from := 1200*time.Millisecond + pcrStep; req.Context().Err() == nil; from += segment {
			time.Sleep(segment)
			b, _ := pacedStream(from, from+segment-pcrStep, 200*time.Millisecond, 2)
			if _, err := w.Write(b[2*packetSize:]); err != nil { // the tables once
				return
			}
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(srv.Close)
	r := testRelay(t, Options{Hold: time.Second})
	body := viewer(t, r, srv.URL+"/1005.ts")

	var arrivals []time.Time
	buf := make([]byte, 4<<10)
	start := time.Now()
	for time.Since(start) < 3*time.Second {
		n, err := body.Read(buf)
		require.NoError(t, err)
		if n > 0 {
			arrivals = append(arrivals, time.Now())
		}
	}

	gap := time.Duration(0)
	for i := 1; i < len(arrivals); i++ {
		if arrivals[i].Sub(start) > time.Second { // past the opening backlog
			gap = max(gap, arrivals[i].Sub(arrivals[i-1]))
		}
	}
	assert.Less(t, gap, 350*time.Millisecond, "the upstream's %v silences reached the viewer", segment)
}

// nullPacket is a TS null packet (PID 0x1fff), the padding a CDN sends
// between segments.
func nullPacket() []byte {
	p := bytes.Repeat([]byte{0xff}, packetSize)
	p[0], p[1], p[2], p[3] = 0x47, 0x1f, 0xff, 0x10
	return p
}

// TestAnUpstreamPaddingWithoutMediaIsReopened: tvload.win's CDN fills the
// silences between segments with null packets (2026-11-27), so a
// connection whose media stopped would never trip StallTimeout.
func TestAnUpstreamPaddingWithoutMediaIsReopened(t *testing.T) {
	var conns atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		n := conns.Add(1)
		w.Header().Set("Content-Type", "video/mp2t")
		from := time.Duration(0)
		if n > 1 {
			from = 2 * time.Second // later in the stream's time, as a reopen is
		}
		b, _ := pacedStream(from, from+time.Second, 200*time.Millisecond, 2)
		if _, err := w.Write(b); err != nil {
			return
		}
		w.(http.Flusher).Flush()
		for req.Context().Err() == nil { // padding, and no media again
			time.Sleep(10 * time.Millisecond)
			if _, err := w.Write(nullPacket()); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(srv.Close)
	r := testRelay(t, Options{Hold: -1, MediaStallTimeout: 300 * time.Millisecond})
	body := viewer(t, r, srv.URL+"/1005.ts")

	go func() { _, _ = io.Copy(io.Discard, body) }()

	assert.Eventually(t, func() bool { return conns.Load() >= 2 }, 3*time.Second, 20*time.Millisecond, "the padded connection was reopened")
}

// viewerOn is viewer on channel ch.
func viewerOn(t *testing.T, r *Relay, ch, up string) io.Reader {
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { r.Serve(w, req, ch, up) }))
	t.Cleanup(front.Close)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, front.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { cancel(); _ = resp.Body.Close() })
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return resp.Body
}

// countingDialers are n dialers that dial directly, counting each one's
// connections.
func countingDialers(n int) ([]Dialer, func() []int) {
	var mu sync.Mutex
	counts := make([]int, n)
	ds := make([]Dialer, n)
	for i := range ds {
		ds[i] = func(ctx context.Context, network, address string) (net.Conn, error) {
			mu.Lock()
			counts[i]++
			mu.Unlock()
			var d net.Dialer
			return d.DialContext(ctx, network, address)
		}
	}
	return ds, func() []int {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(counts)
	}
}

// TestEachStreamLeavesThroughItsOwnTunnel: argon-tv allows one stream per
// public IP (owner, 2026-11-27), and each tunnel leaves from its own.
func TestEachStreamLeavesThroughItsOwnTunnel(t *testing.T) {
	r := testRelay(t, Options{Hold: -1})
	r.SetTuners(3)
	dialers, counts := countingDialers(3)
	r.SetEgress(dialers, true)
	up := tsUpstream(t, 0, nil, gop())
	for _, ch := range []string{"1001", "1002", "1003"} {
		_, err := io.CopyN(io.Discard, viewerOn(t, r, ch, up), 10_000)
		require.NoError(t, err)
	}
	assert.Equal(t, []int{1, 1, 1}, counts(), "three streams, three tunnels")

	_, err := io.CopyN(io.Discard, viewerOn(t, r, "1004", up), 10_000)

	require.NoError(t, err)
	assert.Equal(t, []int{2, 1, 1}, counts(), "a fourth channel takes the longest-watched stream's tuner, and its tunnel")
	assert.Equal(t, 3, r.InUse())
}

func TestFewerTunnelsThanTunersCapTheStreams(t *testing.T) {
	r := testRelay(t, Options{Hold: -1})
	r.SetTuners(3)
	dialers, counts := countingDialers(2)
	r.SetEgress(dialers, true)
	up := tsUpstream(t, 0, nil, gop())
	for _, ch := range []string{"1001", "1002", "1003"} {
		_, err := io.CopyN(io.Discard, viewerOn(t, r, ch, up), 10_000)
		require.NoError(t, err)
	}

	assert.Equal(t, 2, r.InUse(), "never two streams through one tunnel")
	assert.Equal(t, []int{2, 1}, counts())
}

// TestEgressWantedButNoneOpenStreamsOneChannel: three streams the default
// way would all leave from one address and knock each other off.
func TestEgressWantedButNoneOpenStreamsOneChannel(t *testing.T) {
	r := testRelay(t, Options{Hold: -1})
	r.SetTuners(3)
	r.SetEgress(nil, true)
	up := tsUpstream(t, 0, nil, gop())
	for _, ch := range []string{"1001", "1002"} {
		_, err := io.CopyN(io.Discard, viewerOn(t, r, ch, up), 10_000)
		require.NoError(t, err)
	}

	assert.Equal(t, 1, r.InUse())
}
