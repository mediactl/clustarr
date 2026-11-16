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
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/artwork"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

func TestFitBoxesAreTheOwnersSizes(t *testing.T) {
	for typ, want := range map[catalogv1alpha1.ImageType]artwork.Box{
		catalogv1alpha1.ImageTypePoster:     {Width: 500, Height: 750},
		catalogv1alpha1.ImageTypeFanart:     {Width: 1280, Height: 720},
		catalogv1alpha1.ImageTypeBanner:     {Width: 758, Height: 140},
		catalogv1alpha1.ImageTypeThumb:      {Width: 1280, Height: 720},
		catalogv1alpha1.ImageTypeScreenshot: {Width: 1280, Height: 720},
	} {
		got, ok := artwork.FitBox(typ)
		assert.True(t, ok, typ)
		assert.Equal(t, want, got, typ)
	}
	for _, typ := range []catalogv1alpha1.ImageType{
		catalogv1alpha1.ImageTypeLogo, catalogv1alpha1.ImageTypeClearart,
		catalogv1alpha1.ImageTypeDisc, catalogv1alpha1.ImageTypeHeadshot,
	} {
		_, ok := artwork.FitBox(typ)
		assert.False(t, ok, typ)
	}
}

func TestBoxSizeFitsInsideAndNeverScalesUp(t *testing.T) {
	poster := artwork.Box{Width: 500, Height: 750}
	for _, tc := range []struct{ w, h, wantW, wantH int }{
		{2000, 3000, 500, 750},
		{1200, 1200, 500, 500}, // a square cover: the width binds
		{1000, 3000, 250, 750}, // a tall one: the height binds
		{300, 450, 300, 450},   // smaller than the box stays
		{500, 750, 500, 750},
		{8000, 1, 500, 1},
	} {
		w, h := poster.Size(tc.w, tc.h)
		assert.Equal(t, [2]int{tc.wantW, tc.wantH}, [2]int{w, h}, "%dx%d", tc.w, tc.h)
	}
}

func TestUnfitIsABoxedTypeWithoutTheCurrentFit(t *testing.T) {
	fit := map[string]string{events.ArtworkMetaKeyFit: artwork.FitVersion}
	assert.True(t, artwork.Unfit(catalogv1alpha1.ImageTypePoster, nil))
	assert.True(t, artwork.Unfit(catalogv1alpha1.ImageTypeFanart, map[string]string{events.ArtworkMetaKeyFit: "0"}))
	assert.False(t, artwork.Unfit(catalogv1alpha1.ImageTypePoster, fit))
	assert.False(t, artwork.Unfit(catalogv1alpha1.ImageTypeLogo, nil), "a logo is stored as fetched")
}

func TestPlanFetchRefetchesAnOriginalStoredBeforeTheFit(t *testing.T) {
	obj := stored(providerURL, catalogv1alpha1.ArtworkSourceProvider)
	obj.Unfit = true
	plan := artwork.PlanFetch(artwork.ResolveSources(nil, posterImages()),
		map[catalogv1alpha1.ImageType]artwork.Object{catalogv1alpha1.ImageTypePoster: obj}, nil, planNow)
	assert.Equal(t, []schema.ArtworkFetch{{Type: "poster", URL: providerURL, Kind: "provider"}}, plan.Fetch)
}

func TestObjectMetaRecordsTheFit(t *testing.T) {
	m := artwork.ObjectMeta(artwork.ObjectRef{}, catalogv1alpha1.ImageTypePoster, events.ArtworkVariantOriginal,
		artwork.Facts{Fit: artwork.FitVersion})
	assert.Equal(t, artwork.FitVersion, m.Metadata[events.ArtworkMetaKeyFit])
	m = artwork.ObjectMeta(artwork.ObjectRef{}, catalogv1alpha1.ImageTypeLogo, events.ArtworkVariantOriginal, artwork.Facts{})
	assert.NotContains(t, m.Metadata, events.ArtworkMetaKeyFit)
}
