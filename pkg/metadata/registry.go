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
	"fmt"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// Registry composes every configured provider of every kind, in priority
// order (index order within each slice). It does not fetch credentials, own
// http.Clients or rate limiters itself -- the gateway (ADR-0007) builds and
// owns the concrete providers and assembles a Registry from them.
type Registry struct {
	Movies     []MovieProvider
	Series     []SeriesProvider
	Artists    []ArtistProvider
	Books      []BookProvider
	Audiobooks []AudiobookProvider
	Comics     []ComicProvider
	Artwork    []ArtworkProvider
	Resolvers  []IDResolver
}

// Lookup fetches a single entity of kind, identified by ids, from the first
// provider in priority order whose call succeeds. "First-wins" here means
// the first provider whose call succeeds, not a field-level merge across
// providers of the same kind (layering a MusicBrainz rating onto an
// Audnexus audiobook, for example) -- that composition is the gateway's
// job, calling Lookup once per kind it needs and combining results itself.
func (r *Registry) Lookup(ctx context.Context, kind commonv1.MediaKind, ids ExternalIDs) (any, error) {
	switch kind {
	case commonv1.MediaKindMovie:
		return lookupFirst(r.Movies, func(p MovieProvider) (*Movie, error) {
			if tmdbID, ok := ids[KeyTMDB]; ok {
				return p.Movie(ctx, tmdbID, "")
			}
			return p.FindMovie(ctx, ids)
		})
	case commonv1.MediaKindSeries:
		tvdbID, ok := ids[KeyTVDB]
		if !ok {
			return nil, fmt.Errorf("metadata: series lookup requires %q in ExternalIDs", KeyTVDB)
		}
		return lookupFirst(r.Series, func(p SeriesProvider) (*Series, error) { return p.Series(ctx, tvdbID) })
	case commonv1.MediaKindArtist:
		mbid, ok := ids[KeyMBArtist]
		if !ok {
			return nil, fmt.Errorf("metadata: artist lookup requires %q in ExternalIDs", KeyMBArtist)
		}
		return lookupFirst(r.Artists, func(p ArtistProvider) (*Artist, error) { return p.Artist(ctx, mbid) })
	case commonv1.MediaKindAuthor:
		return lookupFirst(r.Books, func(p BookProvider) (*Author, error) { return p.Author(ctx, ids) })
	case commonv1.MediaKindAudiobook:
		asin, ok := ids[KeyASIN]
		if !ok {
			return nil, fmt.Errorf("metadata: audiobook lookup requires %q in ExternalIDs", KeyASIN)
		}
		// AudiobookSpec.Region (design §4.2) has ten possible marketplaces,
		// not just "us" -- so the caller (app/catalog/metadata/target.go's
		// externalIDs) stuffs it into ids["region"] the same way
		// rpc.go's lookupEpisodes stuffs an episode order into
		// ids["order"]: Lookup's signature is fixed at (kind, ExternalIDs),
		// so an id-shaped extra parameter travels through the map rather
		// than widening the method. Defaulting to "us" when absent keeps
		// every existing caller (which never set it) working exactly as
		// before.
		region := ids["region"]
		if region == "" {
			region = "us"
		}
		return lookupFirst(r.Audiobooks, func(p AudiobookProvider) (*Audiobook, error) {
			return p.Audiobook(ctx, asin, region)
		})
	case commonv1.MediaKindComic:
		return lookupFirst(r.Comics, func(p ComicProvider) (*ComicVolume, error) { return p.Volume(ctx, ids) })
	case commonv1.MediaKindAlbum:
		mbReleaseGroupID, ok := ids[KeyMBReleaseGroup]
		if !ok {
			return nil, fmt.Errorf("metadata: album lookup requires %q in ExternalIDs", KeyMBReleaseGroup)
		}
		return lookupFirst(r.Artists, func(p ArtistProvider) (*Album, error) { return p.Album(ctx, mbReleaseGroupID) })
	case commonv1.MediaKindBook:
		return lookupFirst(r.Books, func(p BookProvider) (*Book, error) { return p.Book(ctx, ids) })
	default:
		// MediaKindEpisode and MediaKindIssue are deliberately absent, not
		// just unimplemented: both are 1:many children whose provider call
		// returns a list scoped by their parent's id (SeriesProvider.
		// Episodes(tvdbID, order), ComicProvider.Issues(volumeID)), and
		// "first entity from the first provider that succeeds" -- what
		// Lookup does for every case above -- is the wrong shape for a
		// list, exactly as this package's task C6 predecessor found for
		// Episode (see app/catalog/metadata/rpc.go's lookupEpisodes, which
		// bypasses this switch entirely). app/catalog/metadata/rpc.go's
		// lookupIssues is Issue's counterpart to lookupEpisodes, for the
		// same reason. Neither belongs here.
		return nil, fmt.Errorf("metadata: Lookup does not support kind %q", kind)
	}
}

// lookupFirst calls call against each provider in order, returning the
// first success. It joins every provider's error when all fail, and falls
// back to ErrNotFound when providers is empty so a caller can always
// errors.Is against ErrNotFound.
func lookupFirst[P any, T any](providers []P, call func(P) (*T, error)) (*T, error) {
	var errs []error
	for _, p := range providers {
		v, err := call(p)
		if err == nil {
			return v, nil
		}
		errs = append(errs, err)
	}
	if len(errs) == 0 {
		return nil, ErrNotFound
	}
	return nil, errors.Join(errs...)
}
