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
)

// packetSize is an MPEG-TS packet's length.
const packetSize = 188

// errSlow ends a viewer that fell a whole buffer behind the upstream: the
// bytes it would read next have been overwritten.
var errSlow = errors.New("relay: viewer fell a buffer behind")

// ring is one channel's fan-out buffer: the upstream writes, every viewer
// reads at its own cursor, and the writer never waits for a reader.
type ring struct {
	mu      sync.Mutex
	buf     []byte
	written int64         // total bytes ever written
	align   int64         // where the current upstream connection began
	index   tsIndex       // the current connection's tables and keyframes
	notify  chan struct{} // closed and replaced on every write
	err     error         // set by close
}

func newRing(size int) *ring {
	return &ring{buf: make([]byte, size), index: newTSIndex(0), notify: make(chan struct{})}
}

// Write appends p, overwriting the oldest bytes, and wakes every reader.
func (r *ring) Write(p []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.index.write(p)
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

// Total is the bytes ever written.
func (r *ring) Total() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.written
}

// Realign marks the start of a new upstream connection, which begins a new
// packet wherever the last one was cut off.
func (r *ring) Realign() {
	r.mu.Lock()
	r.align = r.written
	r.index = newTSIndex(r.written)
	r.mu.Unlock()
}

// connectionStart is how far into a connection a new viewer still starts
// at its first byte (at most half the ring).
const connectionStart = 1 << 20

// Join returns a new viewer's cursor and the bytes to send it first. A
// viewer starts where a decoder can (talos, 2026-10-21: one that started at
// the newest packet began mid-GOP, so PMS discarded everything up to the next
// keyframe, and the first viewer missed the tables and keyframe a provider
// opens its stream with):
//   - near the connection's start, at its first byte, so the provider's
//     opening burst is the viewer's whole;
//   - else at the latest keyframe, after copies of the latest PAT and PMT,
//     when it is no more than half the ring back;
//   - else at the newest packet boundary.
func (r *ring) Join() (int64, []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	half := int64(len(r.buf)) / 2
	if r.written-r.align <= min(connectionStart, half) {
		return r.align, nil
	}
	if k := r.index.key; k >= 0 && r.written-k <= half {
		return k, append(bytes.Clone(r.index.pat), r.index.pmt...)
	}
	return r.written - (r.written-r.align)%packetSize, nil
}

// Read copies the bytes after *cur into p and advances *cur, waiting for a
// write when there are none. It returns errSlow when *cur's bytes are gone,
// the close error once the ring is closed and drained, or ctx's error.
func (r *ring) Read(ctx context.Context, cur *int64, p []byte) (int, error) {
	for {
		r.mu.Lock()
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

// close ends the ring: readers drain what is left, then get err.
func (r *ring) close(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err == nil {
		r.err = err
		close(r.notify)
		r.notify = make(chan struct{})
	}
}
