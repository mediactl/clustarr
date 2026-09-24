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
	"time"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// FetchOptions narrows a fetch to a language/region and controls which
// expensive-to-fetch satellites (images, credits) a provider bothers
// gathering. Not every provider method takes FetchOptions -- MovieProvider
// and SeriesProvider are pinned to the flat, positional signatures the
// design spec locks (line 714), which predate this struct; it is for the
// five interfaces the spec left to this task's judgment.
type FetchOptions struct {
	Language       string
	Region         string
	IncludeImages  bool
	IncludeCredits bool
}

// Capabilities describes what a provider can do, so a Registry or gateway
// can route a lookup to a provider that actually supports it instead of
// calling every provider and discarding ErrUnsupported responses.
type Capabilities struct {
	// LookupBy lists the ExternalIDs keys this provider can look an entity
	// up by (e.g. KeyTMDB, KeyIMDb).
	LookupBy []string
	Search   bool
	Changes  bool
	Artwork  bool
}

// SearchHit is a single result from a provider's search-by-text method,
// shared by every kind that does not have its own hit type (MovieProvider
// has the richer MovieHit instead).
type SearchHit struct {
	IDs    ExternalIDs
	Title  string
	Year   int32
	Poster string
}

// Provider is the common surface every per-kind provider interface embeds:
// a stable name for logging/metrics and a static description of what it
// supports.
type Provider interface {
	Name() string
	Capabilities() Capabilities
}

// MovieProvider fetches and searches movies. Method signatures are pinned
// verbatim to the design spec (line 714) -- no FetchOptions parameter, no
// extra method.
type MovieProvider interface {
	Provider
	Movie(ctx context.Context, tmdbID string, region string) (*Movie, error)
	FindMovie(ctx context.Context, ids ExternalIDs) (*Movie, error)
	SearchMovies(ctx context.Context, q string, year int) ([]MovieHit, error)
}

// SeriesProvider fetches series, episodes and change feeds. Method
// signatures are pinned verbatim to the design spec (line 714); the spec
// gives SeriesProvider no search method and none is added here.
type SeriesProvider interface {
	Provider
	Series(ctx context.Context, tvdbID string) (*Series, error)
	Episodes(ctx context.Context, tvdbID string, order string) ([]Episode, error)
	Updates(ctx context.Context, since time.Time) ([]string, error)
}

// ArtistProvider fetches artists and their albums.
type ArtistProvider interface {
	Provider
	SearchArtists(ctx context.Context, q string) ([]SearchHit, error)
	Artist(ctx context.Context, mbArtistID string) (*Artist, error)
	Albums(ctx context.Context, mbArtistID string) ([]Album, error)
	Album(ctx context.Context, mbReleaseGroupID string) (*Album, error)
}

// BookProvider fetches authors, works and editions.
type BookProvider interface {
	Provider
	SearchBooks(ctx context.Context, q string) ([]SearchHit, error)
	Author(ctx context.Context, ids ExternalIDs) (*Author, error)
	Books(ctx context.Context, authorID string) ([]Book, error)
	Book(ctx context.Context, ids ExternalIDs) (*Book, error)
	Edition(ctx context.Context, ids ExternalIDs) (*Edition, error)
}

// AudiobookProvider fetches audiobooks and their chapter listings.
type AudiobookProvider interface {
	Provider
	Audiobook(ctx context.Context, asin string, region string) (*Audiobook, error)
	Chapters(ctx context.Context, asin string, region string) ([]Chapter, error)
}

// ComicProvider fetches comic volumes and their issues.
type ComicProvider interface {
	Provider
	SearchVolumes(ctx context.Context, q string) ([]SearchHit, error)
	Volume(ctx context.Context, ids ExternalIDs) (*ComicVolume, error)
	Issues(ctx context.Context, volumeID string) ([]ComicIssue, error)
}

// ArtworkProvider fetches artwork for any media kind, independent of that
// kind's primary metadata provider (Cover Art Archive and Fanart.tv are
// artwork-only).
type ArtworkProvider interface {
	Provider
	Artwork(ctx context.Context, kind commonv1.MediaKind, ids ExternalIDs) ([]Image, error)
}

// IDResolver crosswalks ids for any media kind, independent of that kind's
// primary metadata provider.
type IDResolver interface {
	Provider
	Resolve(ctx context.Context, kind commonv1.MediaKind, ids ExternalIDs) (ExternalIDs, error)
}

// RatingsProvider supplies ratings for any media kind, independent of that
// kind's primary metadata provider -- mdblist and omdb rate a Movie or
// Series without being a MovieProvider or SeriesProvider at all, and tmdb
// implements this alongside MovieProvider, from the same fetch (spec
// §C.2). RatingSources declares, for kind, which Rating.Source values (the
// RatingSource* constants in model.go) this provider can ever fill; the
// gateway's enrichRatings (app/catalog/metadata/enrich.go) uses it to
// compute what a provider still needs to be asked for, so a provider is
// never called for a source it does not declare and never overwrites a
// source a higher-priority provider already filled. Ratings performs one
// call and returns every source this provider has for ids, keyed exactly
// as RatingSources names them; enrichRatings copies out only the sources
// it still needs.
type RatingsProvider interface {
	Provider
	RatingSources(kind commonv1.MediaKind) []string
	Ratings(ctx context.Context, kind commonv1.MediaKind, ids ExternalIDs) (Ratings, error)
}
