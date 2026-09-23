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
	"fmt"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/schema"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// rpcVerb names one of ServeRPC's three subjects, for the outer per-request
// span name (see ServeRPC).
type rpcVerb struct {
	name string
	fn   func(context.Context, schema.MetadataRequest) schema.MetadataResponse
}

// ServeRPC registers the gateway's three request/reply methods --
// rpc.catalogarr.metadata.{lookup,search,resolve} -- in the catalogarr
// queue group. Unlike Handler, these never touch a Kubernetes object: the
// caller (an import list, an interactive lookup) supplies ids and gets a
// provider document back.
//
// The bus hands every inbound RPC request a bare context.Background():
// pkg/events' Requester.Serve does not yet extract Clustarr-Trace from the
// request onto the context it passes handlers (a gap tracked and fixed
// separately, by the task that owns pkg/events). Without a span started
// here, every RPC-triggered provider call ran with no parent at all and the
// provider client's own span became an orphaned root -- likely the majority
// of interactive gateway traffic: import-list searches, resolver calls and
// episode listings. The outer span opened in the Serve callback below, and
// the per-verb span each of lookup/lookupEpisodes/search/resolve opens
// immediately, at least make that traffic traced today; once pkg/events
// extracts the trace header, both spans get a real parent for free.
func ServeRPC(bus events.Requester, reg *pkgmetadata.Registry) error {
	verbs := map[string]rpcVerb{
		events.RPCMetadataLookup: {name: "lookup", fn: func(ctx context.Context, req schema.MetadataRequest) schema.MetadataResponse {
			return lookup(ctx, reg, req)
		}},
		events.RPCMetadataSearch: {name: "search", fn: func(ctx context.Context, req schema.MetadataRequest) schema.MetadataResponse {
			return search(ctx, reg, req)
		}},
		events.RPCMetadataResolve: {name: "resolve", fn: func(ctx context.Context, req schema.MetadataRequest) schema.MetadataResponse {
			return resolve(ctx, reg, req)
		}},
	}
	for subject, v := range verbs {
		v := v
		err := bus.Serve(subject, events.QueueGroupCatalogar, func(ctx context.Context, data []byte) ([]byte, error) {
			ctx, span := tracing.Start(ctx, "metadata.rpc.serve."+v.name)
			defer span.End()

			var req schema.MetadataRequest
			if err := json.Unmarshal(data, &req); err != nil {
				tracing.RecordError(span, err)
				return nil, fmt.Errorf("metadata: decode MetadataRequest: %w", err)
			}
			resp := v.fn(ctx, req)
			if resp.Error != "" {
				tracing.RecordError(span, errors.New(resp.Error))
			}
			return json.Marshal(resp)
		})
		if err != nil {
			return fmt.Errorf("metadata: serve %s: %w", subject, err)
		}
	}
	return nil
}

// lookup answers rpc.catalogarr.metadata.lookup. MediaKindEpisode and
// MediaKindIssue are both verbs pkg/metadata.Registry.Lookup deliberately
// does not support (its switch covers every kind whose provider call
// returns one entity; Episode and Issue are each a list scoped by their
// parent's id -- SeriesProvider.Episodes(tvdbID, order) and
// ComicProvider.Issues(volumeID) -- and "first entity from the first
// provider that succeeds" is the wrong shape for a list, see
// Registry.Lookup's own default case) -- dispatch them to lookupEpisodes and
// lookupIssues respectively before falling through to Registry.Lookup for
// every other kind, which now also covers album and book (task G2-1;
// Registry.Lookup's switch).
func lookup(ctx context.Context, reg *pkgmetadata.Registry, req schema.MetadataRequest) schema.MetadataResponse {
	ctx, span := tracing.Start(ctx, "metadata.rpc.lookup")
	defer span.End()

	switch req.Kind {
	case commonv1.MediaKindEpisode:
		return lookupEpisodes(ctx, reg, req)
	case commonv1.MediaKindIssue:
		return lookupIssues(ctx, reg, req)
	}

	fetchCtx, fetchSpan := tracing.Start(ctx, "metadata.Registry.Lookup")
	v, err := reg.Lookup(fetchCtx, req.Kind, req.IDs)
	if err != nil {
		tracing.RecordError(fetchSpan, err)
		fetchSpan.End()
		tracing.RecordError(span, err)
		return schema.MetadataResponse{Kind: req.Kind, Error: err.Error()}
	}
	fetchSpan.End()

	result, err := json.Marshal(v)
	if err != nil {
		tracing.RecordError(span, err)
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
	ctx, span := tracing.Start(ctx, "metadata.rpc.lookupEpisodes")
	defer span.End()

	tvdbID, ok := req.IDs[pkgmetadata.KeyTVDB]
	if !ok {
		err := fmt.Errorf("metadata: episode lookup requires %q in ids", pkgmetadata.KeyTVDB)
		tracing.RecordError(span, err)
		return schema.MetadataResponse{Kind: req.Kind, Error: err.Error()}
	}
	order := req.IDs["order"]
	var lastErr error
	for _, p := range reg.Series {
		epCtx, epSpan := tracing.Start(ctx, "metadata.SeriesProvider.Episodes")
		episodes, err := p.Episodes(epCtx, tvdbID, order)
		if err != nil {
			tracing.RecordError(epSpan, err)
			epSpan.End()
			lastErr = err
			continue
		}
		epSpan.End()
		return schema.MetadataResponse{Kind: req.Kind, Provider: p.Name(), Results: marshalAll(episodes)}
	}
	if lastErr == nil {
		lastErr = pkgmetadata.ErrNotFound
	}
	tracing.RecordError(span, lastErr)
	return schema.MetadataResponse{Kind: req.Kind, Error: lastErr.Error()}
}

// lookupIssues is lookupEpisodes' counterpart for Comic->Issue: Kind=
// MediaKindIssue, IDs={"comicvine": volumeID} (ComicVine's own id shape,
// "NNNN-NNNNN", covers both volumes and issues, but ComicProvider.Issues
// takes the volume's id and returns every issue in it -- there is no
// single-issue-by-id call, see pkg/metadata/clients/comicvine.Client.Issues).
// Answers with Results, one JSON-encoded pkg/metadata.ComicIssue per entry,
// first ComicProvider-that-succeeds over reg.Comics. This is the RPC path
// G2-2's Comic reconciler is expected to call to fan issues out onto Issue
// objects -- ComicVolume itself (what Registry.Lookup(kind=comic) and this
// gateway's Handler fetch) never carries its issue list; Volume() and
// Issues() are separate ComicVine calls, exactly as Series() and Episodes()
// are separate TVDB calls.
func lookupIssues(ctx context.Context, reg *pkgmetadata.Registry, req schema.MetadataRequest) schema.MetadataResponse {
	ctx, span := tracing.Start(ctx, "metadata.rpc.lookupIssues")
	defer span.End()

	volumeID, ok := req.IDs[pkgmetadata.KeyComicVine]
	if !ok {
		err := fmt.Errorf("metadata: issue lookup requires %q in ids", pkgmetadata.KeyComicVine)
		tracing.RecordError(span, err)
		return schema.MetadataResponse{Kind: req.Kind, Error: err.Error()}
	}
	var lastErr error
	for _, p := range reg.Comics {
		isCtx, isSpan := tracing.Start(ctx, "metadata.ComicProvider.Issues")
		issues, err := p.Issues(isCtx, volumeID)
		if err != nil {
			tracing.RecordError(isSpan, err)
			isSpan.End()
			lastErr = err
			continue
		}
		isSpan.End()
		return schema.MetadataResponse{Kind: req.Kind, Provider: p.Name(), Results: marshalAll(issues)}
	}
	if lastErr == nil {
		lastErr = pkgmetadata.ErrNotFound
	}
	tracing.RecordError(span, lastErr)
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
	ctx, span := tracing.Start(ctx, "metadata.rpc.search")
	defer span.End()

	switch req.Kind {
	case commonv1.MediaKindMovie:
		for _, p := range reg.Movies {
			pCtx, pSpan := tracing.Start(ctx, "metadata.MovieProvider.SearchMovies")
			hits, err := p.SearchMovies(pCtx, req.Text, int(req.Year))
			if err != nil {
				tracing.RecordError(pSpan, err)
				pSpan.End()
				continue
			}
			pSpan.End()
			return schema.MetadataResponse{Kind: req.Kind, Provider: p.Name(), Results: marshalAll(hits)}
		}
	case commonv1.MediaKindArtist:
		for _, p := range reg.Artists {
			pCtx, pSpan := tracing.Start(ctx, "metadata.ArtistProvider.SearchArtists")
			hits, err := p.SearchArtists(pCtx, req.Text)
			if err != nil {
				tracing.RecordError(pSpan, err)
				pSpan.End()
				continue
			}
			pSpan.End()
			return schema.MetadataResponse{Kind: req.Kind, Provider: p.Name(), Results: marshalAll(hits)}
		}
	case commonv1.MediaKindBook:
		for _, p := range reg.Books {
			pCtx, pSpan := tracing.Start(ctx, "metadata.BookProvider.SearchBooks")
			hits, err := p.SearchBooks(pCtx, req.Text)
			if err != nil {
				tracing.RecordError(pSpan, err)
				pSpan.End()
				continue
			}
			pSpan.End()
			return schema.MetadataResponse{Kind: req.Kind, Provider: p.Name(), Results: marshalAll(hits)}
		}
	case commonv1.MediaKindComic:
		for _, p := range reg.Comics {
			pCtx, pSpan := tracing.Start(ctx, "metadata.ComicProvider.SearchVolumes")
			hits, err := p.SearchVolumes(pCtx, req.Text)
			if err != nil {
				tracing.RecordError(pSpan, err)
				pSpan.End()
				continue
			}
			pSpan.End()
			return schema.MetadataResponse{Kind: req.Kind, Provider: p.Name(), Results: marshalAll(hits)}
		}
	}
	err := fmt.Errorf("metadata: search does not support kind %q", req.Kind)
	tracing.RecordError(span, err)
	return schema.MetadataResponse{Kind: req.Kind, Error: err.Error()}
}

// resolve merges the ids every registered IDResolver adds for req.Kind on
// top of the caller-supplied ids. ExternalIDs.Merge keeps the first value
// for a key present in both, so a resolver can only add ids, never override
// one the caller already trusted.
func resolve(ctx context.Context, reg *pkgmetadata.Registry, req schema.MetadataRequest) schema.MetadataResponse {
	ctx, span := tracing.Start(ctx, "metadata.rpc.resolve")
	defer span.End()

	ids := pkgmetadata.ExternalIDs(req.IDs)
	for _, r := range reg.Resolvers {
		rCtx, rSpan := tracing.Start(ctx, "metadata.IDResolver.Resolve")
		resolved, err := r.Resolve(rCtx, req.Kind, ids)
		if err != nil {
			tracing.RecordError(rSpan, err)
			rSpan.End()
			continue
		}
		rSpan.End()
		ids = ids.Merge(resolved)
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
	case *pkgmetadata.Album:
		return e.IDs
	case *pkgmetadata.Book:
		return e.IDs
	default:
		return nil
	}
}
