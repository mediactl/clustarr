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

package openlibrary

import (
	"context"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/obs/tracing"
)

var (
	authorKey = regexp.MustCompile(`^OL\d+A$`)
	yearIn    = regexp.MustCompile(`\b(\d{4})\b`)
)

// SearchAuthors searches Open Library's author index by name
// (GET /search/authors.json). The year is the first four-digit number in
// the free-text birth_date, when there is one; the poster is the author
// photo Open Library serves by key. A doc whose key is not an author key
// is dropped: Add New keys an Author by it.
func (c *Client) SearchAuthors(ctx context.Context, q string) ([]metadata.SearchHit, error) {
	ctx, span := tracing.Start(ctx, "metadata.openlibrary.SearchAuthors")
	defer span.End()

	var raw struct {
		Docs []struct {
			Key       string `json:"key"`
			Name      string `json:"name"`
			BirthDate string `json:"birth_date"`
		} `json:"docs"`
	}
	path := "/search/authors.json?q=" + url.QueryEscape(q) + "&limit=20"
	if err := c.doGet(ctx, path, &raw); err != nil {
		tracing.RecordError(span, err)
		logging.FromContext(ctx).ErrorContext(ctx, "openlibrary: author search failed", "error", err)
		return nil, err
	}
	hits := make([]metadata.SearchHit, 0, len(raw.Docs))
	for _, d := range raw.Docs {
		key := strings.TrimPrefix(d.Key, "/authors/")
		if !authorKey.MatchString(key) {
			continue
		}
		hit := metadata.SearchHit{
			IDs:    metadata.ExternalIDs{metadata.KeyOpenLibraryAuthor: key},
			Title:  d.Name,
			Poster: "https://covers.openlibrary.org/a/olid/" + key + "-M.jpg",
		}
		if m := yearIn.FindStringSubmatch(d.BirthDate); m != nil {
			y, _ := strconv.ParseInt(m[1], 10, 32)
			hit.Year = int32(y)
		}
		hits = append(hits, hit)
	}
	return hits, nil
}

var _ metadata.AuthorSearcher = (*Client)(nil)
