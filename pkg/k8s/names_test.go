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
	"fmt"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation"
)

func TestHashSuffixIsStableAndShort(t *testing.T) {
	got := HashSuffix("tt1375666")
	if len(got) != HashSuffixLength {
		t.Fatalf("HashSuffix length = %d, want %d (%q)", len(got), HashSuffixLength, got)
	}
	if again := HashSuffix("tt1375666"); again != got {
		t.Fatalf("HashSuffix is not deterministic: %q then %q", got, again)
	}
	if other := HashSuffix("tt1375667"); other == got {
		t.Fatalf("HashSuffix collided on neighbouring inputs: %q", got)
	}
}

func TestHashSuffixJoinsPartsUnambiguously(t *testing.T) {
	// "a|b" and "ab" must not hash alike, or a two-part key could collide
	// with a one-part key.
	if HashSuffix("a", "b") == HashSuffix("ab") {
		t.Fatal("HashSuffix does not separate its parts")
	}
}

func TestDeterministicNameMatchesSpecShape(t *testing.T) {
	// §5: Download is named `<target>-<sha1(guid)[:10]>`.
	const guid = "https://indexer.example/details/abcdef"
	name := ChildName("inception", guid)

	prefix, suffix, ok := strings.Cut(name, "-")
	if !ok {
		t.Fatalf("ChildName = %q, want <target>-<hash>", name)
	}
	if prefix != "inception" {
		t.Fatalf("prefix = %q, want %q", prefix, "inception")
	}
	if suffix != HashSuffix(guid) {
		t.Fatalf("suffix = %q, want %q", suffix, HashSuffix(guid))
	}
}

func TestDeterministicNameIsAValidObjectName(t *testing.T) {
	cases := map[string]string{
		"plain":            "inception",
		"spaces and case":  "The Lord of the Rings: The Two Towers",
		"release title":    "Movie.Name.2019.2160p.UHD.BluRay.x265.10bit.HDR-GROUP",
		"path":             "/data/media/movies/Some Movie (2019)/file.mkv",
		"leading garbage":  "---weird---",
		"unicode":          "Amélie",
		"over long":        strings.Repeat("abcdefghij", 40),
		"nothing reusable": "***",
		"empty":            "",
	}
	for label, target := range cases {
		t.Run(label, func(t *testing.T) {
			name := ChildName(target, "guid")
			if errs := validation.IsDNS1123Subdomain(name); len(errs) > 0 {
				t.Fatalf("ChildName(%q) = %q is not a valid name: %v", target, name, errs)
			}
			if len(name) > MaxNameLength {
				t.Fatalf("ChildName(%q) is %d characters, over the %d limit", target, len(name), MaxNameLength)
			}
			if !strings.HasSuffix(name, HashSuffix("guid")) {
				t.Fatalf("ChildName(%q) = %q lost its hash suffix", target, name)
			}
		})
	}
}

func TestLabelSafeNameFitsALabelValue(t *testing.T) {
	name := LabelSafeName(strings.Repeat("transcode-job-", 20), "uid", "profile")
	if errs := validation.IsDNS1123Label(name); len(errs) > 0 {
		t.Fatalf("LabelSafeName = %q is not a valid label: %v", name, errs)
	}
	if len(name) > MaxLabelValueLength {
		t.Fatalf("LabelSafeName is %d characters, over the %d limit", len(name), MaxLabelValueLength)
	}
}

func TestDeterministicNameSeparatesDistinctSeeds(t *testing.T) {
	a := ChildName("inception", "guid-a")
	b := ChildName("inception", "guid-b")
	if a == b {
		t.Fatalf("two guids produced one name: %q", a)
	}
}

func TestNormalizeName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Inception", "inception"},
		{"  spaced  out  ", "spaced-out"},
		{"a/b/c", "a-b-c"},
		{"--lead-and-trail--", "lead-and-trail"},
		{"UPPER.case-1", "upper.case-1"},
		{"***", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := NormalizeName(c.in); got != c.want {
			t.Errorf("NormalizeName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestHashOrdinalIsStableAndInRange(t *testing.T) {
	const replicas = 4
	first := HashOrdinal(replicas, "infohash", "guid")
	if first < 0 || first >= replicas {
		t.Fatalf("HashOrdinal = %d, want [0,%d)", first, replicas)
	}
	if again := HashOrdinal(replicas, "infohash", "guid"); again != first {
		t.Fatalf("HashOrdinal is not deterministic: %d then %d", first, again)
	}
}

func TestHashOrdinalDegenerateReplicaCounts(t *testing.T) {
	for _, replicas := range []int32{-1, 0, 1} {
		if got := HashOrdinal(replicas, "anything"); got != 0 {
			t.Errorf("HashOrdinal(%d) = %d, want 0", replicas, got)
		}
	}
}

func TestHashOrdinalSpreadsAcrossReplicas(t *testing.T) {
	// §6.3 shards Downloads across engine ordinals; a hash that piles every
	// key onto one ordinal would silently serialise the whole fleet.
	const replicas = 4
	seen := map[int32]int{}
	for i := range 200 {
		seen[HashOrdinal(replicas, HashSuffix(fmt.Sprintf("infohash-%03d", i)))]++
	}
	if len(seen) != replicas {
		t.Fatalf("200 keys landed on %d of %d ordinals: %v", len(seen), replicas, seen)
	}
}

func TestHashOrdinalSpreadsStructuredKeys(t *testing.T) {
	// The keys §6.3 actually passes share long prefixes and differ late. A
	// 32-bit FNV put 0 of 200 such keys on ordinal 3 of 4, so this case is
	// kept as its own regression.
	const replicas = 4
	seen := map[int32]int{}
	for i := range 200 {
		seen[HashOrdinal(replicas,
			fmt.Sprintf("magnet:?xt=urn:btih:%040x", i),
			fmt.Sprintf("https://indexer.example/details/%d", i))]++
	}
	if len(seen) != replicas {
		t.Fatalf("200 structured keys landed on %d of %d ordinals: %v", len(seen), replicas, seen)
	}
	for ordinal, n := range seen {
		if n < 200/replicas/3 {
			t.Errorf("ordinal %d got only %d of 200 keys: %v", ordinal, n, seen)
		}
	}
}
