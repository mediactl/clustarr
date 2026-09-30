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

package v1alpha1_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// TestContainerKind: a Search on an author, artist or comic fans out into
// its monitored books, albums or issues; every other kind is searched
// itself (or, for a series, not by fan-out).
func TestContainerKind(t *testing.T) {
	for kind, want := range map[commonv1.MediaKind]bool{
		commonv1.MediaKindAuthor: true, commonv1.MediaKindArtist: true, commonv1.MediaKindComic: true,
		commonv1.MediaKindSeries: false, commonv1.MediaKindMovie: false, commonv1.MediaKindBook: false,
		commonv1.MediaKindAlbum: false, commonv1.MediaKindIssue: false, commonv1.MediaKindAudiobook: false,
	} {
		require.Equal(t, want, catalogv1alpha1.ContainerKind(kind), "kind %s", kind)
	}
}
