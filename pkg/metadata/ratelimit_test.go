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

	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/mediactl/clustarr/pkg/metadata"
)

func TestDefaultLimitsMatchTheDocumentedProviderLimits(t *testing.T) {
	l := metadata.DefaultLimits()

	// docs/research/metadata.md §4.5 / §2.1-2.6.
	require.Equal(t, rate.Limit(30), l.TMDB, "TMDB: ~40 rps envelope, plan for ~30")
	require.Equal(t, 40, l.TMDBBurst)
	require.Equal(t, rate.Limit(1), l.MusicBrainz, "MusicBrainz: 1 request/second per IP, hard limit")
	require.Equal(t, 1, l.MusicBrainzBurst)
	require.InDelta(t, float64(200)/3600, float64(l.ComicVine), 0.0001, "ComicVine: 200 requests per resource per hour")
}

func TestNewLimiterUsesTheGivenRateAndBurst(t *testing.T) {
	limiter := metadata.NewLimiter(rate.Limit(1), 1)

	require.Equal(t, rate.Limit(1), limiter.Limit())
	require.Equal(t, 1, limiter.Burst())
}
