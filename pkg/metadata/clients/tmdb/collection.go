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

package tmdb

import (
	"context"
	"fmt"
	"strconv"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Collection fetches a movie collection, GET /collection/{id}, in the
// client's language: the summary a movie's belongs_to_collection does not
// carry, with the same poster and backdrop.
func (c *Client) Collection(ctx context.Context, id string) (*metadata.Collection, error) {
	ctx, span := tracing.Start(ctx, "metadata.tmdb.Collection")
	defer span.End()
	n, err := strconv.Atoi(id)
	if err != nil {
		err = fmt.Errorf("tmdb: invalid collection id %q: %w", id, err)
		tracing.RecordError(span, err)
		return nil, err
	}
	if err := c.limiter.Wait(ctx); err != nil {
		tracing.RecordError(span, err)
		return nil, err
	}
	d, err := c.raw.GetCollectionDetails(n, map[string]string{"language": c.languageFor(c.region)})
	if err != nil {
		mapped := c.mapError(err)
		tracing.RecordError(span, mapped)
		return nil, mapped
	}
	return &metadata.Collection{
		IDs:      metadata.ExternalIDs{metadata.KeyTMDB: strconv.FormatInt(d.ID, 10)},
		Title:    d.Name,
		Overview: d.Overview,
		Images:   collectionImages(d.PosterPath, d.BackdropPath),
	}, nil
}

// collectionImages are a collection's poster and backdrop as URLs; a null
// path is no image.
func collectionImages(poster, backdrop string) []metadata.Image {
	var out []metadata.Image
	if poster != "" {
		out = append(out, metadata.Image{Type: metadata.ImageTypePoster, URL: posterBaseURL + poster})
	}
	if backdrop != "" {
		out = append(out, metadata.Image{Type: metadata.ImageTypeFanart, URL: backdropBaseURL + backdrop})
	}
	return out
}

var _ metadata.CollectionProvider = (*Client)(nil)
