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

package plex

import (
	"cmp"
	"regexp"
	"slices"
	"strings"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/release"
	"github.com/mediactl/clustarr/ui/projection"
)

// The music roots' match (design §7.3). The request fields are the video
// roots' matchRequest; whether PMS's music scanner sends the same body is
// unverified (no PMS build accepts the roots yet, §7.6), so every rule
// reads the documented fields only. Each rule answers only what its root
// serves, and several equal matches answer every candidate ranked for a
// manual match and none for an automatic one: never a guess.

// manualCandidates caps a manual match's ranked list, as the video roots
// cap theirs.
const manualCandidates = 5

// matchMusic dispatches a music root's match request by type: 8 artist, 9
// album, 10 track, each in the root's own shape.
func (h *handler) matchMusic(root rootDef, u urls, idx *projection.Index, req matchRequest) []Metadata {
	manual := req.Manual == 1
	includeChildren := req.IncludeChildren == 1
	if root.audiobooks {
		return matchAudiobooksRoot(root, u, idx, req, manual, includeChildren)
	}
	switch req.Type {
	case typeArtist:
		artists := matchArtists(idx, req, manual)
		out := make([]Metadata, len(artists))
		for i, a := range artists {
			out[i] = buildArtistLevel(root, u, idx, artistLevelOf(a), includeChildren)
		}
		return out
	case typeAlbum:
		albums := matchAlbums(idx, req, manual)
		out := make([]Metadata, len(albums))
		for i, al := range albums {
			out[i] = buildAlbumMetadata(root, u, idx, al, includeChildren)
		}
		return out
	case typeTrack:
		al, tr := matchTrack(idx, req)
		if tr == nil {
			return nil
		}
		return []Metadata{buildTrackMetadata(root, u, idx, al, tr)}
	default:
		return nil
	}
}

// disambiguation is MusicBrainz's trailing "(...)" on a name that needs
// telling apart ("Nirvana (US grunge band)"), which a scanner never sends.
var disambiguation = regexp.MustCompile(`\s*\([^()]*\)\s*$`)

// nameNorm normalises an artist, album or track name for comparison:
// release.TitleNorm (case, accents, punctuation, a leading or trailing
// English article, so "The Beatles" and "Beatles, The" meet) with a
// trailing parenthesised disambiguation dropped.
func nameNorm(s string) string {
	return release.TitleNorm(disambiguation.ReplaceAllString(s, ""))
}

// nameMatches reports whether norm (already [nameNorm]-ed) equals any of
// names, each read with and without its disambiguation.
func nameMatches(norm string, names ...string) bool {
	if norm == "" {
		return false
	}
	for _, n := range names {
		if n == "" {
			continue
		}
		if nameNorm(n) == norm || release.TitleNorm(n) == norm {
			return true
		}
	}
	return false
}

// unique answers an automatic match: the one candidate, or none when
// several are equal (never a guess). A manual match gets every candidate,
// ranked, up to manualCandidates.
func unique[T any](cands []T, manual bool) []T {
	switch {
	case len(cands) == 0:
		return nil
	case manual:
		return cands[:min(manualCandidates, len(cands))]
	case len(cands) == 1:
		return cands
	default:
		return nil
	}
}

// artistNames are the names an Artist is known by: its name, sort name and
// aliases.
func artistNames(a *catalogv1.Artist) []string {
	md := a.Status.Metadata
	if md == nil {
		return nil
	}
	return append([]string{md.Name, md.SortName}, md.Aliases...)
}

func artistName(a *catalogv1.Artist) string {
	if a.Status.Metadata == nil {
		return ""
	}
	return a.Status.Metadata.Name
}

// artistsNamed are the Artists whose name, sort name or an alias
// normalises to title, by name then UID.
func artistsNamed(idx *projection.Index, title string) []*catalogv1.Artist {
	norm := nameNorm(title)
	var out []*catalogv1.Artist
	for _, a := range idx.Artists() {
		if nameMatches(norm, artistNames(a)...) {
			out = append(out, a)
		}
	}
	slices.SortFunc(out, func(a, b *catalogv1.Artist) int {
		if c := cmp.Compare(artistName(a), artistName(b)); c != 0 {
			return c
		}
		return cmp.Compare(a.UID, b.UID)
	})
	return out
}

// matchArtists is type 8 on the music root (design §7.3): a "mbid://" guid,
// else the file (the artist of the album the file belongs to), else the
// title against every Artist's names. A manual match lists the file's
// artist first and the title's candidates after it, as the video roots'
// Fix Match does.
func matchArtists(idx *projection.Index, req matchRequest, manual bool) []*catalogv1.Artist {
	if a, ok := artistByGuid(idx, req.Guid); ok {
		return []*catalogv1.Artist{a}
	}
	var fileArtist *catalogv1.Artist
	if al, _, ok := albumByFile(idx, req.Filename); ok {
		if a, ok := idx.ArtistOfAlbum(al.UID); ok {
			fileArtist = a
			if !manual {
				return []*catalogv1.Artist{a}
			}
		}
	}
	artists := unique(artistsNamed(idx, req.Title), manual)
	if fileArtist != nil {
		artists = leadWith(fileArtist, artists)
	}
	return artists
}

// artistByGuid resolves a match request's "mbid://<artist MBID>" guid.
func artistByGuid(idx *projection.Index, guid string) (*catalogv1.Artist, bool) {
	scheme, id, ok := splitGuid(guid)
	if !ok || scheme != "mbid" {
		return nil, false
	}
	obj, ok := idx.ByMBID(commonv1.MediaKindArtist, id)
	if !ok {
		return nil, false
	}
	a, ok := obj.(*catalogv1.Artist)
	return a, ok
}

// albumByGuid resolves a match request's "mbid://<release-group MBID>"
// guid.
func albumByGuid(idx *projection.Index, guid string) (*catalogv1.Album, bool) {
	scheme, id, ok := splitGuid(guid)
	if !ok || scheme != "mbid" {
		return nil, false
	}
	obj, ok := idx.ByMBID(commonv1.MediaKindAlbum, id)
	if !ok {
		return nil, false
	}
	al, ok := obj.(*catalogv1.Album)
	return al, ok
}

// albumByFile is rule 0 for the music root: the one Album the named file
// belongs to (a MediaFile of kind album whose path ends with the name),
// and the track it holds when the file names one -- by spec.mediaRef.track
// (the recording), else as the track whose fileRef names it. Two files
// resolving to two Albums answer nothing; two tracks of one Album answer
// the Album alone.
func albumByFile(idx *projection.Index, filename string) (*catalogv1.Album, *catalogv1.Track, bool) {
	var album *catalogv1.Album
	var track *catalogv1.Track
	ambiguousTrack := false
	for _, f := range filesFor(idx, filename, commonv1.MediaKindAlbum) {
		al, ok := idx.AlbumByName(f.Namespace, f.Spec.MediaRef.Name)
		if !ok {
			continue
		}
		if album != nil && album.UID != al.UID {
			return nil, nil, false
		}
		album = al
		tr := trackByRecording(al, f.Spec.MediaRef.Track)
		if tr == nil {
			tr = trackByFileRef(al, f.Name)
		}
		switch {
		case tr == nil:
		case track == nil:
			track = tr
		case track.RecordingID != tr.RecordingID:
			ambiguousTrack = true
		}
	}
	if ambiguousTrack {
		track = nil
	}
	return album, track, album != nil
}

// trackByFileRef is the Album's track whose fileRef names the MediaFile.
func trackByFileRef(al *catalogv1.Album, fileName string) *catalogv1.Track {
	for i := range al.Status.Tracks {
		if ref := al.Status.Tracks[i].FileRef; ref != nil && *ref == fileName {
			return &al.Status.Tracks[i]
		}
	}
	return nil
}

// albumTitles are the names an Album is known by: its title, with and
// without its disambiguation.
func albumTitles(al *catalogv1.Album) []string {
	if al.Status.Metadata == nil {
		return nil
	}
	return []string{al.Status.Metadata.Title}
}

// albumsNamed are the Albums among pool whose title normalises to title,
// within one year of year when one is given (strict: a year off by more
// is refused, as the video roots refuse it for an automatic match), by
// year tier then title then UID.
func albumsNamed(pool []*catalogv1.Album, title string, year int32, strict bool) []*catalogv1.Album {
	norm := nameNorm(title)
	type candidate struct {
		album *catalogv1.Album
		tier  int
	}
	var cands []candidate
	for _, al := range pool {
		if !nameMatches(norm, albumTitles(al)...) {
			continue
		}
		var y int32
		if md := al.Status.Metadata; md != nil {
			y = yearOf(md.ReleaseDate)
		}
		tier := yearTier(y, year)
		if !admitsTier(tier, year, strict) {
			continue
		}
		cands = append(cands, candidate{album: al, tier: tier})
	}
	slices.SortFunc(cands, func(a, b candidate) int {
		if c := cmp.Compare(a.tier, b.tier); c != 0 {
			return c
		}
		if c := cmp.Compare(albumTitle(a.album), albumTitle(b.album)); c != 0 {
			return c
		}
		return cmp.Compare(a.album.UID, b.album.UID)
	})
	out := make([]*catalogv1.Album, 0, len(cands))
	for i, c := range cands {
		if !strict || i == 0 || c.tier == cands[0].tier {
			out = append(out, c.album)
		}
	}
	return out
}

func albumTitle(al *catalogv1.Album) string {
	if al.Status.Metadata == nil {
		return ""
	}
	return al.Status.Metadata.Title
}

// albumPool is the Albums an album match searches: the albums of the
// Artists parentTitle names when it names any (design §7.3: "the artist
// resolved first narrows the albums"), else every Album.
func albumPool(idx *projection.Index, artistTitle string) []*catalogv1.Album {
	if artistTitle != "" {
		if artists := artistsNamed(idx, artistTitle); len(artists) > 0 {
			var pool []*catalogv1.Album
			for _, a := range artists {
				pool = append(pool, idx.AlbumsOf(a.UID)...)
			}
			return pool
		}
	}
	return idx.Albums()
}

// matchAlbums is type 9 on the music root (design §7.3): a "mbid://"
// release-group guid, else the file, else parentTitle (the artist) and
// title (the album) with the year within one of the release date.
func matchAlbums(idx *projection.Index, req matchRequest, manual bool) []*catalogv1.Album {
	if al, ok := albumByGuid(idx, req.Guid); ok {
		return []*catalogv1.Album{al}
	}
	fileAlbum, _, byFile := albumByFile(idx, req.Filename)
	if byFile && !manual {
		return []*catalogv1.Album{fileAlbum}
	}
	albums := unique(albumsNamed(albumPool(idx, req.ParentTitle), req.Title, req.Year, !manual), manual)
	if byFile {
		albums = leadWith(fileAlbum, albums)
	}
	return albums
}

// matchTrack is type 10 on the music root (design §7.3): the file first,
// the one certain rule for a track; else grandparentTitle (the artist) and
// parentTitle (the album) resolve the album, and parentIndex (the disc)
// with index (the track number) pick the track, else the title against the
// tracks' titles. One track or none.
func matchTrack(idx *projection.Index, req matchRequest) (*catalogv1.Album, *catalogv1.Track) {
	if al, tr, ok := albumByFile(idx, req.Filename); ok && tr != nil {
		return al, tr
	}
	albums := unique(albumsNamed(albumPool(idx, req.GrandparentTitle), req.ParentTitle, req.Year, true), false)
	if len(albums) != 1 {
		return nil, nil
	}
	al := albums[0]
	if req.Index != nil {
		if tr := trackByNumber(al, req.ParentIndex, *req.Index); tr != nil {
			return al, tr
		}
	}
	if tr := trackByTitle(al, req.Title); tr != nil {
		return al, tr
	}
	return nil, nil
}

// trackByNumber is the Album's track numbered n on disc (any disc when
// nil), nil when none or more than one is.
func trackByNumber(al *catalogv1.Album, disc *int32, n int32) *catalogv1.Track {
	var found *catalogv1.Track
	for i := range al.Status.Tracks {
		tr := &al.Status.Tracks[i]
		if tr.Number != n || (disc != nil && tr.Medium != *disc) {
			continue
		}
		if found != nil {
			return nil
		}
		found = tr
	}
	return found
}

// trackByTitle is the Album's one track whose title normalises to title,
// nil when none or several do.
func trackByTitle(al *catalogv1.Album, title string) *catalogv1.Track {
	norm := nameNorm(title)
	if norm == "" {
		return nil
	}
	var found *catalogv1.Track
	for i := range al.Status.Tracks {
		tr := &al.Status.Tracks[i]
		if !nameMatches(norm, tr.Title) {
			continue
		}
		if found != nil {
			return nil
		}
		found = tr
	}
	return found
}

// The audiobooks root: the same rules with Author (or a synthetic artist)
// for the artist, Audiobook for the album and part for the track (design
// §7.3, "Audiobooks root").

// matchAudiobooksRoot dispatches the audiobooks root's match by type.
func matchAudiobooksRoot(root rootDef, u urls, idx *projection.Index, req matchRequest, manual, includeChildren bool) []Metadata {
	switch req.Type {
	case typeArtist:
		levels := matchAuthors(idx, req, manual)
		out := make([]Metadata, len(levels))
		for i, lvl := range levels {
			out[i] = buildArtistLevel(root, u, idx, lvl, includeChildren)
		}
		return out
	case typeAlbum:
		abs := matchAudiobooks(idx, req, manual)
		out := make([]Metadata, len(abs))
		for i, ab := range abs {
			out[i] = buildAudiobookMetadata(root, u, idx, ab, includeChildren)
		}
		return out
	case typeTrack:
		ab, part := matchPart(idx, req)
		if part == 0 {
			return nil
		}
		return []Metadata{buildPartMetadata(root, u, idx, ab, part)}
	default:
		return nil
	}
}

// authorsNamed are the artist levels whose name normalises to title: the
// Authors by name or sort name, and the synthetic artists by their
// author's name, by title then key.
func authorsNamed(idx *projection.Index, title string) []artistLevel {
	norm := nameNorm(title)
	var out []artistLevel
	for _, a := range idx.Authors() {
		if md := a.Status.Metadata; md != nil && nameMatches(norm, md.Name, md.SortName) {
			out = append(out, authorLevelOf(a))
		}
	}
	for _, s := range syntheticAuthorsOf(idx) {
		if nameMatches(norm, s.name) {
			out = append(out, syntheticLevelOf(s))
		}
	}
	slices.SortFunc(out, func(a, b artistLevel) int {
		if c := cmp.Compare(a.title, b.title); c != 0 {
			return c
		}
		return cmp.Compare(a.key, b.key)
	})
	return out
}

// matchAuthors is type 8 on the audiobooks root: an "asin://" guid names an
// Audiobook, whose author answers; else the file's Audiobook's author; else
// the title against the Authors' names and the synthetic artists'.
func matchAuthors(idx *projection.Index, req matchRequest, manual bool) []artistLevel {
	if ab, ok := audiobookByGuid(idx, req.Guid); ok {
		return []artistLevel{audiobookArtistLevel(idx, ab)}
	}
	var fileLevel *artistLevel
	if ab, _, ok := audiobookByFile(idx, req.Filename); ok {
		lvl := audiobookArtistLevel(idx, ab)
		if !manual {
			return []artistLevel{lvl}
		}
		fileLevel = &lvl
	}
	levels := unique(authorsNamed(idx, req.Title), manual)
	if fileLevel != nil {
		out := []artistLevel{*fileLevel}
		for _, lvl := range levels {
			if lvl.key != fileLevel.key {
				out = append(out, lvl)
			}
		}
		levels = out
	}
	return levels
}

// audiobookByGuid resolves a match request's "asin://<ASIN>" guid.
func audiobookByGuid(idx *projection.Index, guid string) (*catalogv1.Audiobook, bool) {
	scheme, id, ok := splitGuid(guid)
	if !ok || !strings.EqualFold(scheme, "asin") {
		return nil, false
	}
	return idx.ByASIN(id)
}

// audiobookByFile is rule 0 for the audiobooks root: the one Audiobook the
// named file is a part of, and which part it is -- its 1-based position in
// status.fileRefs, else its spec.partIndex, else 0 for a file the
// Audiobook does not place.
func audiobookByFile(idx *projection.Index, filename string) (*catalogv1.Audiobook, int32, bool) {
	var book *catalogv1.Audiobook
	var part int32
	for _, f := range filesFor(idx, filename, commonv1.MediaKindAudiobook) {
		ab, ok := idx.AudiobookByName(f.Namespace, f.Spec.MediaRef.Name)
		if !ok {
			continue
		}
		if book != nil && book.UID != ab.UID {
			return nil, 0, false
		}
		book = ab
		if i := slices.Index(ab.Status.FileRefs, f.Name); i >= 0 {
			part = int32(i + 1)
		} else if f.Spec.PartIndex > 0 && int(f.Spec.PartIndex) <= partCount(ab) {
			part = f.Spec.PartIndex
		}
	}
	return book, part, book != nil
}

// audiobookTitles are the names an Audiobook is known by: its title, and
// its title with its subtitle.
func audiobookTitles(ab *catalogv1.Audiobook) []string {
	md := ab.Status.Metadata
	if md == nil {
		return nil
	}
	names := []string{md.Title}
	if md.Subtitle != "" {
		names = append(names, md.Title+": "+md.Subtitle)
	}
	return names
}

// audiobooksNamed is [albumsNamed] for Audiobooks.
func audiobooksNamed(pool []*catalogv1.Audiobook, title string, year int32, strict bool) []*catalogv1.Audiobook {
	norm := nameNorm(title)
	type candidate struct {
		book *catalogv1.Audiobook
		tier int
	}
	var cands []candidate
	for _, ab := range pool {
		if !nameMatches(norm, audiobookTitles(ab)...) {
			continue
		}
		var y int32
		if md := ab.Status.Metadata; md != nil {
			y = yearOf(md.ReleaseDate)
		}
		tier := yearTier(y, year)
		if !admitsTier(tier, year, strict) {
			continue
		}
		cands = append(cands, candidate{book: ab, tier: tier})
	}
	slices.SortFunc(cands, func(a, b candidate) int {
		if c := cmp.Compare(a.tier, b.tier); c != 0 {
			return c
		}
		if c := cmp.Compare(audiobookTitle(a.book), audiobookTitle(b.book)); c != 0 {
			return c
		}
		return cmp.Compare(a.book.UID, b.book.UID)
	})
	out := make([]*catalogv1.Audiobook, 0, len(cands))
	for i, c := range cands {
		if !strict || i == 0 || c.tier == cands[0].tier {
			out = append(out, c.book)
		}
	}
	return out
}

func audiobookTitle(ab *catalogv1.Audiobook) string {
	if ab.Status.Metadata == nil {
		return ""
	}
	return ab.Status.Metadata.Title
}

// audiobookPool is the Audiobooks an audiobook match searches: those of the
// artist levels parentTitle names when it names any, else every Audiobook.
func audiobookPool(idx *projection.Index, authorTitle string) []*catalogv1.Audiobook {
	if authorTitle != "" {
		if levels := authorsNamed(idx, authorTitle); len(levels) > 0 {
			var pool []*catalogv1.Audiobook
			for _, lvl := range levels {
				switch {
				case lvl.author != nil:
					pool = append(pool, idx.AudiobooksOf(lvl.author.UID)...)
				case lvl.synthetic != nil:
					pool = append(pool, lvl.synthetic.audiobooks...)
				}
			}
			return pool
		}
	}
	return idx.Audiobooks()
}

// matchAudiobooks is type 9 on the audiobooks root: an "asin://" guid, else
// the file, else parentTitle (the author) and title with the year within
// one of the release date.
func matchAudiobooks(idx *projection.Index, req matchRequest, manual bool) []*catalogv1.Audiobook {
	if ab, ok := audiobookByGuid(idx, req.Guid); ok {
		return []*catalogv1.Audiobook{ab}
	}
	fileBook, _, byFile := audiobookByFile(idx, req.Filename)
	if byFile && !manual {
		return []*catalogv1.Audiobook{fileBook}
	}
	abs := unique(audiobooksNamed(audiobookPool(idx, req.ParentTitle), req.Title, req.Year, !manual), manual)
	if byFile {
		abs = leadWith(fileBook, abs)
	}
	return abs
}

// matchPart is type 10 on the audiobooks root: the file first, else
// grandparentTitle (the author) and parentTitle (the audiobook) resolve the
// Audiobook and index picks the part, else the title against the parts'
// titles. One part or none (0).
func matchPart(idx *projection.Index, req matchRequest) (*catalogv1.Audiobook, int32) {
	if ab, part, ok := audiobookByFile(idx, req.Filename); ok && part > 0 {
		return ab, part
	}
	abs := unique(audiobooksNamed(audiobookPool(idx, req.GrandparentTitle), req.ParentTitle, req.Year, true), false)
	if len(abs) != 1 {
		return nil, 0
	}
	ab := abs[0]
	if req.Index != nil && *req.Index >= 1 && int(*req.Index) <= partCount(ab) {
		return ab, *req.Index
	}
	norm := nameNorm(req.Title)
	var found int32
	for i := range partCount(ab) {
		if !nameMatches(norm, partTitle(ab, int32(i+1))) {
			continue
		}
		if found != 0 {
			return nil, 0
		}
		found = int32(i + 1)
	}
	if found == 0 {
		return nil, 0
	}
	return ab, found
}
