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
	"strconv"

	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// plexCollection is the Plex id of the collection the movie with Plex id
// moviePlexID belongs to, from the first PlexCollectionProvider that
// answers; "" when Plex files it in none. A movie with no Plex id, or a
// lookup every provider failed, keeps prior: a failed refresh never
// releases the collection's plex:// GUID.
func plexCollection(ctx context.Context, reg *pkgmetadata.Registry, moviePlexID, prior string) string {
	if reg == nil || moviePlexID == "" {
		return prior
	}
	for _, p := range reg.Plex {
		c, ok := p.(pkgmetadata.PlexCollectionProvider)
		if !ok {
			continue
		}
		pCtx, span := tracing.Start(ctx, "metadata.PlexCollectionProvider.MovieCollection")
		id, err := c.MovieCollection(pCtx, moviePlexID)
		if err != nil {
			tracing.RecordError(span, err)
			span.End()
			continue
		}
		span.End()
		return id
	}
	return prior
}

// withCollectionSummary fills c's summary from the first movie provider
// that is a CollectionProvider, when c has none: a movie's own record
// names its collection but not its summary. A failure leaves c as it was.
func withCollectionSummary(ctx context.Context, reg *pkgmetadata.Registry, c *pkgmetadata.Collection) {
	if reg == nil || c == nil || c.Overview != "" || c.IDs[pkgmetadata.KeyTMDB] == "" {
		return
	}
	for _, p := range reg.Movies {
		cp, ok := p.(pkgmetadata.CollectionProvider)
		if !ok {
			continue
		}
		pCtx, span := tracing.Start(ctx, "metadata.CollectionProvider.Collection")
		got, err := cp.Collection(pCtx, c.IDs[pkgmetadata.KeyTMDB])
		if err != nil {
			tracing.RecordError(span, err)
			span.End()
			continue
		}
		span.End()
		c.Overview = got.Overview
		if len(c.Images) == 0 {
			c.Images = got.Images
		}
		return
	}
}

// knownPlexCollection is obj's current status.metadata.collection.plexID,
// read before this refresh started, when it is for the same TMDB
// collection as c; "" otherwise.
func knownPlexCollection(obj client.Object, c *pkgmetadata.Collection) string {
	m, ok := obj.(*catalogv1alpha1.Movie)
	if !ok || m.Status.Metadata == nil || m.Status.Metadata.Collection == nil || c == nil {
		return ""
	}
	if strconv.FormatInt(m.Status.Metadata.Collection.TmdbID, 10) != c.IDs[pkgmetadata.KeyTMDB] {
		return ""
	}
	return m.Status.Metadata.Collection.PlexID
}
