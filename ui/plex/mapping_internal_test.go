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
	"testing"

	"github.com/stretchr/testify/require"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// TestRatingsFiltersToTheFourPlexSources is ruling R4 (spec §D.5):
// metacritic, trakt and letterboxd never reach Plex, in a fixed
// imdb/tmdb/critic/audience order regardless of input order.
func TestRatingsFiltersToTheFourPlexSources(t *testing.T) {
	in := []catalogv1.Rating{
		{Source: catalogv1.RatingSourceLetterboxd, ValueCentis: 410},
		{Source: catalogv1.RatingSourceRTAudience, ValueCentis: 8800},
		{Source: catalogv1.RatingSourceMetacritic, ValueCentis: 7500},
		{Source: catalogv1.RatingSourceTMDB, ValueCentis: 831},
		{Source: catalogv1.RatingSourceTrakt, ValueCentis: 780},
		{Source: catalogv1.RatingSourceRTCritic, ValueCentis: 9200},
		{Source: catalogv1.RatingSourceIMDb, ValueCentis: 870},
	}

	got := ratings(in)
	require.Equal(t, []RatingObj{
		{Image: ratingImageIMDb, Type: "audience", Value: 8.7},
		{Image: ratingImageTMDB, Type: "audience", Value: 8.3},
		{Image: ratingImageRTCrit, Type: "critic", Value: 9.2},
		{Image: ratingImageRTAud, Type: "audience", Value: 8.8},
	}, got)
}

// TestRatingsRoundsToOneDecimal falsifies a naive pass-through: 831/100.0 is
// 8.31, which must round to 8.3, not truncate or carry the extra digit into
// the response.
func TestRatingsRoundsToOneDecimal(t *testing.T) {
	got := ratings([]catalogv1.Rating{{Source: catalogv1.RatingSourceTMDB, ValueCentis: 831}})
	require.Len(t, got, 1)
	require.InDelta(t, 8.3, got[0].Value, 0.0001)
}

// TestRatingsEmptyForNoAcceptedSource proves an item with only the
// overlay-only sources sends Plex nothing, not a zero-value Rating.
func TestRatingsEmptyForNoAcceptedSource(t *testing.T) {
	got := ratings([]catalogv1.Rating{{Source: catalogv1.RatingSourceMetacritic, ValueCentis: 7500}})
	require.Empty(t, got)
}

// TestGuidRefsFixedOrder proves the Guid[] order is imdb, tmdb, tvdb
// regardless of map iteration order, and that an unsupported provider
// (research §5.2: "internally supported providers" are only these three)
// is left out.
func TestGuidRefsFixedOrder(t *testing.T) {
	got := guidRefs(map[string]string{
		"tvdb":   "298762",
		"tvmaze": "12345",
		"imdb":   "tt0468569",
		"tmdb":   "424680",
	})
	require.Equal(t, []GuidRef{
		{ID: "imdb://tt0468569"},
		{ID: "tmdb://424680"},
		{ID: "tvdb://298762"},
	}, got)
}

// TestRoundOneDecimalWholeNumber proves a value that rounds to a whole
// number is still a valid float (encoding/json then marshals it without a
// trailing zero, e.g. 8 rather than 8.0 -- there is no JSON distinction
// between them).
func TestRoundOneDecimalWholeNumber(t *testing.T) {
	require.InDelta(t, 8.0, roundOneDecimal(8.0), 0.0001)
	require.InDelta(t, 8.7, roundOneDecimal(8.6501), 0.0001)
}
