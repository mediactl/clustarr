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

// Package imdbcsv is a pkg/importlist provider for an IMDb list/watchlist
// exported as CSV: Parse reads the export format directly, and List fetches
// one by URL (the ImportList controller resolves the CRD's ConfigMapRef to
// a URL, or serves the ConfigMap's content itself, before constructing a
// List's Config).
package imdbcsv

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/importlist"
)

// titleTypeKind maps IMDb's "Title Type" export column to a catalog kind.
// A value not listed here is skipped, never guessed, per the scanner's
// never-guess rule (CLAUDE.md, "The scanner never guesses").
var titleTypeKind = map[string]commonv1.MediaKind{
	"movie":        commonv1.MediaKindMovie,
	"tvMovie":      commonv1.MediaKindMovie,
	"video":        commonv1.MediaKindMovie,
	"tvSeries":     commonv1.MediaKindSeries,
	"tvMiniSeries": commonv1.MediaKindSeries,
}

// Parse reads r as an IMDb list/watchlist CSV export and returns every row
// whose "Title Type" column maps to kind. Column order is read from the
// header row rather than assumed, since IMDb has changed the export's
// column set before; a row whose Title Type is not in titleTypeKind is
// skipped, not guessed. Parse never panics: malformed, truncated or empty
// input is reported as an error.
func Parse(r io.Reader, kind commonv1.MediaKind) ([]importlist.Item, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1 // don't hard-fail if the column count has changed

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("importlist/imdbcsv: read header: %w", err)
	}
	col := make(map[string]int, len(header))
	for i, name := range header {
		col[name] = i
	}
	for _, want := range []string{"Const", "Title", "Year", "Title Type"} {
		if _, ok := col[want]; !ok {
			return nil, fmt.Errorf("importlist/imdbcsv: missing column %q", want)
		}
	}

	var items []importlist.Item
	for {
		row, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("importlist/imdbcsv: read row: %w", err)
		}
		if !rowHasColumns(row, col) {
			continue // ragged row shorter than the columns this func reads; skip rather than index out of range
		}
		rowKind, ok := titleTypeKind[row[col["Title Type"]]]
		if !ok || rowKind != kind {
			continue
		}
		year, _ := strconv.Atoi(row[col["Year"]])
		items = append(items, importlist.Item{
			Title:       row[col["Title"]],
			Year:        int32(year),
			ExternalIDs: importlist.ExternalIDs{IMDb: row[col["Const"]]},
		})
	}
	return items, nil
}

// rowHasColumns reports whether row is long enough to hold every column
// index in col. cr.FieldsPerRecord=-1 allows a row shorter than the header
// through encoding/csv without error, so Parse checks bounds itself rather
// than risk a panic indexing row.
func rowHasColumns(row []string, col map[string]int) bool {
	for _, idx := range col {
		if idx >= len(row) {
			return false
		}
	}
	return true
}
