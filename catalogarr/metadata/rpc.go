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
	"fmt"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// ServeRPC registers the gateway's three request/reply methods --
// rpc.catalogarr.metadata.{lookup,search,resolve} -- in the catalogarr
// queue group. Unlike Handler, these never touch a Kubernetes object: the
// caller (an import list, an interactive lookup) supplies ids and gets a
// provider document back.
func ServeRPC(bus events.Requester, reg *pkgmetadata.Registry) error {
	handlers := map[string]func(context.Context, schema.MetadataRequest) schema.MetadataResponse{
		events.RPCMetadataLookup: func(ctx context.Context, req schema.MetadataRequest) schema.MetadataResponse {
			return lookup(ctx, reg, req)
		},
		events.RPCMetadataSearch: func(ctx context.Context, req schema.MetadataRequest) schema.MetadataResponse {
			return search(ctx, reg, req)
		},
		events.RPCMetadataResolve: func(ctx context.Context, req schema.MetadataRequest) schema.MetadataResponse {
			return resolve(ctx, reg, req)
		},
	}
	for subject, h := range handlers {
		h := h
		err := bus.Serve(subject, events.QueueGroupCatalogar, func(ctx context.Context, data []byte) ([]byte, error) {
			var req schema.MetadataRequest
			if err := json.Unmarshal(data, &req); err != nil {
				return nil, fmt.Errorf("metadata: decode MetadataRequest: %w", err)
			}
			return json.Marshal(h(ctx, req))
		})
		if err != nil {
			return fmt.Errorf("metadata: serve %s: %w", subject, err)
		}
	}
	return nil
}

// lookup answers rpc.catalogarr.metadata.lookup. MediaKindEpisode is a verb
// Task C6 needs and pkg/metadata.Registry.Lookup does not support (its
// switch covers movie/series/artist/author/audiobook/comic only, because
// "first entity from the first provider that succeeds" is the wrong shape
// for a list) -- dispatch it to lookupEpisodes before falling through to
// Registry.Lookup for every other kind.
func lookup(ctx context.Context, reg *pkgmetadata.Registry, req schema.MetadataRequest) schema.MetadataResponse {
	if req.Kind == commonv1.MediaKindEpisode {
		return lookupEpisodes(ctx, reg, req)
	}
	v, err := reg.Lookup(ctx, req.Kind, req.IDs)
	if err != nil {
		return schema.MetadataResponse{Kind: req.Kind, Error: err.Error()}
	}
	result, err := json.Marshal(v)
	if err != nil {
		return schema.MetadataResponse{Kind: req.Kind, Error: err.Error()}
	}
	return schema.MetadataResponse{Kind: req.Kind, IDs: idsOf(v), Result: result}
}

// lookupEpisodes is the Task C6 contract: Kind=MediaKindEpisode,
// IDs={"tvdb": tvdbID, "order": order}, order a plain string matching
// SeriesProvider.Episodes' own parameter (not pkg/metadata's typed
// SeasonOrder, which that method does not take). Answers with Results, one
// JSON-encoded pkg/metadata.Episode per entry, first SeriesProvider-that-
// succeeds over reg.Series.
func lookupEpisodes(ctx context.Context, reg *pkgmetadata.Registry, req schema.MetadataRequest) schema.MetadataResponse {
	tvdbID, ok := req.IDs[pkgmetadata.KeyTVDB]
	if !ok {
		return schema.MetadataResponse{Kind: req.Kind, Error: fmt.Sprintf("metadata: episode lookup requires %q in ids", pkgmetadata.KeyTVDB)}
	}
	order := req.IDs["order"]
	var lastErr error
	for _, p := range reg.Series {
		episodes, err := p.Episodes(ctx, tvdbID, order)
		if err != nil {
			lastErr = err
			continue
		}
		return schema.MetadataResponse{Kind: req.Kind, Provider: p.Name(), Results: marshalAll(episodes)}
	}
	if lastErr == nil {
		lastErr = pkgmetadata.ErrNotFound
	}
	return schema.MetadataResponse{Kind: req.Kind, Error: lastErr.Error()}
}

// search dispatches by kind. Only the kinds whose Provider interface (go doc
// ./pkg/metadata) has a search method are supported: movie, artist, book,
// comic. series has none (spec-pinned). album has none either: ArtistProvider
// exposes SearchArtists (by text) and Albums(mbArtistID) (list, not search,
// of a known artist's albums) but no SearchAlbums -- the brief's original
// draft assumed one; there is no provider surface to route an album search
// to, so it falls through to the unsupported-kind response below exactly
// like series. audiobook and author have none either (Audnexus/Open Library
// search is out of scope).
func search(ctx context.Context, reg *pkgmetadata.Registry, req schema.MetadataRequest) schema.MetadataResponse {
	switch req.Kind {
	case commonv1.MediaKindMovie:
		for _, p := range reg.Movies {
			if hits, err := p.SearchMovies(ctx, req.Text, int(req.Year)); err == nil {
				return schema.MetadataResponse{Kind: req.Kind, Provider: p.Name(), Results: marshalAll(hits)}
			}
		}
	case commonv1.MediaKindArtist:
		for _, p := range reg.Artists {
			if hits, err := p.SearchArtists(ctx, req.Text); err == nil {
				return schema.MetadataResponse{Kind: req.Kind, Provider: p.Name(), Results: marshalAll(hits)}
			}
		}
	case commonv1.MediaKindBook:
		for _, p := range reg.Books {
			if hits, err := p.SearchBooks(ctx, req.Text); err == nil {
				return schema.MetadataResponse{Kind: req.Kind, Provider: p.Name(), Results: marshalAll(hits)}
			}
		}
	case commonv1.MediaKindComic:
		for _, p := range reg.Comics {
			if hits, err := p.SearchVolumes(ctx, req.Text); err == nil {
				return schema.MetadataResponse{Kind: req.Kind, Provider: p.Name(), Results: marshalAll(hits)}
			}
		}
	}
	return schema.MetadataResponse{Kind: req.Kind, Error: fmt.Sprintf("metadata: search does not support kind %q", req.Kind)}
}

// resolve merges the ids every registered IDResolver adds for req.Kind on
// top of the caller-supplied ids. ExternalIDs.Merge keeps the first value
// for a key present in both, so a resolver can only add ids, never override
// one the caller already trusted.
func resolve(ctx context.Context, reg *pkgmetadata.Registry, req schema.MetadataRequest) schema.MetadataResponse {
	ids := pkgmetadata.ExternalIDs(req.IDs)
	for _, r := range reg.Resolvers {
		if resolved, err := r.Resolve(ctx, req.Kind, ids); err == nil {
			ids = ids.Merge(resolved)
		}
	}
	return schema.MetadataResponse{Kind: req.Kind, IDs: ids}
}

func marshalAll[T any](items []T) [][]byte {
	out := make([][]byte, 0, len(items))
	for _, it := range items {
		if b, err := json.Marshal(it); err == nil {
			out = append(out, b)
		}
	}
	return out
}

func idsOf(v any) map[string]string {
	switch e := v.(type) {
	case *pkgmetadata.Movie:
		return e.IDs
	case *pkgmetadata.Series:
		return e.IDs
	case *pkgmetadata.Artist:
		return e.IDs
	case *pkgmetadata.Author:
		return e.IDs
	case *pkgmetadata.Audiobook:
		return e.IDs
	case *pkgmetadata.ComicVolume:
		return e.IDs
	default:
		return nil
	}
}
