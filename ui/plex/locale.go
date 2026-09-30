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

package plex

import (
	"net/http"
	"strings"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// locale is what Plex asks the provider for (research §3): the country a
// content rating is chosen for, and the language the request is in.
// Country is ISO 3166-1 alpha-2 upper case, Language ISO 639-1 lower case;
// either may be empty.
type locale struct {
	Country  string
	Language string
}

// plexParam reads a Plex parameter from its header, else its query
// parameter: Plex may send either, and the header wins.
func plexParam(r *http.Request, name string) string {
	if v := r.Header.Get(name); v != "" {
		return v
	}
	return r.URL.Query().Get(name)
}

// localeOf reads X-Plex-Language ("en-US", an IETF tag with its region) and
// X-Plex-Country ("GB"). With no X-Plex-Country, the language tag's region
// is the country.
func localeOf(r *http.Request) locale {
	var loc locale
	lang, region, _ := strings.Cut(plexParam(r, "X-Plex-Language"), "-")
	loc.Language = strings.ToLower(lang)
	loc.Country = strings.ToUpper(plexParam(r, "X-Plex-Country"))
	if loc.Country == "" {
		loc.Country = strings.ToUpper(region)
	}
	return loc
}

// contentRating chooses the rating Plex shows: the requested country's
// when the item has one, else the stored certification the metadata
// gateway chose for the configured region, prefixed with the country it
// chose it from. A rating from outside the US is written "<cc>/<rating>" in
// lower case, as the protocol requires.
func contentRating(certs []catalogv1.Certification, stored catalogv1.Certification, loc locale) string {
	if loc.Country != "" {
		for _, c := range certs {
			if c.Country == loc.Country {
				return prefixed(c)
			}
		}
	}
	if stored.Rating == "" {
		return ""
	}
	if stored.Country != "" {
		return prefixed(stored)
	}
	// A document written before the gateway recorded the country: the US
	// when it has that rating (a region-less install falls back to the US
	// last), else the first country that does.
	var match *catalogv1.Certification
	for i := range certs {
		if certs[i].Rating != stored.Rating {
			continue
		}
		if certs[i].Country == "US" {
			return stored.Rating
		}
		if match == nil {
			match = &certs[i]
		}
	}
	if match == nil {
		return stored.Rating
	}
	return prefixed(*match)
}

func prefixed(c catalogv1.Certification) string {
	if c.Country == "" || c.Country == "US" {
		return c.Rating
	}
	return strings.ToLower(c.Country) + "/" + c.Rating
}

// wantsOriginal reports whether the request is in a language other than
// the item's original one, so the original-language fields are due. A
// request without a language is taken as English, the gateway's default.
func (loc locale) wantsOriginal(originalLanguage string) bool {
	lang := loc.Language
	if lang == "" {
		lang = "en"
	}
	return originalLanguage != "" && lang != originalLanguage
}
