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

package v1alpha1

// AspectRatio is the shape of picture a series is kept in (owner,
// 2026-11-17: "we should attempt to always download widescreen unless the
// original aspect ratio was 4:3", and a season should be one shape).
// Neither TMDB nor TheTVDB says what shape a series was made in, so it is
// a setting, widescreen unless a person says otherwise.
// +kubebuilder:validation:Enum=widescreen;fullscreen;any
type AspectRatio string

const (
	// AspectRatioWidescreen wants a picture wider than 3:2: 16:9 and every
	// wider frame, cropped or letterboxed.
	AspectRatioWidescreen AspectRatio = "widescreen"
	// AspectRatioFullscreen wants a 4:3 picture, as television was made
	// before widescreen: the original of such a series, where a widescreen
	// release of it is a crop.
	AspectRatioFullscreen AspectRatio = "fullscreen"
	// AspectRatioAny holds no file against a shape.
	AspectRatioAny AspectRatio = "any"
)

// AspectRatioFix is what is done about a file that is not the shape its
// series is kept in.
// +kubebuilder:validation:Enum=replace;crop
type AspectRatioFix string

const (
	// AspectRatioFixReplace searches for another release, and the first of
	// the wanted shape replaces the file.
	AspectRatioFixReplace AspectRatioFix = "replace"
	// AspectRatioFixCrop keeps the best release whatever its shape and
	// crops it to the wanted one in its transcode, as a remaster does
	// (owner, 2026-11-17: "for some shows, we should prefer the high
	// quality and crop ourselves during transcode"): a 4:3 picture loses
	// the top and bottom a 16:9 frame has no room for, a 16:9 one its
	// sides. A file already transcoded is never encoded again: it is
	// searched for anew, and the release that comes is cropped.
	AspectRatioFixCrop AspectRatioFix = "crop"
)

// Milli is the display aspect ratio, in thousandths, a picture cropped to
// the shape has: 16:9 for widescreen, 4:3 for fullscreen, 0 for any other.
func (a AspectRatio) Milli() int32 {
	switch a {
	case AspectRatioWidescreen:
		return 1778
	case AspectRatioFullscreen:
		return 1333
	}
	return 0
}

// fullscreenBelowMilli is the display aspect ratio, in thousandths, under
// which a picture is fullscreen: 3:2. A 4:3 picture is 1333 and Academy
// 1375; the narrowest widescreen television, 14:9, is 1556.
const fullscreenBelowMilli = 1500

// AspectOf is the shape of a picture of the display aspect ratio
// displayAspectMilli (MediaInfo.DisplayAspectMilli), "" when it is unknown
// (zero: no video, or a probe older than ProbeVersion 5). It reads the
// frame, so bars drawn into a wider frame are part of the picture: a 4:3
// picture pillarboxed into a 16:9 frame is widescreen here.
func AspectOf(displayAspectMilli int32) AspectRatio {
	switch {
	case displayAspectMilli <= 0:
		return ""
	case displayAspectMilli < fullscreenBelowMilli:
		return AspectRatioFullscreen
	default:
		return AspectRatioWidescreen
	}
}

// WrongAspect reports whether a picture of display aspect ratio
// displayAspectMilli is not the shape want asks for. A picture whose shape
// is unknown is never wrong, nor is any under "any" or no setting.
func WrongAspect(want AspectRatio, displayAspectMilli int32) bool {
	got := AspectOf(displayAspectMilli)
	if got == "" || (want != AspectRatioWidescreen && want != AspectRatioFullscreen) {
		return false
	}
	return got != want
}

// SeasonAspectRatio is one season's own shape, where it is not the
// series': the widescreen seasons of a series that began in 4:3.
type SeasonAspectRatio struct {
	// Number is the season number; 0 is the specials season.
	// +required
	// +kubebuilder:validation:Minimum=0
	Number int32 `json:"number"`

	// AspectRatio is the shape the season is kept in.
	// +required
	AspectRatio AspectRatio `json:"aspectRatio"`
}

// WantedAspect is the shape season's episodes are kept in: the season's
// own (spec.seasonAspectRatios) where it has one, else the series'
// (spec.aspectRatio), widescreen when neither is set.
func (s *Series) WantedAspect(season int32) AspectRatio {
	for _, o := range s.Spec.SeasonAspectRatios {
		if o.Number == season && o.AspectRatio != "" {
			return o.AspectRatio
		}
	}
	if s.Spec.AspectRatio != "" {
		return s.Spec.AspectRatio
	}
	return AspectRatioWidescreen
}

// WrongAspect reports whether mf's probed picture is not the shape want
// asks for (WrongAspect); false for an unprobed file.
func (mf *MediaFile) WrongAspect(want AspectRatio) bool {
	mi := mf.Status.MediaInfo
	if mi == nil {
		return false
	}
	return WrongAspect(want, mi.DisplayAspectMilli)
}

// CropsToAspect reports whether the series keeps the best release and
// crops it to the wanted shape in its transcode (spec.aspectRatioFix crop).
func (s *Series) CropsToAspect() bool {
	return s.Spec.AspectRatioFix == AspectRatioFixCrop
}

// CropTarget is the display aspect ratio, in thousandths, a file of
// season's whose picture is displayAspectMilli is cropped to in its
// transcode: the wanted shape's, when the series crops and the picture is
// the other shape; 0 when nothing is cropped.
func (s *Series) CropTarget(season, displayAspectMilli int32) int32 {
	want := s.WantedAspect(season)
	if !s.CropsToAspect() || !WrongAspect(want, displayAspectMilli) {
		return 0
	}
	return want.Milli()
}
