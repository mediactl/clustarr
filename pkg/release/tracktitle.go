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

package release

import (
	"regexp"
	"strings"
)

// Track titles: whether a music file's title tag names a track's title, the
// check that keeps a file from being attributed to -- or renamed as --
// another song (2026-11-16: the owner's flat scene rips, numbered against
// the wrong edition). The import's attribution (fileimport.AttributeTrackIn)
// and the rename's naming (a mismatch is never renamed) share it.

var (
	// trackBracketedCreditRE is a bracketed credit in a title: a featured
	// artist's ("(feat. X)", "[ft X]", "(featuring X)", "(Co-Starring X)") or
	// a producer's ("(prod. X)", "[produced by X]", "(Produced By: X)").
	trackBracketedCreditRE = regexp.MustCompile(
		`(?i)[(\[]\s*(?:feat\.?|ft\.?|featuring|co-starring|prod\.?(?:\s+by)?:?|produced\s+by:?)\s[^)\]]*[)\]]`)
	// trackBracketedWithRE is a bracketed "(with X)" credit, which may as well be
	// the title's own ("(With Love)"): dropped only where the track number
	// agrees too (TrackTitlesAgreeAtNumber).
	trackBracketedWithRE = regexp.MustCompile(`(?i)[(\[]\s*with\s[^)\]]*[)\]]`)
	// trackTrailingCreditRE starts an unbracketed featured-artist credit (" feat.
	// X", " feat X", " ft. X", " featuring X"; a bare "ft" without its dot
	// is too often a word, "6 Ft Under"), which runs to the next bracket or
	// " - " (dropTrackCredit), never over a version suffix.
	trackTrailingCreditRE = regexp.MustCompile(`(?i)\s(?:feat\.?|ft\.|featuring)\s`)
	// trackRemasterRE is a remaster annotation, bracketed or after " - ":
	// "(2004 Digital Remaster)", "(2017 remastered version)", " - Remastered
	// 2011". A remaster is the song.
	trackRemasterRE = regexp.MustCompile(`(?i)(?:[(\[]\s*|\s-\s+)(?:\d{4}\s+)?(?:digital(?:ly)?\s+)?remaster(?:ed)?(?:\s+version)?(?:\s+\d{4})?\s*[)\]]?`)
	// trackBonusRE is a bonus-track annotation: "(Bonus Track)", "(Japan Bonus
	// Track)".
	trackBonusRE = regexp.MustCompile(`(?i)[(\[]\s*(?:[\p{L}]+\s+)?bonus\s+track\s*[)\]]`)
	// trackVersionRE is a version note naming the album's own recording:
	// "(Album Version)", "(LP Version)", "(Album Version (Explicit))",
	// "(Explicit Version)", "[Clean]", "(Original Mix)". A single version, a
	// radio edit, a remix or a session is another recording and stays.
	trackVersionRE = regexp.MustCompile(`(?i)[(\[]\s*(?:(?:album|lp)\s+version(?:\s*[(\[]\s*(?:explicit|clean)\s*[)\]])?|` +
		`(?:explicit|clean)(?:\s+version)?|original\s+mix)\s*[)\]]`)
	// trackApostrophes are dropped, so "Can't" and "Can’t" agree.
	trackApostrophes = strings.NewReplacer("'", "", "\u2019", "", "\u2018", "", "`", "")
)

// TrackTitlesAgree reports whether a file's title tag names the track title:
// equal once featured-artist and producer credits, remaster and bonus-track
// annotations and the album's own version notes (trackVersionRE) are dropped, "&" read as "and", "part" as "pt", trackApostrophes
// dropped, and both run through release.TitleNorm (case, accents,
// punctuation and brackets, any script) with the spaces taken out, so a
// stylised run-together title agrees. Anything else -- "(a cappella)",
// "(Remix)", "Pt. 2", another song -- is another recording. A title of
// punctuation alone agrees only with itself, and an empty one with nothing.
func TrackTitlesAgree(tag, track string) bool { return trackTitlesAgree(tag, track, false) }

// TrackTitlesAgreeAtNumber is TrackTitlesAgree for a track the file's number names too,
// which also drops a bracketed "(with X)" credit: the number is the
// evidence the bracket might be the title's own.
func TrackTitlesAgreeAtNumber(tag, track string) bool { return trackTitlesAgree(tag, track, true) }

func trackTitlesAgree(tag, track string, with bool) bool {
	a, b := trackTitleKey(tag, with), trackTitleKey(track, with)
	if a == "" && b == "" {
		t := strings.TrimSpace(tag)
		return t != "" && t == strings.TrimSpace(track)
	}
	return a != "" && a == b
}

func trackTitleKey(s string, with bool) string {
	s = trackBracketedCreditRE.ReplaceAllString(s, " ")
	if with {
		s = trackBracketedWithRE.ReplaceAllString(s, " ")
	}
	s = dropTrackCredit(s)
	s = trackRemasterRE.ReplaceAllString(s, " ")
	s = trackBonusRE.ReplaceAllString(s, " ")
	s = trackVersionRE.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, "&", " and ")
	s = trackApostrophes.Replace(s)
	words := strings.Fields(TitleNorm(s))
	for i, w := range words {
		if w == "part" {
			words[i] = "pt"
		}
	}
	return strings.Join(words, "")
}

// dropTrackCredit cuts an unbracketed featured-artist credit out of s: from
// its marker to the next bracket or " - ", or the end.
func dropTrackCredit(s string) string {
	loc := trackTrailingCreditRE.FindStringIndex(s)
	if loc == nil {
		return s
	}
	rest := s[loc[1]:]
	end := len(rest)
	for _, sep := range []string{"(", "[", " - "} {
		if i := strings.Index(rest, sep); i >= 0 && i < end {
			end = i
		}
	}
	return s[:loc[0]] + " " + rest[end:]
}
