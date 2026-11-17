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

package tvdb_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/trailers"
)

// TheTVDB names a series' trailers by their pages' addresses and a
// three-letter language (recorded for series 467005); each is answered
// with its site, its id there and the two-letter language, and one can be
// picked. A series with none, a movie and an item without a TVDB id answer
// none.
func TestTrailersAreTheSeriesPagesByTheirSiteAndID(t *testing.T) {
	c := recordedTVDB(t)
	got, err := c.Trailers(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyTVDB: "467005"})
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, trailers.Video{Site: "YouTube", Key: "DIeeesNOthA", Name: "Trailer", Type: "Trailer", Language: "ja", Provider: "tvdb"}, got[0])
	picked, ok := trailers.Pick(got, "en")
	require.True(t, ok)
	assert.Equal(t, "https://www.youtube.com/watch?v="+picked.Key, picked.URL())

	none, err := c.Trailers(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyTVDB: "78804"})
	require.NoError(t, err)
	assert.Empty(t, none)
	none, err = c.Trailers(context.Background(), commonv1.MediaKindMovie, metadata.ExternalIDs{metadata.KeyTVDB: "467005"})
	require.NoError(t, err)
	assert.Empty(t, none)
	none, err = c.Trailers(context.Background(), commonv1.MediaKindSeries, metadata.ExternalIDs{metadata.KeyTMDB: "1"})
	require.NoError(t, err)
	assert.Empty(t, none)
}
