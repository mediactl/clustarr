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

package subtitles

// HIPolicy mirrors api/subtitle/v1alpha1.HIPolicy — how hearing-impaired
// (SDH) subtitles are treated for one profile language. Never imports that
// package; see doc.go and this package's Query/Candidate convention.
type HIPolicy string

// Hearing-impaired policies, api/subtitle/v1alpha1.HIPolicy's exact values.
const (
	HIPolicyRequired HIPolicy = "required"
	HIPolicyPrefer   HIPolicy = "prefer"
	HIPolicyExcluded HIPolicy = "excluded"
)

// ProfileLanguage mirrors one entry of api/subtitle/v1alpha1.LanguageItem —
// only the fields Plan needs. Key must already be derivable from Language,
// Forced and HI exactly as the SubtitleProfile controller validates it
// (FormatLangKey(Language, Forced, HI == HIPolicyRequired)); Plan trusts it
// rather than re-deriving it, so a HIPolicyPrefer or HIPolicyExcluded entry
// and a HIPolicyRequired entry for the same language are expected to carry
// different Keys ("en" vs "en:hi").
type ProfileLanguage struct {
	Key              LangKey
	Language         string
	Forced           bool
	HI               HIPolicy
	AudioExclude     bool
	AudioOnlyInclude bool
}

// Profile mirrors the subset of api/subtitle/v1alpha1.SubtitleProfileSpec
// that Plan needs: the wanted languages and the cutoff. mustContain /
// mustNotContain filter candidates rather than deciding existence, and
// languageEquals (region-subtag aliasing, e.g. "pt-BR" satisfying "pt") is
// not implemented by this phase's Plan — region subtags are always
// distinct languages here.
type Profile struct {
	Languages []ProfileLanguage

	// Cutoff is the langKey whose satisfaction stops wanting every
	// lower-priority language, mirroring
	// api/subtitle/v1alpha1.SubtitleProfileSpec.Cutoff. Nil means any
	// wanted language satisfies the cutoff, per that field's doc comment.
	Cutoff *LangKey
}

// Existing is one subtitle already present for the media file — an embedded
// stream or a parsed sidecar — reduced to the (language, forced, hi)
// identity the missing-subtitle computation compares (research note §2.2's
// Language.__eq__). Building the list is the caller's job: captionarr's
// SubtitleRequest controller calls ParseSidecar over the media file's
// directory for the sidecar half and derives the embedded half itself from
// the probed MediaInfo (api/common/v1alpha1.MediaInfo.Subtitles), honouring
// the profile's embedded policy (ignorePGS/ignoreVobSub/ignoreASS,
// skipCommentary) and building each LangKey with FormatLangKey — this
// package never imports api/subtitle/v1alpha1 or does file/container I/O.
type Existing struct {
	LangKey LangKey
}

// langTriple is the (language, forced, hi) identity research note §2.2's
// Language.__eq__ compares. LangKey's suffix already carries forced and hi
// (lang.go), so both ProfileLanguage and Existing reduce to this shape for
// comparison.
type langTriple struct {
	lang   string
	forced bool
	hi     bool
}

func (l ProfileLanguage) triple() langTriple {
	return langTriple{lang: l.Language, forced: l.Forced, hi: l.HI == HIPolicyRequired}
}

func existingTriple(e Existing) (langTriple, bool) {
	lang, forced, hi, err := ParseLangKey(e.LangKey)
	if err != nil {
		return langTriple{}, false
	}
	return langTriple{lang: lang, forced: forced, hi: hi}, true
}

// Plan is Bazarr's missing-subtitle computation (research note §2.2,
// design spec §6.5's "subtitlerequest" clause), over this package's own
// plain types: wanted is profile's languages minus existing, honouring
// audioExclude/audioOnlyInclude against audioLangs (the file's audio-track
// languages), the forced/HI identity of each entry, and the cutoff. Once
// the cutoff is satisfied, Plan stops wanting every language and returns
// (nil, true) — it does not report the other languages as still wanted,
// because Bazarr does not either: the cutoff being met is what ends the
// search for the whole media file, not just for the cutoff language.
//
// audioLangs and every ProfileLanguage.Language are compared by exact
// string equality — "pt" and "pt-BR" are always distinct languages, since
// languageEquals aliasing is out of scope (see Profile's doc comment).
func Plan(profile Profile, audioLangs []string, existing []Existing) (wanted []LangKey, cutoffMet bool) {
	audioHas := func(lang string) bool {
		for _, a := range audioLangs {
			if a == lang {
				return true
			}
		}
		return false
	}

	actual := make([]langTriple, 0, len(existing))
	for _, e := range existing {
		if t, ok := existingTriple(e); ok {
			actual = append(actual, t)
		}
	}
	inActual := func(t langTriple) bool {
		for _, a := range actual {
			if a == t {
				return true
			}
		}
		return false
	}

	// cutoffItems is the one profile language Cutoff names, or every
	// language when Cutoff is unset ("any wanted language satisfies the
	// cutoff" — SubtitleProfileSpec.Cutoff's doc comment). A Cutoff that
	// names no language in Languages (a profile the CEL rule should have
	// already rejected) yields an empty cutoffItems, so the loop below
	// simply never marks the cutoff met rather than panicking.
	cutoffItems := profile.Languages
	if profile.Cutoff != nil {
		cutoffItems = nil
		for _, l := range profile.Languages {
			if l.Key == *profile.Cutoff {
				cutoffItems = []ProfileLanguage{l}
				break
			}
		}
	}

	for _, c := range cutoffItems {
		has := audioHas(c.Language)
		switch {
		case c.AudioOnlyInclude && !has:
			continue
		case c.AudioExclude && has:
			cutoffMet = true
		case inActual(c.triple()):
			cutoffMet = true
		case c.HI == HIPolicyPrefer && !c.Forced && inActual(langTriple{lang: c.Language, forced: false, hi: true}):
			// An HI file satisfies a "prefer" (either is fine) cutoff
			// language even though the triples do not match exactly —
			// research note §2.2's "an HI file satisfies a non-HI
			// cutoff". HIPolicyExcluded deliberately does not take this
			// branch: an HI file must not satisfy a cutoff that rejects
			// HI outright.
			cutoffMet = true
		}
		if cutoffMet {
			break
		}
	}
	if cutoffMet {
		return nil, true
	}

	// desired: profile languages minus those excluded by the file's audio.
	type desiredEntry struct {
		key    LangKey
		triple langTriple
	}
	var desired []desiredEntry
	for _, l := range profile.Languages {
		has := audioHas(l.Language)
		if l.AudioExclude && has {
			continue
		}
		if l.AudioOnlyInclude && !has {
			continue
		}
		desired = append(desired, desiredEntry{key: l.Key, triple: l.triple()})
	}

	var missing []desiredEntry
	for _, d := range desired {
		if !inActual(d.triple) {
			missing = append(missing, d)
		}
	}

	// hiSetting is every non-forced profile language's HI policy, by
	// BCP-47 language: an HI actual file can satisfy a "prefer" plain want,
	// and (defensively — see below) a non-HI actual file an "excluded"
	// plain want, even though the triples do not match exactly. Forced
	// entries are excluded because this substitution only ever applies to
	// the non-forced identity (research note §2.2: "hi_setting = ...for
	// item in profile.items if not item.forced").
	hiSetting := make(map[string]HIPolicy, len(profile.Languages))
	for _, l := range profile.Languages {
		if !l.Forced {
			hiSetting[l.Language] = l.HI
		}
	}
	removePlain := func(lang string) {
		for i, m := range missing {
			if m.triple == (langTriple{lang: lang, forced: false, hi: false}) {
				missing = append(missing[:i], missing[i+1:]...)
				return
			}
		}
	}
	for _, a := range actual {
		if a.forced {
			continue
		}
		setting, isWanted := hiSetting[a.lang]
		if !isWanted {
			continue
		}
		switch {
		case a.hi && setting != HIPolicyExcluded:
			removePlain(a.lang)
		case !a.hi && setting == HIPolicyExcluded:
			// Defensive: an exact (lang, false, false) actual entry
			// already removed the matching desired entry above: this
			// branch mirrors research note §2.2's second removal pass
			// for parity and is a no-op in that case.
			removePlain(a.lang)
		}
	}

	if len(missing) == 0 {
		return nil, false
	}
	wanted = make([]LangKey, len(missing))
	for i, m := range missing {
		wanted[i] = m.key
	}
	return wanted, false
}
