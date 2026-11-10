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

package itemread_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/itemread"
	"github.com/mediactl/clustarr/pkg/k8s"
)

// TestMonitoredCascadesFromTheContainer is 2026-11-10 design D3: an Album
// is searched only when it and its Artist are monitored, a Book only with
// its Author, as an Episode needs its Series and an Issue its Comic; a
// standalone Book reads its own flag alone, and a container that is gone
// reads as unmonitored. The RSS matcher applied the same AND already, so
// search and RSS now agree.
func TestMonitoredCascadesFromTheContainer(t *testing.T) {
	const ns = "lib"
	meta := func(name string) metav1.ObjectMeta { return metav1.ObjectMeta{Namespace: ns, Name: name} }
	flag := func(on bool) *bool { return ptr.To(on) }
	containers := []client.Object{
		&catalogv1alpha1.Artist{ObjectMeta: meta("beatles"), Spec: catalogv1alpha1.ArtistSpec{Monitored: flag(true)}},
		&catalogv1alpha1.Artist{ObjectMeta: meta("stones"), Spec: catalogv1alpha1.ArtistSpec{Monitored: flag(false)}},
		&catalogv1alpha1.Author{ObjectMeta: meta("herbert"), Spec: catalogv1alpha1.AuthorSpec{Monitored: flag(true)}},
		&catalogv1alpha1.Author{ObjectMeta: meta("asimov"), Spec: catalogv1alpha1.AuthorSpec{Monitored: flag(false)}},
		&catalogv1alpha1.Series{ObjectMeta: meta("show"), Spec: catalogv1alpha1.SeriesSpec{Monitored: flag(true)}},
		&catalogv1alpha1.Comic{ObjectMeta: meta("saga"), Spec: catalogv1alpha1.ComicSpec{Monitored: flag(false)}},
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(containers...).Build()

	album := func(artist string, on bool) *catalogv1alpha1.Album {
		return &catalogv1alpha1.Album{ObjectMeta: meta("a"), Spec: catalogv1alpha1.AlbumSpec{ArtistRef: artist, Monitored: flag(on)}}
	}
	book := func(author *string, on bool) *catalogv1alpha1.Book {
		return &catalogv1alpha1.Book{ObjectMeta: meta("b"), Spec: catalogv1alpha1.BookSpec{AuthorRef: author, Monitored: flag(on)}}
	}
	audiobook := func(author *string, on bool) *catalogv1alpha1.Audiobook {
		return &catalogv1alpha1.Audiobook{ObjectMeta: meta("ab"), Spec: catalogv1alpha1.AudiobookSpec{AuthorRef: author, Monitored: flag(on)}}
	}
	for _, tc := range []struct {
		name string
		item client.Object
		want bool
	}{
		{"album, both monitored", album("beatles", true), true},
		{"album of an unmonitored artist", album("stones", true), false},
		{"unmonitored album of a monitored artist", album("beatles", false), false},
		{"album whose artist is gone", album("nobody", true), false},
		{"book, both monitored", book(ptr.To("herbert"), true), true},
		{"book of an unmonitored author", book(ptr.To("asimov"), true), false},
		{"standalone book reads its own flag", book(nil, true), true},
		{"standalone book unmonitored", book(nil, false), false},
		{"book whose author is gone", book(ptr.To("nobody"), true), false},
		{"audiobook, both monitored", audiobook(ptr.To("herbert"), true), true},
		{"audiobook of an unmonitored author", audiobook(ptr.To("asimov"), true), false},
		{"audiobook without an author reads its own flag", audiobook(nil, true), true},
		{"audiobook whose author is gone", audiobook(ptr.To("nobody"), true), false},
		{"episode of a monitored series", &catalogv1alpha1.Episode{ObjectMeta: meta("e"), Spec: catalogv1alpha1.EpisodeSpec{SeriesRef: "show"}}, true},
		{"issue of an unmonitored comic", &catalogv1alpha1.Issue{ObjectMeta: meta("i"), Spec: catalogv1alpha1.IssueSpec{ComicRef: "saga", Monitored: flag(true)}}, false},
		{"a movie is its own container", &catalogv1alpha1.Movie{ObjectMeta: meta("m"), Spec: catalogv1alpha1.MovieSpec{Monitored: flag(true)}}, true},
		{"an audiobook is its own container", &catalogv1alpha1.Audiobook{ObjectMeta: meta("ab"), Spec: catalogv1alpha1.AudiobookSpec{Monitored: flag(false)}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := itemread.Monitored(context.Background(), c, tc.item)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestHasFileReadsAnAlbumsTracks is design D6: an Album with any track file
// reads hasFile (so the downloads stage's satisfied test and the search
// planner's redownload escape see it), one with none does not.
func TestHasFileReadsAnAlbumsTracks(t *testing.T) {
	with := &catalogv1alpha1.Album{Status: catalogv1alpha1.AlbumStatus{HasFile: true, TrackFileCount: 3}}
	without := &catalogv1alpha1.Album{Status: catalogv1alpha1.AlbumStatus{}}
	assert.True(t, itemread.HasFile(with))
	assert.False(t, itemread.HasFile(without))
	assert.False(t, itemread.HasFile(&catalogv1alpha1.Artist{}), "a container holds no file of its own")
}

// TestContainerSearchReadsBothShapes: the Series' seasonSearch and the
// Comic's volumeSearch are the one state (design D10), and the in-flight
// marker index reads either.
func TestContainerSearchReadsBothShapes(t *testing.T) {
	st := &catalogv1alpha1.SeasonSearchState{Dispatch: &catalogv1alpha1.Dispatch{Seq: 3}}
	ser := &catalogv1alpha1.Series{Status: catalogv1alpha1.SeriesStatus{SeasonSearch: st}}
	co := &catalogv1alpha1.Comic{Status: catalogv1alpha1.ComicStatus{VolumeSearch: st}}
	assert.Same(t, st, itemread.ContainerSearch(ser))
	assert.Same(t, st, itemread.ContainerSearch(co))
	assert.Nil(t, itemread.ContainerSearch(&catalogv1alpha1.Movie{}))
	assert.Equal(t, []string{itemread.IndexTrue}, itemread.SeasonSearchInFlightValues(co))
	assert.Nil(t, itemread.SeasonSearchInFlightValues(&catalogv1alpha1.Comic{}))
}
