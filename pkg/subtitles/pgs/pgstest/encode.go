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

// Package pgstest writes Presentation Graphic Streams for tests: FFmpeg
// encodes no PGS, so a stream with known bitmaps and timings is built by
// hand, as the Blu-ray spec lays it out (one display set per shown bitmap,
// a clearing set at its end).
package pgstest

import (
	"encoding/binary"
	"image"
	"time"
)

// Cue is one bitmap shown from Start to End at (X, Y) on a 1920x1080 screen.
// Pixels above 127 are white (palette entry 1), the rest transparent.
type Cue struct {
	Start, End time.Duration
	X, Y       int
	Img        *image.Gray
}

// Encode is cues as a .sup stream: for each cue an epoch-start display set
// with one window, one palette (0 transparent, 1 opaque white) and one
// object, then a display set with no object at its end.
func Encode(cues []Cue) []byte {
	var out []byte
	for i, c := range cues {
		b := c.Img.Bounds()
		w, h := b.Dx(), b.Dy()
		pcs := []byte{0x07, 0x80, 0x04, 0x38, 0x10}
		pcs = binary.BigEndian.AppendUint16(pcs, uint16(2*i)) //nolint:gosec // a test's composition number
		pcs = append(pcs, 0x80, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00)
		pcs = binary.BigEndian.AppendUint16(pcs, uint16(c.X)) //nolint:gosec // a test's coordinates
		pcs = binary.BigEndian.AppendUint16(pcs, uint16(c.Y)) //nolint:gosec // a test's coordinates
		wds := []byte{0x01, 0x00}
		for _, v := range []int{c.X, c.Y, w, h} {
			wds = binary.BigEndian.AppendUint16(wds, uint16(v)) //nolint:gosec // a test's coordinates
		}
		pds := []byte{0x00, 0x00, 0x00, 0x00, 0x80, 0x80, 0x00, 0x01, 0xeb, 0x80, 0x80, 0xff}
		rle := encodeRLE(c.Img)
		ods := []byte{0x00, 0x00, 0x00, 0xc0}
		n := 4 + len(rle)
		ods = append(ods, byte(n>>16), byte(n>>8), byte(n))
		ods = binary.BigEndian.AppendUint16(ods, uint16(w)) //nolint:gosec // a test's size
		ods = binary.BigEndian.AppendUint16(ods, uint16(h)) //nolint:gosec // a test's size
		ods = append(ods, rle...)
		start, end := pts(c.Start), pts(c.End)
		for _, s := range [][]byte{segment(start, 0x16, pcs), segment(start, 0x17, wds), segment(start, 0x14, pds), segment(start, 0x15, ods), segment(start, 0x80, nil)} {
			out = append(out, s...)
		}
		clear := []byte{0x07, 0x80, 0x04, 0x38, 0x10}
		clear = binary.BigEndian.AppendUint16(clear, uint16(2*i+1)) //nolint:gosec // a test's composition number
		clear = append(clear, 0x00, 0x00, 0x00, 0x00)
		for _, s := range [][]byte{segment(end, 0x16, clear), segment(end, 0x17, wds), segment(end, 0x80, nil)} {
			out = append(out, s...)
		}
	}
	return out
}

func pts(d time.Duration) uint32 { return uint32(d * 90000 / time.Second) } //nolint:gosec // a test's timestamps

func segment(pts uint32, typ byte, payload []byte) []byte {
	b := []byte{'P', 'G'}
	b = binary.BigEndian.AppendUint32(b, pts)
	b = binary.BigEndian.AppendUint32(b, 0)
	b = append(b, typ)
	b = binary.BigEndian.AppendUint16(b, uint16(len(payload))) //nolint:gosec // a test's payload
	return append(b, payload...)
}

// encodeRLE is the PGS run-length code of img, two colours: runs of
// transparent pixels as 00 <len> (or 00 4x xx for long ones), white pixels
// one byte each, 00 00 at each line's end.
func encodeRLE(img *image.Gray) []byte {
	var out []byte
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; {
			if img.GrayAt(x, y).Y > 127 {
				out = append(out, 0x01)
				x++
				continue
			}
			run := 0
			for x < b.Max.X && img.GrayAt(x, y).Y <= 127 && run < 16383 {
				run++
				x++
			}
			if run < 64 {
				out = append(out, 0x00, byte(run))
			} else {
				out = append(out, 0x00, 0x40|byte(run>>8), byte(run))
			}
		}
		out = append(out, 0x00, 0x00)
	}
	return out
}
