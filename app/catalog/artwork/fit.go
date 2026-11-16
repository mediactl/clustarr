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

package artwork

import (
	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
)

// FitVersion is the version of the fit an original of a boxed type is
// stored under (FitBox), recorded as events.ArtworkMetaKeyFit. The item
// key's plan fetches again an original below it (Unfit), so raising it --
// for a new box or a new encoding -- refits the whole store once.
const FitVersion = "1"

// Box is the largest an original of one image type is stored: it is scaled
// down to fit inside, its aspect ratio kept, and never scaled up.
type Box struct{ Width, Height int }

// fitBoxes are the stored sizes (owner, 2026-11-16: "We can greatly reduce
// the image cache by compressing jpegs and resizing images"): no reader
// draws a poster, a background, a banner or a thumbnail larger. Logos,
// clearart, disc art and headshots are small or transparent and are stored
// as fetched.
var fitBoxes = map[catalogv1alpha1.ImageType]Box{
	catalogv1alpha1.ImageTypePoster:     {500, 750},
	catalogv1alpha1.ImageTypeFanart:     {1280, 720},
	catalogv1alpha1.ImageTypeBanner:     {758, 140},
	catalogv1alpha1.ImageTypeThumb:      {1280, 720},
	catalogv1alpha1.ImageTypeScreenshot: {1280, 720},
}

// FitBox is the box an original of type t is stored within; false for a
// type stored as fetched.
func FitBox(t catalogv1alpha1.ImageType) (Box, bool) {
	b, ok := fitBoxes[t]
	return b, ok
}

// Size is the size a w x h image is stored at: itself when it fits, else
// the largest size of its aspect ratio inside the box, each side at least 1.
func (b Box) Size(w, h int) (int, int) {
	if w <= b.Width && h <= b.Height {
		return w, h
	}
	if w*b.Height >= h*b.Width {
		return b.Width, max((h*b.Width+w/2)/w, 1)
	}
	return max((w*b.Height+h/2)/h, 1), b.Height
}

// Unfit reports an original of type t whose metadata does not record the
// current fit: one stored before originals were fitted, or under an older
// FitVersion. A type with no box is never unfit.
func Unfit(t catalogv1alpha1.ImageType, meta map[string]string) bool {
	if _, boxed := fitBoxes[t]; !boxed {
		return false
	}
	return meta[events.ArtworkMetaKeyFit] != FitVersion
}
