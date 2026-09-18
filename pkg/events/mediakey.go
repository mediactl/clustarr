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

package events

import (
	"crypto/sha1"
	"encoding/hex"
)

// mediaKeyHashLen is how many hex characters of the identity digest the key
// carries. Ten hex characters is 40 bits, matching the convention spec §8.2
// already uses for Download names (sha1(guid)[:10]).
const mediaKeyHashLen = 10

// MediaKey builds the <mediaKey> token that spec §5's work subjects and §5's
// KV buckets use to name one media item: the search, grab and metadata
// subjects interpolate it, and LeaseKey and PendingKey derive the
// clustarr-leases and clustarr-pending keys from it.
//
// Two properties matter, and neither is free:
//
// The kind is part of the identity. A Movie and a Series may both be called
// default/thing, and without the kind they would share one grab lease --
// whichever grabbed first would silently block the other, because a lease is
// taken with Create-fails-if-exists and the loser simply acks and stops.
//
// The digest suffix is what actually guarantees uniqueness, because the
// readable prefix alone cannot. Subject tokens may not contain "." or "/", so
// tok() rewrites both to "-" before the key reaches the wire; after that
// rewrite namespace "default-foo"/name "bar" and namespace "default"/name
// "foo-bar" are the same string. Hashing the three fields with an explicit
// separator that cannot occur in a DNS-1123 name keeps them distinct no
// matter how the prefix is flattened. The prefix stays only so a subject or a
// KV key is still readable when someone is watching the bus.
//
// kind is the commonv1.MediaKind as a string; this package deliberately does
// not import the API types, so callers convert.
func MediaKey(kind, namespace, name string) string {
	sum := sha1.Sum([]byte(kind + "\x00" + namespace + "\x00" + name))
	return tok(kind) + "-" + tok(namespace) + "-" + tok(name) + "-" + hex.EncodeToString(sum[:])[:mediaKeyHashLen]
}
