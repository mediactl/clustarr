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

package transcode

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// Gather reads the file's item once: whether it exists, its original
// language (an Episode's through its Series), the standard's "original"
// (transcodeplan.Inputs.OriginalLanguage), and whether it has an upgrade
// downloading (transcodeplan.UpgradeDownloading).
func TestTheItemReadGivesTheOriginalLanguage(t *testing.T) {
	movie := &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "spirited-away"}}
	movie.Status.Metadata = &catalogv1alpha1.MovieMetadata{OriginalLanguage: "ja"}
	grab := "spirited-away-uhd"
	upgrading := &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "upgrading"}}
	upgrading.Status.ActiveDownloadRef = &grab
	series := &catalogv1alpha1.Series{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "lupin"}}
	series.Status.Metadata = &catalogv1alpha1.SeriesMetadata{OriginalLanguage: "fr"}
	episode := &catalogv1alpha1.Episode{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "lupin-s01e01"},
		Spec: catalogv1alpha1.EpisodeSpec{SeriesRef: "lupin"}}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(movie, upgrading, series, episode).Build()
	file := func(kind commonv1.MediaKind, name string) *catalogv1alpha1.MediaFile {
		return &catalogv1alpha1.MediaFile{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "f"},
			Spec: catalogv1alpha1.MediaFileSpec{MediaRef: commonv1.MediaRef{Kind: kind, Name: name}}}
	}
	for _, tc := range []struct {
		name        string
		mf          *catalogv1alpha1.MediaFile
		exists      bool
		original    string
		downloading bool
	}{
		{"a movie", file(commonv1.MediaKindMovie, "spirited-away"), true, "ja", false},
		{"an episode, through its series", file(commonv1.MediaKindEpisode, "lupin-s01e01"), true, "fr", false},
		{"a gone item", file(commonv1.MediaKindMovie, "gone"), false, "", false},
		{"a movie downloading an upgrade", file(commonv1.MediaKindMovie, "upgrading"), true, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exists, original, downloading, err := readItem(context.Background(), c, tc.mf)
			require.NoError(t, err)
			assert.Equal(t, tc.exists, exists)
			assert.Equal(t, tc.original, original)
			assert.Equal(t, tc.downloading, downloading)
		})
	}
}
