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

package search

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/audiobook"
)

// addedUnsearched is searchplan.View.NoSearchOnAdd for item (2026-11-10
// design D10, D11; the plan's W1.5 leftover: searchForMissing had no
// reader for any kind): the item, or the container it was missing from
// when the container was added, carries searchForMissing false -- a
// Movie's searchForMovie, an Audiobook's own, a Series' for an Episode, a
// Comic's for an Issue, an Artist's for an Album, an Author's for a Book --
// which is Sonarr's "search for missing episodes" unticked (the
// Audiobook's through audiobook.SearchOnAdd). The flag is
// read whatever status.addOptionsApplied says, since it governs the item's
// first automatic search, not the pass that applies the add options, and a
// child is held to it only while it was among what was missing on add
// (onAdd): one released after the container was created is new, and is
// searched as any item is, so the series the rescan added with the flag
// off (scanapply's Series and Movies) still search every episode that airs
// from then on live. r reads an Album's Artist and a Book's Author; owner
// is itemread.Owner's answer, an Episode's Series or an Issue's Comic, and
// the item itself for every other kind. A container that is gone, or a
// Book or Audiobook under none, reads as search on add.
func addedUnsearched(ctx context.Context, r client.Reader, item, owner client.Object) (bool, error) {
	switch t := item.(type) {
	case *catalogv1alpha1.Album:
		var artist catalogv1alpha1.Artist
		if ok, err := getContainer(ctx, r, t.Namespace, t.Spec.ArtistRef, &artist); !ok || err != nil {
			return false, err
		}
		return addedUnsearchedUnder(item, &artist), nil
	case *catalogv1alpha1.Book:
		var author catalogv1alpha1.Author
		if ok, err := getContainer(ctx, r, t.Namespace, ptr.Deref(t.Spec.AuthorRef, ""), &author); !ok || err != nil {
			return false, err
		}
		return addedUnsearchedUnder(item, &author), nil
	}
	return addedUnsearchedUnder(item, owner), nil
}

// getContainer reads ns/name into o; ok is false for an empty name or a
// container that is gone, which is no error.
func getContainer(ctx context.Context, r client.Reader, ns, name string, o client.Object) (bool, error) {
	if name == "" {
		return false, nil
	}
	if err := r.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, o); err != nil {
		return false, client.IgnoreNotFound(err)
	}
	return true, nil
}

// addedUnsearchedUnder is addedUnsearched with the container already read:
// an Episode's Series, an Issue's Comic, an Album's Artist, a Book's
// Author, and a Movie or Audiobook on its own (container the item itself,
// or anything). The season and volume facts call it per child, so no
// container is read twice.
func addedUnsearchedUnder(item, container client.Object) bool {
	switch t := item.(type) {
	case *catalogv1alpha1.Movie:
		return !ptr.Deref(t.Spec.AddOptions.SearchForMovie, true)
	case *catalogv1alpha1.Audiobook:
		return !audiobook.SearchOnAdd(t)
	case *catalogv1alpha1.Episode:
		ser, ok := container.(*catalogv1alpha1.Series)
		return ok && !ptr.Deref(ser.Spec.AddOptions.SearchForMissing, true) && onAdd(t.Status.AirDate, ser.CreationTimestamp)
	case *catalogv1alpha1.Issue:
		co, ok := container.(*catalogv1alpha1.Comic)
		return ok && !ptr.Deref(co.Spec.AddOptions.SearchForMissing, true) && onAdd(t.Status.Date, co.CreationTimestamp)
	case *catalogv1alpha1.Album:
		artist, ok := container.(*catalogv1alpha1.Artist)
		var released *metav1.Time
		if t.Status.Metadata != nil {
			released = t.Status.Metadata.ReleaseDate
		}
		return ok && !ptr.Deref(artist.Spec.AddOptions.SearchForMissing, true) && onAdd(released, artist.CreationTimestamp)
	case *catalogv1alpha1.Book:
		author, ok := container.(*catalogv1alpha1.Author)
		var released *metav1.Time
		if t.Status.Metadata != nil {
			released = t.Status.Metadata.ReleaseDate
		}
		return ok && !ptr.Deref(author.Spec.AddOptions.SearchForMissing, true) && onAdd(released, author.CreationTimestamp)
	}
	return false
}

// onAdd reports a child among what its container was missing when it was
// added: released, aired or undated by the container's creation. A child
// released after that is new. A container without a creation time (a test
// object) counts every child as on add.
func onAdd(released *metav1.Time, added metav1.Time) bool {
	return released == nil || released.IsZero() || added.IsZero() || !released.After(added.Time)
}
