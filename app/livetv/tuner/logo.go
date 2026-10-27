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
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // the providers' logos come as PNG, JPEG, GIF or WebP
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"sync"
	"time"

	_ "golang.org/x/image/webp"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/iptv/xmltv"
	"github.com/mediactl/clustarr/pkg/obs/logging"
)

// Channel logos (owner, 2026-10-27): the providers' logos are often a
// network's black wordmark on a transparent background (Bloomberg, C-SPAN,
// ESPN on tvload.win, from the playlist and the guide alike), invisible on
// Plex's dark guide. The tuner serves every channel's logo itself, at
// logos/<number>.png under each device and range, recoloured white when it
// is dark text on transparency, and the guide's and playlist's logo URLs
// point there; PMS fetches them through the same proxy port as the guide.
// The processed logo is cached in the Live TV store by its source URL.
const (
	// logoPlaceholder stands for the device's base URL in a rendered guide
	// or playlist, which is built before any request names the base; the
	// handler replaces it when serving.
	logoPlaceholder = "http://clustarr-livetv.invalid"
	maxLogoBytes    = 4 << 20
	logoFetchTime   = 15 * time.Second
	// logoDarkShare is how much of a logo's opaque pixels must be dark, and
	// logoTransparentShare how much of it transparent, for it to be dark text
	// on transparency.
	logoDarkShare        = 0.85
	logoTransparentShare = 0.20
)

var errLogoTooLarge = errors.New("livetv: logo over 4 MiB")

// logoURL is the tuner's URL for a channel's logo, base to be filled in.
func logoURL(number string) string { return logoPlaceholder + "/logos/" + number + ".png" }

// withBase fills a rendered guide's or playlist's logo URLs with the
// request's base.
func withBase(b []byte, base string) []byte {
	return bytes.ReplaceAll(b, []byte(logoPlaceholder), []byte(base))
}

// LogoObject is the clustarr-livetv object caching one provider's processed
// logo, by its source URL.
func LogoObject(key types.NamespacedName, source string) string {
	return "logo/" + key.Namespace + "/" + key.Name + "/" + sha([]byte(source))[:32]
}

// serveLogo answers a channel's logo: the processed PNG from the store, or
// fetched from its source, processed and stored first. A logo that cannot
// be had redirects to its source, so Plex still shows the provider's.
func (t *Tuner) serveLogo(w http.ResponseWriter, r *http.Request, p *provider, number string) {
	p.mu.Lock()
	source := p.logoSources[number]
	p.mu.Unlock()
	if source == "" {
		http.NotFound(w, r)
		return
	}
	name := LogoObject(p.key, source)
	if b, ok := t.readBytes(name); ok {
		writeLogo(w, b)
		return
	}
	b, err := t.logoFlight.do(source, func() ([]byte, error) {
		ctx, cancel := context.WithTimeout(r.Context(), logoFetchTime)
		defer cancel()
		b, err := processLogo(ctx, t.o.Fetch, source)
		if err != nil {
			return nil, err
		}
		if _, err := t.o.Store.Put(t.base, name, bytes.NewReader(b), events.ObjectMeta{
			Headers:  map[string]string{"Content-Type": "image/png"},
			Metadata: map[string]string{"source": source},
		}); err != nil {
			logging.FromContext(t.base).Warn("livetv: cache a channel logo", "error", err)
		}
		return b, nil
	})
	if err != nil {
		logging.FromContext(r.Context()).Info("livetv: a channel logo falls back to its source", "number", number, "error", err.Error())
		http.Redirect(w, r, source, http.StatusFound)
		return
	}
	writeLogo(w, b)
}

func writeLogo(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(b)
}

// readBytes is a whole object from the store, ok false when absent or
// unreadable.
func (t *Tuner) readBytes(name string) ([]byte, bool) {
	_, rc, err := t.o.Store.Get(t.base, name)
	if err != nil {
		return nil, false
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(io.LimitReader(rc, maxLogoBytes+1))
	if err != nil || len(b) == 0 || len(b) > maxLogoBytes {
		return nil, false
	}
	return b, true
}

// processLogo fetches a logo and returns it as PNG, recoloured when it is
// dark text on transparency.
func processLogo(ctx context.Context, client *http.Client, source string) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("livetv: logo: %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxLogoBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxLogoBytes {
		return nil, errLogoTooLarge
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("livetv: logo: %w", err)
	}
	out, _ := plexSafeLogo(img)
	var b bytes.Buffer
	if err := png.Encode(&b, out); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// plexSafeLogo returns img, recoloured white where it is dark text on
// transparency: at least logoTransparentShare of its pixels transparent and
// logoDarkShare of its opaque pixels dark. Each pixel keeps its alpha, so
// the edges stay smooth. It reports whether it changed anything.
func plexSafeLogo(img image.Image) (image.Image, bool) {
	b := img.Bounds()
	total := b.Dx() * b.Dy()
	if total == 0 {
		return img, false
	}
	var transparent, opaque, dark int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			if c.A < 32 {
				transparent++
				continue
			}
			opaque++
			if isDark(c) {
				dark++
			}
		}
	}
	if opaque == 0 || float64(transparent)/float64(total) < logoTransparentShare || float64(dark)/float64(opaque) < logoDarkShare {
		return img, false
	}
	out := image.NewNRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			out.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: c.A})
		}
	}
	return out, true
}

// isDark reports whether a pixel would vanish on Plex's dark guide: dim by
// luminance (black, charcoal, navy) or dim in every channel. A saturated
// red or blue wordmark is not dark: it reads on a dark background, and
// recolouring it would lose the brand.
func isDark(c color.NRGBA) bool {
	lum := 0.2126*float64(c.R) + 0.7152*float64(c.G) + 0.0722*float64(c.B)
	return lum < 64 || max(c.R, c.G, c.B) < 90
}

// flight runs one fetch per key at a time; waiters share its result.
type flight struct {
	mu sync.Mutex
	m  map[string]*call
}

type call struct {
	done chan struct{}
	b    []byte
	err  error
}

func (f *flight) do(key string, fn func() ([]byte, error)) ([]byte, error) {
	f.mu.Lock()
	if f.m == nil {
		f.m = map[string]*call{}
	}
	if c, ok := f.m[key]; ok {
		f.mu.Unlock()
		<-c.done
		return c.b, c.err
	}
	c := &call{done: make(chan struct{})}
	f.m[key] = c
	f.mu.Unlock()
	c.b, c.err = fn()
	close(c.done)
	f.mu.Lock()
	delete(f.m, key)
	f.mu.Unlock()
	return c.b, c.err
}

// sourceLogos is lineup with each channel's logo its source URL rather than
// the tuner's, for the stored guide the ui reads.
func sourceLogos(lineup []xmltv.LineupChannel, sources map[string]string) []xmltv.LineupChannel {
	out := make([]xmltv.LineupChannel, len(lineup))
	for i, lc := range lineup {
		if lc.Logo != "" {
			lc.Logo = sources[lc.Number]
		}
		out[i] = lc
	}
	return out
}
