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
	assert.Equal(t, "18", metadata.PickCertification(certs, "GB", "US"))
	assert.Equal(t, "R", metadata.PickCertification(certs, "FR", "US"))
	assert.Equal(t, "18", metadata.PickCertification(certs[:1], "FR", "GB"), "a UK-only film")
	assert.Equal(t, "R", metadata.PickCertification(certs[1:], "FR", "GB"), "US last")
	assert.Equal(t, "", metadata.PickCertification(nil, "US", "US"))
}
