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

package decode

import (
	"sync/atomic"
	"time"
)

// CountDecodedFrames counts the frames the decoders return until restore
// (the onDecodedFrame seam).
func CountDecodedFrames() (n func() int, restore func()) {
	var c atomic.Int64
	prev := onDecodedFrame
	onDecodedFrame = func() { c.Add(1) }
	return func() int { return int(c.Load()) }, func() { onDecodedFrame = prev }
}

// SetSeekTimeout replaces seekTimeout until restore.
func SetSeekTimeout(d time.Duration) (restore func()) {
	prev := seekTimeout
	seekTimeout = d
	return func() { seekTimeout = prev }
}

// StallAtEndOfFile makes every input's reads wait at the end of the file for
// data that never comes (the file protocol's follow), until restore.
func StallAtEndOfFile() (restore func()) {
	testAVOptions = map[string]string{"follow": "1"}
	return func() { testAVOptions = nil }
}
