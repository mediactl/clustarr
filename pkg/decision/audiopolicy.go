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
	"strings"
	"time"

	"github.com/dlclark/regexp2"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/quality"
	"github.com/mediactl/clustarr/pkg/release"
)

// dualAudioPattern is TRaSH's "Dual Audio" ReleaseTitle condition, verbatim
// from the anime-dual-audio custom format this repo vendors
// (pkg/quality/catalogue/data/formats/anime.json). It has no bare "DL",
// which would match every WEB-DL.
const dualAudioPattern = `dual[ ._-]?(audio)|[([]dual[])]|\b(JA|ZH|KO)(?= ?\+ ?.*?\b(EN))|\b(EN)(?= ?\+ ?.*?\b(JA|ZH|KO))|\b(Japanese|Chinese|Korean) ?[ ._\+&-] ?\b(English)|\b(English) ?[ ._\+&-] ?\b(Japanese|Chinese|Korean)|\b(\d{3,4}(p|i)|4K|U(ltra)?HD)\b.*\b(DUAL)\b(?!.*\(|\))`

var dualAudioRegex = func() *regexp2.Regexp {
	re := regexp2.MustCompile(dualAudioPattern, regexp2.IgnoreCase)
	re.MatchTimeout = 100 * time.Millisecond
	return re
}()

// dualAudio reports whether title is a dual-audio anime release: one whose
// audio is the original language and English. Only under an anime score
// set, whose releases follow that convention.
func dualAudio(p quality.Profile, title string) bool {
	if !strings.HasPrefix(p.ScoreSet, "anime-") {
		return false
	}
	ok, err := dualAudioRegex.MatchString(title)
	return err == nil && ok
}

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

// dualAudioApplies is dualAudio, believed only when the title names no
// language besides the original and English: TRaSH's pattern also matches
// "KOREAN.ENGLISH" and "Korean Dual Audio", which are not the Japanese
// original (the phase 2 review).
func dualAudioApplies(p quality.Profile, parsed *release.ParsedRelease, title, originalLanguage string) bool {
	if originalLanguage == "" || !dualAudio(p, title) {
		return false
	}
	if parsed.LanguageUnknown {
		return true
	}
	for _, l := range parsed.Languages {
		if !strings.EqualFold(l, originalLanguage) && !strings.EqualFold(l, "English") {
			return false
		}
	}
	return true
}

// namesWantedLanguages is whether a release's title names its languages
// rather than assuming the original, and -- under an audio policy -- names
// the anchor among them: only such a release replaces a wrong-language file
// without an upgrade (upgradeRejection).
func namesWantedLanguages(p quality.Profile, parsed *release.ParsedRelease, title, originalLanguage string) bool {
	dual := dualAudioApplies(p, parsed, title, originalLanguage)
	if parsed.LanguageUnknown && !dual {
		return false
	}
	if len(p.AudioLanguages) == 0 {
		return true
	}
	anchor := audioAnchor(p)
	name := originalLanguage
	if anchor != "original" {
		name = originalLanguageName(context.Background(), anchor)
	}
	if name == "" {
		return true
	}
	return containsFold(parsed.Languages, name) || (dual && (strings.EqualFold(name, originalLanguage) || strings.EqualFold(name, "English")))
}

// audioRejection is the release check that replaces languageRejection when a
// profile sets audio languages (anime dual-audio spec §5.2). complete is
// whether the release carries every wanted language. A partial release
// passes only when the profile grafts and the release carries the anchor
// (audioAnchor: the original language when listed) that a donor's audio is
// aligned against; an unknown anchor fails open, as languageRejection does.
func audioRejection(ctx context.Context, originalLanguage string, p quality.Profile, parsed *release.ParsedRelease, title string) (*common.Rejection, bool) {
	var wanted []string
	for _, l := range p.AudioLanguages {
		name := originalLanguage
		if l != "original" {
			name = originalLanguageName(ctx, l)
		}
		if name != "" {
			wanted = append(wanted, name)
		}
	}
	have := append([]string(nil), parsed.Languages...)
	if dualAudioApplies(p, parsed, title, originalLanguage) {
		have = append(have, originalLanguage, "English")
	}
	var missing []string
	for _, w := range wanted {
		if !containsFold(have, w) {
			missing = append(missing, w)
		}
	}
	if len(missing) == 0 {
		return nil, true
	}
	anchor := originalLanguage
	if a := audioAnchor(p); a != "original" {
		anchor = originalLanguageName(ctx, a)
	}
	if p.AudioGraft && (anchor == "" || containsFold(have, anchor)) {
		return nil, false
	}
	r := newRejection(ReasonWantedLanguage, "audio %v wanted, found %v", wanted, have)
	return &r, false
}
