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

package openlibrary_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/clients/openlibrary"
)

// TestSearchAuthorsMapsTheRecordedResponse runs the search against a
// response recorded from Open Library's live /search/authors.json
// (2026-09-29).
func TestSearchAuthorsMapsTheRecordedResponse(t *testing.T) {
	body, err := os.ReadFile("../../../../test/data/metadata/openlibrary/search_authors_jane_austen.json")
	require.NoError(t, err)
	var gotPath, gotQ string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQ = r.URL.Path, r.URL.Query().Get("q")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	c := openlibrary.New("clustarr-test", srv.Client(), srv.URL, metadata.NewLimiter(1000, 1))

	hits, err := c.SearchAuthors(context.Background(), "jane austen")
	require.NoError(t, err)
	require.Equal(t, "/search/authors.json", gotPath)
	require.Equal(t, "jane austen", gotQ)
	require.Len(t, hits, 3)
	require.Equal(t, "OL21594A", hits[0].IDs[metadata.KeyOpenLibraryAuthor])
	require.Equal(t, "Jane Austen", hits[0].Title)
	require.Equal(t, int32(1775), hits[0].Year)
	require.Equal(t, "https://covers.openlibrary.org/a/olid/OL21594A-M.jpg", hits[0].Poster)
	require.Zero(t, hits[1].Year, "a null birth date is no year")
}

// TestSearchAuthorsKeepsOnlyAuthorKeys: an "/authors/"-prefixed key is
// trimmed, and anything that is not an author key is dropped.
func TestSearchAuthorsKeepsOnlyAuthorKeys(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"docs":[{"key":"/authors/OL1A","name":"Prefixed","birth_date":"c. 1900"},{"key":"OL2W","name":"A work"},{"key":"","name":"Empty"},{"key":"OL3A","name":"No date"}]}`))
	}))
	t.Cleanup(srv.Close)
	c := openlibrary.New("clustarr-test", srv.Client(), srv.URL, metadata.NewLimiter(1000, 1))

	hits, err := c.SearchAuthors(context.Background(), "x")
	require.NoError(t, err)
	require.Len(t, hits, 2)
	require.Equal(t, "OL1A", hits[0].IDs[metadata.KeyOpenLibraryAuthor])
	require.Equal(t, int32(1900), hits[0].Year)
	require.Equal(t, "OL3A", hits[1].IDs[metadata.KeyOpenLibraryAuthor])
	require.Zero(t, hits[1].Year)
}
