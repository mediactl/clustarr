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

package quality_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
)

// mustLookupQuality is a test-only helper (not a production symbol) that
// resolves a Definition's Quality by kind and name, failing the test if it
// does not exist.
func mustLookupQuality(t *testing.T, kind, name string) common.Quality {
	t.Helper()
	d, ok := quality.Lookup(kind, name)
	require.True(t, ok, "Lookup(%q, %q)", kind, name)
	return d.Quality
}

// sizeDelta tolerates the 1-byte float64 rounding difference between this
// test's compile-time constant arithmetic (arbitrary precision until the
// final int64 conversion) and SizeLimits' runtime float64 multiplication
// (each operation rounds to float64) -- both are correct, they just do not
// always land on the identical byte at MB*minutes scale.
const sizeDelta = 1.0

func TestSizeLimitsAppliesTheTRaSHMovieTable(t *testing.T) {
	p := quality.Profile{Sizes: quality.MovieSizeTable()}
	// Bluray-1080p: min 50.8, max 2000 MB/min (§2.2 table), 110 min runtime.
	min, max := quality.SizeLimits(p, mustLookupQuality(t, "video", "Bluray-1080p"), 110)
	require.InDelta(t, 50.8*1024*1024*110, min, sizeDelta)
	require.InDelta(t, 2000*1024*1024*110, max, sizeDelta)
}

func TestSizeLimitsAppliesTheTRaSHSeriesTable(t *testing.T) {
	p := quality.Profile{Sizes: quality.SeriesSizeTable()}
	// Bluray-1080p Remux (Remux-1080p canonical): min 69.1, max 1000 MB/min, 45 min runtime.
	min, max := quality.SizeLimits(p, mustLookupQuality(t, "video", "Remux-1080p"), 45)
	require.InDelta(t, 69.1*1024*1024*45, min, sizeDelta)
	require.InDelta(t, 1000*1024*1024*45, max, sizeDelta)
}

func TestSizeLimitsAppliesTheAnimeFloorOnly(t *testing.T) {
	p := quality.Profile{Sizes: quality.AnimeSizeTable()}
	// anime.json: min 5 for every quality, max effectively unlimited (2000/1000 UI cap).
	min, _ := quality.SizeLimits(p, mustLookupQuality(t, "video", "HDTV-720p"), 24)
	require.InDelta(t, 5*1024*1024*24, min, sizeDelta)
}

func TestSizeLimitsOnUnknownQualityReturnsZero(t *testing.T) {
	p := quality.Profile{Sizes: quality.MovieSizeTable()}
	min, max := quality.SizeLimits(p, common.Quality{Name: "Not-A-Real-Quality"}, 110)
	require.Zero(t, min)
	require.Zero(t, max)
}
