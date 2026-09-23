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
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

type chainResolver struct {
	name string
	add  func(pkgmetadata.ExternalIDs) pkgmetadata.ExternalIDs
	err  error
	seen *[]pkgmetadata.ExternalIDs
}

func (r chainResolver) Name() string                           { return r.name }
func (r chainResolver) Capabilities() pkgmetadata.Capabilities { return pkgmetadata.Capabilities{} }
func (r chainResolver) Resolve(_ context.Context, _ commonv1.MediaKind, ids pkgmetadata.ExternalIDs) (pkgmetadata.ExternalIDs, error) {
	if r.seen != nil {
		*r.seen = append(*r.seen, ids.Merge(nil))
	}
	if r.err != nil {
		return nil, r.err
	}
	return r.add(ids), nil
}

type stubArtworkProvider struct {
	name   string
	images []pkgmetadata.Image
	err    error
}

func (a stubArtworkProvider) Name() string { return a.name }
func (a stubArtworkProvider) Capabilities() pkgmetadata.Capabilities {
	return pkgmetadata.Capabilities{}
}

func (a stubArtworkProvider) Artwork(context.Context, commonv1.MediaKind, pkgmetadata.ExternalIDs) ([]pkgmetadata.Image, error) {
	return a.images, a.err
}

func TestEnrichAddsCrosswalkIDsWithoutOverridingAnyThePrimaryProviderGave(t *testing.T) {
	var seen []pkgmetadata.ExternalIDs
	reg := &pkgmetadata.Registry{Resolvers: []pkgmetadata.IDResolver{
		chainResolver{name: "kitsu", seen: &seen, add: func(pkgmetadata.ExternalIDs) pkgmetadata.ExternalIDs {
			return pkgmetadata.ExternalIDs{"kitsu": "1555", "mal": "1735", "tvdb": "999"}
		}},
		chainResolver{name: "down", err: errors.New("anilist: 502")},
		chainResolver{name: "anilist", seen: &seen, add: func(ids pkgmetadata.ExternalIDs) pkgmetadata.ExternalIDs {
			return pkgmetadata.ExternalIDs{"anilist": ids["mal"]}
		}},
	}}
	doc := &pkgmetadata.Series{IDs: pkgmetadata.ExternalIDs{"tvdb": "79824", "imdb": "tt0988824"}}

	enrich(context.Background(), reg, commonv1.MediaKindSeries, pkgmetadata.ExternalIDs{"tvdb": "79824"}, nil, doc)

	require.Equal(t, pkgmetadata.ExternalIDs{
		"tvdb": "79824", "imdb": "tt0988824", "kitsu": "1555", "mal": "1735", "anilist": "1735",
	}, doc.IDs, "a resolver adds keys; the provider's own tvdb id is never replaced; a failing resolver adds nothing")
	require.Equal(t, "1735", seen[1]["mal"], "each resolver sees what the ones before it added")
}

func TestEnrichKeepsAnIDAResolverCouldNotSupplyThisTime(t *testing.T) {
	reg := &pkgmetadata.Registry{Resolvers: []pkgmetadata.IDResolver{
		chainResolver{name: "kitsu", err: errors.New("kitsu: unexpected status 502")},
		chainResolver{name: "fresh", add: func(pkgmetadata.ExternalIDs) pkgmetadata.ExternalIDs {
			return pkgmetadata.ExternalIDs{"anidb": "4880-corrected"}
		}},
	}}
	doc := &pkgmetadata.Series{IDs: pkgmetadata.ExternalIDs{"tvdb": "79824"}}
	known := pkgmetadata.ExternalIDs{"tvdb": "79824", "kitsu": "1555", "mal": "1735", "anidb": "4880"}

	enrich(context.Background(), reg, commonv1.MediaKindSeries, nil, known, doc)

	require.Equal(t, "1555", doc.IDs["kitsu"], "a blip must not release an id status.metadata already carried")
	require.Equal(t, "1735", doc.IDs["mal"])
	require.Equal(t, "4880-corrected", doc.IDs["anidb"], "a fresh crosswalk beats a remembered one")
}

func TestEnrichLeavesTheSpecsNonIDKeysOut(t *testing.T) {
	reg := &pkgmetadata.Registry{Resolvers: []pkgmetadata.IDResolver{chainResolver{name: "none", add: func(pkgmetadata.ExternalIDs) pkgmetadata.ExternalIDs { return nil }}}}
	doc := &pkgmetadata.Audiobook{IDs: pkgmetadata.ExternalIDs{"asin": "B0036I54I6"}}

	enrich(context.Background(), reg, commonv1.MediaKindAudiobook, pkgmetadata.ExternalIDs{"asin": "B0036I54I6", "region": "uk"}, nil, doc)

	require.Equal(t, pkgmetadata.ExternalIDs{"asin": "B0036I54I6"}, doc.IDs, "region is a parameter, not an id")
}

func TestEnrichAppendsArtworkAfterTheProvidersOwnImages(t *testing.T) {
	season := int32(3)
	reg := &pkgmetadata.Registry{Artwork: []pkgmetadata.ArtworkProvider{
		stubArtworkProvider{name: "fanart", images: []pkgmetadata.Image{
			{Type: pkgmetadata.ImageTypeLogo, URL: "https://assets.fanart.tv/logo.png"},
			{Type: pkgmetadata.ImageTypePoster, URL: "https://image.tmdb.org/poster.jpg"},
			{Type: pkgmetadata.ImageTypePoster, URL: "https://assets.fanart.tv/s3.jpg", Season: &season},
		}},
		stubArtworkProvider{name: "down", err: errors.New("fanart: 503")},
		stubArtworkProvider{name: "coverart", err: pkgmetadata.ErrUnsupported},
		stubArtworkProvider{name: "second", images: []pkgmetadata.Image{{Type: pkgmetadata.ImageTypeClearart, URL: "https://example/clear.png"}}},
	}}
	doc := &pkgmetadata.Series{IDs: pkgmetadata.ExternalIDs{"tvdb": "79824"}, Images: []pkgmetadata.Image{
		{Type: pkgmetadata.ImageTypePoster, URL: "https://image.tmdb.org/poster.jpg"},
	}}

	enrich(context.Background(), reg, commonv1.MediaKindSeries, nil, nil, doc)

	require.Equal(t, []pkgmetadata.Image{
		{Type: pkgmetadata.ImageTypePoster, URL: "https://image.tmdb.org/poster.jpg"},
		{Type: pkgmetadata.ImageTypeLogo, URL: "https://assets.fanart.tv/logo.png"},
		{Type: pkgmetadata.ImageTypeClearart, URL: "https://example/clear.png"},
	}, doc.Images, "own images first, a repeated URL once, no season-scoped image, failing providers skipped")
}

func TestEnrichWithNothingRegisteredLeavesTheDocumentAlone(t *testing.T) {
	doc := &pkgmetadata.Movie{IDs: pkgmetadata.ExternalIDs{"tmdb": "603"}}
	enrich(context.Background(), &pkgmetadata.Registry{}, commonv1.MediaKindMovie, pkgmetadata.ExternalIDs{"tmdb": "603", "region": "us"}, pkgmetadata.ExternalIDs{"imdb": "tt0133093"}, doc)
	require.Equal(t, pkgmetadata.ExternalIDs{"tmdb": "603"}, doc.IDs)
}
