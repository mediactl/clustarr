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
	"context"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/release"
)

// audioAnchor is the language a graft aligns on and a file must carry: the
// original language when the policy lists it, else its default, else its
// first language.
func audioAnchor(p quality.Profile) string {
	for _, l := range p.AudioLanguages {
		if l == "original" {
			return l
		}
	}
	if p.AudioDefault != "" {
		return p.AudioDefault
	}
	if len(p.AudioLanguages) > 0 {
		return p.AudioLanguages[0]
	}
	return ""
}

// audioNames is a profile's audio policy in the display-name vocabulary
// release languages are parsed into, for one item: resolved once per
// Evaluate rather than once per release. original is the item's original
// language's name ("" unknown), wanted the policy's languages (an unknown
// original dropped, a language listed twice -- an English original beside
// "en" -- kept once), anchor the language a graft aligns on.
type audioNames struct {
	original string
	wanted   []string
	// optional are the policy's optional languages (Profile.AudioOptional):
	// a release lacking one is not refused, but is not complete either.
	optional []string
	anchor   string
}

func resolveAudioNames(ctx context.Context, p quality.Profile, originalLanguage string) audioNames {
	an := audioNames{original: originalLanguage}
	name := func(l string) string {
		if l == "original" {
			return originalLanguage
		}
		return originalLanguageName(ctx, l)
	}
	for _, l := range p.AudioLanguages {
		if n := name(l); n != "" && !containsFold(an.wanted, n) {
			an.wanted = append(an.wanted, n)
		}
	}
	for _, l := range p.AudioOptional {
		if n := name(l); n != "" && !containsFold(an.wanted, n) && !containsFold(an.optional, n) {
			an.optional = append(an.optional, n)
		}
	}
	if a := audioAnchor(p); a != "" {
		an.anchor = name(a)
	}
	return an
}

// namesWantedLanguages is whether a release's title names its languages
// rather than assuming the original -- a dual-audio marker names them too
// (release.ParsedRelease.DualAudio) -- and, under an audio policy, names the
// anchor among them: only such a release replaces a wrong-language file
// without an upgrade (upgradeRejection).
func namesWantedLanguages(an audioNames, p quality.Profile, parsed *release.ParsedRelease) bool {
	if parsed.LanguageUnknown {
		return false
	}
	if len(p.AudioLanguages) == 0 || an.anchor == "" {
		return true
	}
	return containsFold(parsed.Languages, an.anchor)
}

// audioRejection is the release check that replaces languageRejection when a
// profile sets audio languages (anime dual-audio spec §5.2). complete is
// whether the release carries every wanted language, the optional ones
// (Profile.AudioOptional) included; lacking only an optional one refuses
// nothing, but is not complete, so a release carrying it ranks first. A
// partial release
// passes only when the profile grafts and the release carries the anchor
// (audioAnchor: the original language when listed) that a donor's audio is
// aligned against; an unknown anchor fails open, as languageRejection does.
// A dual-audio title's languages are the parser's (LanguagesFor).
//
// A dub alone passes a grafting policy too (owner, 2026-11-27: "Allow
// dubs-only (or vice versa - *with* the graft)"): a release carrying a
// wanted language and not the anchor, which the grab planner takes only as
// a last resort (DubAlone) and whose file's graft aligns on the dub it
// carries, the original its missing language (rollup.GraftAnchor). On talos
// 78 episodes of Lupin the 3rd's second series and six of Sacrificial
// Princess had no other release.
func audioRejection(an audioNames, p quality.Profile, parsed *release.ParsedRelease) (*common.Rejection, bool) {
	have := parsed.Languages
	var missing []string
	for _, w := range an.wanted {
		if !containsFold(have, w) {
			missing = append(missing, w)
		}
	}
	if len(missing) == 0 {
		for _, o := range an.optional {
			if !containsFold(have, o) {
				return nil, false
			}
		}
		return nil, true
	}
	if p.GraftAudio && (an.anchor == "" || containsFold(have, an.anchor) || dubAlone(an, have)) {
		return nil, false
	}
	r := newRejection(ReasonWantedLanguage, "audio %v wanted, found %v", an.wanted, have)
	return &r, false
}

// dubAlone reports languages that carry one the policy wants, an optional
// one included, and not its anchor: a dub without the original.
func dubAlone(an audioNames, have []string) bool {
	if an.anchor == "" || containsFold(have, an.anchor) {
		return false
	}
	for _, w := range append(append([]string(nil), an.wanted...), an.optional...) {
		if containsFold(have, w) {
			return true
		}
	}
	return false
}

// DubAlone reports a release, parsed by ParseForItemState, that a grafting
// audio policy takes though it lacks the anchor (audioRejection): a dub
// without the item's original language. The grab planner holds it to the
// last-resort rule -- never from RSS or a push, and from a search only when
// no release that is not a last resort covers an item it brings -- as it
// holds a last-resort tier's and a last-resort format's. False without a
// grafting policy, for a nil parse, and while the original is unknown.
func DubAlone(ctx context.Context, p quality.Profile, originalTag string, parsed *release.ParsedRelease) bool {
	if !p.GraftAudio || len(p.AudioLanguages) == 0 || parsed == nil {
		return false
	}
	return dubAlone(resolveAudioNames(ctx, p, originalLanguageName(ctx, originalTag)), parsed.Languages)
}
