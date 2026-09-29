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

package names_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/names"
)

// TestItemNamesMatchTheLiveLibrary: the names below are real objects on
// the owner's cluster (2026-09-29), created by the rescan and import list.
// An item the UI adds must land on the same object, so these are goldens.
func TestItemNamesMatchTheLiveLibrary(t *testing.T) {
	require.Equal(t, "127-hours-d7430736bd", names.Movie("127 Hours", 44115))
	require.Equal(t, "blood-machines-68393b9b81", names.Movie("Blood Machines", 536517))
	require.Equal(t, "a-bona-fide-killer-82a2fb8eb6", names.Series("A Bona Fide Killer", 464930))
}

// TestArtistAndAuthorNamesHashKindAndID: the same shape as Movie and
// Series -- the title for reading, a hash of kind and id for identity -- so
// a retitled artist keeps its object and two kinds never collide on an id.
func TestArtistAndAuthorNamesHashKindAndID(t *testing.T) {
	a := names.Artist("Radiohead", "a74b1b7f-71a5-4011-9441-d0b5e4122711")
	require.Equal(t, names.ChildName("Radiohead", "artist", "a74b1b7f-71a5-4011-9441-d0b5e4122711"), a)
	require.Equal(t, names.ChildName("Jane Austen", "author", "OL21594A"), names.Author("Jane Austen", "OL21594A"))
	require.NotEqual(t, names.ChildName("x", "artist", "1"), names.ChildName("x", "author", "1"))
}
