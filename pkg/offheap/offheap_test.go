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

package offheap

import (
	"os"
	"runtime"
	"runtime/metrics"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A mapped buffer is zeroed, page-aligned, writable, and outside the Go
// heap: mapping 64 MiB moves the heap's accounting by nothing.
func TestMapIsOutsideTheGoHeap(t *testing.T) {
	heap := func() uint64 {
		runtime.GC()
		s := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
		metrics.Read(s)
		return s[0].Value.Uint64()
	}
	before := heap()
	b, err := Map(64 << 20)
	require.NoError(t, err)
	assert.Len(t, b, 64<<20)
	assert.Zero(t, uintptr(unsafe.Pointer(&b[0]))%uintptr(os.Getpagesize()), "page-aligned")
	b[0], b[len(b)-1] = 1, 2
	assert.EqualValues(t, 1, b[0])
	after := heap()
	assert.Less(t, after, before+(8<<20), "64 MiB mapped off the heap grew the heap by %d bytes", after-before)
	require.NoError(t, Unmap(b))
}

func TestMapRefusesNoBytesAndUnmapTakesNil(t *testing.T) {
	_, err := Map(0)
	assert.ErrorIs(t, err, ErrSize)
	assert.NoError(t, Unmap(nil))
}

// Discarded pages read as zeroes and take writes again; a partial page at
// either end of the range keeps its bytes.
func TestDiscardGivesBackWholePagesOnly(t *testing.T) {
	page := os.Getpagesize()
	b, err := Map(4 * page)
	require.NoError(t, err)
	t.Cleanup(func() { _ = Unmap(b) })
	for i := range b {
		b[i] = 0xab
	}

	require.NoError(t, Discard(b, page/2, 2*page)) // only page 1 is whole

	assert.EqualValues(t, 0xab, b[page-1], "the partial first page is kept")
	assert.Zero(t, b[page], "the whole page is discarded")
	assert.Zero(t, b[2*page-1])
	assert.EqualValues(t, 0xab, b[2*page], "the partial last page is kept")
	b[page] = 1
	assert.EqualValues(t, 1, b[page], "a discarded page takes writes")
	assert.NoError(t, Discard(b, 0, page-1), "less than a page is nothing to do")
	assert.EqualValues(t, 0xab, b[0])
}
