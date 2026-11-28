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
	"crypto/sha256"
	"encoding/hex"
	"image"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	catalogartwork "github.com/mediactl/clustarr/app/catalog/artwork"
	"github.com/mediactl/clustarr/app/catalog/metadata/artwork"
	"github.com/mediactl/clustarr/pkg/events"
)

// compactKey is the item's compact poster.
func (fx *fixture) compactKey() string {
	return events.ArtworkKey(fx.ref.Kind, fx.ref.UID, string(catalogv1alpha1.ImageTypePoster), events.ArtworkVariantCompact)
}

// compact reads the item's compact poster back, ok false when there is none.
func (fx *fixture) compact(t *testing.T) (body []byte, info events.ObjectInfo, ok bool) {
	t.Helper()
	info, rc, err := fx.store.Get(fx.ctx, fx.compactKey())
	if err != nil {
		require.ErrorIs(t, err, events.ErrObjectNotFound)
		return nil, events.ObjectInfo{}, false
	}
	defer func() { _ = rc.Close() }()
	body, err = io.ReadAll(rc)
	require.NoError(t, err)
	return body, info, true
}

// A titled poster is stored twice (the compact-posters design, 2026-11-27):
// as it always was, and as its compact twin, the same art with the logo in
// the larger box. The regular one is byte for byte what it was before the
// twin existed -- its digest names the poster to Plex, and a new one would
// have Plex refresh every titled item.
func TestFetchTitled_StoresCompactAndLeavesOriginalAlone(t *testing.T) {
	fx := newFixture(t)
	poster := fx.srv.serve("/textless.png", "image/png", encodePNG(t, flat(1000, 1500, grey)))
	logo := fx.srv.serve("/logo.png", "image/png", encodePNG(t, wordmark(1600, 400, 100, red)))
	src := titledSource(logo, nil, poster)
	_, err := fx.fetchTitled(src)
	require.NoError(t, err)

	original, _, _, originalInfo := fx.storedImage(t, catalogv1alpha1.ImageTypePoster)
	sum := sha256.Sum256(original)
	// The digest of this fixture's titled poster at aecf0331, before the
	// twin. Go's JPEG encoder changing would move it too: capture it again
	// from the commit before the change that moved it.
	assert.Equal(t, "56c9acd1bfeb367ca4d80176654d5f0b4ea45645749941b9f774cc239565c2cb", hex.EncodeToString(sum[:]))
	assert.Empty(t, originalInfo.Metadata[events.ArtworkMetaKeyCompact], "the regular poster carries no compact mark")

	body, info, ok := fx.compact(t)
	require.True(t, ok, "the compact twin is stored")
	assert.NotEqual(t, original, body)
	assert.Equal(t, events.ArtworkVariantCompact, info.Metadata[events.ArtworkMetaKeyVariant])
	assert.Equal(t, src.CompactMark(), info.Metadata[events.ArtworkMetaKeyCompact])
	assert.Equal(t, "true", info.Metadata[events.ArtworkMetaKeyTitled])
	assert.Equal(t, src.TitleMark(), info.Metadata[events.ArtworkMetaKeyTitle])
	assert.Equal(t, poster, info.Metadata[events.ArtworkMetaKeySourceURL])
	assert.Equal(t, "image/jpeg", info.Headers[events.HeaderContentType])

	img, _, err := image.Decode(bytes.NewReader(body))
	require.NoError(t, err)
	assert.Equal(t, image.Rect(0, 0, 500, 750), img.Bounds())
	// The ink is 7:1. Regular: 390 wide, x 55..445. Compact: its box's
	// width (CompactTitle.MaxWidthPct of 500), so it reaches past both.
	left := (500 - 500*artwork.CompactTitle.MaxWidthPct/100) / 2
	bottom := 750 - 750*artwork.CompactTitle.BottomPct/100
	assert.True(t, isRedAt(img, left+8, bottom-15), "the compact logo's left end, outside the regular logo")
	assert.True(t, isRedAt(img, 500-left-8, bottom-15), "and its right end")

	regular, _ := fx.decoded(t)
	assert.False(t, isRedAt(regular, left+8, bottom-15), "the regular poster's logo did not grow")
}

// A poster that is not titled has no twin: one stored plain because every
// candidate carried text, one whose source lost its logo, and one dropped,
// each take the twin with them.
func TestFetch_UntitledPosterDeletesCompact(t *testing.T) {
	fx := newFixture(t)
	clean := fx.srv.serve("/clean.png", "image/png", encodePNG(t, flat(1000, 1500, grey)))
	lettered := fx.srv.serve("/lettered.png", "image/png", encodePNG(t, withTitle(flat(1000, 1500, grey))))
	logo := fx.srv.serve("/logo.png", "image/png", encodePNG(t, wordmark(1600, 400, 100, red)))
	titled := func() {
		t.Helper()
		_, err := fx.fetchTitled(titledSource(logo, nil, clean))
		require.NoError(t, err)
		_, _, ok := fx.compact(t)
		require.True(t, ok)
	}

	titled()
	plain := catalogartwork.Source{URL: lettered, Kind: catalogv1alpha1.ArtworkSourceProvider}
	_, err := fx.fetchTitled(titledSource(logo, &plain, lettered))
	require.NoError(t, err)
	_, _, ok := fx.compact(t)
	assert.False(t, ok, "every candidate carried text: the plain poster has no twin")

	titled()
	require.NoError(t, fx.fetch(catalogv1alpha1.ImageTypePoster, clean))
	_, _, ok = fx.compact(t)
	assert.False(t, ok, "the source has no logo any more")

	titled()
	require.NoError(t, fx.f.Drop(fx.ctx, fx.ref, catalogv1alpha1.ImageTypePoster))
	_, _, ok = fx.compact(t)
	assert.False(t, ok, "the poster was dropped")

	// Another type's fetch and drop leave a poster's twin alone.
	titled()
	require.NoError(t, fx.fetch(catalogv1alpha1.ImageTypeFanart, clean))
	require.NoError(t, fx.f.Drop(fx.ctx, fx.ref, catalogv1alpha1.ImageTypeFanart))
	_, _, ok = fx.compact(t)
	assert.True(t, ok)
}
