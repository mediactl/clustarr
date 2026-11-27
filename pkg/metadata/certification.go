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

package metadata

import "strings"

// Certification is one country's age rating for an item.
type Certification struct {
	Country string `json:"country"`
	Rating  string `json:"rating"`
}

// CertificationsFromReleases keeps one rating per country, in the order the
// countries first appear: the theatrical release's certification, else the
// first non-empty one. A country with no certification on any release is
// left out.
func CertificationsFromReleases(rds []ReleaseDate) []Certification {
	first := map[string]string{}
	theatrical := map[string]string{}
	var order []string
	for _, rd := range rds {
		if rd.Certification == "" {
			continue
		}
		if _, ok := first[rd.Country]; !ok {
			first[rd.Country] = rd.Certification
			order = append(order, rd.Country)
		}
		if rd.Type == ReleaseTypeTheatrical {
			if _, ok := theatrical[rd.Country]; !ok {
				theatrical[rd.Country] = rd.Certification
			}
		}
	}
	out := make([]Certification, 0, len(order))
	for _, c := range order {
		rating := first[c]
		if t, ok := theatrical[c]; ok {
			rating = t
		}
		out = append(out, Certification{Country: c, Rating: rating})
	}
	return out
}

// PickCertification chooses the rating to show, with the country it is
// from: the region's, else the
// origin country's, else the US one, else none. Without the origin step a
// film released only in its own country (Weekend, 2011, UK) had no rating
// under the default US region.
func PickCertification(certs []Certification, region, origin string) Certification {
	for _, c := range []string{region, origin, "US"} {
		if c == "" {
			continue
		}
		for _, cert := range certs {
			if cert.Country == c {
				return cert
			}
		}
	}
	return Certification{}
}

// MergeCertifications is own with the countries extra adds: one rating per
// country, own's kept where both have one, own's order first. A series'
// own are TheTVDB's and extra TMDB's tv record's (SeriesExtras).
func MergeCertifications(own, extra []Certification) []Certification {
	if len(extra) == 0 {
		return own
	}
	out := make([]Certification, 0, len(own)+len(extra))
	seen := make(map[string]bool, len(own)+len(extra))
	for _, list := range [][]Certification{own, extra} {
		for _, c := range list {
			if c.Country == "" || c.Rating == "" || seen[c.Country] {
				continue
			}
			seen[c.Country] = true
			out = append(out, c)
		}
	}
	return out
}

// CertificationCountryUS is the country of the scale a translated
// certification is written on (TranslateCertification).
const CertificationCountryUS = "US"

// tvTranslations is what a series' rating in another country reads as on
// the US television scale (TV-Y, TV-Y7, TV-G, TV-PG, TV-14, TV-MA), for
// the countries TranslateCertification reads, in the order it reads them.
// A key is the rating as certificationKey writes it.
//
// Each entry is the rating's own meaning -- everyone, parental guidance,
// the age it names -- and where a country's 15 or 16 could be either
// TV-14 or TV-MA it is the one series with both ratings carry more often.
// Measured 2026-11-27 on the owner's 752 series, TheTVDB's and TMDB's
// ratings together, 687 of them with a US rating and one of these: the
// first country here that has a rating translates to the US one for 82 in
// 100 and to a neighbour of it for 99 in 100 (40 one step lower, 81 one
// higher). South Korea's 15 is TV-14 (198 against 64 TV-MA) where
// Australia's MA 15+ is TV-MA (170 against 33). Taiwan is left out: its 6+
// and 0+ sit on series the US rates TV-14 (76 of 90).
//
// Australia's PG is TV-14, not the TV-PG its word says (owner, 2026-11-27:
// "Australia's PG -- map to TV-14"): the series that carry it are rated
// TV-14 in the US more often than TV-PG (42 against 25), and it is the
// rating of 14 of the 45 series a translation rates. The other countries'
// PG keeps its word.
var tvTranslations = []struct {
	country string
	ratings map[string]string
}{
	{"AU", map[string]string{"P": "TV-Y", "C": "TV-Y7", "G": "TV-G", "PG": "TV-14", "M": "TV-14", "MA15": "TV-MA", "AV15": "TV-MA", "R18": "TV-MA"}},
	{"NZ", map[string]string{"G": "TV-G", "PG": "TV-PG", "M": "TV-14", "16": "TV-MA", "18": "TV-MA"}},
	{"GB", map[string]string{"U": "TV-G", "PG": "TV-PG", "12": "TV-14", "15": "TV-MA", "18": "TV-MA", "R18": "TV-MA"}},
	{"CA", map[string]string{"C": "TV-Y", "C8": "TV-Y7", "G": "TV-G", "PG": "TV-PG", "14": "TV-14", "18": "TV-MA"}},
	{"BR", map[string]string{"L": "TV-G", "10": "TV-PG", "12": "TV-14", "14": "TV-14", "16": "TV-MA", "18": "TV-MA"}},
	{"KR", map[string]string{"ALL": "TV-G", "7": "TV-Y7", "12": "TV-14", "15": "TV-14", "19": "TV-MA"}},
	{"SG", map[string]string{"G": "TV-G", "PG": "TV-PG", "PG13": "TV-14", "NC16": "TV-14", "M18": "TV-MA", "R21": "TV-MA"}},
	{"DE", map[string]string{"0": "TV-G", "6": "TV-PG", "12": "TV-14", "16": "TV-MA", "18": "TV-MA"}},
	{"AT", map[string]string{"0": "TV-G", "6": "TV-PG", "8": "TV-PG", "10": "TV-PG", "12": "TV-14", "14": "TV-14", "16": "TV-MA", "18": "TV-MA"}},
}

// certificationKey is a rating as tvTranslations keys it: upper case, with
// no space, no leading "-" and no trailing "+", so "MA 15+" and "MA15+"
// (TMDB's and TheTVDB's spellings), Germany's "16" and "16+" and France's
// "-16" each read as one rating.
func certificationKey(rating string) string {
	k := strings.ToUpper(strings.ReplaceAll(rating, " ", ""))
	return strings.TrimRight(strings.TrimLeft(k, "-"), "+")
}

// TranslateCertification reads a series' ratings in other countries as one
// on the US television scale: the first country of tvTranslations that has
// a rating the table knows. from is the published rating it read. ok is
// false when no country here has one, and the series stays unrated.
//
// It is for a series alone: a film's ratings are on its countries' cinema
// scales, which the US television one does not answer to.
func TranslateCertification(certs []Certification) (translated, from Certification, ok bool) {
	for _, t := range tvTranslations {
		for _, c := range certs {
			if c.Country != t.country {
				continue
			}
			if rating, known := t.ratings[certificationKey(c.Rating)]; known {
				return Certification{Country: CertificationCountryUS, Rating: rating}, c, true
			}
		}
	}
	return Certification{}, Certification{}, false
}

// PickSeriesCertification is PickCertification for a series, with one more
// step: when the region, the origin country and the US have no rating, a
// rating from another country translated to the US television scale
// (TranslateCertification), its country "US". A translated rating is the
// one certs holds no entry for: certs stays the published ratings.
func PickSeriesCertification(certs []Certification, region, origin string) Certification {
	if cert := PickCertification(certs, region, origin); cert.Rating != "" {
		return cert
	}
	translated, _, _ := TranslateCertification(certs)
	return translated
}

// CertificationPublished reports whether cert is one of certs: false for a
// translated rating, and for none.
func CertificationPublished(certs []Certification, cert Certification) bool {
	if cert.Rating == "" {
		return false
	}
	for _, c := range certs {
		if c == cert {
			return true
		}
	}
	return false
}
