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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// resolveIDs runs every registered IDResolver for kind over ids, each one
// seeing what the ones before it added. ExternalIDs.Merge keeps the first
// value for a key present in both, so a resolver can only add ids, never
// override one already trusted. A resolver that fails, or cannot serve
// kind or these ids, adds nothing and stops nothing: a crosswalk is a
// best-effort addition to a record, never a condition of having one.
func resolveIDs(ctx context.Context, reg *pkgmetadata.Registry, kind commonv1.MediaKind, ids pkgmetadata.ExternalIDs) pkgmetadata.ExternalIDs {
	if ids == nil {
		ids = pkgmetadata.ExternalIDs{}
	}
	for _, r := range reg.Resolvers {
		rCtx, rSpan := tracing.Start(ctx, "metadata.IDResolver.Resolve")
		resolved, err := r.Resolve(rCtx, kind, ids)
		if err != nil {
			tracing.RecordError(rSpan, err)
			rSpan.End()
			continue
		}
		rSpan.End()
		ids = ids.Merge(resolved)
	}
	return ids
}

// enrich folds the Registry's crosswalk and artwork providers into a
// document the Handler has just fetched, before it is cached and written
// to status.metadata -- the one place the gateway builds a record's
// ExternalIDs and Images. specIDs are the ids the object's spec names
// (target.go's externalIDs); known are the ExternalIDs its status.metadata
// already carries.
//
//   - ExternalIDs: the document's own ids, then specIDs, then whatever the
//     Resolvers add (resolveIDs), then known. Each source only adds keys,
//     so the primary provider's ids always win and a fresh crosswalk beats
//     a remembered one. known comes last and only fills gaps: every write
//     here is a complete server-side apply of status.metadata, so an id a
//     resolver supplied last refresh but could not this time -- Kitsu
//     answering 502 once -- would otherwise be released and vanish until
//     the next refresh (CLAUDE.md: a transient failure must not gut a
//     healthy object). The spec's non-id keys ("region", which target.go
//     threads through the map for an Audiobook) are left out.
//   - Images: the document's own images first, then each ArtworkProvider's
//     in priority order, skipping a URL already present. An image scoped to
//     one season is dropped: the CRD's Image has no season field, so a
//     season-3 poster would otherwise read as the series' poster. The
//     builders in patch.go cap the list at the CRD's 50.
//
// Every call is best effort, like resolveIDs: an artwork provider that
// fails leaves the document as the primary provider returned it. A Book
// carries no images in pkg/metadata and an Audiobook a single one, so
// neither takes artwork; no artwork provider serves those kinds anyway.
func enrich(ctx context.Context, reg *pkgmetadata.Registry, kind commonv1.MediaKind, specIDs, known pkgmetadata.ExternalIDs, doc any) {
	if reg == nil || (len(reg.Resolvers) == 0 && len(reg.Artwork) == 0) {
		return
	}
	ctx, span := tracing.Start(ctx, "metadata.enrich")
	defer span.End()

	idsp, imagesp := docFields(doc)
	if idsp == nil {
		return
	}
	ids := idsp.Merge(nonIDKeysRemoved(specIDs))
	*idsp = resolveIDs(ctx, reg, kind, ids).Merge(known)

	if imagesp == nil {
		return
	}
	seen := make(map[string]bool, len(*imagesp))
	for _, img := range *imagesp {
		seen[img.URL] = true
	}
	for _, p := range reg.Artwork {
		aCtx, aSpan := tracing.Start(ctx, "metadata.ArtworkProvider.Artwork")
		imgs, err := p.Artwork(aCtx, kind, *idsp)
		if err != nil {
			tracing.RecordError(aSpan, err)
			aSpan.End()
			continue
		}
		aSpan.End()
		for _, img := range imgs {
			if img.URL == "" || img.Season != nil || seen[img.URL] {
				continue
			}
			seen[img.URL] = true
			*imagesp = append(*imagesp, img)
		}
	}
}

// enrichRatings computes status.metadata.ratings for kind, from seed, then
// reg.Ratings in priority order (spec §C.2), given the ids the primary
// fetch and enrich's crosswalk settled on, and prior -- the target's
// current status.metadata.ratings, read before this refresh started.
//
// seed is the document's own Ratings, exactly as the primary fetch
// (Registry.Lookup, in worker.go) already returned it -- mapMovie
// (pkg/metadata/clients/tmdb) builds Ratings{"tmdb": ...} as a side effect
// of every movie fetch, whether that fetch just happened or came back from
// the cache. seed is folded into filled FIRST, before any RatingsProvider
// is even consulted: this is what stops a provider that is ALSO the
// primary MovieProvider/SeriesProvider for this document (tmdb is both)
// from being asked, a few lines later, to fetch the very same document a
// second time to answer a source it already answered for free. Fix round
// 1: without this, tmdb's own row in spec §C.2's table -- "from the fetch
// it already performs" -- was untrue in the code: every refresh, cached
// document or not, doubled TMDB's traffic and limiter consumption, because
// enrichRatings always found "tmdb" unfilled and always called
// tmdb.Client.Ratings, which re-ran the whole GetMovieDetails call.
//
// After seeding, for each provider in reg.Ratings, in order, this computes
// need: the sources (RatingsProvider.RatingSources(kind)) that provider
// declares and that neither seed nor a higher-priority provider has
// already filled this pass. A provider whose need is empty is skipped
// without a call -- "a provider not declaring a source is never asked for
// it" extends to "and never asked again once seed or a higher-priority
// provider already filled everything it could offer". A provider that IS
// called is called exactly once; on error this logs at warn and moves on
// to the next provider, filling nothing -- a failing provider must not
// blank a source seed, a previous provider (this pass), or a past one
// already supplied. On success only the sources still in need are copied
// from its result, even if it returned more: a provider must never
// overwrite a source seed or a higher-priority provider already filled.
//
// Once every provider has been tried, any source still unfilled is carried
// forward from prior -- exactly as resolveIDs above carries known ids
// forward -- so a provider outage this pass cannot strip a rating badge a
// past refresh already earned. A source with both a zero ValueCentis and
// zero Votes is dropped rather than copied, from seed exactly as from a
// provider's result (Review Focus 5): that shape is "nothing to report",
// not a genuine zero score, and a Rating with no votes at all would render
// as a 0/10 badge no source actually reported.
func enrichRatings(ctx context.Context, reg *pkgmetadata.Registry, kind commonv1.MediaKind, ids pkgmetadata.ExternalIDs, seed pkgmetadata.Ratings, prior []catalogv1alpha1.Rating) []catalogv1alpha1.Rating {
	ctx, span := tracing.Start(ctx, "metadata.enrichRatings")
	defer span.End()

	filled := make(map[catalogv1alpha1.RatingSource]catalogv1alpha1.Rating, len(seed)+len(prior))
	for src, r := range seed {
		if r.ValueCentis == 0 && r.Votes == 0 {
			continue
		}
		filled[catalogv1alpha1.RatingSource(src)] = catalogv1alpha1.Rating{
			Source: catalogv1alpha1.RatingSource(src), ValueCentis: r.ValueCentis, Votes: r.Votes,
		}
	}

	if reg != nil {
		for _, p := range reg.Ratings {
			declared := p.RatingSources(kind)
			if len(declared) == 0 {
				continue
			}
			var need []string
			for _, s := range declared {
				if _, ok := filled[catalogv1alpha1.RatingSource(s)]; !ok {
					need = append(need, s)
				}
			}
			if len(need) == 0 {
				continue
			}

			rCtx, rSpan := tracing.Start(ctx, "metadata.RatingsProvider.Ratings")
			result, err := p.Ratings(rCtx, kind, ids)
			if err != nil {
				tracing.RecordError(rSpan, err)
				rSpan.End()
				logging.FromContext(ctx).Warn("ratings provider failed", "provider", p.Name(), "error", err)
				continue
			}
			rSpan.End()

			for _, s := range need {
				r, ok := result[s]
				if !ok || (r.ValueCentis == 0 && r.Votes == 0) {
					continue
				}
				filled[catalogv1alpha1.RatingSource(s)] = catalogv1alpha1.Rating{
					Source: catalogv1alpha1.RatingSource(s), ValueCentis: r.ValueCentis, Votes: r.Votes,
				}
			}
		}
	}

	for _, r := range prior {
		if _, ok := filled[r.Source]; !ok {
			filled[r.Source] = r
		}
	}

	if len(filled) == 0 {
		return nil
	}
	out := make([]catalogv1alpha1.Rating, 0, len(filled))
	for _, r := range filled {
		out = append(out, r)
	}
	return out
}

// docFields returns pointers to a fetched document's ExternalIDs and, for
// the kinds that carry a list of them, Images.
func docFields(doc any) (*pkgmetadata.ExternalIDs, *[]pkgmetadata.Image) {
	switch d := doc.(type) {
	case *pkgmetadata.Movie:
		return &d.IDs, &d.Images
	case *pkgmetadata.Series:
		return &d.IDs, &d.Images
	case *pkgmetadata.Artist:
		return &d.IDs, &d.Images
	case *pkgmetadata.Album:
		return &d.IDs, &d.Images
	case *pkgmetadata.Author:
		return &d.IDs, &d.Images
	case *pkgmetadata.ComicVolume:
		return &d.IDs, &d.Images
	case *pkgmetadata.Book:
		return &d.IDs, nil
	case *pkgmetadata.Audiobook:
		return &d.IDs, nil
	default:
		return nil, nil
	}
}

// nonIDKeys are keys target.go threads through an ExternalIDs map that are
// parameters, not ids.
var nonIDKeys = map[string]bool{"region": true}

func nonIDKeysRemoved(ids pkgmetadata.ExternalIDs) pkgmetadata.ExternalIDs {
	out := make(pkgmetadata.ExternalIDs, len(ids))
	for k, v := range ids {
		if !nonIDKeys[k] {
			out[k] = v
		}
	}
	return out
}
