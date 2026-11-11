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
	"github.com/mediactl/clustarr/ui/actions"
)

// AddRequest.Tags are an Artist's, an Author's and an Audiobook's
// spec.tags -- the root's defaults.tags, which a root scan gives the items
// it creates from their folders and the Unmatched page's confirmation
// carries (D20) -- and every other kind ignores them.
func TestAddItemSetsTheTagsOfTheFolderKinds(t *testing.T) {
	c := addClient(t)
	ctx := t.Context()
	base := actions.AddRequest{Namespace: "media", RootFolderRef: "root", QualityProfileRef: "profile", Monitored: true, Tags: []string{"lossless"}}

	artist := base
	artist.Kind, artist.Title, artist.ProviderID = commonv1.MediaKindArtist, "Nirvana", "5b11f4ce-a62d-471e-81fc-a69a8278c7da"
	name, _, err := actions.AddItem(ctx, c, artist)
	require.NoError(t, err)
	var ar catalogv1.Artist
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "media", Name: name}, &ar))
	require.Equal(t, []string{"lossless"}, ar.Spec.Tags)

	author := base
	author.Kind, author.Title, author.ProviderID = commonv1.MediaKindAuthor, "Frank Herbert", "OL79034A"
	name, _, err = actions.AddItem(ctx, c, author)
	require.NoError(t, err)
	var au catalogv1.Author
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "media", Name: name}, &au))
	require.Equal(t, []string{"lossless"}, au.Spec.Tags)

	book := base
	book.Kind, book.Title, book.ProviderID = commonv1.MediaKindAudiobook, "Dune", "B002V1OF70"
	name, _, err = actions.AddItem(ctx, c, book)
	require.NoError(t, err)
	var ab catalogv1.Audiobook
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "media", Name: name}, &ab))
	require.Equal(t, []string{"lossless"}, ab.Spec.Tags)

	movie := base
	movie.Kind, movie.Title, movie.ProviderID = commonv1.MediaKindMovie, "Heat", "949"
	name, _, err = actions.AddItem(ctx, c, movie)
	require.NoError(t, err)
	var m catalogv1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "media", Name: name}, &m))
	require.Empty(t, m.Spec.Tags, "a movie ignores the tags")
}
