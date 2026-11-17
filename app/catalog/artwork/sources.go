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
	"strings"

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
	// Language is the provider image's ISO 639-1 language (Image.Language),
	// recorded in the object's metadata (artwork design §B.2 as amended
	// 2026-10-07); "" for a custom override or an image that names none.
	Language string
	// Logo is the URL of the title logo the fetcher draws at the bottom
	// centre of a poster whose own image carries no title (Titled); "" for
	// every other source.
	Logo string
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
		out[img.Type] = Source{URL: img.URL, Kind: catalogv1alpha1.ArtworkSourceProvider, Language: img.Language}
	}
	for _, o := range overrides {
		if o.URL == "" || !KnownType(o.Type) {
			continue
		}
		out[o.Type] = Source{URL: o.URL, Kind: catalogv1alpha1.ArtworkSourceCustom}
	}
	return out
}

// TitleVersion is the version of the title drawing (the fetcher's titled):
// where the logo sits, how large it is and what lies behind it. Raising it
// has every titled poster fetched and drawn again, as FitVersion does a
// fitted one.
const TitleVersion = "1"

// DefaultTitleLanguage is the language of the logo a titled poster takes
// when its item's metadata names none.
const DefaultTitleLanguage = "en"

// TitleMark is what a poster original records of its title logo
// (events.ArtworkMetaKeyTitle): the drawing's version and the logo's URL,
// "" for a poster with none. The plan fetches a poster whose mark is not its
// source's.
func TitleMark(logoURL string) string {
	if logoURL == "" {
		return ""
	}
	return TitleVersion + " " + logoURL
}

// Titled is the poster and logo of a titled poster (owner, 2026-11-17: "For
// anime posters, we should prefer posters with no text - where we will
// overlay the english logo at the bottom center"): the first provider poster
// that names no language, which is how TMDB, TheTVDB and fanart.tv list a
// poster without text, and the first provider logo in language. Both or
// neither: a textless poster without its logo names nothing, so an item
// with no such pair keeps the poster ResolveSources chose.
func Titled(images []catalogv1alpha1.Image, language string) (poster, logo catalogv1alpha1.Image, ok bool) {
	if language == "" {
		language = DefaultTitleLanguage
	}
	var hasPoster, hasLogo bool
	for _, img := range images {
		if !Fetchable(img.URL) {
			continue
		}
		switch {
		case img.Type == catalogv1alpha1.ImageTypePoster && img.Language == "" && !hasPoster:
			poster, hasPoster = img, true
		case img.Type == catalogv1alpha1.ImageTypeLogo && sameLanguage(img.Language, language) && !hasLogo:
			logo, hasLogo = img, true
		}
	}
	return poster, logo, hasPoster && hasLogo
}

// sameLanguage compares two language tags by their primary subtag, so
// "en-US" is "en".
func sameLanguage(a, b string) bool {
	primary := func(s string) string {
		s, _, _ = strings.Cut(strings.ToLower(s), "-")
		return s
	}
	return a != "" && primary(a) == primary(b)
}

// Sources is ResolveSources for it, and for an item that takes a titled
// poster (Item.TitledPoster) its Titled pair in place of the poster and the
// logo: the textless poster with the logo to draw on it, and that logo as
// the item's logo. An item whose metadata has no such pair is unchanged.
func (it Item) Sources() map[catalogv1alpha1.ImageType]Source {
	out := ResolveSources(it.Overrides, it.Images)
	if !it.TitledPoster {
		return out
	}
	poster, logo, ok := Titled(it.Images, it.Language)
	if !ok {
		return out
	}
	out[catalogv1alpha1.ImageTypePoster] = Source{
		URL: poster.URL, Kind: catalogv1alpha1.ArtworkSourceProvider, Logo: logo.URL,
	}
	if _, own := overrideOf(it.Overrides, catalogv1alpha1.ImageTypeLogo); !own {
		out[catalogv1alpha1.ImageTypeLogo] = Source{
			URL: logo.URL, Kind: catalogv1alpha1.ArtworkSourceProvider, Language: logo.Language,
		}
	}
	return out
}

// overrideOf is the last override of type t with a URL, the one
// ResolveSources takes.
func overrideOf(overrides []catalogv1alpha1.ArtworkOverride, t catalogv1alpha1.ImageType) (catalogv1alpha1.ArtworkOverride, bool) {
	for i := len(overrides) - 1; i >= 0; i-- {
		if overrides[i].Type == t && overrides[i].URL != "" {
			return overrides[i], true
		}
	}
	return catalogv1alpha1.ArtworkOverride{}, false
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
