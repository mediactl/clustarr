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

package k8s

import (
	"crypto/sha1" //nolint:gosec // naming only, never a security decision
	"encoding/binary"
	"encoding/hex"
	"regexp"
	"strings"
)

// Name length limits from the apiserver. Most Clustarr kinds are plain
// RFC 1123 subdomains; anything that will become a label value, a Job name or
// a StatefulSet pod name has to fit in 63 characters instead.
const (
	// MaxNameLength is the apiserver's limit for a metadata.name that is
	// validated as a DNS subdomain.
	MaxNameLength = 253

	// MaxLabelValueLength is the limit for a label value and for names
	// validated as a DNS-1123 label.
	MaxLabelValueLength = 63

	// HashSuffixLength is the number of hex characters of the digest kept in
	// a deterministic name, from §5's `<target>-<sha1(guid)[:10]>`. Ten hex
	// characters is 40 bits: enough that a collision inside one namespace is
	// not a practical concern, short enough to leave room for the prefix.
	HashSuffixLength = 10
)

var nonNameRunes = regexp.MustCompile(`[^a-z0-9.-]+`)

// dottedRuns matches a run of separators with a dot in it. Longer than the
// dot alone, it would start or end a DNS label with "-", or leave one empty
// ("mr.-robot", "a..b"), which the apiserver rejects.
var dottedRuns = regexp.MustCompile(`[.-]*\.[.-]*`)

// HashSuffix returns the first [HashSuffixLength] hex characters of the SHA-1
// of parts joined by "|".
//
// The digest is a naming device, not a security or integrity claim: it exists
// so that the same input always produces the same object name and a controller
// can create-if-absent without keeping a side table. §5 names Downloads
// `<target>-<sha1(guid)[:10]>` and §6.4 creates TranscodeJobs "by deterministic
// name" on the same basis.
func HashSuffix(parts ...string) string {
	sum := sha1.Sum([]byte(strings.Join(parts, "|"))) //nolint:gosec // naming only
	return hex.EncodeToString(sum[:])[:HashSuffixLength]
}

// DeterministicName renders "<target>-<HashSuffix(parts...)>", truncating
// target so the result fits in maxLen.
//
// target is normalised to lower case and to the characters a DNS subdomain
// allows, so a release title or a file path can be passed straight in. The
// suffix is never truncated: it is what makes the name unique, while the
// prefix is only there to make `kubectl get` readable.
func DeterministicName(target string, maxLen int, parts ...string) string {
	if maxLen <= 0 || maxLen > MaxNameLength {
		maxLen = MaxNameLength
	}
	suffix := HashSuffix(parts...)

	// "-" + suffix, and at least one character of prefix.
	budget := maxLen - len(suffix) - 1
	if budget < 1 {
		return suffix
	}

	prefix := NormalizeName(target)
	if prefix == "" {
		prefix = "x"
	}
	if len(prefix) > budget {
		prefix = strings.TrimRight(prefix[:budget], ".-")
	}
	if prefix == "" {
		prefix = "x"
	}
	return prefix + "-" + suffix
}

// ChildName is [DeterministicName] with the 253-character subdomain budget. It
// is the default for a custom resource a controller creates on behalf of
// another, such as the Download named after its target media item and the
// release guid.
func ChildName(target string, parts ...string) string {
	return DeterministicName(target, MaxNameLength, parts...)
}

// LabelSafeName is [DeterministicName] with the 63-character budget, for names
// that also have to be a label value or a DNS-1123 label: a Job name, a
// StatefulSet name, or anything mirrored into a selector label.
func LabelSafeName(target string, parts ...string) string {
	return DeterministicName(target, MaxLabelValueLength, parts...)
}

// NormalizeName lower-cases s and reduces it to the characters an RFC 1123
// subdomain allows, collapsing every run of anything else into a single "-"
// and trimming leading and trailing separators. A dot is kept only between
// two alphanumerics: a separator run with a dot in it ("Mr. Robot" becomes
// "mr.-" before this step) collapses to "-" too, so every label stays valid,
// while a name that was already valid is unchanged. An input with nothing
// usable in it returns "".
func NormalizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = nonNameRunes.ReplaceAllString(s, "-")
	s = dottedRuns.ReplaceAllStringFunc(s, func(run string) string {
		if run == "." {
			return run
		}
		return "-"
	})
	s = strings.Trim(s, "-.")
	return s
}

// HashOrdinal maps key onto one of replicas slots, deterministically and
// without reference to how many replicas are currently ready.
//
// §6.3 assigns a Download to a torrent engine as
// `<client>-<hash(infoHash|guid) mod spec.replicas>` and is explicit that the
// divisor is the desired replica count, not the ready count: an engine that is
// temporarily down must keep its share, or every restart would reshuffle the
// whole fleet and re-download what is already on disk.
//
// It returns 0 when replicas is not positive, so a caller that has not read the
// spec yet still produces a legal ordinal.
func HashOrdinal(replicas int32, parts ...string) int32 {
	if replicas <= 1 {
		return 0
	}
	// SHA-1 rather than a 32-bit FNV: the ordinal is the low bits of the
	// digest modulo a small power of two most of the time, and FNV-1a's low
	// bits are poorly distributed for the structured keys this gets
	// ("<infohash>|<guid>"), which piles shards onto a couple of ordinals.
	sum := sha1.Sum([]byte(strings.Join(parts, "|"))) //nolint:gosec // naming only
	n := binary.BigEndian.Uint64(sum[:8])
	return int32(n % uint64(replicas)) //nolint:gosec // bounded by replicas
}
