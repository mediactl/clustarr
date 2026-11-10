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

package ui

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

// TestReaderIndexesCoverEveryParentsChildren: the reader registers one
// field index per parent-to-children relation a page lists through
// (2026-11-10 design §9) -- Episode by seriesRef, Album by artistRef, Book
// and Audiobook by authorRef, Issue by comicRef -- each extractor
// answering the parent's name for its kind, nothing for an orphan and
// nothing for another kind, so a page never lists a namespace and filters
// in memory.
func TestReaderIndexesCoverEveryParentsChildren(t *testing.T) {
	require.Len(t, readerIndexes, 5)
	fields := map[string]bool{}
	for _, ix := range readerIndexes {
		fields[ix.field+"/"+ix.obj.GetObjectKind().GroupVersionKind().Kind] = true
	}
	for _, tc := range []struct {
		name    string
		field   string
		extract client.IndexerFunc
		child   client.Object
		orphan  client.Object
		want    string
	}{
		{"episode", EpisodeSeriesRefField, IndexEpisodeBySeriesRef,
			&catalogv1alpha1.Episode{Spec: catalogv1alpha1.EpisodeSpec{SeriesRef: "andor"}}, &catalogv1alpha1.Episode{}, "andor"},
		{"album", AlbumArtistRefField, IndexAlbumByArtistRef,
			&catalogv1alpha1.Album{Spec: catalogv1alpha1.AlbumSpec{ArtistRef: "radiohead"}}, &catalogv1alpha1.Album{}, "radiohead"},
		{"book", BookAuthorRefField, IndexBookByAuthorRef,
			&catalogv1alpha1.Book{Spec: catalogv1alpha1.BookSpec{AuthorRef: ptr.To("le-guin")}}, &catalogv1alpha1.Book{Spec: catalogv1alpha1.BookSpec{AuthorRef: ptr.To("")}}, "le-guin"},
		{"issue", IssueComicRefField, IndexIssueByComicRef,
			&catalogv1alpha1.Issue{Spec: catalogv1alpha1.IssueSpec{ComicRef: "saga"}}, &catalogv1alpha1.Issue{}, "saga"},
		{"audiobook", AudiobookAuthorRefField, IndexAudiobookByAuthorRef,
			&catalogv1alpha1.Audiobook{Spec: catalogv1alpha1.AudiobookSpec{AuthorRef: ptr.To("herbert")}}, &catalogv1alpha1.Audiobook{}, "herbert"},
	} {
		require.Equal(t, []string{tc.want}, tc.extract(tc.child), tc.name)
		require.Nil(t, tc.extract(tc.orphan), "%s: an orphan is under no parent", tc.name)
		require.Nil(t, tc.extract(&catalogv1alpha1.Movie{}), "%s: another kind indexes nothing", tc.name)
		registered := false
		for _, ix := range readerIndexes {
			if ix.field == tc.field && ix.extract != nil && ix.obj != nil {
				registered = true
			}
		}
		require.True(t, registered, "%s's index is registered on the reader", tc.name)
	}
	// Each of the four spec fields is spelled as the kind's field, with no
	// leading dot, as Episode's is.
	for _, f := range []string{EpisodeSeriesRefField, AlbumArtistRefField, BookAuthorRefField, IssueComicRefField, AudiobookAuthorRefField} {
		require.Regexp(t, `^spec\.[a-z]+Ref$`, f)
	}
}
