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
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/mediactl/clustarr/pkg/events"
)

// natsKVKeyGrammar is nats.go v1.53.1's own validator, restated here so the
// tests below check against the real thing rather than against this repo's
// opinion of it. Anything this rejects fails kv.Put AND kv.Delete.
var natsKVKeyGrammar = regexp.MustCompile(`^[-/_=\.a-zA-Z0-9]+$`)

// TestValidKVKeyAgreesWithNATS pins this package's own predicate against the
// upstream grammar, byte by byte, so the rest of the file can assert with
// events.ValidKVKey and mean the NATS rule.
//
// The byte sweep alone is not the whole rule, and believing it was is how
// this predicate shipped permissive: nats.go's keyValid is the character
// regex PLUS no leading ".", no trailing "." and no ".." anywhere, so
// ValidKVKey said yes to ".foo", "foo." and "a..b" while a real server
// rejects all three. The contract test in pkg/events/natsbus asserts the
// same predicate against an actual server, which is the check that does not
// depend on restating the rule correctly here.
func TestValidKVKeyAgreesWithNATS(t *testing.T) {
	for b := 0; b < 256; b++ {
		s := string([]byte{byte(b)})
		want := natsKVKeyGrammar.MatchString(s) && s != "."
		assert.Equal(t, want, events.ValidKVKey(s),
			"disagreement on byte %#x (%q)", b, s)
	}
	assert.False(t, events.ValidKVKey(""), "the empty string is not a key")

	for _, s := range []string{".foo", "foo.", "a..b", ".", "..", "a..", "..a"} {
		assert.False(t, events.ValidKVKey(s),
			"%q matches the character set but nats.go's keyValid rejects it", s)
	}
	for _, s := range []string{"a.b", "a.b.c", "grab.Movie-default-x"} {
		assert.True(t, events.ValidKVKey(s), "%q is a legal key", s)
	}
}

// TestExclusionKeyIsAlwaysALegalNATSKey is the regression case for the
// defect task C14 fixed: ImportExclusion.spec.externalIDs is an
// unconstrained map[string]string, and its values were interpolated into a
// KV key by a sanitiser that implemented no grammar at all -- it mapped
// space, tab, "*", ">", "/", "\" and the control characters, and passed
// everything else straight through.
//
// Every id below is one a user can type into a CR today. Before the fix
// ExclusionKey returned "imdb.tt0113277:2", "imdb.Amélie" and "imdb.50%"
// verbatim, all three rejected by nats.go. That is not merely a lost write:
// importexclusion's finalizer deletes by the same key, kv.Delete validates
// it identically, and so the object can never shed its finalizer and hangs
// in Terminating until someone hand-edits metadata.finalizers.
func TestExclusionKeyIsAlwaysALegalNATSKey(t *testing.T) {
	for _, id := range []string{
		"tt0113277",   // the ordinary case
		"tt0113277:2", // a colon -- illegal
		"Amélie",      // non-ASCII -- illegal
		"50%",         // a percent -- illegal
		"a,b",         // a comma -- illegal
		"tt 0113277",  // a space -- the one case the old sanitiser caught
		"a\\b",        // a backslash
		"a\x00b",      // a NUL, from a badly-behaved client
		"tt0113277\n", // a stray newline off a copy-paste
		"",            // an empty id, which must still yield a key
		"-",           // the escape character itself
		"...",         // only separators
		"tmdb=949",    // characters legal in the grammar but not the alphabet
		"a/b",         //
		"日本語",         // multi-byte throughout
		"🎬",           // outside the BMP
		"' OR 1=1 --", // nothing is filtered upstream; nothing needs to be
		"very-long-id-" + string(make([]byte, 64)),
	} {
		key := events.ExclusionKey("imdb", id)
		assert.True(t, events.ValidKVKey(key),
			"ExclusionKey(%q, %q) = %q, which NATS rejects on Put and on Delete alike", "imdb", id, key)
		assert.Regexp(t, natsKVKeyGrammar, key)
	}
}

// TestKVKeyBuildersAreInjective is the property the escaping exists for, and
// the one a lossy sanitiser silently breaks. A first attempt at this fix
// mapped every illegal byte to "-", which made the ids "a:b" and "a,b" the
// same key -- for the exclusion bucket that means one excluded item
// suppressing an unrelated one, and for the pending bucket it means two
// media items overwriting each other's best candidate.
func TestKVKeyBuildersAreInjective(t *testing.T) {
	inputs := []string{
		"a:b", "a,b", "a-b", "a b", "a.b", "a/b", "a=b", "a_b", "ab",
		"a--b", "a-2db", "", "-", "--", "-00",
	}
	for _, build := range []struct {
		name string
		fn   func(string) string
	}{
		{"ExclusionKey", func(s string) string { return events.ExclusionKey("imdb", s) }},
		{"LeaseKey", events.LeaseKey},
		{"PendingKey", events.PendingKey},
	} {
		seen := make(map[string]string, len(inputs))
		for _, in := range inputs {
			key := build.fn(in)
			prev, clash := seen[key]
			require.False(t, clash,
				"%s collapsed %q and %q onto %q", build.name, prev, in, key)
			seen[key] = in
		}
	}
}

// The source segment is escaped too, so a provider name can never eat the
// "." the builder puts between the two segments.
func TestExclusionKeySegmentsCannotForgeTheSeparator(t *testing.T) {
	require.NotEqual(t,
		events.ExclusionKey("imdb.tt0113277", "x"),
		events.ExclusionKey("imdb", "tt0113277.x"))
}

// LeaseKey and PendingKey were legal only by accident: MediaKey flattens a
// DNS-1123 namespace and name for the WIRE (a different grammar), and every
// byte that survived happened to also be legal in a KV key. They are now
// legal by construction, for any input at all.
func TestLeaseAndPendingKeysAreLegalForAnyMediaKey(t *testing.T) {
	for _, mk := range []string{
		events.MediaKey("Movie", "default", "the-matrix"),
		events.MediaKey("Episode", "media/ops", "s01e02"),
		events.MediaKey("Movie", "default", "Wall·E (2008)"),
		"hand-written:not-a-media-key",
		"",
	} {
		assert.True(t, events.ValidKVKey(events.LeaseKey(mk)), "LeaseKey(%q)", mk)
		assert.True(t, events.ValidKVKey(events.PendingKey(mk)), "PendingKey(%q)", mk)
	}
}

// A readable prefix is the only reason MediaKey keeps one, so check the
// escaping has not destroyed it for the ordinary DNS-1123 case: "-" doubles,
// which is noisier but still legible, and nothing else changes.
func TestKVKeyTokenLeavesOrdinaryIdentifiersRecognisable(t *testing.T) {
	assert.Equal(t, "tt0113277", events.KVKeyToken("tt0113277"))
	assert.Equal(t, "imdb.tt0113277", events.ExclusionKey("imdb", "tt0113277"))
	assert.Equal(t, "the--matrix", events.KVKeyToken("the-matrix"))
}
