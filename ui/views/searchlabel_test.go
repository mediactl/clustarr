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

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

func TestSearchLabel(t *testing.T) {
	for kind, want := range map[commonv1.MediaKind]string{
		commonv1.MediaKindAuthor: "Search Monitored Books",
		commonv1.MediaKindArtist: "Search Monitored Albums",
		commonv1.MediaKindComic:  "Search Monitored Issues",
		commonv1.MediaKindMovie:  "Search Movie",
		commonv1.MediaKindBook:   "Search Book",
	} {
		require.Equal(t, want, searchLabel(kind), "kind %s", kind)
	}
}
