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

package views_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/views"
)

// What the Add New and Edit dialogs' selects offer is what ui/actions
// admits: the CRDs' enumerations, spelled once in each.
func TestAddAndEditOptionsAreTheActionsChoices(t *testing.T) {
	ch := actions.Choices()
	want := map[string][]string{
		"monitorNewItems (artist)": ch.ArtistMonitorNew, "monitorNewItems (author)": ch.AuthorMonitorNew,
		"comicKind": ch.ComicKinds, "specialVersion": ch.ComicSpecialVersions, "region": ch.AudiobookRegions,
		"primaryTypes": ch.MusicPrimaryTypes, "secondaryTypes": ch.MusicSecondaryTypes, "releaseStatuses": ch.MusicReleaseStatuses,
	}
	got := views.EditOptions()
	require.Len(t, got, len(want))
	for field, values := range want {
		opts, ok := got[field]
		require.True(t, ok, field)
		var have []string
		for _, o := range opts {
			require.NotEmpty(t, o.Label, "%s %q has a label", field, o.Value)
			have = append(have, o.Value)
		}
		require.Equal(t, values, have, field)
	}
}
