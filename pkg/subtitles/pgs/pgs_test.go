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

package pgs_test

import (
	"bytes"
	"image"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/subtitles/pgs"
	"github.com/mediactl/clustarr/pkg/subtitles/pgs/pgstest"
)

func TestDecodeReadsEveryShownBitmapWithItsTimes(t *testing.T) {
	a := image.NewGray(image.Rect(0, 0, 70, 20))
	for y := 2; y < 18; y++ {
		for x := 5; x < 65; x++ {
			a.Pix[y*a.Stride+x] = 255
		}
	}
	b := image.NewGray(image.Rect(0, 0, 130, 30))
	for y := 0; y < 30; y++ {
		for x := 0; x < 130; x += 2 {
			b.Pix[y*b.Stride+x] = 255
		}
	}
	stream := pgstest.Encode([]pgstest.Cue{
		{Start: time.Second, End: 2500 * time.Millisecond, X: 600, Y: 900, Img: a},
		{Start: 4 * time.Second, End: 6 * time.Second, X: 500, Y: 880, Img: b},
	})
	frames, err := pgs.Decode(bytes.NewReader(stream))
	require.NoError(t, err)
	require.Len(t, frames, 2)

	assert.Equal(t, time.Second, frames[0].Start)
	assert.Equal(t, 2500*time.Millisecond, frames[0].End, "the clearing set ends it")
	assert.Equal(t, image.Rect(0, 0, 70, 20), frames[0].Img.Bounds())
	assert.Equal(t, uint8(0xeb), frames[0].Img.GrayAt(10, 10).Y, "white is the palette's luma, weighted by its alpha")
	assert.Equal(t, uint8(0), frames[0].Img.GrayAt(0, 0).Y, "transparent is black")

	assert.Equal(t, 4*time.Second, frames[1].Start)
	assert.Equal(t, 6*time.Second, frames[1].End)
	assert.Equal(t, 130, frames[1].Img.Bounds().Dx())
	assert.Equal(t, uint8(0xeb), frames[1].Img.GrayAt(0, 5).Y)
	assert.Equal(t, uint8(0), frames[1].Img.GrayAt(1, 5).Y, "the run-length code keeps single-pixel runs")
}

func TestALastFrameNoSetEndsGetsTheFinalDuration(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	stream := pgstest.Encode([]pgstest.Cue{{Start: 10 * time.Second, End: 11 * time.Second, Img: img}})
	// Cut the clearing set off: 3 segments of 13 + payload bytes.
	cut := len(stream) - (13 + 11) - (13 + 10) - 13
	frames, err := pgs.Decode(bytes.NewReader(stream[:cut]))
	require.NoError(t, err)
	require.Len(t, frames, 1)
	assert.Equal(t, 10*time.Second+pgs.FinalDuration, frames[0].End)
}

func TestATruncatedStreamIsMalformed(t *testing.T) {
	img := image.NewGray(image.Rect(0, 0, 8, 8))
	stream := pgstest.Encode([]pgstest.Cue{{Start: time.Second, End: 2 * time.Second, Img: img}})
	_, err := pgs.Decode(bytes.NewReader(stream[:40]))
	require.ErrorIs(t, err, pgs.ErrMalformed)
	_, err = pgs.Decode(bytes.NewReader([]byte("not a stream")))
	require.ErrorIs(t, err, pgs.ErrMalformed)
}
