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

package tvdb

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// imdbID is an IMDb title id, the only remote id the search is asked by: a
// bare number could be any source's.
var imdbID = regexp.MustCompile(`^tt\d{1,10}$`)

// remoteIDResponse is GET /search/remoteid/{id}'s body, shaped from
// responses recorded from the live API (test/data/metadata/tvdb/
// search_remoteid_*.json): one row per record the id names, a series, a
// movie or a person, and data null for an id TheTVDB does not know.
type remoteIDResponse struct {
	Data []struct {
		Series *struct {
			ID int64 `json:"id"`
		} `json:"series"`
	} `json:"data"`
}

// Resolve implements metadata.IDResolver: a series' TVDB id from its IMDb
// id, through TheTVDB's remote-id search (GET /search/remoteid/{imdb id}).
// TMDB lists no TVDB id for a good share of anime that are not Japanese
// television, and the anime crosswalks do not carry them either: 14 of the
// owner's MAL list of 164 shows had none (2026-10-21). Anything else asks
// nothing: another kind, a series that has its TVDB id, or one with no IMDb
// id. A movie's or a person's record under the id is no series', and two
// series under one id are not chosen between.
func (c *Client) Resolve(ctx context.Context, kind commonv1.MediaKind, ids metadata.ExternalIDs) (metadata.ExternalIDs, error) {
	imdb := ids[metadata.KeyIMDb]
	if kind != commonv1.MediaKindSeries || ids[metadata.KeyTVDB] != "" || !imdbID.MatchString(imdb) {
		return nil, nil
	}
	ctx, span := tracing.Start(ctx, "metadata.tvdb.Resolve")
	defer span.End()

	var raw remoteIDResponse
	if err := c.doRequest(ctx, http.MethodGet, "/search/remoteid/"+imdb, &raw); err != nil {
		if errors.Is(err, metadata.ErrNotFound) {
			return nil, nil
		}
		tracing.RecordError(span, err)
		return nil, err
	}
	var found int64
	for _, d := range raw.Data {
		if d.Series == nil || d.Series.ID <= 0 || d.Series.ID == found {
			continue
		}
		if found != 0 {
			return nil, nil
		}
		found = d.Series.ID
	}
	if found == 0 {
		return nil, nil
	}
	return metadata.ExternalIDs{metadata.KeyTVDB: strconv.FormatInt(found, 10)}, nil
}

var _ metadata.IDResolver = (*Client)(nil)
