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

package projection_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/ui/projection"
)

// FolderHolder finds the Artist, Author or Audiobook of the namespace and
// root that pins a folder or resolved to it, the folder compared as a
// path ("Nirvana/" is "Nirvana"); an item of another namespace or root, or
// of a kind that pins no folder, holds none, and without the root's path
// only the pins are compared.
func TestFolderHolder(t *testing.T) {
	meta := func(ns, name string) metav1.ObjectMeta { return metav1.ObjectMeta{Namespace: ns, Name: name} }
	pinned := &catalogv1.Author{ObjectMeta: meta("media", "herbert"), Spec: catalogv1.AuthorSpec{RootFolderRef: "books", Folder: new("Frank Herbert/")}}
	placed := &catalogv1.Audiobook{ObjectMeta: meta("media", "dune"), Spec: catalogv1.AudiobookSpec{RootFolderRef: "audio"}, Status: catalogv1.AudiobookStatus{Path: "/data/audio/Frank Herbert/Dune/"}}
	otherNS := &catalogv1.Artist{ObjectMeta: meta("other", "nirvana"), Spec: catalogv1.ArtistSpec{RootFolderRef: "music", Folder: new("Nirvana")}}
	otherRoot := &catalogv1.Artist{ObjectMeta: meta("media", "nirvana-2"), Spec: catalogv1.ArtistSpec{RootFolderRef: "music-2", Folder: new("Nirvana")}}
	movie := &catalogv1.Movie{ObjectMeta: meta("media", "heat"), Spec: catalogv1.MovieSpec{RootFolderRef: "music", Folder: new("Nirvana")}}
	objs := []client.Object{movie, otherNS, otherRoot, pinned, placed}

	require.Same(t, client.Object(pinned), projection.FolderHolder(objs, "media", "books", "/data/books", "Frank Herbert"))
	require.Same(t, client.Object(pinned), projection.FolderHolder(objs, "media", "books", "", "Frank Herbert"), "a pin needs no root path")
	require.Same(t, client.Object(placed), projection.FolderHolder(objs, "media", "audio", "/data/audio/", "Frank Herbert/Dune"))
	require.Nil(t, projection.FolderHolder(objs, "media", "audio", "", "Frank Herbert/Dune"), "a resolved path needs the root's")
	require.Nil(t, projection.FolderHolder(objs, "media", "music", "/data/music", "Nirvana"), "another namespace's, another root's and a movie's hold none")
	require.Nil(t, projection.FolderHolder(objs, "media", "books", "/data/books", ""))
}
