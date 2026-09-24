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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// stubRatingsProvider is a fake metadata.RatingsProvider: it declares
// sources fixed at construction, returns ratings fixed at construction (or
// err), and counts how many times Ratings was actually called -- the
// enrichRatings tests below assert on *calls directly, proving "one call
// per provider" and "a provider not declaring a source is never asked for
// it" (an untouched pointer means zero calls).
type stubRatingsProvider struct {
	name     string
	declared []string
	ratings  pkgmetadata.Ratings
	err      error
	calls    *int
}

func (p stubRatingsProvider) Name() string { return p.name }
func (p stubRatingsProvider) Capabilities() pkgmetadata.Capabilities {
	return pkgmetadata.Capabilities{}
}

func (p stubRatingsProvider) RatingSources(commonv1.MediaKind) []string { return p.declared }

func (p stubRatingsProvider) Ratings(context.Context, commonv1.MediaKind, pkgmetadata.ExternalIDs) (pkgmetadata.Ratings, error) {
	if p.calls != nil {
		*p.calls++
	}
	if p.err != nil {
		return nil, p.err
	}
	return p.ratings, nil
}

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

// TestEnrichRatingsHigherPriorityWins is spec §C.2: a lower-priority
// provider that also declares "imdb" must never overwrite the value the
// first, higher-priority provider already filled -- and it is still asked
// for the source it alone declares ("trakt").
func TestEnrichRatingsHigherPriorityWins(t *testing.T) {
	var firstCalls, secondCalls int
	reg := &pkgmetadata.Registry{Ratings: []pkgmetadata.RatingsProvider{
		stubRatingsProvider{
			name: "first", declared: []string{"imdb"}, calls: &firstCalls,
			ratings: pkgmetadata.Ratings{"imdb": {Source: "imdb", ValueCentis: 900, Votes: 100}},
		},
		stubRatingsProvider{
			name: "second", declared: []string{"imdb", "trakt"}, calls: &secondCalls,
			ratings: pkgmetadata.Ratings{"imdb": {Source: "imdb", ValueCentis: 100, Votes: 1}, "trakt": {Source: "trakt", ValueCentis: 800, Votes: 50}},
		},
	}}

	got := enrichRatings(context.Background(), reg, commonv1.MediaKindMovie, pkgmetadata.ExternalIDs{"tmdb": "603"}, nil)

	require.Equal(t, 1, firstCalls)
	require.Equal(t, 1, secondCalls, "second is still called for trakt, which only it declares")
	byS := ratingsBySource(got)
	require.Equal(t, int32(900), byS[catalogv1alpha1.RatingSourceIMDb].ValueCentis, "first's imdb value wins, not second's")
	require.Equal(t, int32(800), byS[catalogv1alpha1.RatingSourceTrakt].ValueCentis)
}

// TestEnrichRatingsFailingProviderBlanksNothing is spec §C.2: a provider
// error is logged at warn and the loop continues, and it must never erase
// what a previous provider (this pass, or a past one via prior) already
// filled.
func TestEnrichRatingsFailingProviderBlanksNothing(t *testing.T) {
	var failCalls int
	reg := &pkgmetadata.Registry{Ratings: []pkgmetadata.RatingsProvider{
		stubRatingsProvider{
			name: "good", declared: []string{"imdb"},
			ratings: pkgmetadata.Ratings{"imdb": {Source: "imdb", ValueCentis: 833, Votes: 900}},
		},
		stubRatingsProvider{name: "down", declared: []string{"trakt"}, calls: &failCalls, err: errors.New("mdblist: unexpected status 502")},
	}}

	got := enrichRatings(context.Background(), reg, commonv1.MediaKindMovie, pkgmetadata.ExternalIDs{"tmdb": "603"}, nil)

	require.Equal(t, 1, failCalls)
	byS := ratingsBySource(got)
	require.Equal(t, int32(833), byS[catalogv1alpha1.RatingSourceIMDb].ValueCentis, "a later provider failing must not blank an earlier one's fill")
	_, hasTrakt := byS[catalogv1alpha1.RatingSourceTrakt]
	require.False(t, hasTrakt, "the failing provider contributed nothing, not a zero-value entry")
}

// TestEnrichRatingsNeverAsksAProviderForASourceItDoesNotDeclare proves a
// provider whose declared sources are already fully filled by a
// higher-priority provider is skipped without a call at all -- "a provider
// not declaring a source is never asked for it" extended to "and not asked
// again once nothing it declares is still needed".
func TestEnrichRatingsNeverAsksAProviderForASourceItDoesNotDeclare(t *testing.T) {
	var neverCalls int
	reg := &pkgmetadata.Registry{Ratings: []pkgmetadata.RatingsProvider{
		stubRatingsProvider{
			name: "first", declared: []string{"imdb"},
			ratings: pkgmetadata.Ratings{"imdb": {Source: "imdb", ValueCentis: 900, Votes: 100}},
		},
		stubRatingsProvider{
			name: "redundant", declared: []string{"imdb"}, calls: &neverCalls,
			ratings: pkgmetadata.Ratings{"imdb": {Source: "imdb", ValueCentis: 100, Votes: 1}},
		},
	}}

	enrichRatings(context.Background(), reg, commonv1.MediaKindMovie, pkgmetadata.ExternalIDs{"tmdb": "603"}, nil)

	require.Equal(t, 0, neverCalls, "everything redundant declares was already filled by first; it must never be asked")
}

// TestEnrichRatingsCarriesForwardOnTotalFailure is spec §C.2: when every
// provider fails (or declares nothing), whatever prior already had for a
// source survives unchanged -- a transient outage must not strip a badge a
// past refresh already earned.
func TestEnrichRatingsCarriesForwardOnTotalFailure(t *testing.T) {
	reg := &pkgmetadata.Registry{Ratings: []pkgmetadata.RatingsProvider{
		stubRatingsProvider{name: "down", declared: []string{"imdb", "tmdb"}, err: errors.New("tmdb: unexpected status 503")},
	}}
	prior := []catalogv1alpha1.Rating{
		{Source: catalogv1alpha1.RatingSourceIMDb, ValueCentis: 833, Votes: 900},
		{Source: catalogv1alpha1.RatingSourceTMDB, ValueCentis: 810, Votes: 500},
	}

	got := enrichRatings(context.Background(), reg, commonv1.MediaKindMovie, pkgmetadata.ExternalIDs{"tmdb": "603"}, prior)

	require.ElementsMatch(t, prior, got)
}

// TestEnrichRatingsOmitsAZeroValueWithZeroVotes is Review Focus 5: a source
// that answers with ValueCentis 0 and Votes 0 -- "nothing to report", not a
// genuine 0/10 -- is dropped rather than copied into status.metadata.ratings.
func TestEnrichRatingsOmitsAZeroValueWithZeroVotes(t *testing.T) {
	reg := &pkgmetadata.Registry{Ratings: []pkgmetadata.RatingsProvider{
		stubRatingsProvider{
			name: "empty", declared: []string{"imdb"},
			ratings: pkgmetadata.Ratings{"imdb": {Source: "imdb", ValueCentis: 0, Votes: 0}},
		},
	}}

	got := enrichRatings(context.Background(), reg, commonv1.MediaKindMovie, pkgmetadata.ExternalIDs{"tmdb": "603"}, nil)

	require.Empty(t, got, "a zero value with zero votes must be omitted, not sent as an empty rating")
}

// TestEnrichRatingsWithNoRegisteredProvidersReturnsPriorUnchanged mirrors
// TestEnrichWithNothingRegisteredLeavesTheDocumentAlone for ratings: an
// empty Registry.Ratings is a no-op over prior.
func TestEnrichRatingsWithNoRegisteredProvidersReturnsPriorUnchanged(t *testing.T) {
	prior := []catalogv1alpha1.Rating{{Source: catalogv1alpha1.RatingSourceIMDb, ValueCentis: 833, Votes: 900}}
	got := enrichRatings(context.Background(), &pkgmetadata.Registry{}, commonv1.MediaKindMovie, pkgmetadata.ExternalIDs{"tmdb": "603"}, prior)
	require.Equal(t, prior, got)
}

func ratingsBySource(ratings []catalogv1alpha1.Rating) map[catalogv1alpha1.RatingSource]catalogv1alpha1.Rating {
	out := make(map[catalogv1alpha1.RatingSource]catalogv1alpha1.Rating, len(ratings))
	for _, r := range ratings {
		out[r.Source] = r
	}
	return out
}
