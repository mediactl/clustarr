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

package plexextras_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/metadata/plexextras"
)

func TestAnEntryRoundTripsWithItsExtrasUnchanged(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	in := plexextras.Entry{FetchedAt: at, Extras: []json.RawMessage{
		json.RawMessage(`{"title":"Teaser Trailer","subtype":"trailer"}`),
	}}
	b, err := plexextras.Encode(in)
	require.NoError(t, err)
	out, err := plexextras.Decode(b)
	require.NoError(t, err)
	require.True(t, at.Equal(out.FetchedAt))
	require.Len(t, out.Extras, 1)
	require.JSONEq(t, string(in.Extras[0]), string(out.Extras[0]))
}

// "None" is an answer: an entry with no extras decodes as an empty,
// non-nil list, which the ui serves as an empty container.
func TestAnEntryWithNoExtrasDecodesAsAnEmptyList(t *testing.T) {
	b, err := plexextras.Encode(plexextras.Entry{FetchedAt: time.Now()})
	require.NoError(t, err)
	out, err := plexextras.Decode(b)
	require.NoError(t, err)
	require.NotNil(t, out.Extras)
	require.Empty(t, out.Extras)
}

func TestKeysAreValidNATSKeys(t *testing.T) {
	for _, id := range []string{"5d776b83fb0d55001f56a04b", "../x", ""} {
		require.True(t, events.ValidKVKey(plexextras.Key(id)), id)
	}
}
