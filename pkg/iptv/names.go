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

package iptv

import (
	"crypto/sha256"
	"encoding/hex"
)

// ChannelObjectName is the name of the IPTVChannel of a provider's playlist
// entry key: its provider's name and a hash of its key, so a key has one
// channel whoever makes it -- the ui's AddChannel and the lineup controller
// both name channels by it (a second add is AlreadyExists).
func ChannelObjectName(provider, key string) string {
	sum := sha256.Sum256([]byte(key))
	if len(provider) > 52 {
		provider = provider[:52]
	}
	return provider + "-" + hex.EncodeToString(sum[:])[:10]
}
