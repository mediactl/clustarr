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

package importliststate_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/mediactl/clustarr/app/import/importliststate"
	"github.com/mediactl/clustarr/pkg/importlist"
	"github.com/mediactl/clustarr/pkg/metadata"
)

// A book's Open Library work id crosses both ways as metadata's olwork key
// (2026-11-10: the key a deleted Book's ImportExclusion carries, which the
// import-list sync's exclusion lookup reads), never the edition's or the
// author's; an album's MusicBrainz id stays the release group's. An empty
// field leaves no key behind.
func TestOpenLibraryWorkCrossesBothWays(t *testing.T) {
	ids := importlist.ExternalIDs{OpenLibrary: "OL45883W", MusicBrainz: "rg-1"}
	md := importliststate.ToMetadataExternalIDs(ids)
	assert.Equal(t, metadata.ExternalIDs{metadata.KeyOpenLibraryWork: "OL45883W", metadata.KeyMBReleaseGroup: "rg-1"}, md)
	assert.Equal(t, ids, importliststate.FromMetadataExternalIDs(md))

	back := importliststate.FromMetadataExternalIDs(metadata.ExternalIDs{
		metadata.KeyOpenLibraryEdition: "OL1M", metadata.KeyOpenLibraryAuthor: "OL1A",
	})
	assert.Empty(t, back.OpenLibrary, "an edition or an author is not the work")
	assert.NotContains(t, importliststate.ToMetadataExternalIDs(importlist.ExternalIDs{}), metadata.KeyOpenLibraryWork)
}
