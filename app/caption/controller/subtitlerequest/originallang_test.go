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

package subtitlerequest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1alpha1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/k8s"
	"github.com/mediactl/clustarr/pkg/subtitles"
)

// A file whose audio says nothing about its language -- untagged, "und",
// or never probed -- is taken to be in its item's original language; a
// tagged track always wins over the metadata.
func TestAudioLanguagesFallsBackToTheOriginalLanguage(t *testing.T) {
	und := &commonv1alpha1.MediaInfo{Audio: []commonv1alpha1.AudioStream{{Language: "und"}, {Language: ""}}}
	jpn := &commonv1alpha1.MediaInfo{Audio: []commonv1alpha1.AudioStream{{Language: "jpn"}}}

	assert.Equal(t, []string{"en"}, audioLanguages(und, "en"), "untagged audio: the original language")
	assert.Equal(t, []string{"fr"}, audioLanguages(nil, "fre"), "never probed: the original language, normalised")
	assert.Equal(t, []string{"ja"}, audioLanguages(jpn, "en"), "a tagged track wins")
	assert.Empty(t, audioLanguages(und, ""), "no metadata either: nothing is assumed")
	assert.Empty(t, audioLanguages(nil, "xx-not-a-language-zz"), "an unresolvable original language is dropped")
}

// With English excluded by English audio (the default profile), an untagged
// file of an English-original film wants nothing and one of a French film
// wants English.
func TestAnUntaggedFileIsPlannedByItsOriginalLanguage(t *testing.T) {
	pp := subtitles.Profile{Languages: []subtitles.ProfileLanguage{{Key: "en", Language: "en", AudioExclude: true}}}
	und := &commonv1alpha1.MediaInfo{Audio: []commonv1alpha1.AudioStream{{Language: "und"}}}

	wanted, _ := subtitles.Plan(pp, audioLanguages(und, "en"), nil)
	assert.Empty(t, wanted)
	wanted, _ = subtitles.Plan(pp, audioLanguages(und, "fr"), nil)
	assert.Equal(t, []subtitles.LangKey{"en"}, wanted)
}

// The original language is the Movie's, or the Episode's Series'; an item
// or series that cannot be read, or carries no metadata, gives "".
func TestOriginalLanguageReadsTheMovieOrTheEpisodesSeries(t *testing.T) {
	movie := &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "amelie"}}
	movie.Status.Metadata = &catalogv1alpha1.MovieMetadata{OriginalLanguage: "fr"}
	series := &catalogv1alpha1.Series{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "shogun"}}
	series.Status.Metadata = &catalogv1alpha1.SeriesMetadata{OriginalLanguage: "ja"}
	ep := &catalogv1alpha1.Episode{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "shogun-s01e01"},
		Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "shogun"},
	}
	orphan := &catalogv1alpha1.Episode{
		ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "gone-s01e01"},
		Spec:       catalogv1alpha1.EpisodeSpec{SeriesRef: "gone"},
	}
	bare := &catalogv1alpha1.Movie{ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: "bare"}}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).
		WithObjects(movie, series, ep, orphan, bare).Build()
	r := &Reconciler{Client: c}

	file := func(kind commonv1alpha1.MediaKind, name string) *catalogv1alpha1.MediaFile {
		return &catalogv1alpha1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Namespace: "media", Name: name + "-file"},
			Spec:       catalogv1alpha1.MediaFileSpec{MediaRef: commonv1alpha1.MediaRef{Kind: kind, Name: name}},
		}
	}
	ctx := context.Background()
	require.Equal(t, "fr", r.originalLanguage(ctx, file(commonv1alpha1.MediaKindMovie, "amelie")))
	require.Equal(t, "ja", r.originalLanguage(ctx, file(commonv1alpha1.MediaKindEpisode, "shogun-s01e01")))
	require.Empty(t, r.originalLanguage(ctx, file(commonv1alpha1.MediaKindEpisode, "gone-s01e01")))
	require.Empty(t, r.originalLanguage(ctx, file(commonv1alpha1.MediaKindMovie, "bare")))
	require.Empty(t, r.originalLanguage(ctx, file(commonv1alpha1.MediaKindMovie, "missing")))
}
