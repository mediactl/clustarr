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

package artwork_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/overlayplan"
	"github.com/mediactl/clustarr/app/catalog/worker/artwork"
	"github.com/mediactl/clustarr/pkg/events"
)

func (f *fixture) compactOverlayKey() string {
	return events.ArtworkKey(f.kind, f.get().Object.GetUID(), "poster", events.ArtworkVariantOverlayCompact)
}

func (f *fixture) compactPosterKey() string {
	return events.ArtworkKey(f.kind, f.get().Object.GetUID(), "poster", events.ArtworkVariantCompact)
}

// compactOverlay is the compact overlay object's Info, false when there is
// none.
func (f *fixture) compactOverlay() (events.ObjectInfo, bool) {
	f.t.Helper()
	info, err := f.store.Info(context.Background(), f.compactOverlayKey())
	if errors.Is(err, events.ErrObjectNotFound) {
		return events.ObjectInfo{}, false
	}
	require.NoError(f.t, err)
	return info, true
}

// stored decodes the object at key.
func (f *fixture) stored(key string) image.Image {
	f.t.Helper()
	_, rc, err := f.store.Get(context.Background(), key)
	require.NoError(f.t, err)
	defer func() { _ = rc.Close() }()
	img, _, err := image.Decode(rc)
	require.NoError(f.t, err)
	return img
}

// badgeWidth is how far the badge's dark box reaches from the bottom-right
// corner (the profile's default) along the poster's last row, in pixels:
// the fixture's poster is light, the box is Plex's black at 80%.
func badgeWidth(img image.Image) int {
	b := img.Bounds()
	y := b.Max.Y - 2
	n := 0
	for x := b.Max.X - 1; x >= b.Min.X; x-- {
		r, g, bl, _ := img.At(x, y).RGBA()
		if (r>>8)+(g>>8)+(bl>>8) > 3*110 {
			break
		}
		n++
	}
	return n
}

// lightPoster is a w x h poster of one light colour.
func lightPoster(t *testing.T, w, h int, c color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = c.R, c.G, c.B, c.A
	}
	return encodePNGImage(t, img)
}

// newLightFixture is a fixture whose original is a light 400 x 600 poster,
// so a badge's dark box can be measured on it.
func newLightFixture(t *testing.T, ns string) *fixture {
	t.Helper()
	f := newFixture(t, commonv1.MediaKindMovie, ns, catalogv1alpha1.RatingSourceTMDB)
	f.poster = lightPoster(t, 400, 600, color.NRGBA{R: 230, G: 230, B: 230, A: 255})
	var err error
	f.info, err = f.store.ObjectStore.Put(context.Background(), f.originalKey(), bytes.NewReader(f.poster),
		events.ObjectMeta{Headers: map[string]string{"Content-Type": "image/png", "Clustarr-Source": "provider", "Clustarr-Source-URL": "https://img.example/p.png"}})
	require.NoError(t, err)
	f.gateway(f.ratings, true)
	return f
}

func (f *fixture) wantCompactDigest(compactPoster string) string {
	it := f.get()
	p := f.profile()
	return overlayplan.Plan(it, []catalogv1alpha1.OverlayProfile{*p}, f.info.Digest).WithCompact(it, compactPoster).Compact.InputsDigest
}

// An item with badges whose poster is not titled -- most of a library -- has
// no compact poster of the metadata agent's: its compact overlay is drawn on
// the original, with the badge in the compact geometry, beside the regular
// overlay, and a second task finds both right (the compact-posters design,
// 2026-11-27).
func TestTheCompactOverlayIsDrawnOnTheOriginalWhenThereIsNoCompactPoster(t *testing.T) {
	f := newLightFixture(t, "compact-untitled")
	out, err := f.h.Render(context.Background(), f.key, f.kind)
	require.NoError(t, err)
	assert.Equal(t, artwork.OutcomeRendered, out)
	assert.EqualValues(t, 2, f.store.puts.Load())

	info, ok := f.compactOverlay()
	require.True(t, ok, "the compact overlay is stored")
	assert.Equal(t, f.wantCompactDigest(""), info.Headers["Clustarr-Rendered-From"])
	assert.Equal(t, events.ArtworkVariantOverlayCompact, info.Metadata[events.ArtworkMetaKeyVariant])
	assert.Equal(t, f.info.Digest, info.Metadata[events.ArtworkMetaKeyOriginalDigest], "drawn on the original")
	assert.Equal(t, "critics", info.Metadata[events.ArtworkMetaKeyProfile])
	assert.Equal(t, "image/jpeg", info.Headers["Content-Type"])
	_, err = f.store.Info(context.Background(), f.compactPosterKey())
	assert.ErrorIs(t, err, events.ErrObjectNotFound, "the renderer stores no compact poster: that variant is the metadata agent's")

	regular, compact := f.stored(f.overlayKey()), f.stored(f.compactOverlayKey())
	assert.Equal(t, regular.Bounds(), compact.Bounds())
	assert.InDelta(t, 400*int(catalogv1alpha1.DefaultOverlayWidthPercent)/100, badgeWidth(regular), 3, "the regular badge")
	assert.InDelta(t, 400*int(catalogv1alpha1.DefaultOverlayCompactWidthPercent)/100, badgeWidth(compact), 3, "the compact badge is the wider")

	out, err = f.h.Render(context.Background(), f.key, f.kind)
	require.NoError(t, err)
	assert.Equal(t, artwork.OutcomeUnchanged, out)
	assert.EqualValues(t, 2, f.store.puts.Load(), "both right: no Put")
}

// A titled poster has a compact twin, and the compact overlay is drawn on
// that: when the twin appears or is redrawn, only the compact overlay is
// drawn again, and the regular overlay -- the poster Plex holds -- is not
// touched.
func TestTheCompactOverlayFollowsTheCompactPoster(t *testing.T) {
	ctx := context.Background()
	f := newLightFixture(t, "compact-titled")
	_, err := f.h.Render(ctx, f.key, f.kind)
	require.NoError(t, err)
	regular, ok := f.overlay()
	require.True(t, ok)
	onOriginal, _ := f.compactOverlay()

	twin, err := f.store.ObjectStore.Put(ctx, f.compactPosterKey(),
		bytes.NewReader(lightPoster(t, 400, 600, color.NRGBA{R: 200, G: 220, B: 240, A: 255})),
		events.ObjectMeta{Headers: map[string]string{"Content-Type": "image/png", "Clustarr-Source": "provider"}})
	require.NoError(t, err)

	out, err := f.h.Render(ctx, f.key, f.kind)
	require.NoError(t, err)
	assert.Equal(t, artwork.OutcomeCompact, out, "only the compact rendition was drawn")
	assert.EqualValues(t, 3, f.store.puts.Load())
	after, _ := f.overlay()
	assert.Equal(t, regular.Digest, after.Digest)
	assert.Equal(t, regular.ModTime, after.ModTime, "the regular overlay was not written")
	info, _ := f.compactOverlay()
	assert.NotEqual(t, onOriginal.Digest, info.Digest)
	assert.Equal(t, twin.Digest, info.Metadata[events.ArtworkMetaKeyOriginalDigest], "drawn on the compact poster")
	assert.Equal(t, f.wantCompactDigest(twin.Digest), info.Headers["Clustarr-Rendered-From"])
	// The twin's own colour is under the badge-free part of the picture.
	r, g, b, _ := f.stored(f.compactOverlayKey()).At(10, 10).RGBA()
	assert.InDelta(t, 200, int(r>>8), 6)
	assert.InDelta(t, 220, int(g>>8), 6)
	assert.InDelta(t, 240, int(b>>8), 6)
}

// The profile's compact geometry redraws the compact overlay alone.
func TestTheCompactGeometryRedrawsOnlyTheCompactOverlay(t *testing.T) {
	ctx := context.Background()
	f := newLightFixture(t, "compact-geometry")
	_, err := f.h.Render(ctx, f.key, f.kind)
	require.NoError(t, err)
	regular, _ := f.overlay()

	p := f.profile()
	width := int32(40)
	p.Spec.CompactGeometry = &catalogv1alpha1.OverlayGeometry{WidthPercent: &width}
	require.NoError(t, f.c.Update(ctx, p))

	out, err := f.h.Render(ctx, f.key, f.kind)
	require.NoError(t, err)
	assert.Equal(t, artwork.OutcomeCompact, out)
	after, _ := f.overlay()
	assert.Equal(t, regular.Digest, after.Digest)
	assert.Equal(t, regular.ModTime, after.ModTime)
	assert.InDelta(t, 160, badgeWidth(f.stored(f.compactOverlayKey())), 3, "40% of 400")
}

// An item that wants no overlay any more loses the compact one with it.
func TestNoProfileRemovesTheCompactOverlayToo(t *testing.T) {
	ctx := context.Background()
	f := newLightFixture(t, "compact-none")
	_, err := f.h.Render(ctx, f.key, f.kind)
	require.NoError(t, err)
	_, ok := f.compactOverlay()
	require.True(t, ok)

	f.setLabels(nil)
	out, err := f.h.Render(ctx, f.key, f.kind)
	require.NoError(t, err)
	assert.Equal(t, artwork.OutcomeCleared, out)
	_, ok = f.overlay()
	assert.False(t, ok)
	_, ok = f.compactOverlay()
	assert.False(t, ok)

	// A compact overlay left alone, its overlay already gone, goes too.
	f.setLabels(critics)
	_, err = f.h.Render(ctx, f.key, f.kind)
	require.NoError(t, err)
	require.NoError(t, f.store.Delete(ctx, f.overlayKey()))
	f.setLabels(nil)
	out, err = f.h.Render(ctx, f.key, f.kind)
	require.NoError(t, err)
	assert.Equal(t, artwork.OutcomeCleared, out)
	_, ok = f.compactOverlay()
	assert.False(t, ok)
}

// A compact poster replaced between the plan and the draw is not drawn on
// under its old digest: the task is retried, as for an original.
func TestACompactPosterReplacedBeforeTheDrawIsRetried(t *testing.T) {
	ctx := context.Background()
	f := newLightFixture(t, "compact-swap")
	_, err := f.store.ObjectStore.Put(ctx, f.compactPosterKey(),
		bytes.NewReader(lightPoster(t, 400, 600, color.NRGBA{R: 200, G: 220, B: 240, A: 255})),
		events.ObjectMeta{Headers: map[string]string{"Content-Type": "image/png"}})
	require.NoError(t, err)
	compactPoster := f.compactPosterKey()
	var once sync.Once
	f.store.onGet = func(name string) {
		if name != compactPoster {
			return
		}
		once.Do(func() {
			_, err := f.store.ObjectStore.Put(ctx, name,
				bytes.NewReader(lightPoster(t, 400, 600, color.NRGBA{R: 240, G: 200, B: 200, A: 255})),
				events.ObjectMeta{Headers: map[string]string{"Content-Type": "image/png"}})
			require.NoError(t, err)
		})
	}
	_, err = f.h.Render(ctx, f.key, f.kind)
	require.ErrorIs(t, err, artwork.ErrInputsMoved)
	_, ok := f.compactOverlay()
	assert.False(t, ok, "nothing was drawn on the replaced compact poster")
	_, ok = f.overlay()
	assert.True(t, ok, "the regular overlay, drawn first, stands")

	out, err := f.h.Render(ctx, f.key, f.kind)
	require.NoError(t, err)
	assert.Equal(t, artwork.OutcomeCompact, out)
}

// encodePNGImage is img as a PNG.
func encodePNGImage(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}
