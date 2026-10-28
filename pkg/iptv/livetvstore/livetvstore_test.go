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

package livetvstore_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"

	"github.com/mediactl/clustarr/pkg/iptv/livetvstore"
)

var key = types.NamespacedName{Namespace: "media", Name: "argon-tv"}

func TestObjectNames(t *testing.T) {
	assert.Equal(t, "playlist/media/argon-tv", livetvstore.PlaylistObject(key))
	assert.Equal(t, "guide-cache/media/argon-tv", livetvstore.GuideCacheObject(key))
	assert.Equal(t, "guide-channels/media/argon-tv/xmltv", livetvstore.GuideChannelsObject(key))
	assert.Equal(t, "guide/media/argon-tv", livetvstore.GuideObject(key))
	assert.Equal(t, "guide-index/media/argon-tv", livetvstore.GuideIndexObject(key))
	sum := sha256.Sum256([]byte("https://l.example/espn.png"))
	assert.Equal(t, "logo/media/argon-tv/"+hex.EncodeToString(sum[:])[:32], livetvstore.LogoObject(key, "https://l.example/espn.png"))
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	in := livetvstore.Playlist{Account: "a1", Hash: "h", FetchedAt: time.Date(2026, 10, 27, 1, 2, 3, 0, time.UTC),
		Entries: []livetvstore.PlaylistEntry{{Key: "n:1", Name: "ESPN", Group: "Sports", Logo: "https://l/e.png", URL: "http://p/{username}/1.ts"}}}
	var b bytes.Buffer
	require.NoError(t, livetvstore.Encode(&b, in))
	var out livetvstore.Playlist
	require.NoError(t, livetvstore.Decode(&b, &out))
	assert.Equal(t, in, out)
}

func TestDecodeRefusesAnObjectOverItsCap(t *testing.T) {
	var b bytes.Buffer
	require.NoError(t, livetvstore.Encode(&b, map[string]string{"x": strings.Repeat("a", 4096)}))
	var out map[string]string
	err := livetvstore.DecodeLimit(bytes.NewReader(b.Bytes()), &out, 1024)
	require.ErrorIs(t, err, livetvstore.ErrTooLarge)
}
