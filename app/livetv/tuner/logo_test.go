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

package tuner

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wordmark is a 40x20 logo: text pixels of c on a transparent background,
// with a soft edge of half-transparent pixels, as the providers' PNGs are.
func wordmark(c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 40, 20))
	for y := 6; y < 14; y++ {
		for x := 4; x < 36; x++ {
			p := c
			if y == 6 || y == 13 {
				p.A = 128 // anti-aliased edge
			}
			img.SetNRGBA(x, y, p)
		}
	}
	return img
}

// Bloomberg, C-SPAN and ESPN come as black text on transparency, invisible
// on Plex's dark guide: the text turns white and keeps its soft edges.
func TestADarkWordmarkOnTransparencyTurnsWhite(t *testing.T) {
	out, changed := plexSafeLogo(wordmark(color.NRGBA{R: 10, G: 10, B: 10, A: 255}))
	require.True(t, changed)
	assert.Equal(t, color.NRGBA{R: 255, G: 255, B: 255, A: 255}, color.NRGBAModel.Convert(out.At(10, 10)))
	assert.Equal(t, color.NRGBA{R: 255, G: 255, B: 255, A: 128}, color.NRGBAModel.Convert(out.At(10, 6)), "the edge keeps its alpha")
	assert.Equal(t, uint32(0), func() uint32 { _, _, _, a := out.At(0, 0).RGBA(); return a }(), "the background stays transparent")
}

// A coloured logo (CNBC's peacock, ABC News) and a dark logo on an opaque
// background (the Weather Channel's tile) read fine as they are.
func TestColouredAndOpaqueLogosAreLeftAlone(t *testing.T) {
	_, changed := plexSafeLogo(wordmark(color.NRGBA{R: 200, G: 30, B: 30, A: 255}))
	assert.False(t, changed, "coloured text is visible")

	tile := image.NewNRGBA(image.Rect(0, 0, 40, 20))
	for y := range 20 {
		for x := range 40 {
			tile.SetNRGBA(x, y, color.NRGBA{R: 250, G: 250, B: 250, A: 255})
		}
	}
	for y := 6; y < 14; y++ {
		for x := 4; x < 36; x++ {
			tile.SetNRGBA(x, y, color.NRGBA{A: 255})
		}
	}
	_, changed = plexSafeLogo(tile)
	assert.False(t, changed, "dark on an opaque light tile is visible")
}

// processLogo fetches the source and answers PNG, processed.
func TestProcessLogoFetchesAndRecolours(t *testing.T) {
	var src bytes.Buffer
	require.NoError(t, png.Encode(&src, wordmark(color.NRGBA{A: 255})))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(src.Bytes())
	}))
	defer srv.Close()
	b, err := processLogo(context.Background(), srv.Client(), srv.URL+"/logo.png")
	require.NoError(t, err)
	img, err := png.Decode(bytes.NewReader(b))
	require.NoError(t, err)
	assert.Equal(t, color.NRGBA{R: 255, G: 255, B: 255, A: 255}, color.NRGBAModel.Convert(img.At(10, 10)))

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "gone", http.StatusNotFound) }))
	defer bad.Close()
	_, err = processLogo(context.Background(), bad.Client(), bad.URL+"/logo.png")
	assert.Error(t, err)
}

// A logo URL the tuner hands out carries a placeholder base, filled in with
// the request's base when served.
func TestLogoURLsTakeTheRequestsBase(t *testing.T) {
	guide := []byte(`<icon src="` + logoURL("1024") + `"></icon>`)
	assert.Equal(t, `<icon src="http://clustarr-livetv.media.svc.cluster.local:80/hdhr/CAFE0001/logos/1024.png"></icon>`,
		string(withBase(guide, "http://clustarr-livetv.media.svc.cluster.local:80/hdhr/CAFE0001")))
}
