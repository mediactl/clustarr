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
// (transcodeplan.Inputs.OriginalLanguage), whether it has an upgrade
// downloading (transcodeplan.UpgradeDownloading), and whether the item, or
// an Episode's Series, carries transcode.clustarr.io/exclude.
func TestTheItemReadGivesTheOriginalLanguage(t *testing.T) {
	movie := &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "spirited-away"}}
	movie.Status.Metadata = &catalogv1alpha1.MovieMetadata{OriginalLanguage: "ja"}
	grab := "spirited-away-uhd"
	upgrading := &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "upgrading"}}
	upgrading.Status.ActiveDownloadRef = &grab
	series := &catalogv1alpha1.Series{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "lupin"}}
	series.Status.Metadata = &catalogv1alpha1.SeriesMetadata{OriginalLanguage: "fr"}
	episode := &catalogv1alpha1.Episode{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "lupin-s01e01"},
		Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "lupin"},
	}
	exclude := map[string]string{catalogv1alpha1.AnnotationTranscodeExclude: "true"}
	keptMovie := &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "kept", Annotations: exclude}}
	keptSeries := &catalogv1alpha1.Series{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "rogers", Annotations: exclude}}
	keptEpisode := &catalogv1alpha1.Episode{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "rogers-s01e01"},
		Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "rogers"},
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
		WithObjects(movie, upgrading, series, episode, keptMovie, keptSeries, keptEpisode).Build()
	file := func(kind commonv1.MediaKind, name string) *catalogv1alpha1.MediaFile {
		return &catalogv1alpha1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "f"},
			Spec:       catalogv1alpha1.MediaFileSpec{MediaRef: commonv1.MediaRef{Kind: kind, Name: name}},
		}
	}
	for _, tc := range []struct {
		name        string
		mf          *catalogv1alpha1.MediaFile
		exists      bool
		original    string
		downloading bool
		excluded    bool
	}{
		{"a movie", file(commonv1.MediaKindMovie, "spirited-away"), true, "ja", false, false},
		{"an episode, through its series", file(commonv1.MediaKindEpisode, "lupin-s01e01"), true, "fr", false, false},
		{"a gone item", file(commonv1.MediaKindMovie, "gone"), false, "", false, false},
		{"a movie downloading an upgrade", file(commonv1.MediaKindMovie, "upgrading"), true, "", true, false},
		{"an excluded movie", file(commonv1.MediaKindMovie, "kept"), true, "", false, true},
		{"an episode of an excluded series", file(commonv1.MediaKindEpisode, "rogers-s01e01"), true, "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readItem(context.Background(), c, tc.mf)
			require.NoError(t, err)
			assert.Equal(t, tc.exists, got.exists)
			assert.Equal(t, tc.original, got.original)
			assert.Equal(t, tc.downloading, got.downloading)
			assert.Equal(t, tc.excluded, got.excluded)
		})
	}
}

// The item read gives the shape a file is cropped to in its transcode
// (transcodeplan.Inputs.CropAspectMilli): an episode's file of the other
// shape than its series is kept in, when the series crops
// (spec.aspectRatioFix crop). Nothing for a series that replaces, a file
// already the shape, one no probe read the shape of, or a movie.
func TestTheItemReadGivesTheCropTarget(t *testing.T) {
	crops := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "frasier"},
		Spec: catalogv1alpha1.SeriesSpec{
			AspectRatio: catalogv1alpha1.AspectRatioWidescreen, AspectRatioFix: catalogv1alpha1.AspectRatioFixCrop,
			SeasonAspectRatios: []catalogv1alpha1.SeasonAspectRatio{{Number: 2, AspectRatio: catalogv1alpha1.AspectRatioFullscreen}},
		},
	}
	replaces := &catalogv1alpha1.Series{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "seinfeld"},
		Spec:       catalogv1alpha1.SeriesSpec{AspectRatio: catalogv1alpha1.AspectRatioWidescreen},
	}
	episode := func(name, series string, season int32) *catalogv1alpha1.Episode {
		return &catalogv1alpha1.Episode{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: name},
			Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: series, SeasonNumber: season},
		}
	}
	movie := &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "heat"}}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(crops, replaces, movie,
		episode("frasier-s01e01", "frasier", 1), episode("frasier-s02e01", "frasier", 2), episode("seinfeld-s09e01", "seinfeld", 9)).Build()
	file := func(kind commonv1.MediaKind, name string, milli int32) *catalogv1alpha1.MediaFile {
		mf := &catalogv1alpha1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "f"},
			Spec:       catalogv1alpha1.MediaFileSpec{MediaRef: commonv1.MediaRef{Kind: kind, Name: name}},
		}
		if milli >= 0 {
			mf.Status.MediaInfo = &commonv1.MediaInfo{DisplayAspectMilli: milli}
		}
		return mf
	}
	for _, tc := range []struct {
		name string
		mf   *catalogv1alpha1.MediaFile
		want int32
	}{
		{"a 4:3 file of a widescreen series that crops", file(commonv1.MediaKindEpisode, "frasier-s01e01", 1333), 1778},
		{"a 16:9 file of its 4:3 season", file(commonv1.MediaKindEpisode, "frasier-s02e01", 1778), 1333},
		{"a file already the shape", file(commonv1.MediaKindEpisode, "frasier-s01e01", 1778), 0},
		{"a shape no probe read", file(commonv1.MediaKindEpisode, "frasier-s01e01", 0), 0},
		{"an unprobed file", file(commonv1.MediaKindEpisode, "frasier-s01e01", -1), 0},
		{"a series that replaces", file(commonv1.MediaKindEpisode, "seinfeld-s09e01", 1333), 0},
		{"a movie", file(commonv1.MediaKindMovie, "heat", 1333), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readItem(context.Background(), c, tc.mf)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.cropMilli)
		})
	}
}
