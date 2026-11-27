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
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
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
	require.EqualValues(t, pkgmetadata.SchemaVersion(commonv1.MediaKindAuthor), with.SchemaVersion)
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

// A refresh asks for its own kind's schema version, so a field one kind
// learns makes no other kind's answer read as outdated (owner, 2026-11-27:
// "Can we split this for each type?" -- a Series' new rating was about to
// refresh every Album).
func TestForAsksForTheKindsOwnSchemaVersion(t *testing.T) {
	series := For(&catalogv1alpha1.Series{})
	album := For(&catalogv1alpha1.Album{})
	require.EqualValues(t, pkgmetadata.SchemaVersion(commonv1.MediaKindSeries), series.SchemaVersion)
	require.EqualValues(t, pkgmetadata.SchemaVersion(commonv1.MediaKindAlbum), album.SchemaVersion)
	require.Greater(t, series.SchemaVersion, album.SchemaVersion, "a Series has learned its TMDB ratings; an Album nothing since")
	require.Zero(t, For(&catalogv1alpha1.Episode{}).SchemaVersion, "a kind with no document of its own asks for none")
}
