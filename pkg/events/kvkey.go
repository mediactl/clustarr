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
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

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
		//
		// One hex digit, not two: the byte loop below always emits "-" plus
		// TWO hex digits, so "-0" is a shape it cannot produce. "-00" would
		// have been the escape of byte 0x00, and KVKeyToken("") would have
		// collided with KVKeyToken("\x00") -- which is precisely the
		// injectivity this function claims. An empty id is reachable today:
		// ImportExclusion.spec.externalIDs caps the map size but constrains
		// no value.
		return "-0"
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

// RecordKey is a records bucket's key for one object: its UID through
// KVKeyToken (loop spec 2026-10-06 §4.4). It is ProbeKey's rule for every
// remediation.
func RecordKey(uid string) string { return KVKeyToken(uid) }

// RecordSubKey is the key of one of several records an object has in a
// bucket (a subtitle language): "<KVKeyToken(uid)>.<KVKeyToken(sub)>". Neither
// token can hold a ".", so the pair cannot be forged and a Watch wildcard
// matches one token.
func RecordSubKey(uid, sub string) string { return KVKeyToken(uid) + "." + KVKeyToken(sub) }

// ParseKVKeyToken inverts KVKeyToken. It refuses anything KVKeyToken never
// emits, the non-canonical escapes included ("-41" for "A", upper-case hex),
// so a key read back always names exactly one id.
func ParseKVKeyToken(tok string) (string, error) {
	if tok == "-0" {
		return "", nil
	}
	if tok == "" {
		return "", errors.New("events: an empty string is no KV key token")
	}
	var b []byte
	for i := 0; i < len(tok); i++ {
		c := tok[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			b = append(b, c)
		case c == '-' && i+1 < len(tok) && tok[i+1] == '-':
			b = append(b, '-')
			i++
		case c == '-' && i+2 < len(tok):
			v, err := hex.DecodeString(tok[i+1 : i+3])
			if err != nil {
				return "", fmt.Errorf("events: %q is no KV key token: %w", tok, err)
			}
			b = append(b, v[0])
			i += 2
		default:
			return "", fmt.Errorf("events: %q is no KV key token: byte %q at %d", tok, c, i)
		}
	}
	s := string(b)
	if KVKeyToken(s) != tok {
		return "", fmt.Errorf("events: %q is not KVKeyToken's encoding of %q", tok, s)
	}
	return s, nil
}

// ValidKVKey reports whether s is a key a NATS key/value bucket will accept.
// nats.go v1.53.1 validates every key on Put and on Delete alike, and its
// gate (keyValid, kv.go:602 and jetstream/kv.go:911) is FOUR conditions, not
// one:
//
//	^[-/_=\.a-zA-Z0-9]+$   and   no leading "."   and   no trailing "."
//	                        and   no ".." anywhere
//
// Implementing only the regex made this predicate say yes to ".foo", "foo."
// and "a..b", all of which a real server rejects. No builder here can emit
// those shapes -- [KVKeyToken] never returns "" so no segment can be empty --
// but a predicate that is wrong in the permissive direction is worse than no
// predicate, and this one is exported.
func ValidKVKey(s string) bool {
	if s == "" {
		return false
	}
	if s[0] == '.' || s[len(s)-1] == '.' {
		return false
	}
	if strings.Contains(s, "..") {
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
