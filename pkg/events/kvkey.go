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

import "encoding/hex"

// KVKeyToken escapes s into the alphabet [0-9A-Za-z-], a strict subset of
// the key grammar nats.go enforces (see [ValidKVKey]). It is the one
// implementation every KV key in this repo is built from: [LeaseKey],
// [PendingKey] and [ExclusionKey] here, and catalogarr's metadata cache key.
//
// The encoding is injective, and that is the point rather than a nicety. A
// lossy sanitiser that mapped every illegal byte to the same replacement
// would collapse the ids "a:b" and "a,b" onto one key and serve one item's
// data for another -- which is exactly what a first attempt at this did,
// until its own test caught it. "-" escapes itself as "--" and every other
// non-alphanumeric byte becomes "-XX" in hex, so distinct inputs stay
// distinct. Restricting the output to this alphabet also keeps it disjoint
// from the ".", "_" and "=" separators the key builders join tokens with, so
// no caller-supplied value can forge one.
//
// Why this exists at all: KV keys carry values from outside this process --
// provider ids out of ImportExclusion.spec.externalIDs, which is an
// unconstrained map[string]string -- and nats.go validates the key on both
// Put AND Delete. A key that fails validation therefore does not merely lose
// a write: an ImportExclusion whose finalizer deletes such a key can never
// remove that finalizer, and the object hangs in Terminating until someone
// hand-edits metadata.finalizers.
func KVKeyToken(s string) string {
	if s == "" {
		// Not "", which would make an empty segment vanish into its
		// separators: ExclusionKey("", "x") and ExclusionKey(".x", "")
		// must not collide.
		return "-00"
	}
	var b []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			b = append(b, c)
		case c == '-':
			b = append(b, '-', '-')
		default:
			b = append(b, '-')
			b = append(b, hex.EncodeToString([]byte{c})...)
		}
	}
	return string(b)
}

// ValidKVKey reports whether s is a key a NATS key/value bucket will accept:
// nats.go v1.53.1 validates every key, on Put and on Delete alike, against
//
//	^[-/_=\.a-zA-Z0-9]+$
//
// It is the grammar [KVKeyToken] and the key builders in this package are
// written against, and the thing their tests assert rather than restating
// the character set a third time.
func ValidKVKey(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '-', c == '/', c == '_', c == '=', c == '.':
		default:
			return false
		}
	}
	return true
}
