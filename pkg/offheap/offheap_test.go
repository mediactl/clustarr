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
