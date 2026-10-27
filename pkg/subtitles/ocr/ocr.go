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

// Package ocr reads image subtitles into text: PaddleOCR's PP-OCRv5
// recognition model on ONNX Runtime through purego (as pkg/segments/textdet
// runs its detector), over the grey bitmaps pkg/subtitles/pgs decodes from a
// .sup sidecar the MP4 standard kept beside a transcode. A subtitle bitmap
// is already a text line or two on nothing, so no detector runs: the lines
// are split by their rows' ink and each is recognised whole (MP4 standard
// design §5; owner, 2026-10-27: "convert .sup to .srt when .sup is the only
// subtitle format provided in the blu-ray").
package ocr

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"image"
	"math"
	"strings"
	"sync"
	"time"
	"unicode"

	ort "github.com/shota3506/onnxruntime-purego/onnxruntime"

	"github.com/mediactl/clustarr/pkg/subtitles/pgs"
)

//go:embed model/rec.onnx
var model []byte

//go:embed model/dict.txt
var dict string

// Version is the OCR's revision, recorded with every sidecar it writes:
// raise it when the model or the pipeline changes, so a track read under an
// older one may be read again.
const Version = 1

// apiVersion is the ONNX Runtime C API the binding speaks (ORT 1.23).
const apiVersion = 23

// lineHeight is the model's input height; lineMaxWidth bounds a line's
// resized width, so a very long line is squeezed rather than refused.
const (
	lineHeight   = 48
	lineMaxWidth = 2400
)

// MinConfidence is the mean per-character probability under which a line's
// reading is dropped rather than written.
const MinConfidence = 0.5

// ErrUnavailable is ONNX Runtime or the model failing to load.
var ErrUnavailable = errors.New("ocr: unavailable")

// ErrUnreadable is a track whose bitmaps mostly read as nothing: the track
// is left as it is rather than written as a mangled subtitle.
var ErrUnreadable = errors.New("ocr: the track reads as mostly nothing")

// Recognizer runs the recognition model. One Recognize at a time: subtitle
// lines are few and memory stays flat.
type Recognizer struct {
	mu      sync.Mutex
	rt      *ort.Runtime
	env     *ort.Env
	sess    *ort.Session
	in, out string
	chars   []string // index 0 is CTC's blank, the last the space
}

// New loads ONNX Runtime from libPath ("" searches the system's library
// paths) and the embedded model and dictionary. Any failure is ErrUnavailable.
func New(libPath string) (*Recognizer, error) {
	rt, err := ort.NewRuntime(libPath, apiVersion)
	if err != nil {
		return nil, fmt.Errorf("%w: load ONNX Runtime: %w", ErrUnavailable, err)
	}
	env, err := rt.NewEnv("clustarr-ocr", ort.LoggingLevelError)
	if err != nil {
		_ = rt.Close()
		return nil, fmt.Errorf("%w: environment: %w", ErrUnavailable, err)
	}
	sess, err := rt.NewSessionFromReader(env, bytes.NewReader(model), nil)
	if err != nil {
		env.Close()
		_ = rt.Close()
		return nil, fmt.Errorf("%w: load the model: %w", ErrUnavailable, err)
	}
	if len(sess.InputNames()) != 1 || len(sess.OutputNames()) != 1 {
		sess.Close()
		env.Close()
		_ = rt.Close()
		return nil, fmt.Errorf("%w: the model has %d inputs and %d outputs, want 1 and 1",
			ErrUnavailable, len(sess.InputNames()), len(sess.OutputNames()))
	}
	chars := []string{""}
	sc := bufio.NewScanner(strings.NewReader(dict))
	for sc.Scan() {
		chars = append(chars, sc.Text())
	}
	chars = append(chars, " ")
	return &Recognizer{rt: rt, env: env, sess: sess, in: sess.InputNames()[0], out: sess.OutputNames()[0], chars: chars}, nil
}

// Close releases the model and the runtime.
func (r *Recognizer) Close() {
	r.sess.Close()
	r.env.Close()
	_ = r.rt.Close()
}

// Line is one recognised text line and the mean probability of its
// characters.
type Line struct {
	Text       string
	Confidence float64
}

// Lines splits img into text lines by its rows' ink and recognises each.
// A line that reads as nothing is left out.
func (r *Recognizer) Lines(ctx context.Context, img *image.Gray) ([]Line, error) {
	var out []Line
	for _, band := range splitLines(img) {
		l, err := r.recognize(ctx, band)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(l.Text) != "" {
			out = append(out, l)
		}
	}
	return out, nil
}

// recognize reads one line: resized to lineHeight rows, each channel
// normalised as PaddleOCR does, the model's per-step probabilities decoded
// greedily (CTC: the best class per step, repeats collapsed, blanks dropped).
func (r *Recognizer) recognize(ctx context.Context, line *image.Gray) (Line, error) {
	b := line.Bounds()
	w := int(math.Ceil(float64(lineHeight) * float64(b.Dx()) / float64(b.Dy())))
	w = max(lineHeight, min(w, lineMaxWidth))
	resized := resize(line, w, lineHeight)
	input := make([]float32, 3*lineHeight*w)
	for i, v := range resized.Pix {
		f := (float32(v)/255 - 0.5) / 0.5
		input[i], input[lineHeight*w+i], input[2*lineHeight*w+i] = f, f, f
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t, err := ort.NewTensorValue(r.rt, input, []int64{1, 3, lineHeight, int64(w)})
	if err != nil {
		return Line{}, fmt.Errorf("ocr: input tensor: %w", err)
	}
	defer t.Close()
	outs, err := r.sess.Run(ctx, map[string]*ort.Value{r.in: t})
	if err != nil {
		return Line{}, fmt.Errorf("ocr: run: %w", err)
	}
	defer func() {
		for _, v := range outs {
			v.Close()
		}
	}()
	probs, shape, err := ort.GetTensorData[float32](outs[r.out])
	if err != nil {
		return Line{}, fmt.Errorf("ocr: output: %w", err)
	}
	if len(shape) != 3 || shape[0] != 1 {
		return Line{}, fmt.Errorf("ocr: output shape %v, want [1 T C]", shape)
	}
	steps, classes := int(shape[1]), int(shape[2])
	var sb strings.Builder
	var sum float64
	var n, prev int
	for s := 0; s < steps; s++ {
		row := probs[s*classes : (s+1)*classes]
		best, bp := 0, float32(-1)
		for c, p := range row {
			if p > bp {
				best, bp = c, p
			}
		}
		if best != 0 && best != prev {
			if best < len(r.chars) {
				sb.WriteString(r.chars[best])
			}
			sum += float64(bp)
			n++
		}
		prev = best
	}
	if n == 0 {
		return Line{}, nil
	}
	return Line{Text: strings.TrimSpace(sb.String()), Confidence: sum / float64(n)}, nil
}

// splitLines cuts img into its text lines: bands of rows with ink (a pixel
// above half grey), gaps under a few rows closed, bands under 8 rows
// dropped, each cropped to its ink and padded by 4 rows and columns.
func splitLines(img *image.Gray) []*image.Gray {
	b := img.Bounds()
	h, w := b.Dy(), b.Dx()
	ink := func(x, y int) bool { return img.GrayAt(b.Min.X+x, b.Min.Y+y).Y > 127 }
	rows := make([]bool, h)
	for y := range h {
		for x := 0; x < w && !rows[y]; x++ {
			rows[y] = ink(x, y)
		}
	}
	gap := max(2, h/30)
	type band struct{ y0, y1 int }
	var bands []band
	for y := 0; y < h; {
		if !rows[y] {
			y++
			continue
		}
		y0 := y
		for y < h && (rows[y] || (y+gap < h && anyRow(rows[y:min(h, y+gap+1)]))) {
			y++
		}
		bands = append(bands, band{y0, y})
	}
	var out []*image.Gray
	for _, bd := range bands {
		if bd.y1-bd.y0 < 8 {
			continue
		}
		x0, x1 := w, 0
		for y := bd.y0; y < bd.y1; y++ {
			for x := range w {
				if ink(x, y) {
					x0, x1 = min(x0, x), max(x1, x+1)
				}
			}
		}
		if x1 <= x0 {
			continue
		}
		const pad = 4
		line := image.NewGray(image.Rect(0, 0, x1-x0+2*pad, bd.y1-bd.y0+2*pad))
		for y := bd.y0; y < bd.y1; y++ {
			for x := x0; x < x1; x++ {
				line.Pix[(y-bd.y0+pad)*line.Stride+(x-x0+pad)] = img.GrayAt(b.Min.X+x, b.Min.Y+y).Y
			}
		}
		out = append(out, line)
	}
	return out
}

func anyRow(rows []bool) bool {
	for _, r := range rows {
		if r {
			return true
		}
	}
	return false
}

// resize scales img to w x h by bilinear interpolation.
func resize(img *image.Gray, w, h int) *image.Gray {
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	out := image.NewGray(image.Rect(0, 0, w, h))
	at := func(x, y int) float64 {
		return float64(img.GrayAt(b.Min.X+min(max(x, 0), sw-1), b.Min.Y+min(max(y, 0), sh-1)).Y)
	}
	for y := range h {
		sy := (float64(y)+0.5)*float64(sh)/float64(h) - 0.5
		y0 := int(math.Floor(sy))
		fy := sy - float64(y0)
		for x := range w {
			sx := (float64(x)+0.5)*float64(sw)/float64(w) - 0.5
			x0 := int(math.Floor(sx))
			fx := sx - float64(x0)
			v := at(x0, y0)*(1-fx)*(1-fy) + at(x0+1, y0)*fx*(1-fy) + at(x0, y0+1)*(1-fx)*fy + at(x0+1, y0+1)*fx*fy
			out.Pix[y*out.Stride+x] = uint8(math.Round(v))
		}
	}
	return out
}

// Cue is one subtitle: its text (lines joined by newlines) shown from Start
// to End.
type Cue struct {
	Start, End time.Duration
	Text       string
}

// Read recognises every frame of a PGS stream into cues, dropping lines
// under MinConfidence and frames that read as nothing, and joining a frame
// whose text and time continue the one before it (a bitmap re-sent
// unchanged). It is ErrUnreadable when the stream has frames but fewer
// than ReadableShare of them read as text with a letter or digit in it.
func (r *Recognizer) Read(ctx context.Context, stream []byte) ([]Cue, error) {
	frames, err := pgs.Decode(bytes.NewReader(stream))
	if err != nil {
		return nil, err
	}
	var cues []Cue
	readable := 0
	for _, f := range frames {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lines, err := r.Lines(ctx, f.Img)
		if err != nil {
			return nil, err
		}
		var kept []string
		for _, l := range lines {
			if l.Confidence >= MinConfidence {
				kept = append(kept, l.Text)
			}
		}
		text := strings.Join(kept, "\n")
		if strings.IndexFunc(text, func(c rune) bool { return unicode.IsLetter(c) || unicode.IsDigit(c) }) >= 0 {
			readable++
		}
		if text == "" {
			continue
		}
		if n := len(cues); n > 0 && cues[n-1].Text == text && f.Start <= cues[n-1].End+100*time.Millisecond {
			cues[n-1].End = f.End
			continue
		}
		cues = append(cues, Cue{Start: f.Start, End: f.End, Text: text})
	}
	if len(frames) > 0 && readable*100 < len(frames)*ReadableShare {
		return nil, fmt.Errorf("%w: %d of %d bitmaps", ErrUnreadable, readable, len(frames))
	}
	return cues, nil
}

// ReadableShare is the least percentage of a track's bitmaps that must read
// as text for the track to be written.
const ReadableShare = 60

// SRT formats cues as SubRip.
func SRT(cues []Cue) []byte {
	var b strings.Builder
	for i, c := range cues {
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", i+1, srtTime(c.Start), srtTime(c.End), c.Text)
	}
	return []byte(b.String())
}

func srtTime(d time.Duration) string {
	ms := d.Milliseconds()
	return fmt.Sprintf("%02d:%02d:%02d,%03d", ms/3600000, ms/60000%60, ms/1000%60, ms%1000)
}
