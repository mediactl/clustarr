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

package ocr_test

import (
	"context"
	"image"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"github.com/mediactl/clustarr/pkg/subtitles/ocr"
	"github.com/mediactl/clustarr/pkg/subtitles/pgs/pgstest"
)

// render draws lines of text, white on black, anti-aliased in Go Regular at
// size pixels, as a Blu-ray renders a subtitle bitmap: one text line per
// row with a gap between.
func render(t *testing.T, lines []string, size float64) *image.Gray {
	t.Helper()
	f, err := opentype.Parse(goregular.TTF)
	require.NoError(t, err)
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
	require.NoError(t, err)
	defer func() { _ = face.Close() }()
	width := 0
	for _, l := range lines {
		width = max(width, font.MeasureString(face, l).Ceil())
	}
	lineH := int(size * 1.4)
	img := image.NewGray(image.Rect(0, 0, width+20, len(lines)*lineH+10))
	d := font.Drawer{Dst: img, Src: image.White, Face: face}
	for i, l := range lines {
		d.Dot = fixed.P(10, int(size)+i*lineH)
		d.DrawString(l)
	}
	return img
}

func recognizer(t *testing.T) *ocr.Recognizer {
	t.Helper()
	lib := os.Getenv("ORT_LIB_PATH")
	if lib == "" {
		t.Skip("ORT_LIB_PATH is not set")
	}
	r, err := ocr.New(lib)
	require.NoError(t, err)
	t.Cleanup(r.Close)
	return r
}

func TestRecognizesLinesOfASubtitleBitmap(t *testing.T) {
	r := recognizer(t)
	img := render(t, []string{"I'm used to it.", "You coming?"}, 40)
	lines, err := r.Lines(context.Background(), img)
	require.NoError(t, err)
	require.Len(t, lines, 2, "%+v", lines)
	assert.Equal(t, "I'm used to it.", lines[0].Text)
	assert.Equal(t, "You coming?", lines[1].Text)
	for _, l := range lines {
		assert.GreaterOrEqual(t, l.Confidence, ocr.MinConfidence, "%+v", l)
	}
}

func TestReadsAPGSStreamIntoCues(t *testing.T) {
	r := recognizer(t)
	stream := pgstest.Encode([]pgstest.Cue{
		{Start: 1000 * time.Millisecond, End: 2500 * time.Millisecond, X: 400, Y: 900, Img: render(t, []string{"Where were you?"}, 40)},
		{Start: 3000 * time.Millisecond, End: 4200 * time.Millisecond, X: 400, Y: 900, Img: render(t, []string{"Working late.", "Again."}, 40)},
	})
	cues, err := r.Read(context.Background(), stream)
	require.NoError(t, err)
	require.Len(t, cues, 2)
	assert.Equal(t, ocr.Cue{Start: time.Second, End: 2500 * time.Millisecond, Text: "Where were you?"}, cues[0])
	assert.Equal(t, ocr.Cue{Start: 3 * time.Second, End: 4200 * time.Millisecond, Text: "Working late.\nAgain."}, cues[1])
	srt := string(ocr.SRT(cues))
	assert.True(t, strings.HasPrefix(srt, "1\n00:00:01,000 --> 00:00:02,500\nWhere were you?\n\n2\n00:00:03,000 --> 00:00:04,200\nWorking late.\nAgain.\n\n"), srt)
}

// A track of bitmaps that are no text -- a studio logo, a line drawing --
// is unreadable, and left as it is.
func TestATrackThatReadsAsNothingIsUnreadable(t *testing.T) {
	r := recognizer(t)
	blob := image.NewGray(image.Rect(0, 0, 60, 60))
	for y := 10; y < 50; y++ {
		for x := 10; x < 50; x++ {
			blob.Pix[y*blob.Stride+x] = 255
		}
	}
	var cues []pgstest.Cue
	for i := range 5 {
		s := time.Duration(i+1) * time.Second
		cues = append(cues, pgstest.Cue{Start: s, End: s + 500*time.Millisecond, Img: blob})
	}
	_, err := r.Read(context.Background(), pgstest.Encode(cues))
	require.ErrorIs(t, err, ocr.ErrUnreadable)
}
