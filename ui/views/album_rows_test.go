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

package views

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// An artist's albums are grouped by type in Lidarr's order -- Albums, EPs,
// Singles, Broadcasts, Other -- whatever order they came in, a type
// MusicBrainz spells otherwise or not at all grouped under Other, and an
// empty group left out.
func TestAlbumGroupsAreInLidarrsOrder(t *testing.T) {
	rows := []AlbumRow{
		{Name: "s1", Type: "single"}, {Name: "a1", Type: "album"}, {Name: "o1", Type: "mixtape"},
		{Name: "e1", Type: "ep", HasFile: true}, {Name: "a2", Type: "Album", HasFile: true}, {Name: "u1"},
	}
	groups := AlbumGroups(rows)
	keys := make([]string, 0, len(groups))
	for _, g := range groups {
		keys = append(keys, g.Key)
	}
	assert.Equal(t, []string{"album", "ep", "single", "other"}, keys)
	assert.Equal(t, "Albums", groups[0].Label)
	assert.Equal(t, []string{"a1", "a2"}, names(groups[0].Rows), "a group keeps the rows' order")
	assert.Equal(t, "1 / 2", groups[0].Count())
	assert.Equal(t, "1 / 1", groups[1].Count())
	assert.Equal(t, []string{"o1", "u1"}, names(groups[3].Rows), "an unknown type and none at all fall under Other")
	assert.Empty(t, AlbumGroups(nil))

	assert.Equal(t, "Album", AlbumRow{Type: "album"}.TypeLabel())
	assert.Equal(t, "EP", AlbumRow{Type: "EP"}.TypeLabel())
	assert.Equal(t, "Other", AlbumRow{Type: "mixtape"}.TypeLabel())
	assert.Equal(t, "", AlbumRow{}.TypeLabel(), "no badge before the metadata")
}

func names(rows []AlbumRow) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Name)
	}
	return out
}

// The album page's Status fact says a file's state in the movie page's
// words, the catalog's cutoff reason named; the album panel polls and
// grabs through the album's own routes.
func TestAlbumStatusLabelAndPanelRoutes(t *testing.T) {
	file := RowStatus{Kind: "file", Label: "FLAC · 10/10"}
	warn := RowStatus{Kind: "file", Label: "FLAC · 9/10", Warn: true}
	for name, tc := range map[string]struct {
		row  AlbumRow
		want string
	}{
		"downloaded":     {AlbumRow{Status: file, CutoffReason: "CutoffMet"}, "Downloaded"},
		"tracks missing": {AlbumRow{Status: warn, CutoffReason: "TracksMissing"}, "Cutoff Unmet: tracks missing"},
		"cutoff unmet":   {AlbumRow{Status: warn, CutoffReason: "CutoffUnmet"}, "Cutoff Unmet"},
		"another reason": {AlbumRow{Status: warn, CutoffReason: "ProfileUnresolved"}, "Cutoff Unmet: ProfileUnresolved"},
		"missing":        {AlbumRow{Status: RowStatus{Kind: "missing", Label: "Missing"}}, "Missing"},
		"pending":        {AlbumRow{Status: RowStatus{Kind: "pending", Label: "Pending"}}, "Pending"},
	} {
		assert.Equal(t, tc.want, albumStatusLabel(tc.row), name)
	}

	p := AlbumSearchPanel{
		SearchPanel: SearchPanel{Namespace: "default", Name: "wish-abc12"},
		Album:       AlbumRow{Namespace: "default", Name: "wish", Title: "Wish You Were Here"},
		ArtistTitle: "Pink Floyd",
	}
	assert.Equal(t, "Interactive Search - Pink Floyd - Wish You Were Here", p.Heading())
	assert.Equal(t, "/searches/default/wish-abc12", p.SearchPanel.URL(), "the shared panel's page is still the Search's")
}
