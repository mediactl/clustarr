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

package rescan

import (
	"regexp"
	"slices"
	"strings"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

// Album titles as a rip's folder or ALBUM tag spells them (talos,
// 2026-11-16: 230 of the 386 music files whose album tag named no album
// spelled one of the artist's loosely or named an edition of it).

// albumTitleTiers are the comparisons MatchAlbum tries in turn among one
// artist's albums, the first that names an album deciding, so a looser one
// never takes a file a stricter one placed: the album's title as cleaned
// (case, punctuation, articles); spelled loosely (spacing, "&" for "and",
// hyphens, a Roman numeral: "19NaughtyIII", "Love, Peace & Nappiness",
// "Motor-Booty Affair", "SremmLife II"); a release of the group under its
// own title ("F-1 Trillion: Long Bed", "Lil Boat 3.5"); without an edition
// note ("(Expanded Edition)", "Deluxe", "CD", "(2015 SHM-SACD)", ":
// Reloaded"). The track a file is attributed to is still checked against
// its title tag, so an edition with other songs holds them.
var albumTitleTiers = []func(c AlbumCandidate, title string) bool{
	func(c AlbumCandidate, t string) bool { return nameEq(c.Title, t) },
	func(c AlbumCandidate, t string) bool { return looseAlbumEq(c.Title, t) },
	func(c AlbumCandidate, t string) bool {
		return slices.ContainsFunc(c.ReleaseTitles, func(r string) bool { return nameEq(r, t) || looseAlbumEq(r, t) })
	},
	// MusicBrainz titles some albums with the artist's name in front,
	// "Ciara: The Evolution", where the tag names "The Evolution".
	func(c AlbumCandidate, t string) bool {
		return slices.ContainsFunc(c.ArtistNames, func(name string) bool {
			rest, ok := withoutArtistPrefix(c.Title, name)
			return ok && (nameEq(rest, t) || looseAlbumEq(rest, t) || editionAlbumEq(rest, t))
		})
	},
	func(c AlbumCandidate, t string) bool {
		return editionAlbumEq(c.Title, t) ||
			slices.ContainsFunc(c.ReleaseTitles, func(r string) bool { return editionAlbumEq(r, t) })
	},
	// A double A-side's tag names both sides, "Late Nite Tip / Hit 'Em", and
	// MusicBrainz the group by one.
	func(c AlbumCandidate, t string) bool {
		parts := strings.Split(t, " / ")
		return len(parts) > 1 && slices.ContainsFunc(parts, func(p string) bool {
			return nameEq(c.Title, p) || looseAlbumEq(c.Title, p)
		})
	},
}

// looseAlbumEq compares two titles' loose keys: as written; with their
// Roman numerals read as numbers (a stylised "19NaughtyIII" has no numeral
// word to read, "SremmLife II" one); and with their articles dropped
// ("Age Of The Ego (Remixes)", "Age of the Ego: The Remixes").
func looseAlbumEq(a, b string) bool {
	for _, form := range []looseForm{looseAsWritten, looseRoman, looseNoArticles} {
		if ka := looseAlbumKey(a, form); ka != "" && ka == looseAlbumKey(b, form) {
			return true
		}
	}
	return false
}

type looseForm int

const (
	looseAsWritten looseForm = iota
	looseRoman
	looseNoArticles
)

// withoutArtistPrefix is title without a leading "<name>: " or "<name> - ".
func withoutArtistPrefix(title, name string) (string, bool) {
	name = strings.TrimSpace(name)
	if name == "" || len(title) <= len(name) || !strings.EqualFold(title[:len(name)], name) {
		return "", false
	}
	rest := title[len(name):]
	for _, sep := range []string{": ", " - ", " \u2013 "} {
		if after, ok := strings.CutPrefix(rest, sep); ok && strings.TrimSpace(after) != "" {
			return after, true
		}
	}
	return "", false
}

func editionAlbumEq(a, b string) bool {
	return looseAlbumEq(stripAlbumEdition(a), stripAlbumEdition(b))
}

// romanNumerals are the numeral words a loose key reads as numbers; "i",
// "v" and "x" alone are too often words or letters ("Malcolm X").
var romanNumerals = map[string]string{
	"ii": "2", "iii": "3", "iv": "4", "vi": "6", "vii": "7", "viii": "8", "ix": "9", "xi": "11", "xii": "12",
}

// looseAlbumKey is s's words as release.TitleNorm spells them, "&" and "+"
// read as "and", run together in form.
func looseAlbumKey(s string, form looseForm) string {
	words := strings.Fields(release.TitleNorm(albumAnd.Replace(s)))
	out := words[:0]
	for _, w := range words {
		switch {
		case form == looseRoman && romanNumerals[w] != "":
			w = romanNumerals[w]
		case form == looseNoArticles && (w == "the" || w == "a" || w == "an"):
			continue
		}
		out = append(out, w)
	}
	return strings.Join(out, "")
}

var albumAnd = strings.NewReplacer("&", " and ", "+", " and ")

var (
	// albumEditionWordRE names an edition of the album in a note.
	albumEditionWordRE = regexp.MustCompile(`(?i)\b(?:deluxe|expanded|edition|remaster(?:ed)?|reissue|anniversary|` +
		`explicit|clean|reloaded|bonus|sacd|shm|hdcd|cd\d*|disc|lp|vinyl|\d{3}g|\d{2}/\d{2,3}|\d{2}-?bit|hi-?res|hdtracks|flac|` +
		`proper|repack)\b`)
	// albumCatalogNumberRE is a bracketed barcode or catalogue number,
	// "[88875196672]".
	albumCatalogNumberRE = regexp.MustCompile(`^\s*[0-9]{6,14}\s*$`)
	// albumOtherRecordingRE names another recording than the studio
	// album's, which no edition note makes the album's.
	albumOtherRecordingRE = regexp.MustCompile(`(?i)\b(?:live|remix(?:es|ed)?|instrumentals?|a\s?cappella|acoustic|` +
		`unplugged|demos?|karaoke|commentary)\b`)
	// albumTrailingNoteRE is a trailing bracketed note, "(Expanded Edition)".
	albumTrailingNoteRE = regexp.MustCompile(`\s*[(\[]([^()\[\]]*)[)\]]$`)
	// albumTrailingTailRE is a trailing tail after " - " or ": ", "Already
	// Platinum: Reloaded", "Album - Remastered 2011".
	albumTrailingTailRE = regexp.MustCompile(`(?:\s+-\s+|:\s+)([^:]+?)$`)
	// albumTrailingWordsRE are bare trailing edition words, "Still Kool
	// CD", "Yellow Tape 2 Deluxe".
	albumTrailingWordsRE = regexp.MustCompile(`(?i)\s+(?:cd\s*\d*|disc\s*\d+|(?:super\s+)?deluxe(?:\s+(?:edition|version))?)$`)
	// albumChoppedRE starts a Houston chopped-and-screwed version's note,
	// bracketed or not, to the end: "Boss Of All Bosses Chopped & Screwed By
	// Michael "5000" Watts". MusicBrainz seldom lists one as a group of its
	// own, and its tracks carry the album's titles.
	albumChoppedRE = regexp.MustCompile(`(?i)\s*[(\[]?\s*(?:chopped|screwed)\s*(?:&|and|n'?|\+)\s*(?:chopped|screwed)\b.*$`)
)

// stripAlbumEdition drops s's trailing edition notes, one after another,
// keeping any note that names another recording and never the whole title.
func stripAlbumEdition(s string) string {
	t := strings.TrimSpace(s)
	for {
		cut := -1
		if m := albumTrailingNoteRE.FindStringSubmatchIndex(t); m != nil && editionNote(t[m[2]:m[3]]) {
			cut = m[0]
		} else if m := albumTrailingTailRE.FindStringSubmatchIndex(t); m != nil &&
			editionNote(t[m[2]:m[3]]) && len(strings.Fields(t[m[2]:m[3]])) <= 4 {
			cut = m[0]
		} else if m := albumTrailingWordsRE.FindStringIndex(t); m != nil {
			cut = m[0]
		} else if m := albumChoppedRE.FindStringIndex(t); m != nil {
			cut = m[0]
		}
		if cut <= 0 || strings.TrimSpace(t[:cut]) == "" {
			return t
		}
		t = strings.TrimSpace(t[:cut])
	}
}

func editionNote(note string) bool {
	return albumCatalogNumberRE.MatchString(note) ||
		(albumEditionWordRE.MatchString(note) && !albumOtherRecordingRE.MatchString(note))
}

// byTrackTitle narrows albums one title names to the one holding the
// file's track (talos, 2026-11-16: Stephan Bodzin's Boavista single and
// album of 2021 held all 17 of the album's files as ambiguous): the album
// whose track at the file's number carries its title tag, else the one
// album with a track of that title, else, among those at the number, the
// one whose disc holds the file's track total, else the one of them with
// the most tracks, which holds the file as well as any (WOPTOBER's 14- and
// 13-track groups). Anything else leaves hits as they were, ambiguous.
func byTrackTitle(hits []string, mine []AlbumCandidate, tags *commonv1.MediaTags) []string {
	if len(hits) < 2 || tags == nil || strings.TrimSpace(tags.Title) == "" {
		return hits
	}
	var cands []AlbumCandidate
	for _, c := range mine {
		if slices.Contains(hits, c.Name) {
			cands = append(cands, c)
		}
	}
	disc := tags.DiscNumber
	var atNumber, anywhere []AlbumCandidate
	for _, c := range cands {
		d := disc
		if d == 0 && c.Media <= 1 {
			d = 1
		}
		if slices.ContainsFunc(c.Tracks, func(t catalogv1alpha1.Track) bool {
			return tags.TrackNumber > 0 && t.Number == tags.TrackNumber && (d == 0 || t.Medium == d) &&
				release.TrackTitlesAgreeAtNumber(tags.Title, t.Title)
		}) {
			atNumber = append(atNumber, c)
		}
		if slices.ContainsFunc(c.Tracks, func(t catalogv1alpha1.Track) bool { return release.TrackTitlesAgree(tags.Title, t.Title) }) {
			anywhere = append(anywhere, c)
		}
	}
	switch {
	case len(atNumber) == 1:
		return []string{atNumber[0].Name}
	case len(atNumber) == 0 && len(anywhere) == 1:
		return []string{anywhere[0].Name}
	case len(atNumber) > 1 && tags.TrackTotal > 0 && len(fitTotal(atNumber, disc, tags.TrackTotal)) == 1:
		return fitTotal(atNumber, disc, tags.TrackTotal)
	case len(atNumber) > 1:
		// Every one holds the file's title at its number and nothing else
		// tells them apart (WOPTOBER's 14- and 13-track groups): the one with
		// the most tracks holds the file as well as any.
		if most := mostTracks(atNumber); most != "" {
			return []string{most}
		}
	}
	return hits
}

// fitTotal are the candidates whose disc holds total tracks.
func fitTotal(cands []AlbumCandidate, disc, total int32) []string {
	d := max(disc, 1)
	var fit []string
	for _, c := range cands {
		n := 0
		for _, t := range c.Tracks {
			if t.Medium == d || (t.Medium == 0 && d == 1) {
				n++
			}
		}
		if int32(n) == total { //nolint:gosec // a disc's tracks, far under int32
			fit = append(fit, c.Name)
		}
	}
	return fit
}

// mostTracks is the one candidate with the most tracks, "" on a tie.
func mostTracks(cands []AlbumCandidate) string {
	best, n, tie := "", -1, false
	for _, c := range cands {
		switch {
		case len(c.Tracks) > n:
			best, n, tie = c.Name, len(c.Tracks), false
		case len(c.Tracks) == n:
			tie = true
		}
	}
	if tie {
		return ""
	}
	return best
}
