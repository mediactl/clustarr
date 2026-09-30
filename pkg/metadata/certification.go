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

// PickCertification chooses the rating to show: the region's, else the
// origin country's, else the US one, else none. Without the origin step a
// film released only in its own country (Weekend, 2011, UK) had no rating
// under the default US region.
func PickCertification(certs []Certification, region, origin string) string {
	for _, c := range []string{region, origin, "US"} {
		if c == "" {
			continue
		}
		for _, cert := range certs {
			if cert.Country == c {
				return cert.Rating
			}
		}
	}
	return ""
}
