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
	"context"
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRingReaderWaitsThenReads(t *testing.T) {
	r := newRing(16)
	cur := r.Join()
	got := make(chan []byte)
	go func() {
		p := make([]byte, 8)
		n, err := r.Read(context.Background(), &cur, p)
		assert.NoError(t, err)
		got <- p[:n]
	}()
	time.Sleep(10 * time.Millisecond)
	r.Write([]byte("hello"))
	assert.Equal(t, []byte("hello"), <-got)
}

func TestRingReadsAcrossTheWrap(t *testing.T) {
	r := newRing(8)
	cur := int64(0)
	r.Write([]byte("abcdef"))
	p := make([]byte, 6)
	n, err := r.Read(context.Background(), &cur, p)
	require.NoError(t, err)
	assert.Equal(t, "abcdef", string(p[:n]))
	r.Write([]byte("ghijkl")) // wraps
	n, err = r.Read(context.Background(), &cur, p)
	require.NoError(t, err)
	assert.Equal(t, "ghijkl", string(p[:n]))
}

func TestRingReaderTooFarBehindGetsErrSlow(t *testing.T) {
	r := newRing(8)
	cur := int64(0)
	r.Write([]byte("0123456789"))
	_, err := r.Read(context.Background(), &cur, make([]byte, 4))
	assert.ErrorIs(t, err, errSlow)
}

func TestRingCloseEndsReaders(t *testing.T) {
	r := newRing(8)
	cur := r.Join()
	done := make(chan error)
	go func() {
		_, err := r.Read(context.Background(), &cur, make([]byte, 4))
		done <- err
	}()
	r.close(io.EOF)
	assert.ErrorIs(t, <-done, io.EOF)
}

func TestRingJoinIsAPacketBoundaryFromTheAlignment(t *testing.T) {
	r := newRing(4096)
	r.Write(make([]byte, 400)) // 2 packets and 24 bytes
	assert.Equal(t, int64(376), r.Join())
	r.Realign() // a reopened upstream starts a new packet at 400
	r.Write(make([]byte, 200))
	assert.Equal(t, int64(588), r.Join())
}

func TestRingReadHonoursTheContext(t *testing.T) {
	r := newRing(8)
	cur := r.Join()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := r.Read(ctx, &cur, make([]byte, 4))
	assert.ErrorIs(t, err, context.Canceled)
}
