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
	"strings"
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

// stubPageProvider is a fake metadata.PageProvider: it answers the link
// fixed at construction (or err) and records the ids it was asked with, so
// a test can prove what addressed the page.
type stubPageProvider struct {
	name  string
	link  pkgmetadata.Link
	err   error
	calls *int
	seen  *[]pkgmetadata.ExternalIDs
}

func (p stubPageProvider) Name() string                           { return p.name }
func (p stubPageProvider) Capabilities() pkgmetadata.Capabilities { return pkgmetadata.Capabilities{} }
func (p stubPageProvider) Page(_ context.Context, _ commonv1.MediaKind, ids pkgmetadata.ExternalIDs) (pkgmetadata.Link, error) {
	if p.calls != nil {
		*p.calls++
	}
	if p.seen != nil {
		*p.seen = append(*p.seen, ids.Merge(nil))
	}
	if p.err != nil {
		return pkgmetadata.Link{}, p.err
	}
	return p.link, nil
}

// A supplementary provider's page (2026-11-10 design §9.3: Hardcover's for
// an Open Library-keyed Author or Book; §9.4: Metron's for a ComicVine-keyed
// Comic) is asked for after the primary lookup and appended to the
// document's links as its own type; a document already carrying a page of
// that type -- a Link of the provider's Name(), the contract's type -- is
// not given a second and the provider is not asked at all (the ask would
// cost a request per cache miss for an answer thrown away), a provider that
// fails adds nothing and stops nothing, an empty answer adds nothing, a
// Book is addressed by its first edition's ISBN-13 beside its own ids, as
// its ratings are, a Comic by the ids the crosswalk settled on, and no
// other kind is asked.
func TestEnrichAppendsASupplementaryProvidersPageLink(t *testing.T) {
	page := pkgmetadata.Link{Type: "hardcover", URL: "https://hardcover.app/authors/jane-austen"}
	downCalls, hcCalls := 0, 0
	var seen []pkgmetadata.ExternalIDs
	reg := &pkgmetadata.Registry{Pages: []pkgmetadata.PageProvider{
		stubPageProvider{name: "down", err: errors.New("hardcover: 503"), calls: &downCalls},
		stubPageProvider{name: "hardcover", link: page, calls: &hcCalls, seen: &seen},
	}}
	olLink := pkgmetadata.Link{Type: "wikipedia", URL: "https://en.wikipedia.org/wiki/Jane_Austen"}

	author := &pkgmetadata.Author{IDs: pkgmetadata.ExternalIDs{"openlibrary": "OL21594A"}, Links: []pkgmetadata.Link{olLink}}
	enrich(context.Background(), reg, commonv1.MediaKindAuthor, pkgmetadata.ExternalIDs{"hardcover": "51942"}, nil, author)
	require.Equal(t, []pkgmetadata.Link{olLink, page}, author.Links, "the page is appended after the primary's own links")
	require.Equal(t, 1, downCalls, "the failing provider is passed over")
	require.Equal(t, 1, hcCalls)
	require.Equal(t, []pkgmetadata.ExternalIDs{{"openlibrary": "OL21594A", "hardcover": "51942"}}, seen, "asked with the document's ids and the spec's")

	downCalls, hcCalls, seen = 0, 0, nil
	own := &pkgmetadata.Author{IDs: pkgmetadata.ExternalIDs{"hardcover": "51942"}, Links: []pkgmetadata.Link{{Type: "hardcover", URL: "https://hardcover.app/authors/other"}}}
	enrich(context.Background(), reg, commonv1.MediaKindAuthor, nil, nil, own)
	require.Equal(t, []pkgmetadata.Link{{Type: "hardcover", URL: "https://hardcover.app/authors/other"}}, own.Links, "a page of the type already carried is not doubled, and the primary's wins")
	require.Zero(t, hcCalls, "the provider whose page the document already carries is not asked")
	require.Equal(t, 1, downCalls, "the one whose page it lacks still is")

	downCalls, hcCalls, seen = 0, 0, nil
	book := &pkgmetadata.Book{
		IDs:      pkgmetadata.ExternalIDs{"openlibrary": "OL27448W"},
		Editions: []pkgmetadata.Edition{{IDs: pkgmetadata.ExternalIDs{"openlibrary": "OL1M"}}, {IDs: pkgmetadata.ExternalIDs{pkgmetadata.KeyISBN13: "9781416971702"}}},
	}
	enrich(context.Background(), reg, commonv1.MediaKindBook, nil, nil, book)
	require.Equal(t, []pkgmetadata.Link{page}, book.Links)
	require.Equal(t, []pkgmetadata.ExternalIDs{{"openlibrary": "OL27448W", pkgmetadata.KeyISBN13: "9781416971702"}}, seen, "a Book is addressed by its first edition's ISBN-13 beside its own ids")
	require.Equal(t, pkgmetadata.ExternalIDs{"openlibrary": "OL27448W"}, book.IDs, "the document's own ids are not touched")

	empty := &pkgmetadata.Author{IDs: pkgmetadata.ExternalIDs{"openlibrary": "OL1A"}}
	enrich(context.Background(), &pkgmetadata.Registry{Pages: []pkgmetadata.PageProvider{stubPageProvider{name: "nothing"}}}, commonv1.MediaKindAuthor, nil, nil, empty)
	require.Empty(t, empty.Links, "an empty answer -- an item the provider cannot address -- adds nothing")

	failing := &pkgmetadata.Author{IDs: pkgmetadata.ExternalIDs{"openlibrary": "OL1A"}, Links: []pkgmetadata.Link{olLink}}
	enrich(context.Background(), &pkgmetadata.Registry{Pages: []pkgmetadata.PageProvider{stubPageProvider{name: "down", err: errors.New("hardcover: 503")}}}, commonv1.MediaKindAuthor, nil, nil, failing)
	require.Equal(t, []pkgmetadata.Link{olLink}, failing.Links, "a failing provider adds nothing and loses nothing")

	// A Comic: asked with the ids the document and the crosswalk settled on
	// (a ComicVine-sourced comic's metron id comes from Metron's Resolve),
	// the page appended after the primary's own links; a Metron-sourced
	// comic, whose document carries the page, is not asked.
	metronPage := pkgmetadata.Link{Type: "metron", URL: "https://metron.cloud/series/death-of-the-inhumans-2018/"}
	mCalls := 0
	var mSeen []pkgmetadata.ExternalIDs
	comics := &pkgmetadata.Registry{
		Resolvers: []pkgmetadata.IDResolver{chainResolver{name: "metron", add: func(pkgmetadata.ExternalIDs) pkgmetadata.ExternalIDs {
			return pkgmetadata.ExternalIDs{"metron": "1"}
		}}},
		Pages: []pkgmetadata.PageProvider{stubPageProvider{name: "metron", link: metronPage, calls: &mCalls, seen: &mSeen}},
	}
	cvLink := pkgmetadata.Link{Type: "comicvine", URL: "https://comicvine.gamespot.com/death-of-the-inhumans/4050-111265/"}
	comic := &pkgmetadata.ComicVolume{IDs: pkgmetadata.ExternalIDs{"comicvine": "4050-111265"}, Links: []pkgmetadata.Link{cvLink}}
	enrich(context.Background(), comics, commonv1.MediaKindComic, pkgmetadata.ExternalIDs{"comicvine": "4050-111265"}, nil, comic)
	require.Equal(t, []pkgmetadata.Link{cvLink, metronPage}, comic.Links, "a Comic is asked for its page, appended after the primary's own links")
	require.Equal(t, []pkgmetadata.ExternalIDs{{"comicvine": "4050-111265", "metron": "1"}}, mSeen, "asked with the crosswalk's metron id beside the source's")

	mCalls, mSeen = 0, nil
	ownComic := &pkgmetadata.ComicVolume{IDs: pkgmetadata.ExternalIDs{"metron": "1"}, Links: []pkgmetadata.Link{metronPage}}
	enrich(context.Background(), comics, commonv1.MediaKindComic, nil, nil, ownComic)
	require.Equal(t, []pkgmetadata.Link{metronPage}, ownComic.Links)
	require.Zero(t, mCalls, "a Metron-sourced comic carries its page already and is not asked")

	movie := &pkgmetadata.Movie{IDs: pkgmetadata.ExternalIDs{"tmdb": "603"}}
	downCalls, hcCalls = 0, 0
	enrich(context.Background(), reg, commonv1.MediaKindMovie, nil, nil, movie)
	require.Zero(t, downCalls+hcCalls, "only an Author, a Book or a Comic is asked for a page")
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

	got := enrichRatings(context.Background(), reg, commonv1.MediaKindMovie, pkgmetadata.ExternalIDs{"tmdb": "603"}, nil, nil)

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

	got := enrichRatings(context.Background(), reg, commonv1.MediaKindMovie, pkgmetadata.ExternalIDs{"tmdb": "603"}, nil, nil)

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

	enrichRatings(context.Background(), reg, commonv1.MediaKindMovie, pkgmetadata.ExternalIDs{"tmdb": "603"}, nil, nil)

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

	got := enrichRatings(context.Background(), reg, commonv1.MediaKindMovie, pkgmetadata.ExternalIDs{"tmdb": "603"}, nil, prior)

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

	got := enrichRatings(context.Background(), reg, commonv1.MediaKindMovie, pkgmetadata.ExternalIDs{"tmdb": "603"}, nil, nil)

	require.Empty(t, got, "a zero value with zero votes must be omitted, not sent as an empty rating")
}

// TestEnrichRatingsWithNoRegisteredProvidersReturnsPriorUnchanged mirrors
// TestEnrichWithNothingRegisteredLeavesTheDocumentAlone for ratings: an
// empty Registry.Ratings is a no-op over prior.
func TestEnrichRatingsWithNoRegisteredProvidersReturnsPriorUnchanged(t *testing.T) {
	prior := []catalogv1alpha1.Rating{{Source: catalogv1alpha1.RatingSourceIMDb, ValueCentis: 833, Votes: 900}}
	got := enrichRatings(context.Background(), &pkgmetadata.Registry{}, commonv1.MediaKindMovie, pkgmetadata.ExternalIDs{"tmdb": "603"}, nil, prior)
	require.Equal(t, prior, got)
}

// TestEnrichRatingsNeverCallsAProviderForASourceTheSeedAlreadyFilled is fix
// round 1's regression guard: seed (the document's own Ratings, from the
// primary fetch that already ran) fills "tmdb" for free, so a provider
// declaring only "tmdb" -- exactly tmdb.Client's own shape -- must never be
// called at all. Before this fix, enrichRatings always found "tmdb"
// unfilled and called the provider regardless of seed, doubling TMDB
// traffic on every refresh (fresh fetch or cache hit) despite the document
// already carrying the answer.
func TestEnrichRatingsNeverCallsAProviderForASourceTheSeedAlreadyFilled(t *testing.T) {
	var calls int
	reg := &pkgmetadata.Registry{Ratings: []pkgmetadata.RatingsProvider{
		stubRatingsProvider{
			name: "tmdb", declared: []string{"tmdb"}, calls: &calls,
			ratings: pkgmetadata.Ratings{"tmdb": {Source: "tmdb", ValueCentis: 100, Votes: 1}},
		},
	}}
	seed := pkgmetadata.Ratings{"tmdb": {Source: "tmdb", ValueCentis: 837, Votes: 36892}}

	got := enrichRatings(context.Background(), reg, commonv1.MediaKindMovie, pkgmetadata.ExternalIDs{"tmdb": "27205"}, seed, nil)

	require.Equal(t, 0, calls, "the seed already answered \"tmdb\"; the provider must never be asked")
	byS := ratingsBySource(got)
	require.Equal(t, int32(837), byS[catalogv1alpha1.RatingSourceTMDB].ValueCentis, "seed's value, not the provider's -- the provider was never even called")
}

// TestEnrichRatingsSeedStillLeavesRoomForASourceItDidNotCover proves seed
// only pre-fills what it actually carries: a provider declaring a source
// seed lacks is still called for that source, and its own declared source
// seed already covered is excluded from need.
func TestEnrichRatingsSeedStillLeavesRoomForASourceItDidNotCover(t *testing.T) {
	var calls int
	reg := &pkgmetadata.Registry{Ratings: []pkgmetadata.RatingsProvider{
		stubRatingsProvider{
			name: "mdblist-stub", declared: []string{"tmdb", "imdb"}, calls: &calls,
			ratings: pkgmetadata.Ratings{
				"tmdb": {Source: "tmdb", ValueCentis: 100, Votes: 1}, // must be ignored: seed already has tmdb
				"imdb": {Source: "imdb", ValueCentis: 833, Votes: 900},
			},
		},
	}}
	seed := pkgmetadata.Ratings{"tmdb": {Source: "tmdb", ValueCentis: 837, Votes: 36892}}

	got := enrichRatings(context.Background(), reg, commonv1.MediaKindMovie, pkgmetadata.ExternalIDs{"tmdb": "27205"}, seed, nil)

	require.Equal(t, 1, calls, "the provider is still called -- it declares imdb, which seed does not cover")
	byS := ratingsBySource(got)
	require.Equal(t, int32(837), byS[catalogv1alpha1.RatingSourceTMDB].ValueCentis, "seed's tmdb value is never overwritten by the provider's result")
	require.Equal(t, int32(833), byS[catalogv1alpha1.RatingSourceIMDb].ValueCentis, "the provider still fills the source seed did not cover")
}

// TestEnrichRatingsSeedZeroValueIsOmittedLikeAProviderResults is Review
// Focus 5 applied to seed too: a zero-value, zero-vote entry in the
// document's own Ratings map is "nothing to report", not a real zero, and
// must not block a registered provider from being asked for that source.
func TestEnrichRatingsSeedZeroValueIsOmittedLikeAProviderResults(t *testing.T) {
	var calls int
	reg := &pkgmetadata.Registry{Ratings: []pkgmetadata.RatingsProvider{
		stubRatingsProvider{
			name: "tmdb", declared: []string{"tmdb"}, calls: &calls,
			ratings: pkgmetadata.Ratings{"tmdb": {Source: "tmdb", ValueCentis: 837, Votes: 36892}},
		},
	}}
	seed := pkgmetadata.Ratings{"tmdb": {Source: "tmdb", ValueCentis: 0, Votes: 0}}

	got := enrichRatings(context.Background(), reg, commonv1.MediaKindMovie, pkgmetadata.ExternalIDs{"tmdb": "27205"}, seed, nil)

	require.Equal(t, 1, calls, "seed's zero-value tmdb entry must not count as filled")
	byS := ratingsBySource(got)
	require.Equal(t, int32(837), byS[catalogv1alpha1.RatingSourceTMDB].ValueCentis, "the provider's real value fills what seed could not")
}

func ratingsBySource(ratings []catalogv1alpha1.Rating) map[catalogv1alpha1.RatingSource]catalogv1alpha1.Rating {
	out := make(map[catalogv1alpha1.RatingSource]catalogv1alpha1.Rating, len(ratings))
	for _, r := range ratings {
		out[r.Source] = r
	}
	return out
}

type stubExtrasProvider struct {
	tagline  string
	keywords []string
	err      error
	asked    *[]pkgmetadata.ExternalIDs
}

func (s stubExtrasProvider) SeriesExtras(_ context.Context, ids pkgmetadata.ExternalIDs) (pkgmetadata.SeriesExtras, error) {
	if s.asked != nil {
		*s.asked = append(*s.asked, ids)
	}
	return pkgmetadata.SeriesExtras{Tagline: s.tagline, Keywords: s.keywords}, s.err
}

// TVDB has no taglines or keywords, so the gateway fills a series' from
// the first extras provider that has either (TMDB's tv record, spec
// 2026-09-30 §3.4), asking with the ids the crosswalk settled on, and
// never over a tagline the primary provider gave.
func TestEnrichFillsASeriesTaglineAndKeywordsFromAnExtrasProvider(t *testing.T) {
	var asked []pkgmetadata.ExternalIDs
	reg := &pkgmetadata.Registry{Extras: []pkgmetadata.SeriesExtrasProvider{
		stubExtrasProvider{err: errors.New("tmdb: 503")},
		stubExtrasProvider{tagline: "Space. For all.", keywords: []string{"time travel", "alien"}, asked: &asked},
	}}
	doc := &pkgmetadata.Series{IDs: pkgmetadata.ExternalIDs{"tvdb": "78804", "tmdb": "57243"}}

	enrich(context.Background(), reg, commonv1.MediaKindSeries, nil, nil, doc)

	require.Equal(t, "Space. For all.", doc.Tagline)
	require.Equal(t, []string{"time travel", "alien"}, doc.Keywords)
	require.Equal(t, []pkgmetadata.ExternalIDs{{"tvdb": "78804", "tmdb": "57243"}}, asked)

	own := &pkgmetadata.Series{IDs: pkgmetadata.ExternalIDs{"tvdb": "78804"}, Tagline: "Its own."}
	enrich(context.Background(), reg, commonv1.MediaKindSeries, nil, nil, own)
	require.Equal(t, "Its own.", own.Tagline)
	require.Equal(t, []string{"time travel", "alien"}, own.Keywords, "the keywords are the provider's even beside the primary's tagline")
}

type stubBiographyProvider struct {
	name  string
	text  string
	err   error
	calls *int
}

func (s stubBiographyProvider) Name() string { return s.name }
func (s stubBiographyProvider) Capabilities() pkgmetadata.Capabilities {
	return pkgmetadata.Capabilities{}
}
func (s stubBiographyProvider) Biography(context.Context, []pkgmetadata.Link) (string, string, error) {
	if s.calls != nil {
		*s.calls++
	}
	if s.err != nil {
		return "", "", s.err
	}
	return s.text, s.name, nil
}

// An artist without an overview gets the first biography a provider
// answers from its links, with its source recorded; a failing provider is
// passed over; an artist whose own provider wrote an overview, or one with
// no links, asks nobody.
func TestEnrichFillsAnArtistsBiographyFromItsLinks(t *testing.T) {
	calls := 0
	reg := &pkgmetadata.Registry{Biographies: []pkgmetadata.BiographyProvider{
		stubBiographyProvider{name: "down", err: errors.New("wikipedia: 503"), calls: &calls},
		stubBiographyProvider{name: "wikipedia", text: "Muse are an English rock band.", calls: &calls},
	}}
	links := []pkgmetadata.Link{{Type: "wikidata", URL: "https://www.wikidata.org/wiki/Q22151"}}
	doc := &pkgmetadata.Artist{IDs: pkgmetadata.ExternalIDs{"musicbrainz": "9c9f1380"}, Links: links}

	enrich(context.Background(), reg, commonv1.MediaKindArtist, nil, nil, doc)
	require.Equal(t, "Muse are an English rock band.", doc.Overview)
	require.Equal(t, "wikipedia", doc.BiographySource)
	require.Equal(t, 2, calls, "the failing provider is passed over")

	calls = 0
	own := &pkgmetadata.Artist{IDs: pkgmetadata.ExternalIDs{"musicbrainz": "x"}, Overview: "From the annotation.", BiographySource: "musicbrainz", Links: links}
	enrich(context.Background(), reg, commonv1.MediaKindArtist, nil, nil, own)
	require.Equal(t, "From the annotation.", own.Overview)
	require.Equal(t, "musicbrainz", own.BiographySource)
	require.Zero(t, calls, "an overview already written asks nobody")

	nolinks := &pkgmetadata.Artist{IDs: pkgmetadata.ExternalIDs{"musicbrainz": "y"}}
	enrich(context.Background(), reg, commonv1.MediaKindArtist, nil, nil, nolinks)
	require.Empty(t, nolinks.Overview)
	require.Zero(t, calls, "no links, no request")

	author := &pkgmetadata.Author{IDs: pkgmetadata.ExternalIDs{"openlibrary": "OL22242A"}, Links: links}
	enrich(context.Background(), reg, commonv1.MediaKindAuthor, nil, nil, author)
	require.Equal(t, "Muse are an English rock band.", author.Overview, "an author is served the same way")
}

// The ratings enum round-trips (2026-11-10 design D8): every
// pkg/metadata.RatingSource* constant maps onto the CRD's RatingSource of
// the same spelling and back, the musicbrainz client's "mb" onto
// musicbrainz, case-insensitively, and a name the enum lacks maps onto
// nothing -- the apiserver would reject the whole status apply for it.
func TestRatingSourceRoundTripsTheEnum(t *testing.T) {
	pairs := map[string]catalogv1alpha1.RatingSource{
		pkgmetadata.RatingSourceIMDb:        catalogv1alpha1.RatingSourceIMDb,
		pkgmetadata.RatingSourceTMDB:        catalogv1alpha1.RatingSourceTMDB,
		pkgmetadata.RatingSourceRTCritic:    catalogv1alpha1.RatingSourceRTCritic,
		pkgmetadata.RatingSourceRTAudience:  catalogv1alpha1.RatingSourceRTAudience,
		pkgmetadata.RatingSourceMetacritic:  catalogv1alpha1.RatingSourceMetacritic,
		pkgmetadata.RatingSourceTrakt:       catalogv1alpha1.RatingSourceTrakt,
		pkgmetadata.RatingSourceLetterboxd:  catalogv1alpha1.RatingSourceLetterboxd,
		pkgmetadata.RatingSourceMusicBrainz: catalogv1alpha1.RatingSourceMusicBrainz,
		pkgmetadata.RatingSourceAudnexus:    catalogv1alpha1.RatingSourceAudnexus,
		pkgmetadata.RatingSourceHardcover:   catalogv1alpha1.RatingSourceHardcover,
		pkgmetadata.RatingSourceAniList:     catalogv1alpha1.RatingSourceAniList,
		pkgmetadata.RatingSourceComicVine:   catalogv1alpha1.RatingSourceComicVine,
	}
	require.Len(t, pairs, 12, "one pair per RatingSource the CRD enum names")
	for name, want := range pairs {
		got, ok := ratingSource(name)
		require.True(t, ok, name)
		require.Equal(t, want, got, name)
		require.Equal(t, name, string(got), "the two vocabularies spell a source the same way")
		back, ok := ratingSource(string(want))
		require.True(t, ok)
		require.Equal(t, want, back)
		upper, ok := ratingSource(strings.ToUpper(name))
		require.True(t, ok, "case-insensitive")
		require.Equal(t, want, upper)
	}
	mb, ok := ratingSource("mb")
	require.True(t, ok)
	require.Equal(t, catalogv1alpha1.RatingSourceMusicBrainz, mb, "the musicbrainz client's own key")
	_, ok = ratingSource("goodreads")
	require.False(t, ok, "a source the enum lacks is dropped, never sent")
	_, ok = ratingSource("")
	require.False(t, ok)
}

// A non-video kind's own rating seeds enrichRatings under its CRD name:
// MusicBrainz's "mb" lands as musicbrainz, a source the enum lacks is
// dropped rather than rejected by the apiserver, a value off the CRD's
// range too, and a RatingsProvider declaring a source for the kind
// (Hardcover's for a Book) fills what the seed did not.
func TestEnrichRatingsReadsANonVideoSeedThroughTheEnum(t *testing.T) {
	calls := 0
	reg := &pkgmetadata.Registry{Ratings: []pkgmetadata.RatingsProvider{
		stubRatingsProvider{name: "hardcover", declared: []string{pkgmetadata.RatingSourceHardcover},
			ratings: pkgmetadata.Ratings{pkgmetadata.RatingSourceHardcover: {Source: "hardcover", ValueCentis: 822, Votes: 63}}, calls: &calls},
	}}
	seed := pkgmetadata.Ratings{
		"mb":        {Source: "mb", ValueCentis: 860, Votes: 12},
		"goodreads": {Source: "goodreads", ValueCentis: 900, Votes: 1000},
		"anilist":   {Source: "anilist", ValueCentis: 12000, Votes: 5},
	}
	prior := []catalogv1alpha1.Rating{{Source: catalogv1alpha1.RatingSourceAudnexus, ValueCentis: 940, Votes: 0}}

	got := enrichRatings(context.Background(), reg, commonv1.MediaKindBook, pkgmetadata.ExternalIDs{"isbn13": "9781416971702"}, seed, prior)

	require.Equal(t, 1, calls)
	require.ElementsMatch(t, []catalogv1alpha1.Rating{
		{Source: catalogv1alpha1.RatingSourceMusicBrainz, ValueCentis: 860, Votes: 12},
		{Source: catalogv1alpha1.RatingSourceHardcover, ValueCentis: 822, Votes: 63},
		{Source: catalogv1alpha1.RatingSourceAudnexus, ValueCentis: 940},
	}, got, "mb reads as musicbrainz, goodreads and the out-of-range anilist are dropped, the prior audnexus is kept")

	// A provider declaring a source the enum lacks is never asked for it.
	calls = 0
	reg = &pkgmetadata.Registry{Ratings: []pkgmetadata.RatingsProvider{
		stubRatingsProvider{name: "odd", declared: []string{"goodreads"}, ratings: pkgmetadata.Ratings{"goodreads": {ValueCentis: 1}}, calls: &calls},
	}}
	require.Nil(t, enrichRatings(context.Background(), reg, commonv1.MediaKindBook, nil, nil, nil))
	require.Zero(t, calls)
}
