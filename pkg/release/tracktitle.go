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
	"slices"
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
		`(?i)[(\[]\s*(?:feat\.?|ft\.?|featuring|co-starring|prod\.?(?:\s+by)?:?|produced\s+by:?|scored\s+by:?)\s[^)\]]*[)\]]`)
	// trackBracketedWithRE is a bracketed "(with X)" credit, which may as well be
	// the title's own ("(With Love)"): dropped only where the track number
	// agrees too (TrackTitlesAgreeAtNumber).
	trackBracketedWithRE = regexp.MustCompile(`(?i)[(\[]\s*with\s[^)\]]*[)\]]`)
	// trackTrailingCreditRE starts an unbracketed featured-artist credit (" feat.
	// X", " feat X", " ft. X", " featuring X", " F/ X"; a bare "ft" without
	// its dot is too often a word, "6 Ft Under", and is read as a credit only
	// at the file's own number, trackBareFtRE), which runs to the next
	// bracket or " - " (dropTrackCredit), never over a version suffix.
	trackTrailingCreditRE = regexp.MustCompile(`(?i)\s(?:(?:feat\.?|ft\.|featuring)\s|f/)`)
	trackBareFtRE         = regexp.MustCompile(`(?i)\sft\s`)
	// trackNoteAtNumberRE is a note that is the track's own only where its
	// number agrees too: "(Skit)", "(Interlude)", "(Intro)", "(Outro)" and a
	// soundtrack's "(from “Queen & Slim: The Soundtrack”)", which may as well
	// be a title ("(From Me to You)").
	trackNoteAtNumberRE = regexp.MustCompile(`(?i)[(\[]\s*(?:from\s[^)\]]*|skit|interlude|intro|outro)\s*[)\]]`)
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
	// trackAnd reads "&" and "+" as "and": "Flesh + Blood".
	trackAnd = strings.NewReplacer("&", " and ", "+", " and ")
)

// TrackTitlesAgree reports whether a file's title tag names the track title:
// equal once featured-artist and producer credits, remaster and bonus-track
// annotations and the album's own version notes (trackVersionRE) are dropped, "&" and "+" read as "and", "part" as "pt", trackApostrophes
// dropped, and both run through release.TitleNorm (case, accents,
// punctuation and brackets, any script) with the spaces taken out, so a
// stylised run-together title agrees. Anything else -- "(a cappella)",
// "(Remix)", "Pt. 2", another song -- is another recording. A title of
// punctuation alone agrees only with itself, and an empty one with nothing.
func TrackTitlesAgree(tag, track string) bool { return trackTitlesAgree(tag, track, false) }

// TrackTitlesAgreeAtNumber is TrackTitlesAgree for a track the file's number
// names too, where the number is the evidence for what a title alone could
// not carry (talos, 2026-11-16: Ludacris, Gucci Mane and Missy Elliott rips
// held as other songs at their own numbers): it also drops a bracketed
// "(with X)" credit, a bare " Ft X" credit and the notes trackNoteAtNumberRE
// names, and it agrees with one part of MusicBrainz's "Interlude / Song", with
// a title ID3v1 cut at 30 characters that the track's starts with, with the
// same words in another order, and through a typo of one or two letters in a
// title of eight or more ("The Thrillest", "Fcuk"; no more than one in six).
// An added word ("Live", "Remix", "Pt. 2") is more than two letters, so it
// never passes as a typo.
func TrackTitlesAgreeAtNumber(tag, track string) bool { return trackTitlesAgree(tag, track, true) }

// id3v1TitleRunes is ID3v1's title field, 30 bytes: a title of exactly that
// length may have been cut.
const id3v1TitleRunes = 30

func trackTitlesAgree(tag, track string, atNumber bool) bool {
	baseA, baseB := trackTitleBase(tag, atNumber), trackTitleBase(track, atNumber)
	a, b := trackTitleKey(baseA), trackTitleKey(baseB)
	if a == "" && b == "" {
		t := strings.TrimSpace(baseA)
		return t != "" && t == strings.TrimSpace(baseB)
	}
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	if !atNumber {
		return false
	}
	for _, part := range strings.Split(baseB, "/") {
		if strings.TrimSpace(part) != baseB && trackTitleKey(part) == a {
			return true
		}
	}
	if len([]rune(strings.TrimSpace(tag))) == id3v1TitleRunes && len(a) >= 10 && strings.HasPrefix(b, a) {
		return true
	}
	if wa, wb := trackTitleWords(baseA), trackTitleWords(baseB); len(wa) >= 2 && slices.Equal(wa, wb) {
		return true
	}
	ra, rb := []rune(a), []rune(b)
	if min(len(ra), len(rb)) < 8 {
		return false
	}
	d := osaDistance(ra, rb)
	return d <= 2 && d*6 <= max(len(ra), len(rb))
}

// trackTitleBase is s with every credit and note the comparison sets aside
// dropped, its own spelling otherwise.
func trackTitleBase(s string, atNumber bool) string {
	s = trackBracketedCreditRE.ReplaceAllString(s, " ")
	if atNumber {
		s = trackBracketedWithRE.ReplaceAllString(s, " ")
		s = trackNoteAtNumberRE.ReplaceAllString(s, " ")
	}
	s = dropTrackCredit(s, trackTrailingCreditRE)
	if atNumber {
		s = dropTrackCredit(s, trackBareFtRE)
	}
	s = trackRemasterRE.ReplaceAllString(s, " ")
	s = trackBonusRE.ReplaceAllString(s, " ")
	return trackVersionRE.ReplaceAllString(s, " ")
}

// trackTitleWords are a base's words as the key spells them, sorted.
func trackTitleWords(base string) []string {
	s := trackApostrophes.Replace(trackAnd.Replace(base))
	words := strings.Fields(TitleNorm(s))
	for i, w := range words {
		if w == "part" {
			words[i] = "pt"
		}
	}
	slices.Sort(words)
	return words
}

func trackTitleKey(base string) string {
	s := trackApostrophes.Replace(trackAnd.Replace(base))
	words := strings.Fields(TitleNorm(s))
	for i, w := range words {
		if w == "part" {
			words[i] = "pt"
		}
	}
	return strings.Join(words, "")
}

// osaDistance is the optimal string alignment distance between a and b:
// insertions, deletions, substitutions and adjacent transpositions.
func osaDistance(a, b []rune) int {
	prev2 := make([]int, len(b)+1)
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
			if i > 1 && j > 1 && a[i-1] == b[j-2] && a[i-2] == b[j-1] {
				cur[j] = min(cur[j], prev2[j-2]+1)
			}
		}
		prev2, prev, cur = prev, cur, prev2
	}
	return prev[len(b)]
}

// dropTrackCredit cuts an unbracketed featured-artist credit re starts out
// of s: from its marker to the next bracket or " - ", or the end.
func dropTrackCredit(s string, re *regexp.Regexp) string {
	loc := re.FindStringIndex(s)
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
