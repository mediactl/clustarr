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

package fileimport

import (
	"testing"

	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
)

// TestFieldIndexesDeclareTheMediaFileTargetIndex: the import domain declares
// the spec.mediaRef.target index (W3.4) and the process registers it once;
// its value is "<kind>/<name>", and a multi-episode file is every episode's
// existing file.
func TestFieldIndexesDeclareTheMediaFileTargetIndex(t *testing.T) {
	ix := FieldIndexes()
	require.Len(t, ix, 1)
	require.Equal(t, MediaFileByTargetIndexKey, ix[0].Name)
	require.IsType(t, &catalogv1alpha1.MediaFile{}, ix[0].Object)

	mf := &catalogv1alpha1.MediaFile{Spec: catalogv1alpha1.MediaFileSpec{
		MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"},
	}}
	require.Equal(t, []string{targetKey(string(commonv1.MediaKindMovie), "heat")}, ix[0].Extract(mf))

	double := &catalogv1alpha1.MediaFile{Spec: catalogv1alpha1.MediaFileSpec{MediaRef: commonv1.MediaRef{
		Kind: commonv1.MediaKindEpisode, Name: "the-wire-s01e01", Keys: []string{"the-wire-s01e01", "the-wire-s01e02"},
	}}}
	require.Equal(t, []string{"episode/the-wire-s01e01", "episode/the-wire-s01e02"}, ix[0].Extract(double),
		"a double episode backs both episodes, each once")

	require.Empty(t, ix[0].Extract(&catalogv1alpha1.MediaFile{}), "a MediaFile naming no item is not indexed")
	require.Empty(t, ix[0].Extract(&catalogv1alpha1.Movie{}), "only MediaFiles are indexed")
}
