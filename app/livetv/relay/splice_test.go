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
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// keyAt is a keyframe packet whose PES carries pts.
func keyAt(pts int64) []byte { return videoPacketAt(pts, true, 0x09, 0x67, 0x68, 0x65) }

// frameAt is a non-keyframe packet whose PES carries pts.
func frameAt(pts int64) []byte { return videoPacketAt(pts, false, 0x09, 0x06, 0x41) }

// pesAt is a video PES header carrying the presentation time pts.
func pesAt(pts int64) []byte {
	return []byte{
		0x00, 0x00, 0x01, 0xe0, 0x00, 0x00, 0x80, 0x80, 0x05,
		byte(0x21 | pts>>29&0x0e), byte(pts >> 22), byte(0x01 | pts>>14&0xfe), byte(pts >> 7), byte(0x01 | pts<<1&0xfe),
	}
}

// videoPacketAt is videoPacket with the PES presentation time pts.
func videoPacketAt(pts int64, rai bool, nals ...byte) []byte {
	pes := pesAt(pts)
	for _, n := range nals {
		pes = append(pes, 0x00, 0x00, 0x00, 0x01, n, 0xaa, 0xbb)
	}
	return tsPacket(testVideoPID, true, rai, pes)
}

// gopAt is gop with its two video packets at pts and pts+3000.
func gopAt(pts int64) []byte {
	return cat(patPacket(), pmtPacket(0x1b), keyAt(pts), fill(testVideoPID, 40), fill(testAudioPID, 2), frameAt(pts+3000), fill(testVideoPID, 40))
}

func TestAPESPresentationTimeIsRead(t *testing.T) {
	for _, pts := range []int64{0, 1, 90000, 1<<33 - 1, 0x1_2345_6789} {
		got, ok := pesPTS(pesAt(pts))
		require.True(t, ok, "pts %d", pts)
		assert.Equal(t, pts&(ptsModulus-1), got)
	}
	_, ok := pesPTS([]byte{0, 0, 1, 0xe0, 0, 0, 0x80, 0x00, 0x00})
	assert.False(t, ok, "no PTS flag")
	x := newTSIndex(0)
	x.write(cat(patPacket(), pmtPacket(0x1b), keyAt(9000), frameAt(12000)))
	assert.Equal(t, int64(12000), x.pts)
	assert.Equal(t, int64(9000), x.keyPTS)
}

func TestPTSAheadAllowsForTheWrap(t *testing.T) {
	assert.True(t, ptsAhead(100, 50))
	assert.False(t, ptsAhead(50, 100))
	assert.False(t, ptsAhead(100, 100))
	assert.True(t, ptsAhead(5, ptsModulus-100), "just past the wrap")
	assert.False(t, ptsAhead(ptsModulus-100, 5))
}

// TestAReopenedConnectionsReplayedBacklogIsDropped: tvload.win opens every
// connection with about a minute of backlog, so a reopen spliced in as it
// came stepped PMS's time back and the conversion's muxer refused it
// (talos, 2026-11-05 05:00Z). The ring drops the reopened connection's
// bytes until a keyframe past the last time delivered, and stores the
// connection's tables before it.
func TestAReopenedConnectionsReplayedBacklogIsDropped(t *testing.T) {
	r := newRing(1 << 20)
	var dropped int64
	var aligned bool
	r.onSplice = func(d int64, a bool) { dropped, aligned = d, a }
	first := cat(patPacket(), pmtPacket(0x1b), keyAt(9000), fill(testVideoPID, 4), frameAt(12000), fill(testVideoPID, 4))
	r.Write(first)
	r.Realign()

	backlog := cat(patPacket(), pmtPacket(0x1b), keyAt(3000), fill(testVideoPID, 4), frameAt(6000), fill(testVideoPID, 4), keyAt(12000), fill(testVideoPID, 2))
	live := cat(keyAt(15000), fill(testVideoPID, 4), frameAt(18000))
	writeChunked(r, cat(backlog, live))

	assert.Equal(t, int64(len(backlog)), dropped, "everything up to the first keyframe past 12000")
	assert.True(t, aligned)
	cur := int64(len(first))
	got := make([]byte, int(r.Total()-cur))
	n, err := r.Read(context.Background(), &cur, got)
	require.NoError(t, err)
	assert.Equal(t, cat(patPacket(), pmtPacket(0x1b), live), got[:n], "the tables, then the stream from the keyframe at 15000")
	assert.Equal(t, int64(18000), r.lastPTS)
}

func TestAReopenedConnectionWithoutTablesIsPassedThroughAfterTheProbe(t *testing.T) {
	r := newRing(4 << 20)
	var aligned bool
	spliced := false
	r.onSplice = func(_ int64, a bool) { spliced, aligned = true, a }
	r.Write(cat(patPacket(), pmtPacket(0x1b), keyAt(9000)))
	r.Realign()
	junk := bytes.Repeat(tsPacket(0x1ff, false, false, bytes.Repeat([]byte{0x22}, packetSize-4)), catchUpProbe/packetSize+3)
	writeChunked(r, junk)
	assert.True(t, spliced)
	assert.False(t, aligned)
	assert.Equal(t, int64(2*packetSize), r.Total()-r.align, "the packets past the probe are stored")
}

func TestAReopenedConnectionThatNeverPassesTheLastTimeSplicesAtTheBudget(t *testing.T) {
	r := newRing(16 * packetSize)
	var aligned bool
	spliced := false
	r.onSplice = func(_ int64, a bool) { spliced, aligned = true, a }
	r.Write(cat(patPacket(), pmtPacket(0x1b), keyAt(900000)))
	r.Realign()
	behind := cat(patPacket(), pmtPacket(0x1b))
	for range 20 {
		behind = append(behind, keyAt(3000)...)
	}
	writeChunked(r, behind)
	assert.True(t, spliced, "the budget (the ring's capacity) spent, the next keyframe splices")
	assert.True(t, aligned)
	// 16 packets dropped spend the budget, the 17th marks it spent, the
	// 18th (a keyframe) splices: the tables, it, and the 4 after it.
	assert.Equal(t, int64(7*packetSize), r.Total()-r.align)
	cur := r.align
	got := make([]byte, 3*packetSize)
	_, err := r.Read(context.Background(), &cur, got)
	require.NoError(t, err)
	assert.Equal(t, cat(patPacket(), pmtPacket(0x1b), keyAt(3000)), got, "the tables first, then the keyframe")
}

func TestAReopenWithNoTimeDeliveredBeforeIsNotCaughtUp(t *testing.T) {
	r := newRing(1 << 20)
	r.Write(fill(0x1ff, 3))
	r.Realign()
	assert.Nil(t, r.catch)
	r.Write(fill(0x1ff, 2))
	assert.Equal(t, int64(2*packetSize), r.Total()-r.align)
}

// stallingUpstream streams first on its first connection and then sends
// nothing without closing; every later connection streams later.
func stallingUpstream(t *testing.T, first, later []byte) (url string, connections *atomic.Int32) {
	var conns atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := conns.Add(1)
		w.Header().Set("Content-Type", "video/mp2t")
		for i := 0; r.Context().Err() == nil; i++ {
			if n == 1 && i == 4 {
				// The first connection stalls after four GOPs: open, silent.
				for r.Context().Err() == nil {
					time.Sleep(5 * time.Millisecond)
				}
				return
			}
			g := later
			if n == 1 {
				g = first
			}
			if _, err := w.Write(g); err != nil {
				return
			}
			w.(http.Flusher).Flush()
			time.Sleep(5 * time.Millisecond)
		}
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/1005.ts", &conns
}

// TestAStalledUpstreamIsReopenedAndTheViewerStaysAttached: an upstream that
// went silent without closing was never reopened (talos, 2026-11-04 02:16Z:
// "input stalled" 30 s later). Now it is dropped after StallTimeout and
// reopened, and the reopened connection, which opens later in the
// stream's time, is spliced in from its first keyframe.
func TestAStalledUpstreamIsReopenedAndTheViewerStaysAttached(t *testing.T) {
	up, conns := stallingUpstream(t, gopAt(0), gopAt(90000))
	r := testRelay(t, Options{StallTimeout: 200 * time.Millisecond})
	body := viewer(t, r, up)
	_, err := io.ReadFull(body, make([]byte, 4*len(gopAt(0))))
	require.NoError(t, err)

	got := make([]byte, len(gopAt(90000)))
	_, err = io.ReadFull(body, got)

	require.NoError(t, err, "the viewer reads on from the reopened connection")
	assert.Equal(t, gopAt(90000), got, "the reopened connection from its tables and first keyframe")
	assert.Equal(t, int32(2), conns.Load(), "the stalled connection was dropped and one reopened")
}
