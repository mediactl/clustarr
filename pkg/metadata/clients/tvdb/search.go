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
	"net/http"
	"net/url"
	"strconv"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// searchLimit is how many series one search asks TheTVDB for.
const searchLimit = 20

// searchResponse is GET /search's body, shaped from a response recorded
// from the live API (test/data/metadata/tvdb/search_breaking_bad.json).
type searchResponse struct {
	Data []struct {
		TVDBID       string            `json:"tvdb_id"`
		Name         string            `json:"name"`
		Year         string            `json:"year"`
		ImageURL     string            `json:"image_url"`
		Thumbnail    string            `json:"thumbnail"`
		Translations map[string]string `json:"translations"`
	} `json:"data"`
}

// SearchSeries searches TheTVDB's series index by title
// (GET /search?query=&type=series). The title is the hit's English
// translation when it has one. A hit with no usable tvdb id is dropped:
// Add New keys a Series by it.
func (c *Client) SearchSeries(ctx context.Context, q string) ([]metadata.SearchHit, error) {
	ctx, span := tracing.Start(ctx, "metadata.tvdb.SearchSeries")
	defer span.End()

	path := "/search?query=" + url.QueryEscape(q) + "&type=series&limit=" + strconv.Itoa(searchLimit)
	var raw searchResponse
	if err := c.doRequest(ctx, http.MethodGet, path, &raw); err != nil {
		tracing.RecordError(span, err)
		logging.FromContext(ctx).ErrorContext(ctx, "tvdb: series search failed", "error", err)
		return nil, err
	}
	hits := make([]metadata.SearchHit, 0, len(raw.Data))
	for _, d := range raw.Data {
		if id, err := strconv.ParseInt(d.TVDBID, 10, 64); err != nil || id <= 0 {
			continue
		}
		title := d.Name
		if t := d.Translations[titleLanguage]; t != "" {
			title = t
		}
		year, _ := strconv.ParseInt(d.Year, 10, 32)
		// A search card shows the poster at thumbnail size, and TheTVDB's
		// full poster is ~450 KB: the thumbnail, when there is one.
		poster := d.Thumbnail
		if poster == "" {
			poster = d.ImageURL
		}
		hits = append(hits, metadata.SearchHit{
			IDs:    metadata.ExternalIDs{metadata.KeyTVDB: d.TVDBID},
			Title:  title,
			Year:   int32(year),
			Poster: poster,
		})
	}
	return hits, nil
}

var _ metadata.SeriesSearcher = (*Client)(nil)
