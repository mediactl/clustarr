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
	"fmt"
	"net/url"
	"strings"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

const collectionGUIDPrefix = "plex://collection/"

// MovieCollection reads the movie's own metadata, GET
// /library/metadata/<id>, for the collection Plex files it under: its
// Collection[].guid is plex://collection/<24 hex> (recorded 2026-10-06,
// Back to the Future). A movie in no collection is "".
func (c *Client) MovieCollection(ctx context.Context, moviePlexID string) (string, error) {
	ctx, span := tracing.Start(ctx, "metadata.plex.MovieCollection")
	defer span.End()
	if !idPattern.MatchString(moviePlexID) {
		return "", fmt.Errorf("plex: %q is not a Plex id", moviePlexID)
	}
	var out struct {
		MediaContainer struct {
			Metadata []struct {
				Collection []struct {
					GUID string `json:"guid"`
				} `json:"Collection"`
			} `json:"Metadata"`
		} `json:"MediaContainer"`
	}
	if err := c.h.GetJSON(ctx, c.baseURL+"/library/metadata/"+url.PathEscape(moviePlexID), c.header(), &out); err != nil {
		tracing.RecordError(span, err)
		return "", err
	}
	for _, m := range out.MediaContainer.Metadata {
		for _, col := range m.Collection {
			if id, ok := strings.CutPrefix(col.GUID, collectionGUIDPrefix); ok && idPattern.MatchString(id) {
				return id, nil
			}
		}
	}
	return "", nil
}

var _ metadata.PlexCollectionProvider = (*Client)(nil)
