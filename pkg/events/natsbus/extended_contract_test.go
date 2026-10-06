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

package natsbus_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/contracttest"
	"github.com/mediactl/clustarr/pkg/events/natsbus"
	"github.com/mediactl/clustarr/pkg/metadata"
	"github.com/mediactl/clustarr/pkg/metadata/extended"
	"github.com/mediactl/clustarr/pkg/metadata/plexextras"
)

// TestExtendedMetadataRoundTripsOnARealServer holds the extended-metadata
// bucket and its keys to a real NATS server: membus has no key grammar
// (CLAUDE.md, "A NATS KV key must match …").
func TestExtendedMetadataRoundTripsOnARealServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bus, err := natsbus.New(connect(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(ctx, contracttest.Topology()))

	kv := bus.KV(events.BucketMetadataExtended)
	doc := extended.FromPeople([]metadata.Person{{Kind: metadata.PersonCast, Name: "Tom Cullen", Character: "Russell"}}, nil)
	b, err := extended.Encode(doc)
	require.NoError(t, err)
	for _, kind := range []commonv1.MediaKind{commonv1.MediaKindMovie, commonv1.MediaKindSeries, commonv1.MediaKindEpisode} {
		key := extended.Key(kind, "95b3d1b1-5840-47c6-9385-3513f35e1f56")
		_, err := kv.Put(ctx, key, b)
		require.NoError(t, err, key)
		e, err := kv.Get(ctx, key)
		require.NoError(t, err)
		got, err := extended.Decode(e.Value)
		require.NoError(t, err)
		require.Equal(t, doc, got)
	}
	_, err = kv.Get(ctx, extended.Key(commonv1.MediaKindMovie, "absent"))
	require.True(t, errors.Is(err, events.ErrKeyNotFound), "%v", err)
}

// TestPlexExtrasRoundTripOnARealServer holds the Plex extras bucket and its
// keys to a real NATS server, with an entry the size of a long-running
// show's season (hundreds of clips).
func TestPlexExtrasRoundTripOnARealServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bus, err := natsbus.New(connect(t))
	require.NoError(t, err)
	t.Cleanup(func() { _ = bus.Close() })
	require.NoError(t, bus.Ensure(ctx, contracttest.Topology()))

	kv := bus.KV(events.BucketPlexExtras)
	extras := make([]json.RawMessage, 500)
	for i := range extras {
		extras[i] = json.RawMessage(`{"title":"Teaser Trailer","subtype":"trailer","Media":[{"url":"https://www.internetvideoarchive.net/clip/` + strings.Repeat("x", 1500) + `"}]}`)
	}
	in := plexextras.Entry{FetchedAt: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC), Extras: extras}
	b, err := plexextras.Encode(in)
	require.NoError(t, err)
	key := plexextras.Key("5d776b83fb0d55001f56a04b")
	_, err = kv.Put(ctx, key, b)
	require.NoError(t, err)
	e, err := kv.Get(ctx, key)
	require.NoError(t, err)
	out, err := plexextras.Decode(e.Value)
	require.NoError(t, err)
	require.Len(t, out.Extras, len(extras))
	_, err = kv.Get(ctx, plexextras.Key("absent"))
	require.True(t, errors.Is(err, events.ErrKeyNotFound), "%v", err)
}
