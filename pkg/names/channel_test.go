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

package names_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/mediactl/clustarr/pkg/names"
)

// TestChannelObjectNameIsTheProviderAndTheKeysHash: an IPTVChannel's name
// is its provider's, cut to 52 characters, and the first ten hex digits of
// its playlist key's SHA-256 -- the one rule the ui's add and the lineup
// controller both name a channel by, so a key has one channel whoever makes
// it (sha256sum of the key, by hand).
func TestChannelObjectNameIsTheProviderAndTheKeysHash(t *testing.T) {
	for _, c := range []struct{ provider, key, want string }{
		{"argon-tv", "n:abc", "argon-tv-f827674384"},
		{"argon-tv", "t:espn.us", "argon-tv-e2b91f1ca8"},
		{"m3u", "", "m3u-e3b0c44298"},
	} {
		assert.Equal(t, c.want, names.ChannelObjectName(c.provider, c.key), "%+v", c)
	}
	assert.NotEqual(t, names.ChannelObjectName("argon-tv", "n:abc"), names.ChannelObjectName("argon-tv", "n:abd"), "a key is its own channel")

	long := names.ChannelObjectName(strings.Repeat("p", 70), "n:abc")
	assert.Len(t, long, 63, "a 52-character provider and the hash fit a label")
	assert.Empty(t, validation.IsDNS1123Subdomain(long))
}
