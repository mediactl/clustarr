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

package schema_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events/schema"
)

// TestScanObservationCreateOfEachKindRoundTrips: a scan observation
// proposing a create of each kind the applier makes -- a movie by its TMDB
// id, a series by its TheTVDB id, and since the 2026-11-10 non-video
// libraries design's D16 an artist by its MusicBrainz id, an author by its
// Open Library id and an audiobook by its ASIN and region, each with the
// folder the scan resolved -- travels under the v1 name with every id and
// the folder intact, and a folder the scan could not identify travels as
// an unmatched fact marked Folder. The payload keeps its name: a receiver
// decoding "import.ScanObservation.v1" reads the new fields, and a
// producer that sets none sends none (omitempty), so an older manager's
// and agent's observations still agree.
func TestScanObservationCreateOfEachKindRoundTrips(t *testing.T) {
	seen := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	base := func(kind, rel string, c schema.ItemCreate) schema.ScanObservation {
		return schema.ScanObservation{
			Scan:       schema.Ref{Namespace: "media", Name: "scan-1"},
			RootFolder: schema.Ref{Namespace: "media", Name: "music"},
			Kind:       schema.ScanObservationPresent,
			Path:       rel,
			Attribution: &schema.ScanAttribution{
				Kind: kind, Name: c.Name, ProviderID: providerIDOf(c), Evidence: "folder", Create: &c,
			},
			SeenAt: seen,
		}
	}
	for _, tc := range []struct {
		name string
		in   schema.ScanObservation
		want map[string]any // the create's wire keys and values, so a renamed field shows
	}{
		{"movie", base("movie", "Heat (1995)/Heat (1995).mkv", schema.ItemCreate{
			Kind: "movie", Name: "heat-949", TmdbID: 949, Title: "Heat", Year: 1995, QualityProfileRef: "hd", RootFolderRef: "movies",
		}), map[string]any{"kind": "movie", "tmdbID": float64(949)}},
		{"series", base("series", "Bluey (2018)/Season 01/Bluey - S01E01.mkv", schema.ItemCreate{
			Kind: "series", Name: "bluey-355003", TvdbID: 355003, Title: "Bluey", Year: 2018, Folder: "Bluey (2018)",
		}), map[string]any{"kind": "series", "tvdbID": float64(355003), "folder": "Bluey (2018)"}},
		{"artist", base("artist", "2Pac/All Eyez on Me/01 - Ambitionz az a Ridah.flac", schema.ItemCreate{
			Kind: "artist", Name: "2pac-382f1005", MusicBrainzID: "382f1005-e9ab-42fd-a9a8-6c8d27fb2b2e", Title: "2Pac", Folder: "2Pac",
		}), map[string]any{"kind": "artist", "musicBrainzID": "382f1005-e9ab-42fd-a9a8-6c8d27fb2b2e", "folder": "2Pac"}},
		{"author", base("author", "Terry Pratchett/Guards! Guards!/Guards! Guards!.epub", schema.ItemCreate{
			Kind: "author", Name: "terry-pratchett-ol25712a", OpenLibraryID: "OL25712A", Title: "Terry Pratchett", Folder: "Terry Pratchett",
		}), map[string]any{"kind": "author", "openLibraryID": "OL25712A", "folder": "Terry Pratchett"}},
		{"audiobook", base("audiobook", "Terry Pratchett/Guards! Guards! [B002V5D7KE]/Part 01.m4b", schema.ItemCreate{
			Kind: "audiobook", Name: "guards-guards-b002v5d7ke", ASIN: "B002V5D7KE", Region: "us", Title: "Guards! Guards!",
			Folder: "Terry Pratchett/Guards! Guards! [B002V5D7KE]",
		}), map[string]any{"kind": "audiobook", "asin": "B002V5D7KE", "region": "us", "folder": "Terry Pratchett/Guards! Guards! [B002V5D7KE]"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name, data, err := schema.Encode(tc.in)
			require.NoError(t, err)
			assert.Equal(t, "import.ScanObservation.v1", name, "the payload keeps its name")

			var wire struct {
				Attribution struct {
					Create map[string]any `json:"create"`
				} `json:"attribution"`
			}
			require.NoError(t, json.Unmarshal(data, &wire))
			for k, v := range tc.want {
				assert.Equal(t, v, wire.Attribution.Create[k], "wire key %q", k)
			}
			for _, k := range []string{"musicBrainzID", "openLibraryID", "asin", "region", "folder"} {
				if _, wanted := tc.want[k]; !wanted {
					assert.NotContains(t, wire.Attribution.Create, k, "an id of another kind is omitted, not sent empty")
				}
			}

			var out schema.ScanObservation
			require.NoError(t, schema.Decode(name, data, &out))
			assert.Equal(t, tc.in, out)
		})
	}

	t.Run("unmatched folder", func(t *testing.T) {
		in := schema.ScanObservation{
			Scan:       schema.Ref{Namespace: "media", Name: "scan-1"},
			RootFolder: schema.Ref{Namespace: "media", Name: "music"},
			Kind:       schema.ScanObservationUnmatched,
			Path:       "Nirvana",
			Unmatched: &schema.UnmatchedFact{
				Code:   "ambiguous_title",
				Reason: "two MusicBrainz artists are named Nirvana at the search's top score",
				Candidates: []string{
					"Nirvana (90s US grunge band) [5b11f4ce-a62d-471e-81fc-a69a8278c7da]",
					"Nirvana (60s band from the UK) [9282c8b4-ca0b-4c6b-b7e3-b4f1d9e3f8b1]",
				},
				Folder: true,
			},
			SeenAt: seen,
		}
		name, data, err := schema.Encode(in)
		require.NoError(t, err)
		var out schema.ScanObservation
		require.NoError(t, schema.Decode(name, data, &out))
		assert.Equal(t, in, out)
		assert.True(t, out.Unmatched.Folder)
	})
}

func providerIDOf(c schema.ItemCreate) string {
	switch {
	case c.MusicBrainzID != "":
		return c.MusicBrainzID
	case c.OpenLibraryID != "":
		return c.OpenLibraryID
	case c.ASIN != "":
		return c.ASIN
	}
	return ""
}
