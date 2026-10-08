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

package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/events"
	"github.com/mediactl/clustarr/pkg/events/membus"
)

// TestArtEventOnlyForServedDigestChanges: the SSE art event follows the
// digest /art serves -- the overlay if present, else the original (artwork
// design §B.8 as amended 2026-10-07, step 4). An original re-Put under an
// existing overlay serves nothing new and emits nothing; the renderer's
// overlay Put then emits once; deleting the overlay emits the original's
// digest, which /art falls back to.
func TestArtEventOnlyForServedDigestChanges(t *testing.T) {
	bus := membus.New(nil)
	require.NoError(t, bus.Ensure(t.Context(), events.Default()))
	store := bus.ObjectStore(events.BucketArtwork)
	original := events.ArtworkKey(commonv1.MediaKindMovie, "u1", string(catalogv1.ImageTypePoster), events.ArtworkVariantOriginal)
	overlay := events.ArtworkKey(commonv1.MediaKindMovie, "u1", string(catalogv1.ImageTypePoster), events.ArtworkVariantOverlay)
	put := func(name, body string) events.ObjectInfo {
		t.Helper()
		info, err := store.Put(t.Context(), name, strings.NewReader(body),
			events.ObjectMeta{Headers: map[string]string{events.HeaderContentType: "image/png"}})
		require.NoError(t, err)
		return info
	}
	put(original, "original-1")
	put(overlay, "overlay-1")

	s := NewServer(t.Context(), Options{Artwork: store})
	require.Eventually(t, s.artIndex.Synced, 10*time.Second, 10*time.Millisecond, "the artwork index never synced")
	ch, cancel := s.artEvents.subscribe()
	defer cancel()

	none := func(why string) {
		t.Helper()
		select {
		case e := <-ch:
			t.Fatalf("%s, but got %+v", why, e)
		case <-time.After(500 * time.Millisecond):
		}
	}
	one := func(why string) artEvent {
		t.Helper()
		select {
		case e := <-ch:
			return e
		case <-time.After(5 * time.Second):
			t.Fatalf("no art event: %s", why)
			return artEvent{}
		}
	}

	newOriginal := put(original, "original-2")
	none("an original re-Put under an overlay changes nothing /art serves")

	newOverlay := put(overlay, "overlay-2")
	require.Equal(t, artEvent{Key: "movie/u1/poster", V: newOverlay.Digest}, one("the overlay /art serves changed"))
	none("one change, one event")

	require.NoError(t, store.Delete(t.Context(), overlay))
	require.Equal(t, artEvent{Key: "movie/u1/poster", V: newOriginal.Digest}, one("the overlay went: /art serves the original"),
		"the event names the original's digest")
	none("one change, one event")
}
