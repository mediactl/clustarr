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

package backlogsearch

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
)

func artist(name string, monitored bool) *catalogv1alpha1.Artist {
	a := &catalogv1alpha1.Artist{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID("uid-" + name)}}
	a.Spec.Monitored = ptr.To(monitored)
	return a
}

func author(name string, monitored bool) *catalogv1alpha1.Author {
	a := &catalogv1alpha1.Author{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID("uid-" + name)}}
	a.Spec.Monitored = ptr.To(monitored)
	return a
}

func album(name, artist string) *catalogv1alpha1.Album {
	a := &catalogv1alpha1.Album{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID("uid-" + name)}}
	a.Spec.ArtistRef = artist
	a.Status.Phase, a.Status.SearchAttempts = catalogv1alpha1.AlbumPhaseWanted, searched(24*time.Hour)
	return a
}

func book(name string, author *string) *catalogv1alpha1.Book {
	b := &catalogv1alpha1.Book{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID("uid-" + name)}}
	b.Spec.AuthorRef = author
	b.Status.Phase, b.Status.SearchAttempts = catalogv1alpha1.BookPhaseWanted, searched(24*time.Hour)
	return b
}

// TestTheBacklogReadsAnAlbumsArtistAndABooksAuthor is 2026-11-10 design D3:
// a cycle grants an Album only under a monitored Artist and a Book only
// under a monitored Author -- the container check it made for an Episode's
// Series and an Issue's Comic -- while a standalone Book is governed by its
// own flag, and a child whose container is gone is no candidate.
func TestTheBacklogReadsAnAlbumsArtistAndABooksAuthor(t *testing.T) {
	r := newReconciler(t,
		artist("beatles", true), artist("stones", false),
		author("herbert", true), author("asimov", false),
		album("abbey-road", "beatles"), album("exile", "stones"), album("orphan", "nobody"),
		book("dune", ptr.To("herbert")), book("foundation", ptr.To("asimov")), book("lone", nil),
	)
	bs := &catalogv1alpha1.BacklogSearch{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "default", Generation: 1}}
	bs.Spec.Missing.PerCycle = ptr.To[int32](10)
	st := &catalogv1alpha1.BacklogSearchStatus{}

	_, err := r.cycle(context.Background(), bs, st, now)
	require.NoError(t, err)

	require.ElementsMatch(t, []string{"Album/abbey-road", "Book/dune", "Book/lone"}, drain(r.Book),
		"the monitored artist's album, the monitored author's book and the standalone book; nothing under an unmonitored or missing container")
	for _, uid := range []string{"uid-abbey-road", "uid-dune", "uid-lone"} {
		_, ok := r.Book.Granted(uid, now)
		require.True(t, ok, uid)
	}
	for _, uid := range []string{"uid-exile", "uid-orphan", "uid-foundation"} {
		_, ok := r.Book.Granted(uid, now)
		require.False(t, ok, uid)
	}
	require.NotNil(t, st.Missing)
	require.EqualValues(t, 3, st.Missing.Candidates)
}
