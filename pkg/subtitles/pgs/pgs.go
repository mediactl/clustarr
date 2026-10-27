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

// Package pgs reads Blu-ray Presentation Graphic Stream subtitles -- a .sup
// file, as FFmpeg's sup muxer writes the track the MP4 standard keeps beside
// a transcode (standard.SidecarSUP) -- into timed grey bitmaps for the OCR
// (pkg/subtitles/ocr). It is a decoder in Go, not ffgo: the format is five
// segment kinds and a run-length code (the Blu-ray spec's; FFmpeg's
// pgssubdec is the reference), and the OCR runs in an agent that need not
// touch FFmpeg for it.
package pgs

import (
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"time"
)

// Segment types.
const (
	segPalette      = 0x14
	segObject       = 0x15
	segPresentation = 0x16
	segWindow       = 0x17
	segEnd          = 0x80
)

// Frame is one display set with something on screen: the composed subtitle
// as a grey image -- each pixel its palette entry's luma weighted by its
// alpha, so text reads light on black whatever the palette -- shown from
// Start to End. End is the next display set's time (one that clears the
// screen or replaces it); a last set no set ends is given FinalDuration.
type Frame struct {
	Start, End time.Duration
	Img        *image.Gray
}

// FinalDuration ends a stream's last frame when no display set does.
const FinalDuration = 5 * time.Second

// ErrMalformed is a stream the decoder cannot read: a truncated segment, a
// run-length code that runs off its object, a reference to an object or
// palette no segment defined.
var ErrMalformed = errors.New("pgs: malformed stream")

// Decode reads every display set of r and returns the frames that show
// something, in order. A set with no object ends the frame before it.
func Decode(r io.Reader) ([]Frame, error) {
	var out []Frame
	err := Walk(r, func(f Frame) error { out = append(out, f); return nil })
	return out, err
}

// Walk is Decode one frame at a time: fn is called with each frame once
// the set that ends it has been read (or the stream has, with
// FinalDuration), so only one frame's bitmap is held -- a film's track is
// hundreds of bitmaps, and the OCR reads them in a pod with a memory limit.
// An error from fn ends the walk with it.
func Walk(r io.Reader, fn func(Frame) error) error {
	d := decoder{palettes: map[uint8][256]paletteEntry{}, objects: map[uint16]*object{}, windows: map[uint8]window{}, emit: fn}
	hdr := make([]byte, 13)
	for {
		if _, err := io.ReadFull(r, hdr); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return fmt.Errorf("%w: truncated segment header", ErrMalformed)
			}
			return err
		}
		if hdr[0] != 'P' || hdr[1] != 'G' {
			return fmt.Errorf("%w: no PG magic", ErrMalformed)
		}
		pts := time.Duration(binary.BigEndian.Uint32(hdr[2:6])) * time.Second / 90000
		typ, size := hdr[10], int(binary.BigEndian.Uint16(hdr[11:13]))
		payload := make([]byte, size)
		if _, err := io.ReadFull(r, payload); err != nil {
			return fmt.Errorf("%w: truncated segment %#x", ErrMalformed, typ)
		}
		if err := d.segment(pts, typ, payload); err != nil {
			return err
		}
	}
	if p := d.pending; p != nil {
		if p.End == 0 {
			p.End = p.Start + FinalDuration
		}
		return d.emit(*p)
	}
	return nil
}

type paletteEntry struct{ y, a uint8 }

type object struct {
	w, h   int
	data   []byte // run-length coded, every fragment appended
	length int    // the first fragment's declared data length
}

type window struct{ x, y, w, h int }

type composition struct {
	pts     time.Duration
	palette uint8
	objects []objectRef
}

type objectRef struct {
	id      uint16
	x, y    int
	cropped bool
	cx, cy  int
	cw, ch  int
}

type decoder struct {
	palettes map[uint8][256]paletteEntry
	objects  map[uint16]*object
	windows  map[uint8]window
	current  *composition
	// pending is the last frame shown, until a set ends it; emit takes
	// each finished frame.
	pending *Frame
	emit    func(Frame) error
}

func (d *decoder) segment(pts time.Duration, typ uint8, p []byte) error {
	switch typ {
	case segPresentation:
		return d.presentation(pts, p)
	case segWindow:
		return d.window(p)
	case segPalette:
		return d.palette(p)
	case segObject:
		return d.object(p)
	case segEnd:
		return d.end()
	}
	return nil // a segment type the spec does not name is skipped
}

func (d *decoder) presentation(pts time.Duration, p []byte) error {
	if len(p) < 11 {
		return fmt.Errorf("%w: short presentation segment", ErrMalformed)
	}
	c := &composition{pts: pts, palette: p[9]}
	n := int(p[10])
	off := 11
	for i := 0; i < n; i++ {
		if len(p) < off+8 {
			return fmt.Errorf("%w: short composition object", ErrMalformed)
		}
		ref := objectRef{
			id: binary.BigEndian.Uint16(p[off:]), cropped: p[off+3]&0x80 != 0,
			x: int(binary.BigEndian.Uint16(p[off+4:])), y: int(binary.BigEndian.Uint16(p[off+6:])),
		}
		off += 8
		if ref.cropped {
			if len(p) < off+8 {
				return fmt.Errorf("%w: short cropped object", ErrMalformed)
			}
			ref.cx, ref.cy = int(binary.BigEndian.Uint16(p[off:])), int(binary.BigEndian.Uint16(p[off+2:]))
			ref.cw, ref.ch = int(binary.BigEndian.Uint16(p[off+4:])), int(binary.BigEndian.Uint16(p[off+6:]))
			off += 8
		}
		c.objects = append(c.objects, ref)
	}
	// An epoch start (composition state 0x80) forgets the objects and
	// palettes before it; an acquisition point or a normal case keeps them.
	if p[7]&0x80 != 0 {
		d.objects = map[uint16]*object{}
		d.palettes = map[uint8][256]paletteEntry{}
	}
	d.current = c
	return nil
}

func (d *decoder) window(p []byte) error {
	if len(p) < 1 {
		return fmt.Errorf("%w: short window segment", ErrMalformed)
	}
	n, off := int(p[0]), 1
	for i := 0; i < n; i++ {
		if len(p) < off+9 {
			return fmt.Errorf("%w: short window", ErrMalformed)
		}
		d.windows[p[off]] = window{
			x: int(binary.BigEndian.Uint16(p[off+1:])), y: int(binary.BigEndian.Uint16(p[off+3:])),
			w: int(binary.BigEndian.Uint16(p[off+5:])), h: int(binary.BigEndian.Uint16(p[off+7:])),
		}
		off += 9
	}
	return nil
}

func (d *decoder) palette(p []byte) error {
	if len(p) < 2 {
		return fmt.Errorf("%w: short palette segment", ErrMalformed)
	}
	pal := d.palettes[p[0]]
	for off := 2; off+5 <= len(p); off += 5 {
		pal[p[off]] = paletteEntry{y: p[off+1], a: p[off+4]}
	}
	d.palettes[p[0]] = pal
	return nil
}

func (d *decoder) object(p []byte) error {
	if len(p) < 4 {
		return fmt.Errorf("%w: short object segment", ErrMalformed)
	}
	id, seq := binary.BigEndian.Uint16(p), p[3]
	if seq&0x80 != 0 { // the first fragment: size, dimensions, then data
		if len(p) < 11 {
			return fmt.Errorf("%w: short object header", ErrMalformed)
		}
		length := int(p[4])<<16 | int(p[5])<<8 | int(p[6])
		o := &object{w: int(binary.BigEndian.Uint16(p[7:])), h: int(binary.BigEndian.Uint16(p[9:])), length: length - 4}
		o.data = append(o.data, p[11:]...)
		d.objects[id] = o
		return nil
	}
	o, ok := d.objects[id]
	if !ok {
		return fmt.Errorf("%w: a fragment of object %d before its first", ErrMalformed, id)
	}
	o.data = append(o.data, p[4:]...)
	return nil
}

// end composes the current display set. A set with objects is a frame; a
// set without ends the frame before it.
func (d *decoder) end() error {
	c := d.current
	if c == nil {
		return nil
	}
	d.current = nil
	if p := d.pending; p != nil && p.End == 0 && c.pts > p.Start {
		p.End = c.pts
	}
	if len(c.objects) == 0 {
		// A clearing set: the frame before it is complete.
		return d.flush()
	}
	pal, ok := d.palettes[c.palette]
	if !ok {
		return fmt.Errorf("%w: palette %d is not defined", ErrMalformed, c.palette)
	}
	// The frame is the bounding box of the placed objects.
	minX, minY, maxX, maxY := 1<<30, 1<<30, 0, 0
	type placed struct {
		img  *image.Gray
		x, y int
	}
	var parts []placed
	for _, ref := range c.objects {
		o, ok := d.objects[ref.id]
		if !ok {
			return fmt.Errorf("%w: object %d is not defined", ErrMalformed, ref.id)
		}
		img, err := o.render(pal)
		if err != nil {
			return err
		}
		if ref.cropped && ref.cw > 0 && ref.ch > 0 {
			r := image.Rect(ref.cx, ref.cy, ref.cx+ref.cw, ref.cy+ref.ch).Intersect(img.Bounds())
			img = img.SubImage(r).(*image.Gray)
		}
		b := img.Bounds()
		parts = append(parts, placed{img: img, x: ref.x, y: ref.y})
		minX, minY = min(minX, ref.x), min(minY, ref.y)
		maxX, maxY = max(maxX, ref.x+b.Dx()), max(maxY, ref.y+b.Dy())
	}
	if len(parts) == 0 || maxX <= minX || maxY <= minY {
		return nil
	}
	out := image.NewGray(image.Rect(0, 0, maxX-minX, maxY-minY))
	for _, p := range parts {
		b := p.img.Bounds()
		for y := 0; y < b.Dy(); y++ {
			for x := 0; x < b.Dx(); x++ {
				v := p.img.GrayAt(b.Min.X+x, b.Min.Y+y).Y
				if v > 0 {
					out.Pix[(p.y-minY+y)*out.Stride+(p.x-minX+x)] = v
				}
			}
		}
	}
	if err := d.flush(); err != nil {
		return err
	}
	d.pending = &Frame{Start: c.pts, Img: out}
	return nil
}

// flush emits the pending frame, if any; one no set ended yet (a second
// set at the same time) ends where it began.
func (d *decoder) flush() error {
	p := d.pending
	if p == nil {
		return nil
	}
	d.pending = nil
	if p.End == 0 {
		p.End = p.Start
	}
	return d.emit(*p)
}

// render decodes the object's run-length code against pal into a grey
// image: each pixel its entry's luma weighted by its alpha.
func (o *object) render(pal [256]paletteEntry) (*image.Gray, error) {
	if o.w <= 0 || o.h <= 0 || o.w > 4096 || o.h > 4096 {
		return nil, fmt.Errorf("%w: object of %dx%d", ErrMalformed, o.w, o.h)
	}
	img := image.NewGray(image.Rect(0, 0, o.w, o.h))
	grey := func(idx byte) uint8 {
		e := pal[idx]
		return uint8(uint32(e.y) * uint32(e.a) / 255)
	}
	x, y, i := 0, 0, 0
	put := func(idx byte, n int) {
		for ; n > 0 && y < o.h; n-- {
			if x < o.w {
				img.Pix[y*img.Stride+x] = grey(idx)
			}
			x++
		}
	}
	for i < len(o.data) && y < o.h {
		b := o.data[i]
		i++
		if b != 0 {
			put(b, 1)
			continue
		}
		if i >= len(o.data) {
			return nil, fmt.Errorf("%w: run-length code ends mid-run", ErrMalformed)
		}
		f := o.data[i]
		i++
		switch {
		case f == 0: // end of line
			x, y = 0, y+1
		case f>>6 == 0:
			put(0, int(f&0x3f))
		case f>>6 == 1:
			if i >= len(o.data) {
				return nil, fmt.Errorf("%w: run-length code ends mid-run", ErrMalformed)
			}
			put(0, int(f&0x3f)<<8|int(o.data[i]))
			i++
		case f>>6 == 2:
			if i >= len(o.data) {
				return nil, fmt.Errorf("%w: run-length code ends mid-run", ErrMalformed)
			}
			put(o.data[i], int(f&0x3f))
			i++
		default:
			if i+1 >= len(o.data) {
				return nil, fmt.Errorf("%w: run-length code ends mid-run", ErrMalformed)
			}
			put(o.data[i+1], int(f&0x3f)<<8|int(o.data[i]))
			i += 2
		}
	}
	return img, nil
}
