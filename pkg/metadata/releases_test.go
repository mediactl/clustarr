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

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/metadata"
)

func TestDeriveRegionalReleasesPrefersRequestedRegionThenUS(t *testing.T) {
	dates := []metadata.ReleaseDate{
		{Country: "GB", Type: metadata.ReleaseTypeTheatrical, Date: time.Date(2010, 7, 9, 0, 0, 0, 0, time.UTC)},
		{Country: "US", Type: metadata.ReleaseTypeTheatricalLimited, Date: time.Date(2010, 7, 8, 0, 0, 0, 0, time.UTC)},
		{Country: "US", Type: metadata.ReleaseTypeTheatrical, Date: time.Date(2010, 7, 16, 0, 0, 0, 0, time.UTC)},
		{Country: "US", Type: metadata.ReleaseTypeDigital, Date: time.Date(2010, 12, 7, 0, 0, 0, 0, time.UTC)},
		{Country: "US", Type: metadata.ReleaseTypePhysical, Date: time.Date(2010, 12, 7, 0, 0, 0, 0, time.UTC)},
	}

	inCinemas, digital, physical := metadata.DeriveRegionalReleases(dates, "US")

	require.True(t, inCinemas.Equal(time.Date(2010, 7, 8, 0, 0, 0, 0, time.UTC)), "earliest US theatrical date wins over the later wide release and over GB")
	require.True(t, digital.Equal(time.Date(2010, 12, 7, 0, 0, 0, 0, time.UTC)))
	require.True(t, physical.Equal(time.Date(2010, 12, 7, 0, 0, 0, 0, time.UTC)))
}

func TestDeriveRegionalReleasesFallsBackToUSThenFirstCountry(t *testing.T) {
	dates := []metadata.ReleaseDate{
		{Country: "FR", Type: metadata.ReleaseTypeTheatrical, Date: time.Date(2010, 7, 9, 0, 0, 0, 0, time.UTC)},
	}

	inCinemas, _, _ := metadata.DeriveRegionalReleases(dates, "DE")

	require.NotNil(t, inCinemas, "no DE or US entry exists, so the first country present (FR) must be used")
}

func TestDeriveMovieStatus(t *testing.T) {
	now := time.Date(2010, 8, 1, 0, 0, 0, 0, time.UTC)
	inCinemas := time.Date(2010, 7, 16, 0, 0, 0, 0, time.UTC) // 16 days before now
	oldCinemas := time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC) // well past 90 days
	future := time.Date(2010, 12, 25, 0, 0, 0, 0, time.UTC)
	digital := time.Date(2010, 7, 20, 0, 0, 0, 0, time.UTC)

	require.Equal(t, metadata.MovieStatusTBA, metadata.DeriveMovieStatus(nil, nil, nil, now))
	require.Equal(t, metadata.MovieStatusAnnounced, metadata.DeriveMovieStatus(&future, nil, nil, now))
	require.Equal(t, metadata.MovieStatusInCinemas, metadata.DeriveMovieStatus(&inCinemas, nil, nil, now))
	require.Equal(t, metadata.MovieStatusReleased, metadata.DeriveMovieStatus(&oldCinemas, nil, nil, now))
	require.Equal(t, metadata.MovieStatusReleased, metadata.DeriveMovieStatus(&inCinemas, &digital, nil, now), "a reached digital date is released even inside the 90-day cinema window")
}

func TestDeriveSecondaryYear(t *testing.T) {
	d := func(country string, typ metadata.ReleaseType, y int, m time.Month, day int) metadata.ReleaseDate {
		return metadata.ReleaseDate{Country: country, Type: typ, Date: time.Date(y, m, day, 0, 0, 0, 0, time.UTC)}
	}
	tests := []struct {
		name  string
		dates []metadata.ReleaseDate
		year  int32
		want  int32
	}{
		{"no dates", nil, 2020, 0},
		{"no premiere at all", []metadata.ReleaseDate{d("US", metadata.ReleaseTypeTheatrical, 2021, 1, 8)}, 2021, 0},
		{"premiere in the primary year", []metadata.ReleaseDate{d("US", metadata.ReleaseTypePremiere, 2021, 1, 2)}, 2021, 0},
		{
			"festival premiere a year before general release",
			[]metadata.ReleaseDate{
				d("US", metadata.ReleaseTypeTheatrical, 2020, 2, 14),
				d("CA", metadata.ReleaseTypePremiere, 2019, 9, 7),
			},
			2020, 2019,
		},
		{
			"earliest premiere across every country wins",
			[]metadata.ReleaseDate{
				d("FR", metadata.ReleaseTypePremiere, 2019, 5, 20),
				d("US", metadata.ReleaseTypePremiere, 2018, 9, 1),
			},
			2019, 2018,
		},
		{
			"a later-year digital release is not a secondary year",
			[]metadata.ReleaseDate{d("US", metadata.ReleaseTypeDigital, 2022, 3, 1)},
			2021, 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, metadata.DeriveSecondaryYear(tt.dates, tt.year))
		})
	}
}

func TestDeriveSecondaryYearTakesTheYearInUTC(t *testing.T) {
	// 1 January 00:30 UTC is still 31 December on any host west of UTC;
	// CLAUDE.md's metav1.Time gotcha is the same trap.
	west := time.FixedZone("UTC-8", -8*3600)
	premiere := time.Date(2020, 1, 1, 0, 30, 0, 0, time.UTC).In(west)
	dates := []metadata.ReleaseDate{{Country: "US", Type: metadata.ReleaseTypePremiere, Date: premiere}}

	require.Equal(t, int32(2020), metadata.DeriveSecondaryYear(dates, 2021))
	require.Equal(t, int32(0), metadata.DeriveSecondaryYear(dates, 2020))
}
