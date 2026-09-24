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

package series_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/series"
	"github.com/mediactl/clustarr/pkg/naming"
)

// TestPath's expected folder literal is the real Jellyfin preset output for
// SeriesFolder, confirmed against test/data/naming/dialects.json's golden
// ("The Series Title! (2010) [tvdbid-153021]") rather than guessed.
func TestPath(t *testing.T) {
	eng := naming.NewEngine(naming.Config{Dialect: naming.DialectJellyfin})
	ctx := naming.Context{Kind: commonv1.MediaKindSeries, SeriesTitle: "The Series Title!", SeriesYear: 2010, TvdbID: "153021"}

	got, err := series.Path("/data/media/tv", nil, eng, ctx)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/tv/The Series Title! (2010) [tvdbid-153021]", got)

	override := "The Series Title (Extended)"
	got, err = series.Path("/data/media/tv", &override, eng, ctx)
	require.NoError(t, err)
	assert.Equal(t, "/data/media/tv/The Series Title (Extended)", got)
}
