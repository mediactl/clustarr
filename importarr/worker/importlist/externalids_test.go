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

package importlist_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	importlistpkg "github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/metadata"

	worker "github.com/mediactl/clustarr/importarr/worker/importlist"
)

func TestToMetadataExternalIDsCarriesOnlyNonEmptyFields(t *testing.T) {
	in := importlistpkg.ExternalIDs{
		IMDb: "tt0113277",
		TMDB: "949",
		// TVDB deliberately left empty.
	}
	got := worker.ToMetadataExternalIDs(in)
	assert.Equal(t, metadata.ExternalIDs{
		metadata.KeyIMDb: "tt0113277",
		metadata.KeyTMDB: "949",
	}, got)
}

func TestToMetadataExternalIDsEveryField(t *testing.T) {
	in := importlistpkg.ExternalIDs{
		IMDb:        "tt0113277",
		TMDB:        "949",
		TVDB:        "1396",
		MusicBrainz: "f5093c22-e18e-4dc4-92be-7ba6dfc32a3f",
		ISBN:        "9780316769488",
		ASIN:        "B002TXNI2K",
		ComicVine:   "4050-12345",
		AniList:     "101922",
	}
	got := worker.ToMetadataExternalIDs(in)
	want := metadata.ExternalIDs{
		metadata.KeyIMDb:           "tt0113277",
		metadata.KeyTMDB:           "949",
		metadata.KeyTVDB:           "1396",
		metadata.KeyMBReleaseGroup: "f5093c22-e18e-4dc4-92be-7ba6dfc32a3f",
		metadata.KeyISBN13:         "9780316769488",
		metadata.KeyASIN:           "B002TXNI2K",
		metadata.KeyComicVine:      "4050-12345",
		metadata.KeyAniList:        "101922",
	}
	assert.Equal(t, want, got)
}

func TestExternalIDsRoundTrip(t *testing.T) {
	in := importlistpkg.ExternalIDs{
		IMDb:        "tt0113277",
		TMDB:        "949",
		TVDB:        "1396",
		MusicBrainz: "f5093c22-e18e-4dc4-92be-7ba6dfc32a3f",
		ISBN:        "9780316769488",
		ASIN:        "B002TXNI2K",
		ComicVine:   "4050-12345",
		AniList:     "101922",
	}
	got := worker.FromMetadataExternalIDs(worker.ToMetadataExternalIDs(in))
	assert.Equal(t, in, got)
}

func TestFromMetadataExternalIDsDropsUnrecognisedKeys(t *testing.T) {
	in := metadata.ExternalIDs{
		metadata.KeyIMDb: "tt0113277",
		"wikidata":       "Q83495", // not in pkg/importlist.ExternalIDs' vocabulary
	}
	got := worker.FromMetadataExternalIDs(in)
	assert.Equal(t, importlistpkg.ExternalIDs{IMDb: "tt0113277"}, got)
}
