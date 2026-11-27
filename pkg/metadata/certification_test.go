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

package metadata_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/pkg/metadata"
)

// TestCertificationsFromReleasesPrefersTheatrical keeps one rating per
// country: the theatrical release's, else the first non-empty one.
func TestCertificationsFromReleasesPrefersTheatrical(t *testing.T) {
	now := time.Now()
	rds := []metadata.ReleaseDate{
		{Country: "GB", Type: metadata.ReleaseTypePremiere, Date: now},
		{Country: "GB", Type: metadata.ReleaseTypeDigital, Certification: "15", Date: now},
		{Country: "GB", Type: metadata.ReleaseTypeTheatrical, Certification: "18", Date: now},
		{Country: "DE", Type: metadata.ReleaseTypePhysical, Certification: "16", Date: now},
		{Country: "FR", Type: metadata.ReleaseTypeTheatrical, Date: now},
	}
	assert.Equal(t, []metadata.Certification{{Country: "GB", Rating: "18"}, {Country: "DE", Rating: "16"}},
		metadata.CertificationsFromReleases(rds))
}

// TestPickCertificationFallsBackRegionOriginUS is the choice that gives a
// UK-only film like Weekend (2011) its rating instead of none.
func TestPickCertificationFallsBackRegionOriginUS(t *testing.T) {
	certs := []metadata.Certification{{Country: "GB", Rating: "18"}, {Country: "US", Rating: "R"}}
	gb, us := metadata.Certification{Country: "GB", Rating: "18"}, metadata.Certification{Country: "US", Rating: "R"}
	assert.Equal(t, gb, metadata.PickCertification(certs, "GB", "US"))
	assert.Equal(t, us, metadata.PickCertification(certs, "FR", "US"))
	assert.Equal(t, gb, metadata.PickCertification(certs[:1], "FR", "GB"), "a UK-only film")
	assert.Equal(t, us, metadata.PickCertification(certs[1:], "FR", "GB"), "US last")
	assert.Equal(t, metadata.Certification{}, metadata.PickCertification(nil, "US", "US"))
}

// A series' ratings are its own provider's, with the countries TMDB's tv
// record adds: TheTVDB lists Germany's alone for Maria the Virgin Witch
// (owner, 2026-11-27) and TMDB eight others.
func TestMergeCertificationsKeepsTheSeriesOwnAndAddsCountries(t *testing.T) {
	own := []metadata.Certification{{Country: "DE", Rating: "16+"}, {Country: "AU", Rating: "MA15+"}}
	extra := []metadata.Certification{
		{Country: "US", Rating: "TV-MA"},
		{Country: "AU", Rating: "MA 15+"},
		{Country: "BR", Rating: "16"},
		{Country: "", Rating: "12"},
		{Country: "CH", Rating: ""},
		{Country: "US", Rating: "TV-14"},
	}
	assert.Equal(t, []metadata.Certification{
		{Country: "DE", Rating: "16+"},
		{Country: "AU", Rating: "MA15+"},
		{Country: "US", Rating: "TV-MA"},
		{Country: "BR", Rating: "16"},
	}, metadata.MergeCertifications(own, extra))
	assert.Equal(t, own, metadata.MergeCertifications(own, nil))
	assert.Equal(t, extra[:1], metadata.MergeCertifications(nil, extra[:1]))
}

// A rating published in another country reads as one on the US television
// scale (owner, 2026-11-27: "translate foreign only ratings like MA 15+ as
// TV-MA"), in both spellings the two providers use, from the first country
// of the table that has one.
func TestTranslateCertificationReadsAForeignRatingOnTheUSScale(t *testing.T) {
	c := func(country, rating string) metadata.Certification {
		return metadata.Certification{Country: country, Rating: rating}
	}
	for _, tc := range []struct {
		name  string
		certs []metadata.Certification
		want  string
		from  metadata.Certification
	}{
		{"the owner's example, as TMDB spells it", []metadata.Certification{c("AU", "MA 15+")}, "TV-MA", c("AU", "MA 15+")},
		{"as TheTVDB spells it", []metadata.Certification{c("AU", "MA15+")}, "TV-MA", c("AU", "MA15+")},
		{"Australia's M is the step below", []metadata.Certification{c("AU", "M")}, "TV-14", c("AU", "M")},
		{"Australia's PG is TV-14, the owner's ruling", []metadata.Certification{c("AU", "PG")}, "TV-14", c("AU", "PG")},
		{"another country's PG keeps its word", []metadata.Certification{c("NZ", "PG")}, "TV-PG", c("NZ", "PG")},
		{"a preschool programme", []metadata.Certification{c("AU", "P")}, "TV-Y", c("AU", "P")},
		{"Germany's 16+, Maria the Virgin Witch on TheTVDB", []metadata.Certification{c("DE", "16+")}, "TV-MA", c("DE", "16+")},
		{"South Korea's 15 is TV-14, not Australia's TV-MA", []metadata.Certification{c("KR", "15")}, "TV-14", c("KR", "15")},
		{"South Korea's 19", []metadata.Certification{c("KR", "19")}, "TV-MA", c("KR", "19")},
		{"Brazil's 14", []metadata.Certification{c("BR", "14")}, "TV-14", c("BR", "14")},
		{"Brazil's all ages", []metadata.Certification{c("BR", "L")}, "TV-G", c("BR", "L")},
		{"Singapore's NC16", []metadata.Certification{c("SG", "NC16")}, "TV-14", c("SG", "NC16")},
		{"Canada's 18+", []metadata.Certification{c("CA", "18+")}, "TV-MA", c("CA", "18+")},
		{"lower case", []metadata.Certification{c("GB", "pg")}, "TV-PG", c("GB", "pg")},
		{
			"Australia is read before the countries after it, whatever the list's order",
			[]metadata.Certification{c("KR", "15"), c("BR", "16"), c("AU", "MA 15+")},
			"TV-MA", c("AU", "MA 15+"),
		},
		{
			"a rating the table does not know leaves the next country to answer",
			[]metadata.Certification{c("AU", "Exempt"), c("KR", "19")},
			"TV-MA", c("KR", "19"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, from, ok := metadata.TranslateCertification(tc.certs)
			assert.True(t, ok)
			assert.Equal(t, c("US", tc.want), got)
			assert.Equal(t, tc.from, from, "the published rating it read, as published")
		})
	}
	for _, certs := range [][]metadata.Certification{
		nil,
		{c("TW", "6+")},     // a country left out: its low ratings sit on TV-14 series
		{c("FR", "-12")},    // a country with no table
		{c("KR", "Exempt")}, // no rating at all
	} {
		_, _, ok := metadata.TranslateCertification(certs)
		assert.False(t, ok, "%v", certs)
	}
}

// A series is rated from what is published first -- the region's, the
// origin country's, the US's -- and from a translation only when none of
// the three has a rating; a film is never translated (PickCertification).
func TestPickSeriesCertificationTranslatesOnlyWhenNothingPublishedAnswers(t *testing.T) {
	au := metadata.Certification{Country: "AU", Rating: "MA 15+"}
	jp := metadata.Certification{Country: "JP", Rating: "R15+"}
	us := metadata.Certification{Country: "US", Rating: "TV-14"}

	assert.Equal(t, us, metadata.PickSeriesCertification([]metadata.Certification{au, us}, "US", "JP"), "the region's")
	assert.Equal(t, jp, metadata.PickSeriesCertification([]metadata.Certification{au, jp}, "US", "JP"), "the origin's, as published")
	assert.Equal(t, us, metadata.PickSeriesCertification([]metadata.Certification{au, us}, "GB", "JP"), "the US's")

	translated := metadata.PickSeriesCertification([]metadata.Certification{au}, "US", "JP")
	assert.Equal(t, metadata.Certification{Country: "US", Rating: "TV-MA"}, translated)
	assert.False(t, metadata.CertificationPublished([]metadata.Certification{au}, translated), "no country published it")
	assert.True(t, metadata.CertificationPublished([]metadata.Certification{au, us}, us))
	assert.False(t, metadata.CertificationPublished([]metadata.Certification{au}, metadata.Certification{}))

	assert.Equal(t, metadata.Certification{}, metadata.PickSeriesCertification(nil, "US", "JP"))
	assert.Equal(t, metadata.Certification{}, metadata.PickCertification([]metadata.Certification{au}, "US", "JP"), "a film stays unrated")
}
