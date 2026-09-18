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

package naming_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/naming"
)

func TestTrackFile(t *testing.T) {
	e := naming.NewEngine(naming.Config{})
	c := naming.Context{
		ArtistName: "Boards of Canada", AlbumTitle: "Music Has the Right to Children",
		Year: 1998, Track: 3, TrackTitle: "Telephasic Workshop",
	}
	got, err := e.TrackFile(c)
	require.NoError(t, err)
	require.Equal(t, "Music Has the Right to Children (1998)/Boards of Canada - Music Has the Right to Children - 03 - Telephasic Workshop", got)
}
