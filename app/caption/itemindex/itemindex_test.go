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

package itemindex_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/app/caption/itemindex"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func file(ns, name string, kind commonv1.MediaKind, item string) *catalogv1alpha1.MediaFile {
	return &catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       catalogv1alpha1.MediaFileSpec{MediaRef: commonv1.MediaRef{Kind: kind, Name: item}},
	}
}

// MediaFilesOf returns exactly the files of one item: its kind and name,
// in its namespace -- never a same-named item of another kind, another
// namespace's, or a file with no item.
func TestMediaFilesOfReturnsOnlyThatItemsFiles(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
		WithIndex(&catalogv1alpha1.MediaFile{}, itemindex.MediaFileByItem, itemindex.Extract).
		WithObjects(
			file("media", "heat-a", commonv1.MediaKindMovie, "heat"),
			file("media", "heat-b", commonv1.MediaKindMovie, "heat"),
			file("media", "heat-ep", commonv1.MediaKindEpisode, "heat"),
			file("other", "heat-elsewhere", commonv1.MediaKindMovie, "heat"),
			file("media", "unmatched", "", ""),
		).Build()

	got, err := itemindex.MediaFilesOf(context.Background(), c, "media", commonv1.MediaKindMovie, "heat")
	require.NoError(t, err)
	var names []string
	for _, mf := range got {
		names = append(names, mf.Name)
	}
	require.ElementsMatch(t, []string{"heat-a", "heat-b"}, names)
}

func TestExtractSkipsAFileWithNoItem(t *testing.T) {
	require.Empty(t, itemindex.Extract(file("media", "x", "", "")))
	require.Equal(t, []string{itemindex.Key(commonv1.MediaKindEpisode, "s01e01")},
		itemindex.Extract(file("media", "x", commonv1.MediaKindEpisode, "s01e01")))
}
