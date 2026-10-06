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

package plex

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata/extended"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/release"
	"github.com/mediactl/clustarr/ui/projection"
)

// Collections are the movie provider's collection feature (Plex's provider
// docs: type 18, Feature "collection" at /library/collections, and a
// movie's Collection[] entry whose key lists the collection's items;
// "currently only supported in movie libraries"). A collection is a TMDB
// collection (status.metadata.collection), its items the library's movies
// in it. It is answered with its plex://collection/ GUID when the metadata
// gateway learned its Plex id, else under the provider's own scheme by its
// TMDB id; its summary and artwork come from a member movie's extended
// document. The shapes follow Plex's own metadata service
// (/library/collections/<id> and its /children, recorded 2026-10-06).

const (
	metadataTypeCollection = "collection"
	// collectionKeyPrefix starts a collection's own ratingKey, its TMDB
	// collection id after it.
	collectionKeyPrefix = "tmdb-collection-"
)

// collectionKey is a collection's ratingKey: the Plex id PMS will ask for
// it by once it holds the collection under its plex:// GUID, else
// tmdb-collection-<id>.
func (u urls) collectionKey(c catalogv1.CollectionRef) string {
	if u.plexCollection(c) {
		return c.PlexID
	}
	return collectionKeyPrefix + strconv.FormatInt(c.TmdbID, 10)
}

// plexCollection reports whether c is answered with its plex:// GUID: the
// GUIDs are on and its Plex id names this collection and no other.
func (u urls) plexCollection(c catalogv1.CollectionRef) bool {
	if !u.plexGUIDs || u.idx == nil || !plexIDPattern.MatchString(c.PlexID) {
		return false
	}
	id, ok := u.idx.CollectionByPlexID(c.PlexID)
	return ok && id == c.TmdbID
}

func (u urls) collectionGuid(root rootDef, c catalogv1.CollectionRef) string {
	if u.plexCollection(c) {
		return plexScheme + "://" + metadataTypeCollection + "/" + c.PlexID
	}
	return GUID(root.identifier, metadataTypeCollection, collectionKeyPrefix+strconv.FormatInt(c.TmdbID, 10))
}

func collectionChildrenKey(key string) string {
	return "/library/collections/" + key + "/children"
}

// movieCollectionRef is a movie's single Collection[] entry: the
// collection's GUID, the key its items are listed at, and its name. The
// summary and artwork are added from the movie's extended document.
func movieCollectionRef(root rootDef, u urls, c catalogv1.CollectionRef) CollectionRef {
	if !root.collections || c.TmdbID == 0 {
		return CollectionRef{Tag: c.Name}
	}
	return CollectionRef{
		Guid: u.collectionGuid(root, c),
		Key:  collectionChildrenKey(u.collectionKey(c)),
		Tag:  c.Name,
	}
}

// withCollectionDoc adds a collection's summary and artwork to ref.
func withCollectionDoc(u urls, ref *CollectionRef, doc *extended.Collection) {
	if doc == nil {
		return
	}
	ref.Summary = doc.Summary
	ref.Thumb = u.proxied(doc.Poster)
	ref.Art = u.proxied(doc.Art)
}

// resolveCollection resolves a collection key -- its Plex id or
// tmdb-collection-<id> -- to the library's movies in it, oldest first;
// false for a collection no movie belongs to.
func resolveCollection(idx *projection.Index, key string) ([]*catalogv1.Movie, bool) {
	var tmdbID int64
	switch {
	case plexIDPattern.MatchString(key):
		id, ok := idx.CollectionByPlexID(key)
		if !ok {
			return nil, false
		}
		tmdbID = id
	case strings.HasPrefix(key, collectionKeyPrefix):
		id, err := strconv.ParseInt(strings.TrimPrefix(key, collectionKeyPrefix), 10, 64)
		if err != nil || id <= 0 {
			return nil, false
		}
		tmdbID = id
	default:
		return nil, false
	}
	movies := idx.CollectionMovies(tmdbID)
	return movies, len(movies) > 0
}

// collectionDoc is the collection's summary and artwork from the first
// member movie whose extended document carries them.
func (h *handler) collectionDoc(ctx context.Context, movies []*catalogv1.Movie) *extended.Collection {
	if h.opts.Extended == nil {
		return nil
	}
	rctx, cancel := context.WithTimeout(ctx, ExtendedReadTimeout)
	defer cancel()
	for _, m := range movies {
		doc, ok, err := h.opts.Extended(rctx, commonv1.MediaKindMovie, m.UID)
		if err != nil {
			logging.FromContext(ctx).WarnContext(ctx, "plex: read a collection's extended document", "error", err)
			return nil
		}
		if ok && doc.Collection != nil {
			return doc.Collection
		}
	}
	return nil
}

// buildCollectionMetadata is a collection's own Metadata object: type
// collection, its name, the count and year span of its movies in the
// library, and its summary and artwork.
func (h *handler) buildCollectionMetadata(ctx context.Context, root rootDef, u urls, movies []*catalogv1.Movie) Metadata {
	ref := *movies[0].Status.Metadata.Collection
	key := u.collectionKey(ref)
	md := Metadata{
		RatingKey:  key,
		Key:        collectionChildrenKey(key),
		Guid:       u.collectionGuid(root, ref),
		Type:       metadataTypeCollection,
		Title:      ref.Name,
		ChildCount: len(movies),
	}
	for _, m := range movies {
		y := m.Status.Metadata.Year
		if y == 0 {
			continue
		}
		if md.MinYear == 0 || y < md.MinYear {
			md.MinYear = y
		}
		if y > md.MaxYear {
			md.MaxYear = y
		}
	}
	if doc := h.collectionDoc(ctx, movies); doc != nil {
		md.Summary = doc.Summary
		md.Thumb = u.proxied(doc.Poster)
		md.Art = u.proxied(doc.Art)
	}
	return md
}

// handleCollection answers GET {root}/library/collections/{key}: the
// collection itself.
func (h *handler) handleCollection(root rootDef) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idx, ok := h.index(w, r)
		if !ok {
			return
		}
		movies, ok := resolveCollection(idx, r.PathValue("key"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		u := h.urlsFor(r, idx)
		h.writePage(w, r, root, []Metadata{h.buildCollectionMetadata(r.Context(), root, u, movies)}, pageRequest{size: 1})
	}
}

// handleCollectionChildren answers GET
// {root}/library/collections/{key}/children: the collection's movies in
// the library, oldest first, paged as PMS asks.
func (h *handler) handleCollectionChildren(root rootDef) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idx, ok := h.index(w, r)
		if !ok {
			return
		}
		movies, ok := resolveCollection(idx, r.PathValue("key"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		u := h.urlsFor(r, idx)
		items := make([]Metadata, len(movies))
		for i, m := range movies {
			items[i] = buildMovieMetadata(root, u, m)
		}
		h.writePage(w, r, root, items, parsePaging(r))
	}
}

// matchCollections answers a type 18 match: the collection a plex://
// collection GUID or one of this provider's collection GUIDs names, else
// the collections whose name is the request's title.
func (h *handler) matchCollections(ctx context.Context, root rootDef, u urls, idx *projection.Index, req matchRequest) []Metadata {
	var keys []string
	switch {
	case strings.HasPrefix(req.Guid, plexScheme+"://"+metadataTypeCollection+"/"):
		keys = []string{strings.TrimPrefix(req.Guid, plexScheme+"://"+metadataTypeCollection+"/")}
	case strings.HasPrefix(req.Guid, root.identifier+"://"+metadataTypeCollection+"/"):
		keys = []string{strings.TrimPrefix(req.Guid, root.identifier+"://"+metadataTypeCollection+"/")}
	case req.Title != "":
		want := release.TitleNorm(req.Title)
		seen := map[int64]bool{}
		for _, m := range idx.Movies() {
			md := m.Status.Metadata
			if md == nil || md.Collection == nil || seen[md.Collection.TmdbID] || release.TitleNorm(md.Collection.Name) != want {
				continue
			}
			seen[md.Collection.TmdbID] = true
			keys = append(keys, collectionKeyPrefix+strconv.FormatInt(md.Collection.TmdbID, 10))
		}
	}
	var out []Metadata
	for _, k := range keys {
		if movies, ok := resolveCollection(idx, k); ok {
			out = append(out, h.buildCollectionMetadata(ctx, root, u, movies))
		}
	}
	return out
}
