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

package metadatainputs

import (
	"testing"

	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
	pkgmetadata "github.com/mediactl/clustarr/pkg/metadata"
)

// An Author's ask carries its spec.hardcoverID beside the Open Library key
// (2026-11-10 design §5.4), mirroring the gateway's externalIDs, so a pin
// or a change of it is a new ask with its own Msg-Id; blank or absent, it
// is left out and the ask is the one it always was.
func TestForCarriesAnAuthorsHardcoverID(t *testing.T) {
	id := " 51942 "
	with := For(&catalogv1alpha1.Author{Spec: catalogv1alpha1.AuthorSpec{OpenLibraryID: "OL23919A", HardcoverID: &id}})
	require.Equal(t, map[string]string{pkgmetadata.KeyOpenLibraryAuthor: "OL23919A", "hardcover": "51942"}, with.ProviderIDs)
	require.EqualValues(t, pkgmetadata.SchemaVersion, with.SchemaVersion)
	require.Empty(t, with.Language, "the fetch language is the gateway's, not the ask's")

	blank := ""
	without := For(&catalogv1alpha1.Author{Spec: catalogv1alpha1.AuthorSpec{OpenLibraryID: "OL23919A", HardcoverID: &blank}})
	require.Equal(t, map[string]string{pkgmetadata.KeyOpenLibraryAuthor: "OL23919A"}, without.ProviderIDs)
	require.Equal(t, without, For(&catalogv1alpha1.Author{Spec: catalogv1alpha1.AuthorSpec{OpenLibraryID: "OL23919A"}}))

	require.NotEqual(t, MsgID("uid", with), MsgID("uid", without), "a Hardcover id pinned is a new ask")
	require.False(t, Matches(without, with), "a record answered without the id does not answer the ask with it")
	require.True(t, Matches(with, Forced(with, 7)), "force and its epoch are the ask's urgency, not its identity")
}

// Matches compares the language too, so a record's inputs must stay the
// task's: the gateway writes the document's language from its own table and
// never into the inputs it answers with.
func TestMatchesComparesTheLanguage(t *testing.T) {
	a := schema.MetadataInputs{ProviderIDs: map[string]string{"tmdb": "603"}, SchemaVersion: 8}
	b := a
	b.Language = "de"
	require.False(t, Matches(a, b))
	require.True(t, Matches(a, a))
}
