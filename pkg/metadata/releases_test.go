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
