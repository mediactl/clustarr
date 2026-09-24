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

package torznab_test

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/newznab"
	"github.com/mediactl/clustarr/pkg/torznab"
)

func TestWriteCapsRoundTrips(t *testing.T) {
	f, err := os.Open("../../test/data/torznab/caps.xml")
	require.NoError(t, err)
	want, err := torznab.ParseCaps(f)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	var buf bytes.Buffer
	require.NoError(t, torznab.WriteCaps(&buf, want))
	require.True(t, strings.HasPrefix(buf.String(), `<?xml version="1.0" encoding="UTF-8"?>`))

	got, err := torznab.ParseCaps(&buf)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestWriteCapsUsenetVariantRoundTrips(t *testing.T) {
	f, err := os.Open("../../test/data/newznab/caps.xml")
	require.NoError(t, err)
	want, err := torznab.ParseCaps(f)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	var buf bytes.Buffer
	require.NoError(t, torznab.WriteCaps(&buf, want))

	got, err := torznab.ParseCaps(&buf)
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestWriteResultsRoundTrips(t *testing.T) {
	rels := parseResultsFile(t, "../../test/data/torznab/search_with_attrs.xml")

	var buf bytes.Buffer
	require.NoError(t, torznab.WriteResults(&buf, rels))
	require.Contains(t, buf.String(), `xmlns:torznab="http://torznab.com/schemas/2015/feed"`)

	got, err := torznab.ParseResults(&buf)
	require.NoError(t, err)
	require.Equal(t, rels, got)
}

func TestWriteResultsUsenetRoundTrips(t *testing.T) {
	rels := parseResultsFile(t, "../../test/data/newznab/usenet_search.xml")

	var buf bytes.Buffer
	require.NoError(t, torznab.WriteResults(&buf, rels))

	got, err := torznab.ParseResults(&buf)
	require.NoError(t, err)
	require.Equal(t, rels, got)
}

func TestWriteErrorRoundTrips(t *testing.T) {
	want := &torznab.Error{Code: torznab.ErrRequestLimitReached, Description: "Request limit reached"}

	var buf bytes.Buffer
	require.NoError(t, torznab.WriteError(&buf, want))

	got, err := torznab.ParseError(&buf)
	require.NoError(t, err)
	require.Equal(t, want.Code, got.Code)
	require.Equal(t, want.Description, got.Description)
}

// TestWriteResultsSynthesizesAttrsFromTypedFieldsAlone exercises WriteResults'
// fallback path: a Release built directly (as pkg/cardigann's Task B7 will
// do), with typed fields set but no Attrs at all, still writes wire attrs
// that a subsequent ParseResults can recover the typed values from.
func TestWriteResultsSynthesizesAttrsFromTypedFieldsAlone(t *testing.T) {
	seeders := int32(9)
	dvf := 0.5
	rel := torznab.Release{
		Title:                "Synthesized.Release-GRP",
		GUID:                 "guid-1",
		Size:                 42,
		Categories:           []newznab.CategoryID{newznab.CatMovies},
		Seeders:              &seeders,
		InfoHash:             "abc123",
		DownloadVolumeFactor: &dvf,
		IDs:                  map[string]string{"tmdb": "603"},
	}

	var buf bytes.Buffer
	require.NoError(t, torznab.WriteResults(&buf, []torznab.Release{rel}))

	got, err := torznab.ParseResults(&buf)
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "Synthesized.Release-GRP", got[0].Title)
	require.NotNil(t, got[0].Seeders)
	require.Equal(t, int32(9), *got[0].Seeders)
	require.Equal(t, "abc123", got[0].InfoHash)
	require.NotNil(t, got[0].DownloadVolumeFactor)
	require.Equal(t, 0.5, *got[0].DownloadVolumeFactor)
	require.Equal(t, "603", got[0].IDs["tmdb"])
	require.Equal(t, []newznab.CategoryID{newznab.CatMovies}, got[0].Categories)
}

// The non-video fields round-trip both ways: from a parsed feed (verbatim
// through Attrs, repeated author included) and from a Release built in code,
// as pkg/cardigann builds one, where only the typed field is set.
func TestWriteResultsRoundTripsNonVideoFields(t *testing.T) {
	f, err := os.Open("../../test/data/torznab/nonvideo_search.xml")
	require.NoError(t, err)
	want, err := torznab.ParseResults(f)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	var buf bytes.Buffer
	require.NoError(t, torznab.WriteResults(&buf, want))
	got, err := torznab.ParseResults(&buf)
	require.NoError(t, err)
	require.Equal(t, want, got)

	built := []torznab.Release{{
		Title: "Some.Artist-Some.Album-2020-FLAC", GUID: "g",
		Artist: "Some Artist", Album: "Some Album", Author: "An Author", Publisher: "A Label",
	}}
	buf.Reset()
	require.NoError(t, torznab.WriteResults(&buf, built))
	back, err := torznab.ParseResults(&buf)
	require.NoError(t, err)
	require.Len(t, back, 1)
	require.Equal(t, "Some Artist", back[0].Artist)
	require.Equal(t, "Some Album", back[0].Album)
	require.Equal(t, "An Author", back[0].Author)
	require.Equal(t, "A Label", back[0].Publisher)
}
