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
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

// Non-video attribution: music, book, audiobook and comic root folders.
//
// Every matcher here attributes a file to an EXISTING catalog item and to
// nothing else. None of them creates an item, because an Artist, Album,
// Author, Book, Audiobook, Comic or Issue can only be created with an
// immutable provider id (spec.musicBrainzID, spec.releaseGroupID,
// spec.openLibraryID, spec.workID, spec.asin, spec.sourceID), and nothing
// on disk yields one without a guess:
//
//   - pkg/release's id extraction knows tmdb, imdb and tvdb tokens only.
//   - The metadata gateway's search verb would turn a folder name into an id,
//     which is the id-less title resolution [MatchMovie] already refuses
//     for movies -- and for artists it is a stub (musicbrainz.SearchArtists).
//   - An Audible ASIN embedded in a folder name (Audiobookshelf's
//     "[B002V0QK4C]") IS an id, but an audiobook's identity is the ASIN plus
//     its marketplace (spec.region), and the folder carries only the ASIN.
//     Creating one with the CRD's default region "us" would guess the other
//     half. So an embedded ASIN matches an existing audiobook and is never
//     used to create one.
//
// Matching is layered, each layer exact, and a layer that finds two or more
// items reports them as ambiguous rather than picking one:
//
//  1. an embedded ASIN (audiobook only);
//  2. the item's declared folder -- status.path, which its own controller
//     resolves -- for the kinds whose folder is theirs alone: an album, an
//     audiobook and a comic. Not a book: pkg/naming's book folder is the
//     AUTHOR's folder ("{Author Name}"), so a Book's folder does not
//     identify the book;
//  3. the names the layout carries (pkg/naming's presets: "Artist/Album
//     (Year)/...", "Author/Book Title/...", "Author/[Series/]Book/...",
//     "Series/Series c001.cbz") compared by [release.CleanTitle] equality
//     against the items' cached metadata, narrowed by year when both sides
//     know one -- exactly [MatchMovie]'s title rule.
//
// Candidates are limited to the items stored under the root folder being
// walked, so a file is never attributed across roots.

// Reason codes specific to non-video attribution. Like the codes in
// match.go they are closed and label-safe.
const (
	// CodeUnrecognisedLayout means the file's position under the root
	// folder does not carry the names its kind's layout needs.
	CodeUnrecognisedLayout = "unrecognised_layout"

	// CodeUnknownID means the file carried a provider id that no existing
	// item has, and the id alone is not enough to create one.
	CodeUnknownID = "unknown_id"

	// CodeNoChild means the parent item was identified but none of its
	// children (a comic's issues) matches.
	CodeNoChild = "no_child_item"

	// CodeRecordedElsewhere means a MediaFile for this path already points
	// at a different item. spec.mediaRef is immutable, so the file cannot
	// be re-attributed by a rescan.
	CodeRecordedElsewhere = "recorded_elsewhere"
)

// ItemMatch is a non-video matcher's verdict on one file.
type ItemMatch struct {
	// Ref is the item the file backs; set when Unmatched is false.
	Ref commonv1.MediaRef

	// Unmatched, Code, Reason and Candidates mean what they mean on
	// [MatchResult].
	Unmatched  bool
	Code       string
	Reason     string
	Candidates []string
}

// AlbumCandidate is one existing Album, with what matching needs from it
// and from its Artist.
type AlbumCandidate struct {
	Name  string
	Title string // status.metadata.title
	Year  int    // status.metadata.releaseDate's year; 0 when unknown
	Path  string // status.path, when the Album controller has resolved it

	// ArtistNames are the names the artist's folder may carry: its
	// metadata name, spec.folder, and the last segment of status.path.
	ArtistNames []string
}

// BookCandidate is one existing Book.
type BookCandidate struct {
	Name  string
	Title string
	Year  int

	// AuthorNames are the parent Author's metadata name, spec.folder and
	// status.path's last segment. Standalone books have none.
	AuthorNames []string

	// Standalone is true when the book has no authorRef: its author is
	// unknown on the catalog side, so the author folder cannot contradict
	// it (the year rule's "only when both sides know" applied to authors).
	Standalone bool
}

// AudiobookCandidate is one existing Audiobook.
type AudiobookCandidate struct {
	Name string
	ASIN string
	Path string
	Year int

	// Titles are the forms the book folder may carry once its sequence,
	// year and {narrator} are stripped: the title, title plus subtitle,
	// and title plus narrators (pkg/naming's audiobook preset appends the
	// narrator without braces).
	Titles []string

	// Authors are each credited author's name and all of them joined with
	// ", " (the form pkg/naming's AuthorName token renders).
	Authors []string
}

// ComicCandidate is one existing Comic.
type ComicCandidate struct {
	Name   string
	Titles []string // metadata title, spec.folder, status.path's last segment
	Year   int
	Path   string
}

// IssueCandidate is one existing Issue.
type IssueCandidate struct {
	Name     string
	ComicRef string
	Number   string
	Centis   int32
}

var (
	// titleYearRE splits "Title (1997)".
	titleYearRE = regexp.MustCompile(`^(.*\S)\s*\((\d{4})\)$`)

	// asinTokenREs are the two embedded-ASIN conventions: pkg/release's
	// "[ASIN B0...]" and Audiobookshelf's bare "[B002V0QK4C]"
	// (docs/research/naming.md §A3).
	asinTokenREs = []*regexp.Regexp{
		regexp.MustCompile(`(?i:\[ASIN)[\s:-]+([A-Z0-9]{10})\]`),
		regexp.MustCompile(`\[(B[0-9A-Z]{9})\]`),
	}

	// Audiobookshelf's book-folder grammar: "Vol 1 - 1994 - Title {Narrator}".
	narratorSuffixRE = regexp.MustCompile(`\s*\{[^{}]*\}\s*$`)
	sequencePrefixRE = regexp.MustCompile(`(?i)^(?:vol(?:ume)?\.?\s*)?(\d+(?:\.\d+)?)\s+-\s+`)
	yearPrefixRE     = regexp.MustCompile(`^(\d{4})\s+-\s+`)
	discDirRE        = regexp.MustCompile(`(?i)^(?:disc|disk|cd)\s*\d+$`)

	// issueFileRE is pkg/naming's own issue-file preset,
	// "{Comic Series Title} c{issue}", which pkg/release's comic parser
	// does not recognise.
	issueFileRE = regexp.MustCompile(`^(.+?)\s+c(\d+(?:\.\d+)?)$`)
)

// MatchAlbum attributes a file under a music root folder to one Album.
// absPath is the file's absolute path, rel its path relative to the root.
func MatchAlbum(absPath, rel string, cands []AlbumCandidate) ItemMatch {
	var inFolder []AlbumCandidate
	for _, c := range cands {
		if underFolder(absPath, c.Path) {
			inFolder = append(inFolder, c)
		}
	}
	if len(inFolder) == 1 {
		return matched(commonv1.MediaKindAlbum, inFolder[0].Name)
	}
	if len(inFolder) > 1 {
		cands = inFolder
	}

	segs := strings.Split(filepath.ToSlash(rel), "/")
	if len(segs) < 3 {
		return unmatchedLayout("a music file must sit at <artist>/<album>/<track> under the root folder")
	}
	artist, albumDir := segs[0], segs[1]
	title, year := splitTitleYear(albumDir)
	if p, err := release.ParseKind(albumDir, commonv1.MediaKindAlbum); err == nil && p.Music != nil {
		// A folder still named like a release, "Artist - Album (1997) [FLAC]".
		title, year = p.Music.Album, p.Music.Year
	}

	var hits []string
	for _, c := range cands {
		if cleanEq(c.Title, title) && anyCleanEq(c.ArtistNames, artist) && yearOK(c.Year, year) {
			hits = append(hits, c.Name)
		}
	}
	return decide(commonv1.MediaKindAlbum, hits, fmt.Sprintf(
		"no existing album titled %q by an artist named %q among %d albums under this root folder",
		title, artist, len(cands)))
}

// MatchBook attributes a file under a book root folder to one Book. It has
// no declared-folder layer; see the note at the top of this file.
func MatchBook(rel string, cands []BookCandidate) ItemMatch {
	segs := strings.Split(filepath.ToSlash(rel), "/")
	base := strings.TrimSuffix(segs[len(segs)-1], filepath.Ext(segs[len(segs)-1]))

	var author, title string
	var year int
	switch {
	case len(segs) >= 3:
		// pkg/naming's "Author/Book Title/<file>"; the book folder is the
		// file's own directory, the author folder the top one.
		author = segs[0]
		title, year = splitTitleYear(segs[len(segs)-2])
	case len(segs) == 2:
		author = segs[0]
		title, year = splitTitleYear(base)
		if p, err := release.ParseKind(base, commonv1.MediaKindBook); err == nil && p.Book != nil {
			title, year = p.Book.Title, p.Book.Year
		}
	default:
		p, err := release.ParseKind(base, commonv1.MediaKindBook)
		if err != nil || p.Book == nil {
			return unmatchedLayout("a book file must sit at <author>/<title>/<file> or <author>/<title>.<ext> " +
				"under the root folder, or be named \"Author - Title (Year) [FORMAT]\"")
		}
		author, title, year = p.Book.Author, p.Book.Title, p.Book.Year
	}

	var hits []string
	for _, c := range cands {
		if cleanEq(c.Title, title) && (c.Standalone || anyCleanEq(c.AuthorNames, author)) && yearOK(c.Year, year) {
			hits = append(hits, c.Name)
		}
	}
	return decide(commonv1.MediaKindBook, hits, fmt.Sprintf(
		"no existing book titled %q by an author named %q among %d books under this root folder",
		title, author, len(cands)))
}

// MatchAudiobook attributes a file under an audiobook root folder to one
// Audiobook.
func MatchAudiobook(absPath, rel string, cands []AudiobookCandidate) ItemMatch {
	if asin := embeddedASIN(rel); asin != "" {
		var hits []string
		for _, c := range cands {
			if strings.EqualFold(c.ASIN, asin) {
				hits = append(hits, c.Name)
			}
		}
		if len(hits) == 0 {
			return ItemMatch{Unmatched: true, Code: CodeUnknownID, Reason: fmt.Sprintf(
				"the path carries ASIN %s, which no existing audiobook under this root folder has; an ASIN alone "+
					"does not say which Audible marketplace (spec.region) it belongs to, so it is not used to create one",
				asin)}
		}
		// An embedded id is the identity: never fall back to the title
		// when it names nothing, and report two audiobooks sharing it
		// (the same ASIN in two regions) as ambiguous.
		return decide(commonv1.MediaKindAudiobook, hits, "")
	}

	var inFolder []AudiobookCandidate
	for _, c := range cands {
		if underFolder(absPath, c.Path) {
			inFolder = append(inFolder, c)
		}
	}
	if len(inFolder) == 1 {
		return matched(commonv1.MediaKindAudiobook, inFolder[0].Name)
	}
	if len(inFolder) > 1 {
		cands = inFolder
	}

	segs := strings.Split(filepath.ToSlash(rel), "/")
	dirs := segs[:len(segs)-1]
	for len(dirs) > 0 && discDirRE.MatchString(dirs[len(dirs)-1]) {
		dirs = dirs[:len(dirs)-1]
	}
	if len(dirs) < 2 {
		return unmatchedLayout("an audiobook file must sit at <author>/[<series>/]<book>/<file> under the root folder")
	}
	author := dirs[0]
	title, year := parseAudiobookFolder(dirs[len(dirs)-1])

	var hits []string
	for _, c := range cands {
		if anyCleanEq(c.Titles, title) && anyCleanEq(c.Authors, author) && yearOK(c.Year, year) {
			hits = append(hits, c.Name)
		}
	}
	return decide(commonv1.MediaKindAudiobook, hits, fmt.Sprintf(
		"no existing audiobook titled %q by an author named %q among %d audiobooks under this root folder",
		title, author, len(cands)))
}

// MatchIssue attributes a file under a comic root folder to one Issue: first
// the Comic, by declared folder or by series name, then the Issue of that
// comic whose number the file name carries.
func MatchIssue(absPath, rel string, comics []ComicCandidate, issues []IssueCandidate) ItemMatch {
	segs := strings.Split(filepath.ToSlash(rel), "/")
	base := strings.TrimSuffix(segs[len(segs)-1], filepath.Ext(segs[len(segs)-1]))

	var parsedSeries, number string
	var parsedYear int
	if p, err := release.ParseKind(base, commonv1.MediaKindComic); err == nil && p.Comic != nil {
		parsedSeries, number, parsedYear = p.Comic.Series, p.Comic.Issue, p.Comic.Year
	} else if m := issueFileRE.FindStringSubmatch(base); m != nil {
		parsedSeries, number = m[1], m[2]
	}
	if number == "" {
		return ItemMatch{Unmatched: true, Code: CodeParseError, Reason: fmt.Sprintf(
			"no issue number could be read from %q (want \"Series 001 (Year)\", \"Series v01 c001 (Year)\" or \"Series c001\")",
			segs[len(segs)-1])}
	}

	var comic string
	var inFolder []ComicCandidate
	for _, c := range comics {
		if underFolder(absPath, c.Path) {
			inFolder = append(inFolder, c)
		}
	}
	switch {
	case len(inFolder) == 1:
		comic = inFolder[0].Name
	default:
		if len(inFolder) > 1 {
			comics = inFolder
		}
		series, year := parsedSeries, parsedYear
		if len(segs) >= 2 {
			// The layout's series folder wins over the file name's own
			// series: "Saga/Saga 001 (2012).cbz" and a one-shot named
			// differently from its folder both resolve by the folder.
			var folderYear int
			series, folderYear = splitTitleYear(segs[0])
			if folderYear != 0 {
				year = folderYear
			}
		}
		var hits []string
		for _, c := range comics {
			if anyCleanEq(c.Titles, series) && yearOK(c.Year, year) {
				hits = append(hits, c.Name)
			}
		}
		m := decide(commonv1.MediaKindComic, hits, fmt.Sprintf(
			"no existing comic named %q among %d comics under this root folder", series, len(comics)))
		if m.Unmatched {
			return m
		}
		comic = m.Ref.Name
	}

	var hits []string
	for _, is := range issues {
		if is.ComicRef == comic && sameIssueNumber(is, number) {
			hits = append(hits, is.Name)
		}
	}
	if len(hits) == 0 {
		return ItemMatch{Unmatched: true, Code: CodeNoChild, Reason: fmt.Sprintf(
			"comic %s has no issue numbered %q; issues are created by the Comic controller from its metadata, "+
				"never by the scanner", comic, number), Candidates: []string{comic}}
	}
	return decide(commonv1.MediaKindIssue, hits, "")
}

// sameIssueNumber compares a parsed issue number with an Issue: numerically
// through calculatedNumberCentis when the parsed number is numeric ("001"
// is issue 1, "12.5" is 1250), and by cleaned text otherwise.
func sameIssueNumber(is IssueCandidate, number string) bool {
	if centis, ok := issueCentis(number); ok {
		return is.Centis == centis
	}
	return cleanEq(is.Number, number)
}

// issueCentis converts "001" to 100 and "12.5" to 1250. Anything that is not
// a plain decimal with at most two fractional digits reports false.
func issueCentis(number string) (int32, bool) {
	whole, frac, _ := strings.Cut(number, ".")
	if whole == "" || len(frac) > 2 {
		return 0, false
	}
	w, err := strconv.ParseInt(whole, 10, 32)
	if err != nil || w < 0 {
		return 0, false
	}
	var f int64
	if frac != "" {
		for len(frac) < 2 {
			frac += "0"
		}
		f, err = strconv.ParseInt(frac, 10, 32)
		if err != nil {
			return 0, false
		}
	}
	c := w*100 + f
	if c > 1<<31-1 {
		return 0, false
	}
	return int32(c), true
}

// embeddedASIN returns the first ASIN token in any segment of rel, upper-cased.
func embeddedASIN(rel string) string {
	for _, re := range asinTokenREs {
		if m := re.FindStringSubmatch(rel); m != nil {
			return strings.ToUpper(m[1])
		}
	}
	return ""
}

// parseAudiobookFolder strips Audiobookshelf's book-folder decorations:
// a trailing "{Narrator}", a leading "Vol N - " sequence and a "YYYY - "
// year. What remains is the title as the folder spells it.
func parseAudiobookFolder(dir string) (title string, year int) {
	s := narratorSuffixRE.ReplaceAllString(dir, "")
	if m := sequencePrefixRE.FindStringSubmatch(s); m != nil {
		s = s[len(m[0]):]
		if y := yearPrefixRE.FindStringSubmatch(s); y != nil {
			year, _ = strconv.Atoi(y[1])
			s = s[len(y[0]):]
		} else if len(m[1]) == 4 {
			// "1994 - Title": the only number was the year.
			year, _ = strconv.Atoi(m[1])
		}
	}
	return strings.TrimSpace(s), year
}

// splitTitleYear splits "Title (1997)" into its parts; a name without a
// trailing four-digit year is all title.
func splitTitleYear(s string) (string, int) {
	if m := titleYearRE.FindStringSubmatch(strings.TrimSpace(s)); m != nil {
		y, _ := strconv.Atoi(m[2])
		return m[1], y
	}
	return strings.TrimSpace(s), 0
}

// underFolder reports whether absPath lies strictly beneath folder.
func underFolder(absPath, folder string) bool {
	if folder == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(folder), absPath)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, "../")
}

// cleanEq is CleanTitle equality with an empty side never matching.
func cleanEq(a, b string) bool {
	ca := release.CleanTitle(a)
	return ca != "" && ca == release.CleanTitle(b)
}

func anyCleanEq(names []string, want string) bool {
	for _, n := range names {
		if cleanEq(n, want) {
			return true
		}
	}
	return false
}

// yearOK narrows only when both sides know a year, as in matchByTitle.
func yearOK(a, b int) bool { return a == 0 || b == 0 || a == b }

func matched(kind commonv1.MediaKind, name string) ItemMatch {
	return ItemMatch{Ref: commonv1.MediaRef{Kind: kind, Name: name}}
}

func unmatchedLayout(reason string) ItemMatch {
	return ItemMatch{Unmatched: true, Code: CodeUnrecognisedLayout, Reason: reason}
}

// decide turns a hit list into a verdict: one hit matches, none is
// noneReason, two or more are ambiguous with their names as candidates.
func decide(kind commonv1.MediaKind, hits []string, noneReason string) ItemMatch {
	switch len(hits) {
	case 1:
		return matched(kind, hits[0])
	case 0:
		return ItemMatch{Unmatched: true, Code: CodeNoMatch, Reason: noneReason}
	default:
		slices.Sort(hits)
		reason := fmt.Sprintf("ambiguous: %d existing %s items match", len(hits), kind)
		if len(hits) > MaxCandidates {
			hits = hits[:MaxCandidates]
		}
		return ItemMatch{Unmatched: true, Code: CodeAmbiguousTitle, Reason: reason, Candidates: hits}
	}
}
