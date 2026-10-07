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
	"net/url"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// ImageTypes is the CRD's ImageType enum in its declared order: the order
// the gateway's Fetcher.Sync visits types in, and so the order
// status.artwork is rendered in. Callers read it; none modifies it.
var ImageTypes = []catalogv1alpha1.ImageType{
	catalogv1alpha1.ImageTypePoster,
	catalogv1alpha1.ImageTypeFanart,
	catalogv1alpha1.ImageTypeBanner,
	catalogv1alpha1.ImageTypeLogo,
	catalogv1alpha1.ImageTypeClearart,
	catalogv1alpha1.ImageTypeThumb,
	catalogv1alpha1.ImageTypeScreenshot,
	catalogv1alpha1.ImageTypeDisc,
	catalogv1alpha1.ImageTypeHeadshot,
}

// KnownType reports whether t is one of ImageTypes.
func KnownType(t catalogv1alpha1.ImageType) bool {
	for _, k := range ImageTypes {
		if k == t {
			return true
		}
	}
	return false
}

// Source is where one image type's original comes from.
type Source struct {
	URL  string
	Kind catalogv1alpha1.ArtworkSource
}

// ResolveSources picks one source per image type (spec §B.4): the
// spec.artwork override for that type if there is one -- custom wins --
// else the first provider image of that type with an absolute http(s) URL.
// A type with neither is absent from the map.
func ResolveSources(overrides []catalogv1alpha1.ArtworkOverride, images []catalogv1alpha1.Image) map[catalogv1alpha1.ImageType]Source {
	out := make(map[catalogv1alpha1.ImageType]Source, len(ImageTypes))
	for _, img := range images {
		if _, taken := out[img.Type]; taken || !KnownType(img.Type) || !Fetchable(img.URL) {
			continue
		}
		out[img.Type] = Source{URL: img.URL, Kind: catalogv1alpha1.ArtworkSourceProvider}
	}
	for _, o := range overrides {
		if o.URL == "" || !KnownType(o.Type) {
			continue
		}
		out[o.Type] = Source{URL: o.URL, Kind: catalogv1alpha1.ArtworkSourceCustom}
	}
	return out
}

// Fetchable reports whether raw is an absolute http or https URL with a
// host: the only kind of source the gateway fetches.
func Fetchable(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// Index keys entries by image type.
func Index(entries []catalogv1alpha1.ArtworkEntry) map[catalogv1alpha1.ImageType]catalogv1alpha1.ArtworkEntry {
	out := make(map[catalogv1alpha1.ImageType]catalogv1alpha1.ArtworkEntry, len(entries))
	for _, e := range entries {
		out[e.Type] = e
	}
	return out
}
