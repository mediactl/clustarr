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

package v1alpha1_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// A picture is fullscreen under 3:2 and widescreen from it, by its display
// aspect ratio; one whose ratio is unknown has no shape and is never
// wrong, and nothing is wrong under "any" or no setting.
func TestAPictureIsFullscreenUnderThreeToTwo(t *testing.T) {
	for milli, want := range map[int32]catalogv1alpha1.AspectRatio{
		0:    "",
		1333: catalogv1alpha1.AspectRatioFullscreen, // 4:3
		1375: catalogv1alpha1.AspectRatioFullscreen, // Academy
		1499: catalogv1alpha1.AspectRatioFullscreen,
		1500: catalogv1alpha1.AspectRatioWidescreen, // 3:2
		1556: catalogv1alpha1.AspectRatioWidescreen, // 14:9
		1778: catalogv1alpha1.AspectRatioWidescreen, // 16:9
		2000: catalogv1alpha1.AspectRatioWidescreen,
		2400: catalogv1alpha1.AspectRatioWidescreen,
	} {
		assert.Equal(t, want, catalogv1alpha1.AspectOf(milli), "%d", milli)
	}

	assert.True(t, catalogv1alpha1.WrongAspect(catalogv1alpha1.AspectRatioWidescreen, 1333))
	assert.True(t, catalogv1alpha1.WrongAspect(catalogv1alpha1.AspectRatioFullscreen, 1778))
	assert.False(t, catalogv1alpha1.WrongAspect(catalogv1alpha1.AspectRatioWidescreen, 1778))
	assert.False(t, catalogv1alpha1.WrongAspect(catalogv1alpha1.AspectRatioWidescreen, 2400))
	assert.False(t, catalogv1alpha1.WrongAspect(catalogv1alpha1.AspectRatioFullscreen, 1333))
	assert.False(t, catalogv1alpha1.WrongAspect(catalogv1alpha1.AspectRatioWidescreen, 0), "an unknown shape is never wrong")
	assert.False(t, catalogv1alpha1.WrongAspect(catalogv1alpha1.AspectRatioAny, 1333))
	assert.False(t, catalogv1alpha1.WrongAspect("", 1333))
}

// A season is kept in its own shape where it has one, else the series',
// and widescreen when nothing is set; a file is held against it by its
// probe, an unprobed file never.
func TestASeasonIsKeptInItsOwnShapeElseTheSeries(t *testing.T) {
	s := &catalogv1alpha1.Series{}
	assert.Equal(t, catalogv1alpha1.AspectRatioWidescreen, s.WantedAspect(1), "widescreen when nothing is set")

	s.Spec.AspectRatio = catalogv1alpha1.AspectRatioFullscreen
	s.Spec.SeasonAspectRatios = []catalogv1alpha1.SeasonAspectRatio{{Number: 3, AspectRatio: catalogv1alpha1.AspectRatioWidescreen}}
	assert.Equal(t, catalogv1alpha1.AspectRatioFullscreen, s.WantedAspect(1))
	assert.Equal(t, catalogv1alpha1.AspectRatioWidescreen, s.WantedAspect(3))
	assert.Equal(t, catalogv1alpha1.AspectRatioFullscreen, s.WantedAspect(0))

	mf := &catalogv1alpha1.MediaFile{}
	assert.False(t, mf.WrongAspect(catalogv1alpha1.AspectRatioWidescreen), "an unprobed file")
	mf.Status.MediaInfo = &commonv1.MediaInfo{Width: 1440, Height: 1080, DisplayAspectMilli: 1333}
	assert.True(t, mf.WrongAspect(s.WantedAspect(3)))
	assert.False(t, mf.WrongAspect(s.WantedAspect(1)))
	mf.Status.MediaInfo.DisplayAspectMilli = 0
	assert.False(t, mf.WrongAspect(s.WantedAspect(3)), "a file probed before the probe read its shape")
}

// A series that crops (spec.aspectRatioFix crop) cuts a file of the other
// shape to the wanted one's ratio, 16:9 or 4:3, by its season's shape; a
// series that replaces crops nothing, nor is a file of the wanted shape,
// of an unknown one, or under "any" cropped.
func TestASeriesThatCropsCutsTheOtherShapeToTheWantedRatio(t *testing.T) {
	assert.Equal(t, int32(1778), catalogv1alpha1.AspectRatioWidescreen.Milli())
	assert.Equal(t, int32(1333), catalogv1alpha1.AspectRatioFullscreen.Milli())
	assert.Zero(t, catalogv1alpha1.AspectRatioAny.Milli())
	assert.Zero(t, catalogv1alpha1.AspectRatio("").Milli())

	s := &catalogv1alpha1.Series{Spec: catalogv1alpha1.SeriesSpec{
		AspectRatio:        catalogv1alpha1.AspectRatioWidescreen,
		SeasonAspectRatios: []catalogv1alpha1.SeasonAspectRatio{{Number: 1, AspectRatio: catalogv1alpha1.AspectRatioFullscreen}, {Number: 2, AspectRatio: catalogv1alpha1.AspectRatioAny}},
	}}
	assert.False(t, s.CropsToAspect(), "a series replaces unless it is set to crop")
	assert.Zero(t, s.CropTarget(3, 1333), "a series that replaces crops nothing")

	s.Spec.AspectRatioFix = catalogv1alpha1.AspectRatioFixCrop
	assert.True(t, s.CropsToAspect())
	assert.Equal(t, int32(1778), s.CropTarget(3, 1333), "4:3 in a widescreen season")
	assert.Zero(t, s.CropTarget(3, 1778), "already the shape")
	assert.Zero(t, s.CropTarget(3, 2400), "wider than 16:9 is widescreen: left as it is")
	assert.Zero(t, s.CropTarget(3, 0), "a shape no probe read")
	assert.Equal(t, int32(1333), s.CropTarget(1, 1778), "16:9 in a 4:3 season")
	assert.Zero(t, s.CropTarget(1, 1333))
	assert.Zero(t, s.CropTarget(2, 1333), "a season of any shape")
}
