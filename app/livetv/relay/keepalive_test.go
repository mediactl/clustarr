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
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/obs/logging"
)

func TestTheIndexTalliesWhatCameSinceTheNewestPCR(t *testing.T) {
	x := newTSIndex(0)
	x.write(cat(patPacket(), pmtPacket(0x1b), pcrPacket(testVideoPID, 0), nullPacket(), nullPacket(), frameAt(0), fill(testAudioPID, 3)))

	assert.Equal(t, quietTally{padding: 2, video: 1, other: 3}, x.quiet)

	x.write(pcrPacket(testVideoPID, 3600))

	assert.Equal(t, quietTally{}, x.quiet, "a PCR is media: the tally starts again")
}

func TestLostSyncIsPlacedAndItsPacketsTalliedAsGarbled(t *testing.T) {
	x := newTSIndex(1000)
	bad := bytes.Repeat([]byte{0x22}, packetSize)
	x.write(cat(patPacket(), pmtPacket(0x1b), pcrPacket(testVideoPID, 0), bad, nullPacket(), frameAt(0)))

	assert.True(t, x.lost)
	assert.Equal(t, int64(1000+3*packetSize), x.lostAt)
	assert.Equal(t, quietTally{garbled: 3}, x.quiet, "nothing after the loss is read for what it is")
}

func TestTheRingSaysWhenSyncIsLostAndHowFarIn(t *testing.T) {
	r := newRing(1 << 20)
	at := int64(-1)
	r.onSyncLost = func(n int64) { at = n }
	r.Write(fill(testVideoPID, 2))
	r.Realign()
	writeChunked(r, cat(fill(testVideoPID, 3), bytes.Repeat([]byte{0x22}, packetSize), fill(testVideoPID, 2)))

	assert.Equal(t, int64(3*packetSize), at, "measured from the connection's first byte")
}

func TestABoundaryIsAWholePacketIntoTheConnection(t *testing.T) {
	r := newRing(1 << 20)
	r.Write(fill(testVideoPID, 2))
	r.Write([]byte{0x47, 0x01}) // the connection ends mid-packet
	r.Realign()
	r.Write(fill(testVideoPID, 1))

	for pos, want := range map[int64]bool{
		0:                       false, // the earlier connection's
		2 * packetSize:          false,
		2*packetSize + 2:        true, // the new connection's first byte
		3*packetSize + 2:        true,
		2*packetSize + 2 + 100:  false,
		2*packetSize + 2 + 1000: false,
	} {
		assert.Equal(t, want, r.boundary(&reader{pos: pos}), "position %d", pos)
	}
}

func TestReadIdleGivesUpWhenThereIsNothingToRead(t *testing.T) {
	r := newRing(1 << 20)
	v, _ := r.Join()
	t.Cleanup(func() { r.Leave(v) })
	began := time.Now()

	n, err := r.ReadIdle(context.Background(), v, make([]byte, 10), 30*time.Millisecond)

	assert.Zero(t, n)
	require.ErrorIs(t, err, errIdle)
	assert.GreaterOrEqual(t, time.Since(began), 30*time.Millisecond)

	r.Write([]byte("abc"))
	n, err = r.ReadIdle(context.Background(), v, make([]byte, 10), 30*time.Millisecond)

	require.NoError(t, err)
	assert.Equal(t, 3, n, "what there is comes at once")
}

// TestAReplayThatEndsBehindTheViewersIsReported is talos's 07:00Z on
// 2026-11-28: two reopens replayed media no later than what Plex had, and
// nothing said so.
func TestAReplayThatEndsBehindTheViewersIsReported(t *testing.T) {
	r := newRing(1 << 20)
	r.Write(cat(patPacket(), pmtPacket(0x1b), keyAt(90000), frameAt(180000)))
	r.Realign()
	replay := cat(patPacket(), pmtPacket(0x1b), pcrPacket(testVideoPID, 0), keyAt(0), frameAt(90000), nullPacket(), nullPacket())
	writeChunked(r, replay)

	got := r.silence()

	assert.True(t, got.catching)
	assert.Equal(t, int64(len(replay)), got.dropped)
	assert.True(t, got.replayed)
	assert.Equal(t, time.Second, got.behind, "the replay's newest video, a second behind the viewers'")
	assert.Equal(t, quietTally{padding: 2, video: 2}, got.quietTally)
}

func TestAConnectionNotCatchingUpReportsOnlyItsTally(t *testing.T) {
	r := newRing(1 << 20)
	r.Write(cat(patPacket(), pmtPacket(0x1b), pcrPacket(testVideoPID, 0), nullPacket()))

	assert.Equal(t, silence{quietTally: quietTally{padding: 1}}, r.silence())
}

func TestThePacerSaysHowLongAStreamWasDry(t *testing.T) {
	p := newPacer(2 * time.Second)
	var waited []time.Duration
	p.refilled = func(d time.Duration) { waited = append(waited, d) }
	t0 := time.Now()
	feed(&p, 0, 3*time.Second)
	releasedAt(&p, t0, 3*time.Second)
	releasedAt(&p, t0.Add(2*time.Second), 3*time.Second) // the clock reaches the head: dry

	feed(&p, 3*time.Second+pcrStep, 6*time.Second)
	releasedAt(&p, t0.Add(9*time.Second), 6*time.Second)

	assert.Equal(t, []time.Duration{7 * time.Second}, waited, "dry from t0+2s, a cushion again at t0+9s")
}

// loggedViewer is viewer with the relay's lines kept, read back by logs.
func loggedViewer(t *testing.T, r *Relay, up string) (io.Reader, func() string) {
	var mu sync.Mutex
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}), nil))
	front := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { r.Serve(w, req, "1005", up) }))
	front.Config.BaseContext = func(net.Listener) context.Context { return logging.NewContext(context.Background(), logger) }
	front.Start()
	t.Cleanup(front.Close)
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, front.URL, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { cancel(); _ = resp.Body.Close() })
	require.Equal(t, http.StatusOK, resp.StatusCode)
	return resp.Body, func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
}

// arrival is one read of a viewer's body.
type arrival struct {
	at time.Time
	b  []byte
}

// TestADryStreamsViewerIsSentPaddingUntilTheUpstreamReturns is talos's
// 07:00Z on 2026-11-28: the upstream sent no new media, each reopen
// replayed only what the viewer had, and once the cushion ran out the
// viewer was sent nothing at all, so PMS's recorder (-rw_timeout 30 s) or
// the conversion's stall watch ended the session. Padded, the viewer hears
// from the relay every KeepAlive, never mid-packet, until new media comes.
func TestADryStreamsViewerIsSentPaddingUntilTheUpstreamReturns(t *testing.T) {
	var back atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		from := time.Duration(0) // every reopen replays the same minute
		if back.Load() {
			from = 5 * time.Second // the source has media again
		}
		b, _ := pacedStream(from, from+2*time.Second, 200*time.Millisecond, 2)
		if _, err := w.Write(b); err != nil {
			return
		}
		w.(http.Flusher).Flush()
		for req.Context().Err() == nil { // the CDN pads, with nothing behind it
			time.Sleep(10 * time.Millisecond)
			if _, err := w.Write(nullPacket()); err != nil {
				return
			}
			w.(http.Flusher).Flush()
		}
	}))
	t.Cleanup(srv.Close)
	// A hold past dryCushion, so the stream can count as resumed.
	r := testRelay(t, Options{Hold: 1200 * time.Millisecond, MediaStallTimeout: 300 * time.Millisecond, KeepAlive: 50 * time.Millisecond})
	body, logs := loggedViewer(t, r, srv.URL+"/1005.ts")
	reads := make(chan arrival, 4096)
	go func() {
		defer close(reads)
		for {
			buf := make([]byte, 64<<10)
			n, err := body.Read(buf)
			if n > 0 {
				reads <- arrival{time.Now(), buf[:n]}
			}
			if err != nil {
				return
			}
		}
	}()
	start := time.Now()
	var got []arrival
	collect := func(until time.Duration) {
		deadline := time.After(time.Until(start.Add(until)))
		for {
			select {
			case a := <-reads:
				got = append(got, a)
			case <-deadline:
				return
			}
		}
	}

	collect(2200 * time.Millisecond) // reopens from about 300 ms; the cushion runs out at about 1.2 s
	back.Store(true)
	collect(3400 * time.Millisecond)

	// The longest the viewer heard nothing while dry, the window's edges
	// counted, so a window with no read at all is one long gap.
	from, to := start.Add(1400*time.Millisecond), start.Add(2200*time.Millisecond)
	last, gap := from, time.Duration(0)
	for _, a := range got {
		if a.at.After(from) && a.at.Before(to) {
			gap, last = max(gap, a.at.Sub(last)), a.at
		}
	}
	gap = max(gap, to.Sub(last))
	assert.Less(t, gap, 200*time.Millisecond, "the viewer went without a byte while its stream was dry")
	var stream []byte
	for _, a := range got {
		stream = append(stream, a.b...)
	}
	require.Zero(t, len(stream)%packetSize, "whole packets only")
	newMedia := false
	for p := 0; p < len(stream); p += packetSize {
		pkt := stream[p : p+packetSize]
		require.Equal(t, byte(0x47), pkt[0], "a packet at %d without its sync byte: padding went in mid-packet", p)
		if base, ok := pcrBase(pkt); ok && base >= ticks(5*time.Second) {
			newMedia = true
		}
	}
	assert.True(t, newMedia, "the source's new media reached the viewer after the padding")
	text := logs()
	assert.Contains(t, text, "livetv: upstream sends no media; reopening")
	assert.Contains(t, text, "catchingUp=true")
	assert.Contains(t, text, "replayBehind=0s", "each replay ended where the viewer already was")
	assert.Regexp(t, `paddingPackets=[1-9]`, text)
	assert.Contains(t, text, "livetv: stream resumed; its viewers waited for the upstream")
}
