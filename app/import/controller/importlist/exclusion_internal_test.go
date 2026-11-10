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

package importlist

import (
	"context"
	"testing"

	"github.com/jonboulle/clockwork"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
	pkgimportlist "github.com/mediactl/clustarr/pkg/importlist"
)

// isExcluded reads an album's exclusion under its MusicBrainz release-group
// id and a book's under its Open Library work id (2026-11-10, the keys a
// deleted Album's and Book's ImportExclusions carry), beside the video
// keys it read before; an entry under a key the item does not carry blocks
// nothing, and an item with no recognised id at all is never excluded.
func TestIsExcludedReadsAnAlbumsReleaseGroupAndABooksWork(t *testing.T) {
	ctx := context.Background()
	bus := membus.New(clockwork.NewRealClock())
	require.NoError(t, bus.Ensure(ctx, events.Default()))
	t.Cleanup(func() { _ = bus.Close() })
	kv := bus.KV(events.BucketImportExclusions)
	put := func(name, kind, provider, id string) {
		t.Helper()
		data, err := events.ExclusionEntry{Namespace: "media", Name: name, Kind: kind, Reason: "deleted from the library"}.Encode()
		require.NoError(t, err)
		_, err = kv.Put(ctx, events.ExclusionKey(provider, id), data)
		require.NoError(t, err)
	}
	put("dsotm", "album", catalogv1alpha1.ExclusionIDKeyMusicBrainz, "rg-dsotm")
	put("dispossessed", "book", catalogv1alpha1.ExclusionIDKeyOpenLibrary, "OL1W")
	put("heat", "movie", catalogv1alpha1.ExclusionIDKeyTMDB, "949")

	tests := map[string]struct {
		ids  pkgimportlist.ExternalIDs
		want string // the blocking exclusion's name, "" for none
	}{
		"an album by its release group":        {ids: pkgimportlist.ExternalIDs{MusicBrainz: "rg-dsotm"}, want: "dsotm"},
		"a book by its open library work":      {ids: pkgimportlist.ExternalIDs{OpenLibrary: "OL1W"}, want: "dispossessed"},
		"a book with the work beside an isbn":  {ids: pkgimportlist.ExternalIDs{ISBN: "9780060125639", OpenLibrary: "OL1W"}, want: "dispossessed"},
		"a movie still blocks":                 {ids: pkgimportlist.ExternalIDs{TMDB: "949"}, want: "heat"},
		"another release group is not blocked": {ids: pkgimportlist.ExternalIDs{MusicBrainz: "rg-wywh"}, want: ""},
		"another work is not blocked":          {ids: pkgimportlist.ExternalIDs{OpenLibrary: "OL2W"}, want: ""},
		"no recognised id is never blocked":    {ids: pkgimportlist.ExternalIDs{}, want: ""},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			blocked, entry, err := isExcluded(ctx, kv, tt.ids)
			require.NoError(t, err)
			assert.Equal(t, tt.want != "", blocked)
			assert.Equal(t, tt.want, entry.Name)
		})
	}
}
