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

package imageset_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/imageset"
)

// MediUX's export for Cyberpunk: Edgerunners (set 38985, by RandalPink87),
// as the owner gave it: the show's poster, each season's poster and each
// episode's thumbnail, keyed by the show's TheTVDB id; no background.
const edgerunners = `384541: # TVDB id for Cyberpunk: Edgerunners. Set by RandalPink87 on MediUX. https://mediux.pro/sets/38985
  url_poster: https://api.mediux.pro/assets/3cec73e6-4f43-4b49-b7b4-2ca132b8e49a
  seasons:
    1:
      url_poster: https://api.mediux.pro/assets/4aa6df5a-66a7-411b-83fb-960a449f44e0
      episodes:
        1:
          url_poster: https://api.mediux.pro/assets/a0e12102-0983-4eb9-993f-5f8bf49ee7b8
        2:
          url_poster: https://api.mediux.pro/assets/0df0d794-adf0-4f76-b7e4-46f4e01a4758
        10:
          url_poster: https://api.mediux.pro/assets/7734bc4b-9118-43b1-bee5-c40eb55f5f22
    2:
      url_poster: https://api.mediux.pro/assets/6dee8b27-cddf-404c-954a-b3561b54c6ab
      episodes:
        1:
          url_poster: https://api.mediux.pro/assets/250cb54e-8164-4429-9ef2-11563863b2cc
`

func TestParseReadsMediUXsExport(t *testing.T) {
	entries, err := imageset.Parse(edgerunners)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	e := entries[0]
	require.Equal(t, "384541", e.ID)
	require.Equal(t, []catalogv1.ArtworkOverride{
		{Type: catalogv1.ImageTypePoster, URL: "https://api.mediux.pro/assets/3cec73e6-4f43-4b49-b7b4-2ca132b8e49a"},
	}, e.Artwork, "a poster and no background")
	one, two, ten := int32(1), int32(2), int32(10)
	require.Equal(t, []catalogv1.SeasonArtworkOverride{
		{Season: 1, Type: catalogv1.ImageTypePoster, URL: "https://api.mediux.pro/assets/4aa6df5a-66a7-411b-83fb-960a449f44e0"},
		{Season: 1, Episode: &one, Type: catalogv1.ImageTypeScreenshot, URL: "https://api.mediux.pro/assets/a0e12102-0983-4eb9-993f-5f8bf49ee7b8"},
		{Season: 1, Episode: &two, Type: catalogv1.ImageTypeScreenshot, URL: "https://api.mediux.pro/assets/0df0d794-adf0-4f76-b7e4-46f4e01a4758"},
		{Season: 1, Episode: &ten, Type: catalogv1.ImageTypeScreenshot, URL: "https://api.mediux.pro/assets/7734bc4b-9118-43b1-bee5-c40eb55f5f22"},
		{Season: 2, Type: catalogv1.ImageTypePoster, URL: "https://api.mediux.pro/assets/6dee8b27-cddf-404c-954a-b3561b54c6ab"},
		{Season: 2, Episode: &one, Type: catalogv1.ImageTypeScreenshot, URL: "https://api.mediux.pro/assets/250cb54e-8164-4429-9ef2-11563863b2cc"},
	}, e.SeasonArtwork, "seasons then episodes, in number order, episode 10 after 2")
	got, ok := imageset.For(entries, "384541")
	require.True(t, ok)
	require.Equal(t, e, got)
	got, ok = imageset.For(entries, "999")
	require.True(t, ok, "the only entry stands for an item whose id it does not name")
	require.Equal(t, e, got)
}

func TestParseReadsThePosterDBIdsAndStringKeys(t *testing.T) {
	entries, err := imageset.Parse(`
"603":
  tpdb_poster: 12345
  tpdb_background: "67890"
  seasons:
    "0":
      tpdb_poster: 1
"604": {}
`)
	require.NoError(t, err)
	require.Len(t, entries, 1, "an entry with no image is left out")
	require.Equal(t, []catalogv1.ArtworkOverride{
		{Type: catalogv1.ImageTypePoster, URL: "https://theposterdb.com/api/assets/12345"},
		{Type: catalogv1.ImageTypeFanart, URL: "https://theposterdb.com/api/assets/67890"},
	}, entries[0].Artwork)
	require.Equal(t, []catalogv1.SeasonArtworkOverride{{Season: 0, Type: catalogv1.ImageTypePoster, URL: "https://theposterdb.com/api/assets/1"}}, entries[0].SeasonArtwork)
	_, ok := imageset.For(append(entries, imageset.Entry{ID: "x"}), "nope")
	require.False(t, ok, "two entries and neither is the item's")
}

func TestParseRefusesWhatIsNoSet(t *testing.T) {
	_, err := imageset.Parse("")
	require.True(t, errors.Is(err, imageset.ErrNoEntries))
	_, err = imageset.Parse("1:\n  seasons:\n    x:\n      url_poster: https://a/b\n")
	require.ErrorContains(t, err, "season")
	_, err = imageset.Parse("1:\n  seasons:\n    1:\n      episodes:\n        0:\n          url_poster: https://a/b\n")
	require.ErrorContains(t, err, "episode")
	_, err = imageset.Parse("- not a map")
	require.Error(t, err)
}
