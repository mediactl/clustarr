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
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

// Extras returns the extras Plex's metadata service holds for a movie,
// show or season: GET /library/metadata/<id>/extras, paged at the
// client's page size (the service refuses a page over 100 with 400 Bad
// Request, 2026-10-06). Each extra is the service's own JSON, unchanged,
// so every field PMS reads -- extraType, subtype, Media.url to Internet
// Video Archive -- reaches it. An item with none is an empty, non-nil
// list; a failed request is an error.
func (c *Client) Extras(ctx context.Context, plexID string) ([]json.RawMessage, error) {
	ctx, span := tracing.Start(ctx, "metadata.plex.Extras")
	defer span.End()
	if !idPattern.MatchString(plexID) {
		return nil, fmt.Errorf("plex: %q is not a Plex id", plexID)
	}
	path := c.baseURL + "/library/metadata/" + url.PathEscape(plexID) + "/extras"
	all := []json.RawMessage{}
	for range maxPages {
		q := url.Values{
			"X-Plex-Container-Start": {strconv.Itoa(len(all))},
			"X-Plex-Container-Size":  {strconv.Itoa(c.pageSize)},
		}
		var out struct {
			MediaContainer struct {
				TotalSize int               `json:"totalSize"`
				Metadata  []json.RawMessage `json:"Metadata"`
			} `json:"MediaContainer"`
		}
		if err := c.h.GetJSON(ctx, path+"?"+q.Encode(), c.header(), &out); err != nil {
			tracing.RecordError(span, err)
			return nil, err
		}
		mc := out.MediaContainer
		all = append(all, mc.Metadata...)
		if len(mc.Metadata) == 0 || len(all) >= mc.TotalSize {
			return all, nil
		}
	}
	return nil, fmt.Errorf("plex: %s extras: more than %d pages", plexID, maxPages)
}

var _ metadata.PlexExtrasProvider = (*Client)(nil)
