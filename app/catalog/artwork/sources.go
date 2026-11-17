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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	// every other source. A source with a logo is a titled one: the fetcher
	// stores the first of URL and Candidates in which it finds no text,
	// with the logo drawn on it, else Plain as it is.
	Logo string
	// Candidates are the posters to try after URL, in order: a provider
	// lists a poster under no language when it says the poster has no
	// text, and some of those carry a title all the same.
	Candidates []string
	// Plain is the poster the item takes when every candidate carries
	// text: the one ResolveSources chose. Nil when it chose none.
	Plain *Source
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

// TitleVersion is the version of the titled poster: how a poster is judged
// to carry text and how the logo is drawn on it (the fetcher's titled).
// Raising it has every titled poster fetched, judged and drawn again, as
// FitVersion does a fitted one.
const TitleVersion = "1"

// DefaultTitleLanguage is the language of the logo a titled poster takes
// when its item's metadata names none.
const DefaultTitleLanguage = "en"

// MaxTitleCandidates bounds how many posters a titled source names: each is
// a fetch and a read for text.
const MaxTitleCandidates = 6

// TitleMark is what a poster original records of the titled source it was
// stored for (events.ArtworkMetaKeyTitle): TitleVersion and a hash of the
// source's inputs -- the logo, every candidate and the plain poster -- and
// "" for a source with no logo. It names the question, not the answer: the
// fetcher stores whichever poster the answer is under the same mark, so the
// plan, which cannot know which candidate carries text, fetches again only
// when the question changed.
func (s Source) TitleMark() string {
	if s.Logo == "" {
		return ""
	}
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "logo\x00%s\x00poster\x00%s\x00", s.Logo, s.URL)
	for _, c := range s.Candidates {
		_, _ = fmt.Fprintf(h, "poster\x00%s\x00", c)
	}
	if s.Plain != nil {
		_, _ = fmt.Fprintf(h, "plain\x00%s\x00%s\x00", s.Plain.URL, s.Plain.Kind)
	}
	return TitleVersion + " " + hex.EncodeToString(h.Sum(nil))[:32]
}

// Titled is the posters and logo of a titled poster (owner, 2026-11-17: "For
// anime posters, we should prefer posters with no text - where we will
// overlay the english logo at the bottom center", and the same day for
// every Movie and Series: "We should always overlay the title image
// ourselves to standardize the positioning"): every provider poster
// that names no language, in order and at most MaxTitleCandidates -- which
// is how TMDB, TheTVDB and fanart.tv list a poster without text, though
// some carry a title all the same, so the fetcher reads each for text -- and
// the first provider logo in language. Both or neither: a textless poster
// without its logo names nothing, so an item with no such pair keeps the
// poster ResolveSources chose.
func Titled(images []catalogv1alpha1.Image, language string) (posters []catalogv1alpha1.Image, logo catalogv1alpha1.Image, ok bool) {
	language = titleLanguage(language)
	hasLogo := false
	for _, img := range images {
		if !Fetchable(img.URL) {
			continue
		}
		switch {
		case img.Type == catalogv1alpha1.ImageTypePoster && img.Language == "" && len(posters) < MaxTitleCandidates:
			posters = append(posters, img)
		case img.Type == catalogv1alpha1.ImageTypeLogo && sameLanguage(img.Language, language) && !hasLogo:
			logo, hasLogo = img, true
		}
	}
	return posters, logo, len(posters) > 0 && hasLogo
}

// titleLanguage is language, or DefaultTitleLanguage for none.
func titleLanguage(language string) string {
	if language == "" {
		return DefaultTitleLanguage
	}
	return language
}

// posterIn is the first provider poster in language that can be fetched.
func posterIn(images []catalogv1alpha1.Image, language string) (catalogv1alpha1.Image, bool) {
	language = titleLanguage(language)
	for _, img := range images {
		if img.Type == catalogv1alpha1.ImageTypePoster && Fetchable(img.URL) && sameLanguage(img.Language, language) {
			return img, true
		}
	}
	return catalogv1alpha1.Image{}, false
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
// poster (Item.TitledPoster) its Titled posters and logo in place of the
// poster and the logo: the textless posters to try, the logo to draw, the
// plain poster as the one to fall back to, and that logo as the item's
// logo. An item whose metadata has no such pair takes its plain poster.
//
// The plain poster is the one ResolveSources chose, unless that is a
// provider's and another provider poster is in the titles' language: then
// the first of those (owner, 2026-11-17: "tmdb may also be used for falling
// back to english language posters when no "xx"/no language poster is
// found for overlay"). The first provider poster is its provider's best,
// whatever it says -- TheTVDB's best scored was a Japanese one -- and a
// poster nothing is drawn on has to name the item by itself. An override
// -- the owner's, or a chosen image set's -- stays.
func (it Item) Sources() map[catalogv1alpha1.ImageType]Source {
	out := ResolveSources(it.Overrides, it.Images)
	if !it.TitledPoster {
		return out
	}
	if cur, has := out[catalogv1alpha1.ImageTypePoster]; has && cur.Kind == catalogv1alpha1.ArtworkSourceProvider {
		if p, ok := posterIn(it.Images, it.Language); ok && !sameLanguage(cur.Language, titleLanguage(it.Language)) {
			out[catalogv1alpha1.ImageTypePoster] = Source{URL: p.URL, Kind: catalogv1alpha1.ArtworkSourceProvider, Language: p.Language}
		}
	}
	posters, logo, ok := Titled(it.Images, it.Language)
	if !ok {
		return out
	}
	titled := Source{URL: posters[0].URL, Kind: catalogv1alpha1.ArtworkSourceProvider, Logo: logo.URL}
	for _, p := range posters[1:] {
		titled.Candidates = append(titled.Candidates, p.URL)
	}
	if plain, has := out[catalogv1alpha1.ImageTypePoster]; has {
		titled.Plain = &plain
	}
	out[catalogv1alpha1.ImageTypePoster] = titled
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
