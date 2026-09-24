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

package schema_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events/schema"
)

func TestArtworkFetchTaskEncodeDecodeRoundTrip(t *testing.T) {
	in := schema.ArtworkFetchTask{
		MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"},
	}
	name, data, err := schema.Encode(in)
	require.NoError(t, err)
	assert.Equal(t, "catalog.ArtworkFetchTask.v1", name)

	var out schema.ArtworkFetchTask
	require.NoError(t, schema.Decode(name, data, &out))
	assert.Equal(t, in, out)
}

// An ArtworkFetchTask must never be decoded from a message carrying another
// payload's schema header, same as every other task in this package.
func TestArtworkFetchTaskRejectsAForeignSchemaHeader(t *testing.T) {
	_, data, err := schema.Encode(schema.ArtworkFetchTask{
		MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindMovie, Name: "heat"},
	})
	require.NoError(t, err)

	var out schema.ArtworkFetchTask
	assert.Error(t, schema.Decode("catalog.ArtworkFetchTask.v2", data, &out))
}

func TestRenderOverlayTaskEncodeDecodeRoundTrip(t *testing.T) {
	in := schema.RenderOverlayTask{
		MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindSeries, Name: "the-expanse"},
		Reason:   "ratings",
	}
	name, data, err := schema.Encode(in)
	require.NoError(t, err)
	assert.Equal(t, "catalog.RenderOverlayTask.v1", name)

	var out schema.RenderOverlayTask
	require.NoError(t, schema.Decode(name, data, &out))
	assert.Equal(t, in, out)
}

func TestMsgIDForArtworkFetch(t *testing.T) {
	got := schema.MsgIDForArtworkFetch(types.UID("u1"), "h9")
	assert.Equal(t, "u1/artwork/h9", got)
	// Deterministic: the same uid and spec hash always produce the same ID,
	// which is what makes a hot reconcile loop a no-op inside the stream's
	// deduplication window.
	assert.Equal(t, got, schema.MsgIDForArtworkFetch(types.UID("u1"), "h9"))
	assert.NotEqual(t, got, schema.MsgIDForArtworkFetch(types.UID("u1"), "h10"))
}

func TestMsgIDForRenderOverlay(t *testing.T) {
	got := schema.MsgIDForRenderOverlay(types.UID("u1"), "d9")
	assert.Equal(t, "u1/render/d9", got)
	assert.Equal(t, got, schema.MsgIDForRenderOverlay(types.UID("u1"), "d9"))
	assert.NotEqual(t, got, schema.MsgIDForRenderOverlay(types.UID("u1"), "d10"))
}

// A fetch and a render task for the same uid must never collide on one
// Msg-Id: they are published on different subjects to different consumers,
// and collapsing them would drop one task as a duplicate of the other.
func TestMsgIDForArtworkFetchAndRenderOverlayNeverCollide(t *testing.T) {
	assert.NotEqual(t,
		schema.MsgIDForArtworkFetch(types.UID("u1"), "x"),
		schema.MsgIDForRenderOverlay(types.UID("u1"), "x"))
}
