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

// Command compactmock draws a sheet of posters as the regular and the
// compact rendition would be, side by side and at the sizes a phone shows
// them, so the compact geometry can be settled by eye before any poster is
// stored (docs/superpowers/specs/2026-11-27-compact-posters-design.md §5.5).
//
// It draws with the code the agents draw with: the metadata agent's
// DrawTitled and the renderer's overlayrender.Render. Nothing is read from
// or written to a cluster.
//
//	go run ./hack/compactmock --out sheet.png \
//	    --item 'poster.jpg;logo.png;metacritic=8700,imdb=950' \
//	    --item 'https://host/poster.jpg;;myanimelist=812;dub'
//
// An item is a poster, then optionally a logo, ratings (source=centis,
// comma-separated) and the word dub, separated by semicolons. A poster or a
// logo is a file or an http(s) URL. Each row of the sheet is one item: the
// regular and the compact rendition at 500 px wide, at 250 px (a phone's
// library grid at 2x) and at 125 px. On the 250 px tiles a white outline at
// the top right is Plex's episode-count box as measured on the owner's
// screenshot: 28% of the poster's width wide, 27% of it tall.
package main

import (
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	catalogartwork "github.com/mediactl/clustarr/app/catalog/artwork"
	"github.com/mediactl/clustarr/app/catalog/metadata/artwork"
	"github.com/mediactl/clustarr/app/catalog/overlayplan"
	"github.com/mediactl/clustarr/pkg/overlay"
	overlayrender "github.com/mediactl/clustarr/pkg/overlay/render"
)

// maxBody caps a poster or logo read from a URL.
const maxBody = 32 << 20

// Sheet layout, in pixels.
const (
	margin  = 16
	fullW   = 500
	fullH   = 750
	refWPct = 28 // Plex's count box: width, of the poster's width
	refHPct = 27 // and height, of the poster's width
)

// tileWidths are the widths each rendition is shown at.
var tileWidths = []int{500, 250, 125}

type item struct {
	poster, logo string
	ratings      []catalogv1alpha1.Rating
	dub          bool
}

type items []item

func (i *items) String() string { return fmt.Sprint(len(*i), " items") }

func (i *items) Set(v string) error {
	parts := strings.Split(v, ";")
	it := item{poster: strings.TrimSpace(parts[0])}
	if it.poster == "" {
		return errors.New("an item starts with its poster")
	}
	if len(parts) > 1 {
		it.logo = strings.TrimSpace(parts[1])
	}
	if len(parts) > 2 && strings.TrimSpace(parts[2]) != "" {
		for r := range strings.SplitSeq(parts[2], ",") {
			source, centis, ok := strings.Cut(strings.TrimSpace(r), "=")
			if !ok {
				return fmt.Errorf("rating %q is not source=centis", r)
			}
			n, err := strconv.ParseInt(centis, 10, 32)
			if err != nil {
				return fmt.Errorf("rating %q: %w", r, err)
			}
			it.ratings = append(it.ratings, catalogv1alpha1.Rating{
				Source: catalogv1alpha1.RatingSource(source), ValueCentis: int32(n),
			})
		}
	}
	if len(parts) > 3 {
		it.dub = strings.TrimSpace(parts[3]) == "dub"
	}
	*i = append(*i, it)
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "compactmock:", err)
		os.Exit(1)
	}
}

func run() error {
	var its items
	out := flag.String("out", "compact-sheet.png", "the PNG sheet to write")
	corner := flag.String("corner", string(catalogv1alpha1.OverlayCornerTopLeft), "the badge corner")
	titleW := flag.Int("title-width", artwork.CompactTitle.MaxWidthPct, "compact title: most of the poster's width, percent")
	titleH := flag.Int("title-height", artwork.CompactTitle.MaxHeightPct, "compact title: most of the poster's height, percent")
	badgeW := flag.Int("badge-width", int(catalogv1alpha1.DefaultOverlayCompactWidthPercent), "compact badge: width, percent of the poster's")
	badgeS := flag.Int("badge-score", int(catalogv1alpha1.DefaultOverlayCompactScorePercent), "compact badge: score height, percent of the box's")
	flag.Var(&its, "item", "poster;logo;source=centis,...;dub (repeatable)")
	flag.Parse()
	if len(its) == 0 {
		return errors.New("no --item given")
	}

	w, s := int32(*badgeW), int32(*badgeS)
	spec := catalogv1alpha1.OverlayProfileSpec{
		Corner:          catalogv1alpha1.OverlayCorner(*corner),
		CompactGeometry: &catalogv1alpha1.OverlayGeometry{WidthPercent: &w, ScorePercent: &s},
	}
	for _, it := range its {
		for _, r := range it.ratings {
			spec.Badges = append(spec.Badges, catalogv1alpha1.OverlayBadge{Source: r.Source})
		}
	}
	spec.Badges = dedupe(spec.Badges)
	compactTitle := artwork.TitleGeometry{
		MaxWidthPct: *titleW, MaxHeightPct: *titleH, BottomPct: artwork.CompactTitle.BottomPct,
	}

	rowW := margin
	for _, tw := range tileWidths {
		rowW += 2*(tw+margin) + margin
	}
	sheet := image.NewRGBA(image.Rect(0, 0, rowW, margin+len(its)*(fullH+margin)))
	draw.Draw(sheet, sheet.Bounds(), image.NewUniform(color.RGBA{R: 24, G: 24, B: 27, A: 255}), image.Point{}, draw.Src)

	for row, it := range its {
		regular, compact, err := renditions(it, spec, compactTitle)
		if err != nil {
			return fmt.Errorf("item %d (%s): %w", row+1, it.poster, err)
		}
		x, y := margin, margin+row*(fullH+margin)
		for _, tw := range tileWidths {
			for _, img := range []image.Image{regular, compact} {
				th := img.Bounds().Dy() * tw / img.Bounds().Dx()
				tile := image.Rect(x, y, x+tw, y+th)
				xdraw.CatmullRom.Scale(sheet, tile, img, img.Bounds(), xdraw.Src, nil)
				if tw == 250 {
					ref := image.Rect(tile.Max.X-tw*refWPct/100, tile.Min.Y, tile.Max.X, tile.Min.Y+tw*refHPct/100)
					outline(sheet, ref, color.RGBA{R: 255, G: 255, B: 255, A: 255})
				}
				x += tw + margin
			}
			x += margin
		}
	}

	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	if err := png.Encode(f, sheet); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// renditions draws it as the regular poster and as the compact one.
func renditions(it item, spec catalogv1alpha1.OverlayProfileSpec, compactTitle artwork.TitleGeometry) (regular, compact image.Image, err error) {
	poster, err := load(it.poster)
	if err != nil {
		return nil, nil, fmt.Errorf("poster: %w", err)
	}
	var logo image.Image
	if it.logo != "" {
		if logo, err = load(it.logo); err != nil {
			return nil, nil, fmt.Errorf("logo: %w", err)
		}
	}
	box, _ := catalogartwork.FitBox(catalogv1alpha1.ImageTypePoster)
	pw, ph := box.Size(poster.Bounds().Dx(), poster.Bounds().Dy())

	badges := overlayplan.Badges(spec, it.ratings)
	if it.dub {
		badges = append(badges, overlay.DubBadge())
	}
	one := func(title artwork.TitleGeometry, t overlay.Template) (image.Image, error) {
		canvas := image.NewRGBA(image.Rect(0, 0, pw, ph))
		xdraw.CatmullRom.Scale(canvas, canvas.Bounds(), poster, poster.Bounds(), xdraw.Src, nil)
		if logo != nil {
			if err := artwork.DrawTitled(canvas, logo, title); err != nil {
				return nil, err
			}
		}
		return overlayrender.Render(canvas, badges, t)
	}
	if regular, err = one(artwork.RegularTitle, overlay.TemplateSpec(spec)); err != nil {
		return nil, nil, err
	}
	if compact, err = one(compactTitle, overlay.CompactTemplateSpec(spec)); err != nil {
		return nil, nil, err
	}
	return regular, compact, nil
}

// load decodes the image at ref, a file or an http(s) URL.
func load(ref string) (image.Image, error) {
	var r io.Reader
	if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Get(ref) //nolint:noctx // a one-shot developer tool
		if err != nil {
			return nil, err
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GET: %s", resp.Status)
		}
		r = io.LimitReader(resp.Body, maxBody)
	} else {
		f, err := os.Open(ref)
		if err != nil {
			return nil, err
		}
		defer func() { _ = f.Close() }()
		r = f
	}
	img, _, err := image.Decode(r)
	return img, err
}

// outline draws r's border on dst, two pixels thick.
func outline(dst *image.RGBA, r image.Rectangle, c color.RGBA) {
	u := image.NewUniform(c)
	for _, edge := range []image.Rectangle{
		image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+2),
		image.Rect(r.Min.X, r.Max.Y-2, r.Max.X, r.Max.Y),
		image.Rect(r.Min.X, r.Min.Y, r.Min.X+2, r.Max.Y),
		image.Rect(r.Max.X-2, r.Min.Y, r.Max.X, r.Max.Y),
	} {
		draw.Draw(dst, edge, u, image.Point{}, draw.Src)
	}
}

// dedupe keeps the first badge of each source: a profile's badges are a map
// keyed by source.
func dedupe(badges []catalogv1alpha1.OverlayBadge) []catalogv1alpha1.OverlayBadge {
	seen := map[catalogv1alpha1.RatingSource]bool{}
	var out []catalogv1alpha1.OverlayBadge
	for _, b := range badges {
		if !seen[b.Source] {
			seen[b.Source] = true
			out = append(out, b)
		}
	}
	return out
}
