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

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

type stubAudiobookSearcher struct {
	name   string
	hits   []pkgmetadata.SearchHit
	err    error
	region *string
}

func (s stubAudiobookSearcher) Name() string { return s.name }
func (s stubAudiobookSearcher) Capabilities() pkgmetadata.Capabilities {
	return pkgmetadata.Capabilities{Search: true}
}
func (s stubAudiobookSearcher) SearchAudiobooks(_ context.Context, _ string, region string) ([]pkgmetadata.SearchHit, error) {
	if s.region != nil {
		*s.region = region
	}
	return s.hits, s.err
}

// An audiobook search goes to the AudiobookSearchers in order, in the
// request's region (2026-11-10 design §5.6); with none configured the answer
// names the missing provider, not an unsupported kind.
func TestSearchAudiobooksThroughTheAudibleCatalog(t *testing.T) {
	var region string
	reg := &pkgmetadata.Registry{AudiobookSearchers: []pkgmetadata.AudiobookSearcher{
		stubAudiobookSearcher{name: "down", err: errors.New("audible: 503")},
		stubAudiobookSearcher{name: "audible", region: &region, hits: []pkgmetadata.SearchHit{{IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyASIN: "B01NH0D987"}, Title: "The Left Hand of Darkness", Year: 2016}}},
	}}
	resp := search(context.Background(), reg, schema.MetadataRequest{Kind: commonv1.MediaKindAudiobook, Text: "left hand of darkness", Region: "uk"})
	require.Empty(t, resp.Error)
	require.Equal(t, "audible", resp.Provider)
	require.Len(t, resp.Results, 1)
	var hit pkgmetadata.SearchHit
	require.NoError(t, json.Unmarshal(resp.Results[0], &hit))
	require.Equal(t, "B01NH0D987", hit.IDs[pkgmetadata.KeyASIN])
	require.Equal(t, "uk", region)

	none := search(context.Background(), &pkgmetadata.Registry{}, schema.MetadataRequest{Kind: commonv1.MediaKindAudiobook, Text: "x"})
	require.NotEmpty(t, none.Error)
	require.NotContains(t, none.Error, "does not support kind", "audiobook is a searchable kind; the provider is what is missing")
}
