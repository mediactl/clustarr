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

package events_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
)

// TestObjectStoresCarryTheirKeyScheme: every object store records its key
// scheme and the version of its objects' metadata set in the bucket's own
// metadata, which Ensure writes (artwork design §B.2 as amended 2026-10-07,
// ADR-0021 §2): the scheme, not a claim that any backfill finished.
func TestObjectStoresCarryTheirKeyScheme(t *testing.T) {
	want := map[string]map[string]string{
		events.BucketArtwork: {
			events.BucketMetaKeyKeyScheme: events.ArtworkKeyScheme,
			events.ArtworkMetaKeyVersion:  events.ArtworkMetaVersion,
		},
		events.ObjectStoreFingerprints: {
			events.BucketMetaKeyKeyScheme: events.FingerprintKeyScheme,
			events.ObjectMetaKeyVersion:   events.FingerprintMetaVersion,
		},
	}
	got := map[string]bool{}
	for _, o := range events.Default().ObjectStores {
		got[o.Name] = true
		assert.Equal(t, want[o.Name], o.Metadata, o.Name)
		cfg := events.ObjectStoreConfig(o)
		assert.Equal(t, o.Metadata, cfg.Metadata, o.Name)
		cfg.Metadata[events.BucketMetaKeyKeyScheme] = "changed"
		assert.Equal(t, want[o.Name][events.BucketMetaKeyKeyScheme], o.Metadata[events.BucketMetaKeyKeyScheme],
			"%s: ObjectStoreConfig copies the map, so a caller's edit never reaches the topology", o.Name)
	}
	assert.Len(t, got, len(want))
}

// TestValidateRefusesAChunkOverOneMiB is ADR-0020 §5: a chunk is at most an
// eighth of the servers' 8 MiB max_payload, a store's and a Put's alike;
// artwork writes 1 MiB chunks and the fingerprints keep nats.go's default.
func TestValidateRefusesAChunkOverOneMiB(t *testing.T) {
	assert.EqualValues(t, events.MiB, events.MaxObjectChunkSize)
	assert.EqualValues(t, 8*events.MiB/8, events.MaxObjectChunkSize, "an eighth of max_payload")

	top := events.Default()
	require.NoError(t, top.Validate())
	art, ok := top.ObjectStore(events.BucketArtwork)
	require.True(t, ok)
	assert.Equal(t, events.ArtworkChunkSize, art.ChunkSize)
	assert.Equal(t, events.MaxObjectChunkSize, art.ChunkSize, "artwork writes chunks at the cap")
	fp, ok := top.ObjectStore(events.ObjectStoreFingerprints)
	require.True(t, ok)
	assert.Zero(t, fp.ChunkSize, "about 20 KB each: nats.go's 128 KiB default")

	for i := range top.ObjectStores {
		if top.ObjectStores[i].Name == events.BucketArtwork {
			top.ObjectStores[i].ChunkSize = events.MaxObjectChunkSize + 1
		}
	}
	err := top.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), events.BucketArtwork+".ChunkSize")

	// A Put's own override is held to the same cap; else the store's
	// chunk size applies, else nats.go's default (0).
	got, err := events.ObjectChunkSize(art, events.ObjectMeta{})
	require.NoError(t, err)
	assert.Equal(t, events.ArtworkChunkSize, got)
	got, err = events.ObjectChunkSize(art, events.ObjectMeta{ChunkSize: 64 * events.KiB})
	require.NoError(t, err)
	assert.EqualValues(t, 64*events.KiB, got, "a Put's override wins over the store's")
	got, err = events.ObjectChunkSize(fp, events.ObjectMeta{})
	require.NoError(t, err)
	assert.Zero(t, got)
	_, err = events.ObjectChunkSize(fp, events.ObjectMeta{ChunkSize: events.MaxObjectChunkSize + 1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ObjectMeta.ChunkSize")
}
