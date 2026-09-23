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
	"fmt"
	"regexp"
	"strings"
	"unicode"

	common "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

// The identity rules for the non-video kinds a release can be FOR: an album,
// a book, an audiobook and a comic issue. Artist, Author and Comic are the
// containers those belong to, and no search targets one directly, so
// identityRejection still has no rule for them.
//
// None of these rules compares ids or consults Identity.IDQueryIndexers. A
// release's ids are tmdb/imdb/tvdb (ReleaseInfo.IDs), none of which a music,
// book or comic item has, and a Torznab music or book search is keyed by
// name (artist/album, author/title), never by id -- so every non-video
// release is identified by what its title names, as Lidarr, Readarr and
// Mylar identify one. Everything else follows the video rules: evidence of a
// different item is WrongItem, no evidence either way is UnknownItem, and an
// unevaluable name fails closed.

// albumYearTolerance is how far an album release's year may sit from the
// album's own and still be the same album: Lidarr's
// AlbumYearMatchingOptions.HardRejectYearDiff. Lidarr's AlbumYearMatcher
// matches, with a falling score, anything up to five years from the album's
// release date and rejects only beyond it -- a remaster or a regional
// release legitimately carries a later year (Lidarr
// src/NzbDrone.Core/Music/AlbumYearMatcher.cs, AlbumYearMatchingOptions.cs,
// develop).
const albumYearTolerance = 5

// albumEditionYearTolerance is how far an album release's year may sit from
// one of the album's editions (Identity.EditionYears) and be that edition:
// Lidarr's AlbumYearMatchingOptions.ExactMatchYearTolerance. Lidarr's
// AlbumYearMatcher.Match(Album, int?) tries the album's own release date
// first, then -- "Check album releases for remasters/editions with different
// years" -- each release the album accepts (r.Monitored ||
// album.AnyReleaseOk), taking any within this tolerance, and only then falls
// back to the primary result's five-year hard reject.
const albumEditionYearTolerance = 1

// issueYearTolerance is how far a comic issue release's year may sit from
// the issue's cover-date year. Mylar compares the release's year with the
// issue's date year (search.py sets ComicYear from IssueDate), exactly by
// default, and allows one year either side for an issue dated across the new
// year (IssDateFix) or with fuzzy year matching on (UseFuzzy "2")
// (mylar3 mylar/search_filer.py). A cover date runs months ahead of the
// store date, so one year either side is the rule here.
const issueYearTolerance = 1

// albumRejection is the album rule, Lidarr's: the release's artist is one of
// the item's (ParsingService.GetArtist compares clean artist names), its
// album title is one of the item's (GetAlbums / FindAlbumInSearchCriteria),
// and its year is within albumYearTolerance of the album's or
// albumEditionYearTolerance of one of its editions' (albumYearRejection).
func albumRejection(id Identity, idx identityIndex, p *release.ParsedRelease) *common.Rejection {
	if p.Music == nil {
		r := newRejection(ReasonUnknownItem, "release title %q names no artist and album", p.Title)
		return &r
	}
	if r := verdict(
		namePart{
			noun: "artist", nouns: "artists", item: id.Creators, itemKeys: idx.creators,
			release: p.Music.Artist, releaseKeys: releaseCreatorKeys(p.Music.Artist),
		}.fact(),
		namePart{
			noun: "album title", nouns: "titles", item: id.Titles, itemKeys: idx.titles,
			release: p.Music.Album, releaseKeys: nonEmptyKeys(titleKey(p.Music.Album)),
		}.fact(),
	); r != nil {
		return r
	}
	return albumYearRejection(id, p.Music.Year)
}

// albumYearRejection is Lidarr's AlbumYearMatcher: a release year within
// albumEditionYearTolerance of any of the album's edition years is that
// edition; otherwise the album's own year bounds it by albumYearTolerance.
// An unknown year on either side constrains nothing, as yearRejection says.
func albumYearRejection(id Identity, releaseYear int) *common.Rejection {
	if releaseYear >= minPlausibleYear {
		for _, y := range id.EditionYears {
			if y < minPlausibleYear {
				continue
			}
			if d := y - releaseYear; d >= -albumEditionYearTolerance && d <= albumEditionYearTolerance {
				return nil
			}
		}
	}
	r := yearRejection(id.Year, releaseYear, albumYearTolerance)
	if r != nil {
		if n := datedEditions(id.EditionYears); n > 0 {
			r.Reason += fmt.Sprintf(", and is not within %d year of any of its %d dated edition(s)", albumEditionYearTolerance, n)
		}
	}
	return r
}

// datedEditions counts the edition years a release could have matched.
func datedEditions(years []int) int {
	n := 0
	for _, y := range years {
		if y >= minPlausibleYear {
			n++
		}
	}
	return n
}

// bookRejection is the book and audiobook rule, Readarr's: the release's
// author is one of the item's (ParsingService.GetAuthor compares clean
// author names) and its title is one of the item's (GetBooks compares the
// title and the clean title). No year: Readarr matches none ("TODO: Search
// by Title and Year instead of just Title when matching"), and a book's
// editions legitimately span decades.
func bookRejection(id Identity, idx identityIndex, p *release.ParsedRelease) *common.Rejection {
	if p.Book == nil {
		r := newRejection(ReasonUnknownItem, "release title %q names no author and title", p.Title)
		return &r
	}
	return verdict(
		namePart{
			noun: "author", nouns: "authors", item: id.Creators, itemKeys: idx.creators,
			release: p.Book.Author, releaseKeys: releaseCreatorKeys(p.Book.Author),
		}.fact(),
		namePart{
			noun: "title", nouns: "titles", item: id.Titles, itemKeys: idx.titles,
			release: p.Title, releaseKeys: releaseTitleKeys(p),
		}.fact(),
	)
}

// issueRejection is the comic issue rule, Mylar's: the release's series is
// the issue's comic (volume), its issue number is the issue's, and its year
// is within issueYearTolerance of the issue's cover year.
func issueRejection(id Identity, idx identityIndex, p *release.ParsedRelease) *common.Rejection {
	if p.Comic == nil {
		r := newRejection(ReasonUnknownItem, "release title %q names no series and issue", p.Title)
		return &r
	}
	if r := verdict(
		namePart{
			noun: "series", nouns: "titles", item: id.Titles, itemKeys: idx.titles,
			release: p.Comic.Series, releaseKeys: releaseTitleKeys(p),
		}.fact(),
		issueNumberFact(id.Issue, p.Comic.Issue),
	); r != nil {
		return r
	}
	return yearRejection(id.Year, p.Comic.Year, issueYearTolerance)
}

// fact is one comparison's outcome: wrong names evidence of a different
// item, unknown says the comparison could not be made, both empty means it
// agreed.
type fact struct{ wrong, unknown string }

// verdict folds several facts into one rejection. Evidence wins: any wrong
// fact is WrongItem, whatever else could not be compared, because one
// readable disagreement is enough to know the release is for something
// else. Otherwise any unknown fact is UnknownItem, failing closed.
func verdict(facts ...fact) *common.Rejection {
	var wrong, unknown []string
	for _, f := range facts {
		if f.wrong != "" {
			wrong = append(wrong, f.wrong)
		}
		if f.unknown != "" {
			unknown = append(unknown, f.unknown)
		}
	}
	switch {
	case len(wrong) > 0:
		r := newRejection(ReasonWrongItem, "%s", strings.Join(wrong, "; "))
		return &r
	case len(unknown) > 0:
		r := newRejection(ReasonUnknownItem, "%s", strings.Join(unknown, "; "))
		return &r
	}
	return nil
}

// namePart is one named half of a non-video identity -- the artist, the
// album title, the author -- with both sides already keyed.
type namePart struct {
	noun, nouns string // "artist", "artists"
	item        []string
	itemKeys    map[string]struct{}
	release     string
	releaseKeys []string
}

func (n namePart) fact() fact {
	switch {
	case len(n.item) == 0:
		return fact{unknown: fmt.Sprintf("the item has no %s yet", n.noun)}
	case len(n.itemKeys) == 0:
		return fact{unknown: fmt.Sprintf("none of the item's %s (primary %q) has anything the comparison can read", n.nouns, n.item[0])}
	case len(n.releaseKeys) == 0:
		return fact{unknown: fmt.Sprintf("release %s %q has nothing the comparison can read", n.noun, n.release)}
	}
	for _, k := range n.releaseKeys {
		if _, ok := n.itemKeys[k]; ok {
			return fact{}
		}
	}
	return fact{wrong: fmt.Sprintf("release %s %q matches none of the item's %d known %s (primary %q)",
		n.noun, n.release, len(n.item), n.nouns, n.item[0])}
}

// issueNumberFact compares issue numbers through issueNumberKey.
func issueNumberFact(item, rel string) fact {
	want, got := issueNumberKey(item), issueNumberKey(rel)
	switch {
	case want == "":
		return fact{unknown: "the item has no issue number"}
	case got == "":
		return fact{unknown: fmt.Sprintf("release issue number %q has nothing the comparison can read", rel)}
	case want != got:
		return fact{wrong: fmt.Sprintf("release is issue %s, the item is issue %s", rel, item)}
	}
	return fact{}
}

// issueNumberKey is an issue number's comparison form. A plain number loses
// its padding and trailing fractional zeros, so "050", "50" and "50.0" are
// one issue and "12.50" is "12.5" -- Mylar's helpers.issuedigits compares
// issues numerically for the same reason. Anything else ("Annual 1",
// "12.HU") is compared on its lower-cased letters and digits alone.
func issueNumberKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	whole, frac, dotted := strings.Cut(s, ".")
	if allDigits(whole) && (!dotted || allDigits(frac)) && (whole != "" || frac != "") {
		whole = strings.TrimLeft(whole, "0")
		if whole == "" {
			whole = "0"
		}
		if frac = strings.TrimRight(frac, "0"); frac != "" {
			return whole + "." + frac
		}
		return whole
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return r
		}
		return -1
	}, s)
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// creatorKeys keys one creator's name both ways round when it is written
// "Last, First" -- how library catalogues, and many book releases, file an
// author ("Herbert, Frank - Dune") -- so it meets "Frank Herbert".
func creatorKeys(name string) []string {
	keys := nonEmptyKeys(titleKey(name))
	if before, after, ok := strings.Cut(name, ","); ok && !strings.Contains(after, ",") {
		if k := titleKey(after + " " + before); k != "" && k != keys0(keys) {
			keys = append(keys, k)
		}
	}
	return keys
}

// coCredit splits a release's credit into the people it names: "Stephen King
// & Peter Straub", "Artist feat. Guest", "Gaiman, Pratchett".
var coCredit = regexp.MustCompile(`(?i)\s+(?:&|and|feat\.?|ft\.?|featuring)\s+|\s*[;,]\s*`)

// releaseCreatorKeys keys a release's credit whole and each co-credited name
// in it, so a co-written book or a featured-artist album is identified by
// any one of its creators the item lists. Only the RELEASE side is split: an
// item's Creators already lists each creator separately, and splitting its
// "Simon & Garfunkel" into "Simon" would let a release by some other Simon
// match it.
func releaseCreatorKeys(credit string) []string {
	keys := creatorKeys(credit)
	for _, part := range coCredit.Split(credit, -1) {
		for _, k := range creatorKeys(part) {
			if !containsKey(keys, k) {
				keys = append(keys, k)
			}
		}
	}
	return keys
}

// creatorKeySet keys every one of an item's creators.
func creatorKeySet(creators []string) map[string]struct{} {
	keys := make(map[string]struct{}, 2*len(creators))
	for _, c := range creators {
		for _, k := range creatorKeys(c) {
			keys[k] = struct{}{}
		}
	}
	return keys
}

func nonEmptyKeys(k string) []string {
	if k == "" {
		return nil
	}
	return []string{k}
}

func keys0(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	return keys[0]
}

func containsKey(keys []string, k string) bool {
	for _, have := range keys {
		if have == k {
			return true
		}
	}
	return false
}
