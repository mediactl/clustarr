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
	"slices"
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
		want = audioAnchor(p) // the anchor a graft aligns on (anime dual-audio spec §5.3)
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

// MissingAudio is the languages p's audio policy wants that a file whose
// probed audio languages are audio lacks, as canonical tags ("original" is
// originalTag; a language that does not resolve is dropped). It is nil with
// no policy, or when audio is unknown (nil): an untagged track may be the
// wanted one.
func MissingAudio(p quality.Profile, originalTag string, audio []string) []string {
	if audio == nil {
		return nil
	}
	var missing []string
	for _, w := range WantedAudio(p, originalTag) {
		found := false
		for _, a := range audio {
			if baseLanguage(a) == baseLanguage(w) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, w)
		}
	}
	return missing
}

// WantedAudio is p's audio policy languages as canonical tags, each once:
// "original" is originalTag, and a language that does not resolve is
// dropped. It is nil with no policy. The optional languages are not among
// them (OptionalAudio).
func WantedAudio(p quality.Profile, originalTag string) []string {
	return audioTags(p.AudioLanguages, originalTag, nil)
}

// OptionalAudio is p's optional audio languages (Profile.AudioOptional) as
// canonical tags, each once and none of WantedAudio's.
func OptionalAudio(p quality.Profile, originalTag string) []string {
	return audioTags(p.AudioOptional, originalTag, WantedAudio(p, originalTag))
}

// audioTags is languages as canonical tags, each once and none of except's.
func audioTags(languages []string, originalTag string, except []string) []string {
	var out []string
	for _, l := range languages {
		if l == "original" {
			l = originalTag
		}
		if w, ok := lang.Normalize(l); ok && !slices.Contains(out, string(w)) && !slices.Contains(except, string(w)) {
			out = append(out, string(w))
		}
	}
	return out
}

// MissingOptionalAudio is the optional languages (OptionalAudio) a file
// whose probed audio languages are audio lacks; nil when audio is unknown.
func MissingOptionalAudio(p quality.Profile, originalTag string, audio []string) []string {
	if audio == nil {
		return nil
	}
	var missing []string
	for _, o := range OptionalAudio(p, originalTag) {
		if !slices.ContainsFunc(audio, func(a string) bool { return baseLanguage(a) == baseLanguage(o) }) {
			missing = append(missing, o)
		}
	}
	return missing
}

// IncompleteAudio reports whether a file must be replaced to carry every
// language p's audio policy wants: the policy does not graft (a grafting
// profile adds a missing dub from a donor instead) and the file's audio is
// known to lack one (MissingAudio). Only a release carrying every wanted
// language replaces such a file, whatever its quality and over a
// transcoded file too (2026-10-20: the owner prefers a dual-audio release
// to a graft for anime films; Akira's file carried Portuguese and
// Japanese).
func IncompleteAudio(p quality.Profile, originalTag string, audio []string) bool {
	return !p.GraftAudio && len(MissingAudio(p, originalTag, audio)) > 0
}

// ReplaceableAudio reports whether a release carrying every language p's
// audio policy wants replaces a file whatever its quality, a transcoded one
// included: IncompleteAudio's file, or one known to lack a wanted language
// under a policy taking complete audio first (Audio.CompleteFirst) that
// grafts. Such a file reads CutoffUnmet, reason MissingAudio, and a graft
// stays its fallback while no complete release exists (owner, 2026-11-02:
// "Ensure we use dual audio as the highest priority"; 2,068 episodes of
// hevc-small-1080p-anime-tv lacked English, their grafts still searching).
func ReplaceableAudio(p quality.Profile, originalTag string, audio []string) bool {
	return (!p.GraftAudio || p.AudioCompleteFirst) && len(MissingAudio(p, originalTag, audio)) > 0
}

// AudioUpgradeable reports whether a release carrying every language p's
// audio policy wants, the optional ones included, replaces a file whatever
// its quality: ReplaceableAudio's file, or one lacking only an optional
// language under the same policies (an anime whose English dub is still
// airing; owner, 2026-11-03). Unlike ReplaceableAudio's, such a file meets
// the cutoff.
func AudioUpgradeable(p quality.Profile, originalTag string, audio []string) bool {
	return ReplaceableAudio(p, originalTag, audio) ||
		(!p.GraftAudio || p.AudioCompleteFirst) && len(MissingOptionalAudio(p, originalTag, audio)) > 0
}

// CompletesAudio reports whether a file whose probed audio languages are
// audio carries every language p's audio policy wants, the optional ones
// included: a policy, known audio and nothing missing.
func CompletesAudio(p quality.Profile, originalTag string, audio []string) bool {
	return len(p.AudioLanguages) > 0 && audio != nil && len(MissingAudio(p, originalTag, audio)) == 0 &&
		len(MissingOptionalAudio(p, originalTag, audio)) == 0
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
