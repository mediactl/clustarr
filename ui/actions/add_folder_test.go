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

package actions_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/names"
	"github.com/mediactl/clustarr/ui/actions"
)

// AddRequest.Folder pins an Artist's, an Author's and an Audiobook's
// spec.folder as written -- the folder a root scan could not identify,
// confirmed on the Unmatched page (the 2026-11-10 non-video libraries
// design's D20) -- its surrounding whitespace and a trailing "/" trimmed;
// an empty one pins nothing, so the root's preset names the folder.
func TestAddItemPinsTheFolder(t *testing.T) {
	c := addClient(t)
	ctx := t.Context()
	base := actions.AddRequest{Namespace: "media", RootFolderRef: "root", QualityProfileRef: "profile", Monitored: true}

	artist := base
	artist.Kind, artist.Title, artist.ProviderID, artist.Folder = commonv1.MediaKindArtist, "Sunn O)))", "5b11f4ce-a62d-471e-81fc-a69a8278c7da", " Sunn O)))/ "
	name, _, err := actions.AddItem(ctx, c, artist)
	require.NoError(t, err)
	require.Equal(t, names.Artist("Sunn O)))", artist.ProviderID), name)
	var ar catalogv1.Artist
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "media", Name: name}, &ar))
	require.NotNil(t, ar.Spec.Folder)
	require.Equal(t, "Sunn O)))", *ar.Spec.Folder, "the folder as written, never sanitized")

	author := base
	author.Kind, author.Title, author.ProviderID, author.Folder = commonv1.MediaKindAuthor, "Frank Herbert", "OL79034A", "Frank Herbert"
	name, _, err = actions.AddItem(ctx, c, author)
	require.NoError(t, err)
	var au catalogv1.Author
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "media", Name: name}, &au))
	require.Equal(t, "Frank Herbert", *au.Spec.Folder)

	book := base
	book.Kind, book.Title, book.ProviderID, book.Folder = commonv1.MediaKindAudiobook, "Dune", "B002V1OF70", "Frank Herbert/Dune"
	name, _, err = actions.AddItem(ctx, c, book)
	require.NoError(t, err)
	var ab catalogv1.Audiobook
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "media", Name: name}, &ab))
	require.Equal(t, "Frank Herbert/Dune", *ab.Spec.Folder, "an audiobook's <author>/<title>")

	plain := base
	plain.Kind, plain.Title, plain.ProviderID = commonv1.MediaKindArtist, "Radiohead", "a74b1b7f-71a5-4011-9441-d0b5e4122711"
	name, _, err = actions.AddItem(ctx, c, plain)
	require.NoError(t, err)
	var unpinned catalogv1.Artist
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "media", Name: name}, &unpinned))
	require.Nil(t, unpinned.Spec.Folder, "no folder pins nothing")

	movie := base
	movie.Kind, movie.Title, movie.ProviderID, movie.Folder = commonv1.MediaKindMovie, "Heat", "949", "Heat (1995)"
	name, _, err = actions.AddItem(ctx, c, movie)
	require.NoError(t, err)
	var m catalogv1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "media", Name: name}, &m))
	require.Nil(t, m.Spec.Folder, "a movie ignores the folder")
}

// A folder that cannot lie under the root -- absolute, or walking the tree
// -- is ErrInvalid and writes nothing, as pkg/naming/catalogctx's join of a
// pin would refuse it.
func TestAddItemRefusesAFolderOutsideTheRoot(t *testing.T) {
	c := addClient(t)
	for _, folder := range []string{"/data/media/music/Nirvana", "..", "../Nirvana", "Nirvana/../x", "./Nirvana", "a//b", "Frank Herbert/ /Dune"} {
		req := actions.AddRequest{
			Kind: commonv1.MediaKindArtist, Namespace: "media", Title: "Nirvana", ProviderID: "5b11f4ce-a62d-471e-81fc-a69a8278c7da",
			RootFolderRef: "root", QualityProfileRef: "profile", Folder: folder,
		}
		_, _, err := actions.AddItem(t.Context(), c, req)
		require.ErrorIs(t, err, actions.ErrInvalid, folder)
		require.ErrorIs(t, actions.Validate(req), actions.ErrInvalid, folder)
	}
	var list catalogv1.ArtistList
	require.NoError(t, c.List(t.Context(), &list))
	require.Empty(t, list.Items)
}
