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
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The ring's buffer lives outside the Go heap and goes back to the kernel
// once the ring is closed and its last viewer has left: a viewer still
// draining a closed ring keeps it, and a write after the release is dropped,
// never a write into unmapped memory.
func TestTheRingReleasesItsBufferAfterItsLastViewerLeaves(t *testing.T) {
	r := newRing(4096)
	require.True(t, r.mapped, "the buffer is mapped off the heap")
	r.Write(make([]byte, 376))

	cur, _ := r.Join()
	r.close(io.EOF)
	assert.False(t, r.released(), "a viewer is still draining")

	// The viewer drains what was written, then reads the close.
	buf := make([]byte, 512)
	n, err := r.Read(t.Context(), &cur, buf)
	require.NoError(t, err)
	assert.Equal(t, 376, n)
	_, err = r.Read(t.Context(), &cur, buf)
	assert.ErrorIs(t, err, io.EOF)

	r.Leave()
	assert.True(t, r.released(), "the last viewer left a closed ring")
	r.Write(make([]byte, 188))
	assert.Equal(t, int64(376), r.Total(), "a write after the release is dropped")
	_, err = r.Read(t.Context(), &cur, buf)
	assert.ErrorIs(t, err, io.EOF, "a late reader gets the close")
	assert.Equal(t, 4096, r.size())
}

// A ring nobody is viewing is released at close.
func TestARingWithNoViewerIsReleasedAtClose(t *testing.T) {
	r := newRing(4096)
	r.Write(make([]byte, 188))
	r.close(io.EOF)
	assert.True(t, r.released())
}
