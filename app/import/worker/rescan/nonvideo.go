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

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
)

// Non-video attribution: music, book, audiobook and comic root folders.
//
// The matchers here attribute a file to an EXISTING catalog item. What a
// folder no item has becomes is identify.go's: amendment §A1.5 as written --
// an embedded identifier first, then the catalog, then the metadata gateway
// -- where the gateway is asked and only an exact, unique answer is taken
// (the 2026-11-10 non-video libraries design's D16). An Artist, Author or
// Audiobook is proposed from its folder that way; an Album, Book, Comic or
// Issue never is, since each comes from its container's metadata fan-out.
//
// Matching is layered, each layer exact, and a layer that finds two or more
// items reports them as ambiguous rather than picking one:
//
//  1. an embedded id: an ASIN (audiobook), the MusicBrainz release-group
//     and release ids a Picard-tagged track carries (album, D17), an
//     artist folder's [mbid-...] token (artist);
//  2. the item's declared folder -- status.path, which its own controller
//     resolves -- for the kinds whose folder is theirs alone: an artist, an
//     album, an audiobook and a comic. Not a book: pkg/naming's book folder
//     is the AUTHOR's folder ("{Author Name}"), so a Book's folder does not
//     identify the book;
//  3. the names the layout carries (pkg/naming's presets: "Artist/Album
//     (Year)/...", "Author/Book Title/...", "Author/[Series/]Book/...",
//     "Series/Series c001.cbz") and, for a track, its ALBUM tag, compared by
//     [release.CleanTitle] equality against the items' cached metadata,
//     narrowed by year when both sides know one -- exactly [MatchMovie]'s
//     title rule.
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
	// item has. [MatchAudiobook] says so for an ASIN; the walk then
	// proposes the Audiobook from its folder's ASIN in the root's region
	// (identify.go), and lists the file under this code only when no
	// folder carries it.
	CodeUnknownID = "unknown_id"

	// CodeNoChild means the parent item was identified but none of its
	// children (a comic's issues, an artist's albums by a track's ALBUM
	// tag) matches.
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

	// Artist is the Album's spec.artistRef: the walk reads an artist's
	// albums by it (which of them are still waiting for their metadata or
	// tracks), never the matcher.
	Artist string

	// ReleaseGroupID is spec.releaseGroupID and ReleaseIDs every release
	// of the group status.metadata lists (status.metadata.releases[].id,
	// with the selected release's id): the MUSICBRAINZ_RELEASEGROUPID and
	// MUSICBRAINZ_ALBUMID tags a Picard-tagged track carries name one of
	// them (2026-11-10 design D17).
	ReleaseGroupID string
	ReleaseIDs     []string

	// ArtistNames are the names the artist's folder may carry: its
	// metadata name and aliases, spec.folder, and the last segment of
	// status.path.
	ArtistNames []string

	// Tracks is the album's selected release's listing and Media its disc
	// count, for the track a scanned file is attributed to
	// (fileimport.AttributeTrack, 2026-11-10 design D6).
	Tracks []catalogv1alpha1.Track
	Media  int32
}

// BookCandidate is one existing Book.
type BookCandidate struct {
	Name  string
	Title string
	Year  int

	// Author is the Book's spec.authorRef ("" for a standalone book) and
	// Path its status.path relative to the root: the walk reads an
	// author's books by the one and keeps a standalone book's own folder
	// from being taken for an author's by the other, never the matcher.
	Author string
	Path   string

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

// ArtistCandidate is one existing Artist of the scan's namespace, with what
// identifying an artist folder needs from it (MatchArtist; identify.go's
// existing-id check reads every root's).
type ArtistCandidate struct {
	Name          string
	MusicBrainzID string // spec.musicBrainzID

	// Path is status.path relative to the root folder ("" when the Artist
	// controller has not resolved it, or it lies outside this root), and
	// Folder spec.folder as written: the folder the artist's files are in.
	Path   string
	Folder string

	// Names are status.metadata.name and its aliases.
	Names []string

	// OtherRootFolder is spec.rootFolderRef when it is not the root folder
	// being scanned, as on [MovieCandidate]: such an artist is never
	// matched, and never proposed again for its id.
	OtherRootFolder string
}

// AuthorCandidate is one existing Author of the scan's namespace; its fields
// mean what [ArtistCandidate]'s do, keyed by an Open Library author id.
type AuthorCandidate struct {
	Name            string
	OpenLibraryID   string // spec.openLibraryID
	Path            string
	Folder          string
	Names           []string
	OtherRootFolder string
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

// MatchArtist identifies the existing Artist a first segment under a music
// root names (2026-11-10 design D16, §5.7), in layers, each exact: an
// [mbid-...] token naming an artist's id; the artist's resolved folder
// (status.path); its spec.folder; its name or an alias by
// [release.CleanTitle] equality. Only the artists of the root being walked
// are candidates. A folder none of them names is unmatched, CodeNoMatch,
// for identify.go to ask the metadata gateway about; a token naming no
// artist of this root is that too, never a fall-back to the names.
func MatchArtist(segment string, cands []ArtistCandidate) ItemMatch {
	mine := make([]ArtistCandidate, 0, len(cands))
	for _, c := range cands {
		if c.OtherRootFolder == "" {
			mine = append(mine, c)
		}
	}
	return matchContainer(commonv1.MediaKindArtist, segment, mbidFolderRE, len(mine), func(i int) containerKeys {
		c := mine[i]
		return containerKeys{name: c.Name, id: c.MusicBrainzID, path: c.Path, folder: c.Folder, names: c.Names}
	})
}

// MatchAuthor is [MatchArtist] for an author folder under a book root: an
// [olid-OL...A] token, status.path, spec.folder, then the author's name.
func MatchAuthor(segment string, cands []AuthorCandidate) ItemMatch {
	mine := make([]AuthorCandidate, 0, len(cands))
	for _, c := range cands {
		if c.OtherRootFolder == "" {
			mine = append(mine, c)
		}
	}
	return matchContainer(commonv1.MediaKindAuthor, segment, olidFolderRE, len(mine), func(i int) containerKeys {
		c := mine[i]
		return containerKeys{name: c.Name, id: c.OpenLibraryID, path: c.Path, folder: c.Folder, names: c.Names}
	})
}

// containerKeys is what [matchContainer] reads off one candidate.
type containerKeys struct {
	name, id, path, folder string
	names                  []string
}

// matchContainer is MatchArtist's and MatchAuthor's layers over n
// candidates, token being the kind's id token in a folder name.
func matchContainer(
	kind commonv1.MediaKind, segment string, token *regexp.Regexp, n int, at func(int) containerKeys,
) ItemMatch {
	layer := func(keep func(containerKeys) bool) []string {
		var hits []string
		for i := range n {
			if k := at(i); keep(k) {
				hits = append(hits, k.name)
			}
		}
		return hits
	}
	if m := token.FindStringSubmatch(segment); m != nil {
		// The id is the identity: never fall back to the names when it
		// names no item of this root.
		hits := layer(func(k containerKeys) bool { return k.id != "" && strings.EqualFold(k.id, m[1]) })
		return decide(kind, hits, fmt.Sprintf("no existing %s of this root folder has the id %s its folder names", kind, m[1]))
	}
	for _, keep := range []func(containerKeys) bool{
		func(k containerKeys) bool { return k.path != "" && filepath.ToSlash(k.path) == segment },
		func(k containerKeys) bool {
			return k.folder != "" && filepath.ToSlash(filepath.Clean(k.folder)) == segment
		},
		func(k containerKeys) bool { return anyCleanEq(k.names, segment) },
	} {
		if hits := layer(keep); len(hits) > 0 {
			return decide(kind, hits, "")
		}
	}
	return ItemMatch{Unmatched: true, Code: CodeNoMatch, Reason: fmt.Sprintf(
		"no existing %s of this root folder has the folder or the name %q", kind, segment)}
}

// MatchAlbum attributes a file under a music root folder to one Album
// (2026-11-10 design D17: Lidarr groups tracks by their tags, never by
// folder). absPath is the file's absolute path, rel its path relative to the
// root, tags the file's probed tags (nil when it carries none or was not
// probed), cands the root's albums.
//
// An album whose resolved folder (status.path) holds the file takes it.
// Otherwise the first segment names the artist (cands narrowed to the
// albums whose ArtistNames it equals, an [mbid-...] token set aside), and
// the file's album is, in order: the album the MUSICBRAINZ_RELEASEGROUPID
// tag names; the album one of whose releases the MUSICBRAINZ_ALBUMID tag
// names; the album folder's title and year when the file sits in one
// (three or more segments); the ALBUM tag's title, its date's year
// narrowing only when both sides know one. A rule that names no album
// passes to the next; one that names two is ambiguous. With an ALBUM tag no
// rule placed, the file is CodeNoChild, naming the tag ("no album of 2Pac
// is titled ..."), honest for a compilation the artist's metadata profile
// does not list; a file with no tags and no album folder keeps the layout
// message.
func MatchAlbum(absPath, rel string, tags *commonv1.MediaTags, cands []AlbumCandidate) ItemMatch {
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

	const layout = "a music file must sit at <artist>/<album>/<track> under the root folder"
	segs := strings.Split(filepath.ToSlash(rel), "/")
	if len(segs) < 2 {
		return unmatchedLayout(layout)
	}
	artist := strings.TrimSpace(mbidFolderRE.ReplaceAllString(segs[0], ""))
	var mine []AlbumCandidate
	for _, c := range cands {
		if anyCleanEq(c.ArtistNames, artist) {
			mine = append(mine, c)
		}
	}
	pick := func(keep func(AlbumCandidate) bool) []string {
		var hits []string
		for _, c := range mine {
			if keep(c) {
				hits = append(hits, c.Name)
			}
		}
		return hits
	}

	if tags != nil {
		if id := strings.TrimSpace(tags.MusicBrainzReleaseGroupID); id != "" {
			if hits := pick(func(c AlbumCandidate) bool { return strings.EqualFold(c.ReleaseGroupID, id) }); len(hits) > 0 {
				return decide(commonv1.MediaKindAlbum, hits, "")
			}
		}
		if id := strings.TrimSpace(tags.MusicBrainzReleaseID); id != "" {
			if hits := pick(func(c AlbumCandidate) bool {
				return slices.ContainsFunc(c.ReleaseIDs, func(r string) bool { return strings.EqualFold(r, id) })
			}); len(hits) > 0 {
				return decide(commonv1.MediaKindAlbum, hits, "")
			}
		}
	}

	var folderMiss string
	if len(segs) >= 3 {
		albumDir := segs[1]
		title, year := splitTitleYear(albumDir)
		if p, err := release.ParseKind(albumDir, commonv1.MediaKindAlbum); err == nil && p.Music != nil {
			// A folder still named like a release, "Artist - Album (1997) [FLAC]".
			title, year = p.Music.Album, p.Music.Year
		}
		if hits := pick(func(c AlbumCandidate) bool { return cleanEq(c.Title, title) && yearOK(c.Year, year) }); len(hits) > 0 {
			return decide(commonv1.MediaKindAlbum, hits, "")
		}
		folderMiss = fmt.Sprintf("no existing album titled %q by an artist named %q among %d albums under this root folder",
			title, artist, len(cands))
	}

	if tags != nil && strings.TrimSpace(tags.Album) != "" {
		year := tagYear(tags.Date)
		if hits := pick(func(c AlbumCandidate) bool { return cleanEq(c.Title, tags.Album) && yearOK(c.Year, year) }); len(hits) > 0 {
			return decide(commonv1.MediaKindAlbum, hits, "")
		}
		return ItemMatch{Unmatched: true, Code: CodeNoChild, Reason: fmt.Sprintf(
			"no album of %s is titled %q among the %d albums of that artist under this root folder: its tags "+
				"name no release group or release of one either, and an album the artist's metadata profile "+
				"does not list (a compilation, a live album) is never attributed", artist, tags.Album, len(mine))}
	}
	if folderMiss != "" {
		return ItemMatch{Unmatched: true, Code: CodeNoMatch, Reason: folderMiss}
	}
	return unmatchedLayout(layout)
}

// tagYear is the year a tag's date spells ("2004-03-15", "2004"), 0 when it
// starts with none.
func tagYear(date string) int {
	date = strings.TrimSpace(date)
	if len(date) < 4 {
		return 0
	}
	y, err := strconv.Atoi(date[:4])
	if err != nil || y < 1000 {
		return 0
	}
	return y
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
			// The walk proposes the Audiobook from an ASIN its folder
			// carries, in the root's region (identify.go, design D16).
			return ItemMatch{Unmatched: true, Code: CodeUnknownID, Reason: fmt.Sprintf(
				"the path carries ASIN %s, which no existing audiobook under this root folder has", asin)}
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

	dirs := bookDirs(rel)
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

// bookDirs is an audiobook file's folders under the root, its disc folders
// ("Disc 2", "CD1") set aside: "<author>/[<series>/]<book>".
func bookDirs(rel string) []string {
	segs := strings.Split(filepath.ToSlash(rel), "/")
	dirs := segs[:len(segs)-1]
	for len(dirs) > 0 && discDirRE.MatchString(dirs[len(dirs)-1]) {
		dirs = dirs[:len(dirs)-1]
	}
	return dirs
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
