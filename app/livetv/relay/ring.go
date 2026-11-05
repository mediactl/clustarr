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
	"errors"
	"sync"

	"github.com/mediactl/clustarr/pkg/offheap"
)

// packetSize is an MPEG-TS packet's length.
const packetSize = 188

// errSlow ends a viewer that fell a whole buffer behind the upstream: the
// bytes it would read next have been overwritten. The relay logs and counts
// it (Relay.dropped).
var errSlow = errors.New("relay: viewer fell a buffer behind")

// ring is one channel's fan-out buffer: the upstream writes, every viewer
// reads at its own cursor, and the writer never waits for a reader.
//
// The buffer is mapped outside the Go heap (pkg/offheap): at 64 MiB a
// channel it was a fifth of the manager's live heap on talos (2026-11-03),
// and under GOGC a fifth of the headroom the collector kept beside it. It
// goes back to the kernel once the ring is closed and its last viewer has
// left (Leave), never at a collector's cycle; a viewer still draining a
// closed ring keeps it mapped.
type ring struct {
	mu       sync.Mutex
	buf      []byte // nil once released
	capacity int
	mapped   bool          // buf is offheap's, not the heap's
	viewers  int           // between Join and Leave
	written  int64         // total bytes ever written
	align    int64         // where the current upstream connection began
	index    tsIndex       // the current connection's tables and keyframes
	notify   chan struct{} // closed and replaced on every write
	err      error         // set by close
	// lastPTS is the latest video presentation time any connection has
	// delivered (-1 for none), what a reopened connection catches up to.
	lastPTS int64
	// catch is set while a reopened connection's bytes are being dropped
	// until they pass lastPTS (catchUp); onSplice is told how that ended.
	catch    *catchUp
	onSplice func(dropped int64, aligned bool)
}

// catchUp drops a reopened upstream connection's bytes until a keyframe
// later than the previous connection's last video time, so a viewer
// reading across the reopen sees a forward gap, never time stepping back.
// A provider opens every connection with its own burst of backlog
// (tvload.win: about a minute, 34 MB, in 2-3 s), and spliced in as it came
// the burst's earlier timestamps reached PMS's recorder and cluster-plex's
// conversion, whose TS muxer refused the step ("non monotonically
// increasing dts", talos 2026-11-05 05:00Z) and ended the session as
// "Recording failed". A connection that shows no TS tables within
// catchUpProbe bytes is passed through as it came; one that shows no
// eligible keyframe within the ring's capacity is passed through from its
// next keyframe.
type catchUp struct {
	index   tsIndex
	pkt     [packetSize]byte
	filled  int
	dropped int64
	lastPTS int64
	budget  int64
	lapsed  bool // the budget is spent: the next keyframe splices, eligible or not
}

// catchUpProbe is how far into a reopened connection TS tables must appear
// for it to be caught up at all.
const catchUpProbe = 1 << 20

func newRing(size int) *ring {
	r := &ring{capacity: size, index: newTSIndex(0), notify: make(chan struct{}), lastPTS: -1}
	if buf, err := offheap.Map(size); err == nil {
		r.buf, r.mapped = buf, true
	} else {
		// The heap is the fallback, never a refusal to stream.
		r.buf = make([]byte, size)
	}
	return r
}

// Write appends p, overwriting the oldest bytes, and wakes every reader. A
// released ring takes nothing; a ring catching a reopened connection up
// drops p's packets until the splice (catchUp).
func (r *ring) Write(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.buf == nil {
		return
	}
	if r.catch != nil {
		p = r.catchLocked(p)
		if len(p) == 0 {
			return
		}
	}
	r.appendLocked(p)
}

// appendLocked stores p and wakes the readers; r.mu is held.
func (r *ring) appendLocked(p []byte) {
	r.index.write(p)
	if r.index.pts >= 0 {
		r.lastPTS = r.index.pts
	}
	size := int64(len(r.buf))
	for len(p) > 0 {
		off := r.written % size
		n := copy(r.buf[off:], p)
		r.written += int64(n)
		p = p[n:]
	}
	close(r.notify)
	r.notify = make(chan struct{})
}

// catchLocked feeds p to the catch-up and returns what of it is to be
// stored: nothing while the connection is still behind, else the tables
// and the keyframe the splice starts at followed by the rest of p. r.mu is
// held.
func (r *ring) catchLocked(p []byte) []byte {
	c := r.catch
	for len(p) > 0 {
		n := copy(c.pkt[c.filled:], p)
		c.filled += n
		p = p[n:]
		if c.filled < packetSize {
			return nil
		}
		c.filled = 0
		pkt := c.pkt[:]
		key := !c.index.lost && c.index.packet(pkt)
		c.index.pos += packetSize
		switch {
		case key && (c.lapsed || c.index.keyPTS < 0 || ptsAhead(c.index.keyPTS, c.lastPTS)):
			// The splice: the connection's tables, then this keyframe on.
			return r.spliceLocked(bytes.Clone(pkt), p, true)
		case c.index.lost, c.dropped >= catchUpProbe && c.index.pmtPID < 0:
			// Not a TS the index can read: passed through as it came.
			return r.spliceLocked(bytes.Clone(pkt), p, false)
		case c.dropped >= c.budget && !c.lapsed:
			c.lapsed = true
		}
		c.dropped += packetSize
	}
	return nil
}

// spliceLocked ends the catch-up at pkt: what the ring stores next is the
// connection's tables (when it has them), pkt and rest.
func (r *ring) spliceLocked(pkt, rest []byte, aligned bool) []byte {
	c := r.catch
	r.catch = nil
	out := make([]byte, 0, len(c.index.pat)+len(c.index.pmt)+len(pkt)+len(rest))
	out = append(out, c.index.pat...)
	out = append(out, c.index.pmt...)
	out = append(out, pkt...)
	out = append(out, rest...)
	if r.onSplice != nil {
		r.onSplice(c.dropped, aligned)
	}
	return out
}

// Total is the bytes ever written.
func (r *ring) Total() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.written
}

// Realign marks the start of a new upstream connection, which begins a new
// packet wherever the last one was cut off; with a video time delivered
// before, the new connection is caught up to it first (catchUp).
func (r *ring) Realign() {
	r.mu.Lock()
	r.align = r.written
	r.index = newTSIndex(r.written)
	if r.lastPTS >= 0 && r.buf != nil {
		r.catch = &catchUp{index: newTSIndex(0), lastPTS: r.lastPTS, budget: int64(r.capacity)}
	}
	r.mu.Unlock()
}

// connectionStart is how far into a connection a new viewer still starts
// at its first byte (at most half the ring).
const connectionStart = 1 << 20

// Join returns a new viewer's cursor and the bytes to send it first, and
// counts the viewer until its Leave. A viewer starts where a decoder can
// (talos, 2026-10-21: one that started at the newest packet began mid-GOP,
// so PMS discarded everything up to the next keyframe, and the first viewer
// missed the tables and keyframe a provider opens its stream with):
//   - near the connection's start, at its first byte, so the provider's
//     opening burst is the viewer's whole;
//   - else at the latest keyframe, after copies of the latest PAT and PMT,
//     when it is no more than half the ring back;
//   - else at the newest packet boundary.
func (r *ring) Join() (int64, []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.viewers++
	half := int64(r.capacity) / 2
	if r.written-r.align <= min(connectionStart, half) {
		return r.align, nil
	}
	if k := r.index.key; k >= 0 && r.written-k <= half {
		return k, append(bytes.Clone(r.index.pat), r.index.pmt...)
	}
	return r.written - (r.written-r.align)%packetSize, nil
}

// Leave ends a viewer's reads. The last viewer of a closed ring releases
// its buffer.
func (r *ring) Leave() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.viewers--
	if r.viewers <= 0 && r.err != nil {
		r.releaseLocked()
	}
}

// Read copies the bytes after *cur into p and advances *cur, waiting for a
// write when there are none. It returns errSlow when *cur's bytes are gone,
// the close error once the ring is closed and drained (or released), or
// ctx's error.
func (r *ring) Read(ctx context.Context, cur *int64, p []byte) (int, error) {
	for {
		r.mu.Lock()
		if r.buf == nil {
			err := r.err
			r.mu.Unlock()
			return 0, err
		}
		size := int64(len(r.buf))
		if r.written-*cur > size {
			r.mu.Unlock()
			return 0, errSlow
		}
		if avail := r.written - *cur; avail > 0 {
			n := int(min(int64(len(p)), avail))
			off := int(*cur % size)
			c := copy(p[:n], r.buf[off:])
			if c < n {
				copy(p[c:n], r.buf)
			}
			*cur += int64(n)
			r.mu.Unlock()
			return n, nil
		}
		if r.err != nil {
			err := r.err
			r.mu.Unlock()
			return 0, err
		}
		wait := r.notify
		r.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
}

// close ends the ring: readers drain what is left, then get err. With no
// viewer reading, the buffer is released at once.
func (r *ring) close(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err == nil {
		r.err = err
		close(r.notify)
		r.notify = make(chan struct{})
	}
	if r.viewers <= 0 {
		r.releaseLocked()
	}
}

// releaseLocked gives the buffer back; r.mu is held, and the ring is closed.
func (r *ring) releaseLocked() {
	if r.buf == nil {
		return
	}
	if r.mapped {
		_ = offheap.Unmap(r.buf)
	}
	r.buf = nil
}

// released says whether the buffer has gone back.
func (r *ring) released() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.buf == nil
}

// size is the ring's capacity in bytes.
func (r *ring) size() int { return r.capacity }
