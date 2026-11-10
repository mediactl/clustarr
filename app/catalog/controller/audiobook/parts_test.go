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

package audiobook_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/app/catalog/controller/audiobook"
	"github.com/mediactl/clustarr/pkg/k8s"
)

func part(name, path string, index int32) catalogv1alpha1.MediaFile {
	return catalogv1alpha1.MediaFile{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       catalogv1alpha1.MediaFileSpec{Path: path, PartIndex: index},
	}
}

func names(items []catalogv1alpha1.MediaFile) []string {
	out := make([]string, 0, len(items))
	for i := range items {
		out = append(out, items[i].Name)
	}
	return out
}

// Play order (2026-11-10 design D11): the part index first, the unnumbered
// after every numbered part, path and then name breaking ties.
func TestOrderParts(t *testing.T) {
	cases := []struct {
		name  string
		items []catalogv1alpha1.MediaFile
		want  []string
	}{
		{"by part index, not by path", []catalogv1alpha1.MediaFile{
			part("c", "/b/Chapter 10.mp3", 10), part("a", "/b/Chapter 2.mp3", 2), part("b", "/b/Chapter 1.mp3", 1),
		}, []string{"b", "a", "c"}},
		{"the index outranks a path that sorts the other way", []catalogv1alpha1.MediaFile{
			part("x", "/b/01 Epilogue.mp3", 12), part("y", "/b/02 Prologue.mp3", 1),
		}, []string{"y", "x"}},
		{"an unnumbered part comes after every numbered one, by path", []catalogv1alpha1.MediaFile{
			part("z", "/b/Zed.mp3", 0), part("n", "/b/Part 2.mp3", 2), part("a", "/b/Alpha.mp3", 0),
		}, []string{"n", "a", "z"}},
		{"no index at all: by path, as before the index existed", []catalogv1alpha1.MediaFile{
			part("p3", "/b/Book 03.mp3", 0), part("p1", "/b/Book 01.mp3", 0), part("p2", "/b/Book 02.mp3", 0),
		}, []string{"p1", "p2", "p3"}},
		{"two parts claiming one index: by path, then name", []catalogv1alpha1.MediaFile{
			part("dup-b", "/b/same.mp3", 3), part("dup-a", "/b/same.mp3", 3), part("first", "/b/a.mp3", 3),
		}, []string{"first", "dup-a", "dup-b"}},
		{"empty", nil, []string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := names(c.items)
			got := audiobook.OrderParts(c.items)
			assert.Equal(t, c.want, names(got))
			assert.Equal(t, before, names(c.items), "the input is left as it is")
		})
	}
}

// PartRefs: the names in play order, capped at the status list's size, and
// the count of every part on disk, the cap notwithstanding.
func TestPartRefs(t *testing.T) {
	refs, n := audiobook.PartRefs(nil)
	assert.Nil(t, refs)
	assert.Zero(t, n)

	refs, n = audiobook.PartRefs([]catalogv1alpha1.MediaFile{part("b", "/x/2.mp3", 2), part("a", "/x/1.mp3", 1)})
	assert.Equal(t, []string{"a", "b"}, refs)
	assert.EqualValues(t, 2, n)

	many := make([]catalogv1alpha1.MediaFile, 0, 205)
	for i := 1; i <= 205; i++ {
		many = append(many, part(fmt.Sprintf("p%03d", i), fmt.Sprintf("/x/%03d.mp3", i), int32(i)))
	}
	refs, n = audiobook.PartRefs(many)
	require.Len(t, refs, 200, "fileRefs holds at most its MaxItems")
	assert.Equal(t, "p001", refs[0])
	assert.Equal(t, "p200", refs[199])
	assert.EqualValues(t, 205, n, "partCount counts every part, listed or not")
}

// The author an Audiobook takes from its Book: only when it names none of
// its own and the Book names one.
func TestAuthorRefFromBook(t *testing.T) {
	withAuthor := &catalogv1alpha1.Book{Spec: catalogv1alpha1.BookSpec{AuthorRef: ptr.To("terry-pratchett")}}
	standalone := &catalogv1alpha1.Book{}
	cases := []struct {
		name string
		m    *catalogv1alpha1.Audiobook
		book *catalogv1alpha1.Book
		want string
		ok   bool
	}{
		{"the Book's author", &catalogv1alpha1.Audiobook{}, withAuthor, "terry-pratchett", true},
		{"an empty authorRef of its own still takes the Book's", &catalogv1alpha1.Audiobook{Spec: catalogv1alpha1.AudiobookSpec{AuthorRef: ptr.To("")}}, withAuthor, "terry-pratchett", true},
		{"an author Add New set is kept", &catalogv1alpha1.Audiobook{Spec: catalogv1alpha1.AudiobookSpec{AuthorRef: ptr.To("someone-else")}}, withAuthor, "", false},
		{"a standalone Book names none", &catalogv1alpha1.Audiobook{}, standalone, "", false},
		{"an empty authorRef on the Book names none", &catalogv1alpha1.Audiobook{}, &catalogv1alpha1.Book{Spec: catalogv1alpha1.BookSpec{AuthorRef: ptr.To("")}}, "", false},
		{"no Book resolved", &catalogv1alpha1.Audiobook{}, nil, "", false},
		{"no Audiobook", nil, withAuthor, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := audiobook.AuthorRefFromBook(c.m, c.book)
			assert.Equal(t, c.ok, ok)
			assert.Equal(t, c.want, got)
		})
	}
}

// LinkAuthor writes spec.authorRef and nothing else of the spec.
func TestLinkAuthorPatchesTheOneSpecField(t *testing.T) {
	m := &catalogv1alpha1.Audiobook{
		ObjectMeta: metav1.ObjectMeta{Name: "guards-guards-audio", Namespace: "media"},
		Spec: catalogv1alpha1.AudiobookSpec{
			ASIN: "B002V0QK4C", Region: catalogv1alpha1.AudiobookRegion("us"), BookRef: ptr.To("guards-guards"),
			QualityProfileRef: "audiobook", RootFolderRef: "audiobooks", Tags: []string{"discworld"},
		},
	}
	c := fake.NewClientBuilder().WithScheme(k8s.MustNewScheme()).WithObjects(m).Build()
	require.NoError(t, audiobook.LinkAuthor(t.Context(), c, m, "terry-pratchett"))

	var got catalogv1alpha1.Audiobook
	require.NoError(t, c.Get(t.Context(), types.NamespacedName{Namespace: "media", Name: "guards-guards-audio"}, &got))
	require.NotNil(t, got.Spec.AuthorRef)
	assert.Equal(t, "terry-pratchett", *got.Spec.AuthorRef)
	assert.Equal(t, "B002V0QK4C", got.Spec.ASIN)
	assert.Equal(t, ptr.To("guards-guards"), got.Spec.BookRef)
	assert.Equal(t, []string{"discworld"}, got.Spec.Tags, "the merge patch touches no other spec field")
	assert.Nil(t, m.Spec.AuthorRef, "the caller's object is left as it was read")
}
