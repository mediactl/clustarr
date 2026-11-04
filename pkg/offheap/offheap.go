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

// Package offheap maps byte buffers outside the Go heap: anonymous,
// private, page-aligned mappings the collector never scans, that count
// toward no heap goal, and that go back to the kernel at Unmap rather than
// at the collector's next cycle.
//
// A buffer that is large against the live heap and lives for minutes
// belongs here: a copy's 64 MiB chunks (pkg/fsops) and a Live TV channel's
// 64 MiB ring (app/livetv/relay). On talos on 2026-11-03 one stream's ring
// was a fifth of the manager's live heap, and under GOGC a fifth of the
// headroom the collector kept beside it.
package offheap

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// ErrSize is a Map of no bytes.
var ErrSize = errors.New("offheap: a buffer of no bytes")

// Map maps n bytes of zeroed anonymous memory. The slice is page-aligned
// (and so aligned as O_DIRECT asks). Unmap it with [Unmap], and only once
// nothing holds it.
func Map(n int) ([]byte, error) {
	if n <= 0 {
		return nil, ErrSize
	}
	b, err := unix.Mmap(-1, 0, n, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANONYMOUS)
	if err != nil {
		return nil, fmt.Errorf("offheap: map %d bytes: %w", n, err)
	}
	return b, nil
}

// Unmap returns a buffer [Map] gave to the kernel. It takes the slice Map
// returned, whole; a nil buffer is nothing to do.
func Unmap(b []byte) error {
	if b == nil {
		return nil
	}
	if err := unix.Munmap(b); err != nil {
		return fmt.Errorf("offheap: unmap %d bytes: %w", len(b), err)
	}
	return nil
}
