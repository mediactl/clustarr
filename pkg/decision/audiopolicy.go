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
// whether the release carries every wanted language. A partial release
// passes only when the profile grafts and the release carries the anchor
// (audioAnchor: the original language when listed) that a donor's audio is
// aligned against; an unknown anchor fails open, as languageRejection does.
// A dual-audio title's languages are the parser's (LanguagesFor).
func audioRejection(an audioNames, p quality.Profile, parsed *release.ParsedRelease) (*common.Rejection, bool) {
	have := parsed.Languages
	var missing []string
	for _, w := range an.wanted {
		if !containsFold(have, w) {
			missing = append(missing, w)
		}
	}
	if len(missing) == 0 {
		return nil, true
	}
	if p.GraftAudio && (an.anchor == "" || containsFold(have, an.anchor)) {
		return nil, false
	}
	r := newRejection(ReasonWantedLanguage, "audio %v wanted, found %v", an.wanted, have)
	return &r, false
}
