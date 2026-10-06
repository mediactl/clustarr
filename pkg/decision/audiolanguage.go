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

package decision

import (
	"strings"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/lang"
	"github.com/mediactl/clustarr/pkg/quality"
)

// LacksLanguage reports whether a file whose probed audio languages are
// audio (canonical BCP-47 tags; nil when unknown) lacks the language profile
// p wants: the item's original language for "original", the tag itself for a
// tag, nothing for "any" or "". Unknown audio or an unknown original
// language is never "lacking" -- the verdict only fires on a fact. Tags
// compare by their base language ("pt-BR" carries "pt"). Anime dual-audio
// spec §5.3: Monster S01E01, a Japanese show, was imported with Korean audio
// alone.
func LacksLanguage(p quality.Profile, originalTag string, audio []string) bool {
	if audio == nil {
		return false
	}
	want := p.Language
	if len(p.AudioLanguages) > 0 {
		want = "original" // the anchor a graft aligns on (anime dual-audio spec §5.3)
	}
	switch want {
	case "", "any":
		return false
	case "original":
		want = originalTag
	}
	w, ok := lang.Normalize(want)
	if !ok {
		return false
	}
	for _, a := range audio {
		if baseLanguage(a) == baseLanguage(string(w)) {
			return false
		}
	}
	return true
}

// baseLanguage is a tag's primary subtag, lower-cased.
func baseLanguage(tag string) string {
	b, _, _ := strings.Cut(tag, "-")
	return strings.ToLower(b)
}

// AudioLanguages returns a probe's audio track languages as canonical
// BCP-47 tags in stream order, deduplicated, or nil when they are not all
// known: no probe, no audio stream, or any track untagged ("und", "unk",
// "" or unparseable). One unknown track makes the whole set unknown, since that
// track may be the wanted language; LacksLanguage never fires on nil.
func AudioLanguages(mi *common.MediaInfo) []string {
	if mi == nil || len(mi.Audio) == 0 {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	for _, a := range mi.Audio {
		t, ok := lang.Normalize(a.Language)
		// "unk" is a muxer's "unknown" placeholder that lang.Normalize
		// accepts as a well-formed ISO 639-3 code: 75 Mister Rogers files
		// on the owner's library carry it, and reading it as a language
		// flagged every one of them WrongLanguage (2026-10-06).
		if !ok || t == "unk" {
			return nil
		}
		if !seen[string(t)] {
			seen[string(t)] = true
			out = append(out, string(t))
		}
	}
	return out
}
