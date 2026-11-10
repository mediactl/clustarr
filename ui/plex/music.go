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
	"slices"

	"k8s.io/apimachinery/pkg/types"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/projection"
)

// The music and audiobook roots (docs/superpowers/specs/
// 2026-11-10-non-video-libraries-design.md §7) serve Plex's music
// hierarchy, artist -> album -> track, over two catalog shapes: the music
// root's Artist -> Album -> Album.status.tracks[], and the audiobooks root's
// Author (or a synthetic artist for an Audiobook with no authorRef) ->
// Audiobook -> its parts (status.fileRefs, in play order). Every shape
// Plex has not published is inferred from Plex's own music items and the
// video roots' conventions, and marked so where it is built.

// syntheticAuthor is the audiobooks root's artist for the Audiobooks with
// no authorRef (design §7.2): keyed by [SyntheticArtistKey] of their first
// credited author's name, with no object, no summary and no artwork.
type syntheticAuthor struct {
	key        string
	name       string
	audiobooks []*catalogv1.Audiobook
}

// title is the synthetic artist's title: the author's name, or a label for
// an Audiobook crediting nobody.
func (s syntheticAuthor) title() string {
	if s.name == "" {
		return "Unknown Author"
	}
	return s.name
}

// syntheticAuthorsOf lists the synthetic artists idx holds, by key.
func syntheticAuthorsOf(idx *projection.Index) map[string]syntheticAuthor {
	out := map[string]syntheticAuthor{}
	for name, abs := range idx.UnlinkedAudiobooks() {
		key := SyntheticArtistKey(name)
		out[key] = syntheticAuthor{key: key, name: name, audiobooks: abs}
	}
	return out
}

// syntheticAuthorOf is the synthetic artist an authorless Audiobook sits
// under: its first credited author's name, and every unlinked Audiobook of
// that name.
func syntheticAuthorOf(idx *projection.Index, ab *catalogv1.Audiobook) syntheticAuthor {
	name := projection.FirstAuthorName(ab)
	return syntheticAuthor{key: SyntheticArtistKey(name), name: name, audiobooks: idx.UnlinkedAudiobooks()[name]}
}

// musicItem is what a music ratingKey resolves to on a music root
// ([resolveMusicItem]): exactly one of an Artist, an Album, a track of an
// Album (album and track set), an Author, a synthetic artist, an Audiobook
// or a part of one (audiobook and part set, part 1-based).
type musicItem struct {
	artist    *catalogv1.Artist
	album     *catalogv1.Album
	track     *catalogv1.Track
	author    *catalogv1.Author
	synthetic *syntheticAuthor
	audiobook *catalogv1.Audiobook
	part      int32
}

// resolveMusicItem resolves a parsed key on a music root to the item it
// names, false for a key the root does not serve: the music root serves
// Artists, Albums and tracks, the audiobooks root Authors, synthetic
// artists, Audiobooks and parts, and neither a season or a Plex id's
// item. A track key whose recording the Album does not list, or a part
// past the Audiobook's files, is not found either.
func resolveMusicItem(root rootDef, idx *projection.Index, k Key) (musicItem, bool) {
	if !root.music || k.IsSeason {
		return musicItem{}, false
	}
	if root.audiobooks {
		switch {
		case k.Track != "":
			return musicItem{}, false
		case k.Synthetic != "":
			s, ok := syntheticAuthorsOf(idx)[k.Synthetic]
			if !ok {
				return musicItem{}, false
			}
			return musicItem{synthetic: &s}, true
		case k.IsPart:
			ab, ok := idx.AudiobookByUID(k.UID)
			if !ok || k.Part < 1 || int(k.Part) > len(ab.Status.FileRefs) {
				return musicItem{}, false
			}
			return musicItem{audiobook: ab, part: k.Part}, true
		}
		if a, ok := idx.AuthorByUID(k.UID); ok {
			return musicItem{author: a}, true
		}
		if ab, ok := idx.AudiobookByUID(k.UID); ok {
			return musicItem{audiobook: ab}, true
		}
		return musicItem{}, false
	}
	switch {
	case k.Synthetic != "", k.IsPart:
		return musicItem{}, false
	case k.Track != "":
		al, ok := idx.AlbumByUID(k.UID)
		if !ok {
			return musicItem{}, false
		}
		tr := trackByRecording(al, k.Track)
		if tr == nil {
			return musicItem{}, false
		}
		return musicItem{album: al, track: tr}, true
	}
	if a, ok := idx.ArtistByUID(k.UID); ok {
		return musicItem{artist: a}, true
	}
	if al, ok := idx.AlbumByUID(k.UID); ok {
		return musicItem{album: al}, true
	}
	return musicItem{}, false
}

// artwork is the item's [artworkFor] (design §7.4): an artist's, an album's
// (a track's is its album's), an author's, an audiobook's (a part's is its
// audiobook's); a synthetic artist has none. sourced and contentType are
// the caller's to set ([withURLs]).
func (it musicItem) artwork() artworkFor {
	switch {
	case it.artist != nil:
		return artistArtwork(it.artist)
	case it.album != nil:
		return albumArtwork(it.album)
	case it.author != nil:
		return authorArtwork(it.author)
	case it.audiobook != nil:
		return audiobookArtwork(it.audiobook)
	default:
		return artworkFor{}
	}
}

// metadata builds the item's Metadata object (metadata_music.go).
func (it musicItem) metadata(root rootDef, u urls, idx *projection.Index, includeChildren bool) Metadata {
	switch {
	case it.artist != nil:
		return buildArtistLevel(root, u, idx, artistLevelOf(it.artist), includeChildren)
	case it.track != nil:
		return buildTrackMetadata(root, u, idx, it.album, it.track)
	case it.album != nil:
		return buildAlbumMetadata(root, u, idx, it.album, includeChildren)
	case it.author != nil:
		return buildArtistLevel(root, u, idx, authorLevelOf(it.author), includeChildren)
	case it.synthetic != nil:
		return buildArtistLevel(root, u, idx, syntheticLevelOf(*it.synthetic), includeChildren)
	case it.part != 0:
		return buildPartMetadata(root, u, idx, it.audiobook, it.part)
	default:
		return buildAudiobookMetadata(root, u, idx, it.audiobook, includeChildren)
	}
}

// artistArtwork is an Artist's [artworkFor]: fanart.tv stores its
// artistthumb as the thumb type, which stands in for the poster.
func artistArtwork(a *catalogv1.Artist) artworkFor {
	return artworkFor{kind: commonv1.MediaKindArtist, uid: a.UID, artwork: a.Status.Artwork, thumbAsPoster: true}
}

func albumArtwork(al *catalogv1.Album) artworkFor {
	return artworkFor{kind: commonv1.MediaKindAlbum, uid: al.UID, artwork: al.Status.Artwork}
}

func authorArtwork(a *catalogv1.Author) artworkFor {
	return artworkFor{kind: commonv1.MediaKindAuthor, uid: a.UID, artwork: a.Status.Artwork, thumbAsPoster: true}
}

func audiobookArtwork(ab *catalogv1.Audiobook) artworkFor {
	return artworkFor{kind: commonv1.MediaKindAudiobook, uid: ab.UID, artwork: ab.Status.Artwork}
}

// withURLs gives af the request's URL choices: a match result's source
// URLs ([urls.sourced]) and the content-type check that offers a WebP as
// JPEG.
func withURLs(af artworkFor, u urls) artworkFor {
	af.sourced, af.contentType = u.sourced, u.artType
	return af
}

// trackByRecording is the Album's track with the given recording MBID, nil
// when the selected release does not list it.
func trackByRecording(al *catalogv1.Album, recordingID string) *catalogv1.Track {
	for i := range al.Status.Tracks {
		if al.Status.Tracks[i].RecordingID == recordingID {
			return &al.Status.Tracks[i]
		}
	}
	return nil
}

// sortedTracks is a copy of the Album's tracks in listing order: medium,
// then number, then absolute number, then recording id for two tracks
// numbered alike. Every track is listed, a file or not (design §7.4, Q4):
// Plex matches the file it has to the track it names, as the tv root lists
// every Episode.
func sortedTracks(al *catalogv1.Album) []catalogv1.Track {
	tracks := append([]catalogv1.Track(nil), al.Status.Tracks...)
	slices.SortFunc(tracks, func(a, b catalogv1.Track) int {
		if c := cmp.Compare(a.Medium, b.Medium); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Number, b.Number); c != 0 {
			return c
		}
		if c := cmp.Compare(a.AbsoluteNumber, b.AbsoluteNumber); c != 0 {
			return c
		}
		return cmp.Compare(a.RecordingID, b.RecordingID)
	})
	return tracks
}

// partCount is how many parts an Audiobook has: its files in play order
// (design D11). An Audiobook without files has no parts to list.
func partCount(ab *catalogv1.Audiobook) int {
	return len(ab.Status.FileRefs)
}

// musicChildrenOf is [handler.childrenOf] on a music root: an artist's
// albums, an album's tracks, an author's or synthetic artist's audiobooks,
// an audiobook's parts. A track or a part has no children.
func (h *handler) musicChildrenOf(root rootDef, u urls, idx *projection.Index, k Key) ([]Metadata, bool) {
	item, ok := resolveMusicItem(root, idx, k)
	if !ok {
		return nil, false
	}
	switch {
	case item.artist != nil:
		return albumChildren(root, u, idx, idx.AlbumsOf(item.artist.UID)), true
	case item.track != nil, item.part != 0:
		return nil, false
	case item.album != nil:
		return trackChildren(root, u, idx, item.album), true
	case item.author != nil:
		return audiobookChildren(root, u, idx, idx.AudiobooksOf(item.author.UID)), true
	case item.synthetic != nil:
		return audiobookChildren(root, u, idx, item.synthetic.audiobooks), true
	default:
		return partChildren(root, u, idx, item.audiobook), true
	}
}

// albumChildren builds one Metadata per album, in the index's order
// (release date, then title), with no nested Children of its own.
func albumChildren(root rootDef, u urls, idx *projection.Index, albums []*catalogv1.Album) []Metadata {
	out := make([]Metadata, len(albums))
	for i, al := range albums {
		out[i] = buildAlbumMetadata(root, u, idx, al, false)
	}
	return out
}

// trackChildren builds one Metadata per track of the album, every track,
// a file or not ([sortedTracks]).
func trackChildren(root rootDef, u urls, idx *projection.Index, al *catalogv1.Album) []Metadata {
	tracks := sortedTracks(al)
	out := make([]Metadata, len(tracks))
	for i := range tracks {
		out[i] = buildTrackMetadata(root, u, idx, al, &tracks[i])
	}
	return out
}

// audiobookChildren is [albumChildren] for an author's audiobooks.
func audiobookChildren(root rootDef, u urls, idx *projection.Index, abs []*catalogv1.Audiobook) []Metadata {
	out := make([]Metadata, len(abs))
	for i, ab := range abs {
		out[i] = buildAudiobookMetadata(root, u, idx, ab, false)
	}
	return out
}

// partChildren is [trackChildren] for an audiobook's parts, 1 to
// [partCount].
func partChildren(root rootDef, u urls, idx *projection.Index, ab *catalogv1.Audiobook) []Metadata {
	n := partCount(ab)
	out := make([]Metadata, n)
	for i := range n {
		out[i] = buildPartMetadata(root, u, idx, ab, int32(i+1))
	}
	return out
}

// childrenContainer wraps a listing as a Metadata object's own Children
// block (research §5.2), never paged.
func childrenContainer(items []Metadata) *ChildrenContainer {
	return &ChildrenContainer{Size: len(items), Metadata: items}
}

// musicGrandchildren is [handler.handleGrandchildren] on a music root: an
// artist's tracks across every album, or an author's parts across every
// audiobook, in the albums' order. The page is built lazily (design §7.4):
// the total is counted from the albums' track lists and only the window's
// tracks become Metadata, never every track at once -- an artist's run to
// thousands. An album's or audiobook's key answers false (404), as a
// season's does; its own /children already serves the equivalent request.
func (h *handler) musicGrandchildren(root rootDef, u urls, idx *projection.Index, k Key, p pageRequest) (window []Metadata, total int, ok bool) {
	item, found := resolveMusicItem(root, idx, k)
	if !found {
		return nil, 0, false
	}
	switch {
	case item.artist != nil:
		sorted := map[types.UID][]catalogv1.Track{}
		tracksOf := func(al *catalogv1.Album) []catalogv1.Track {
			if ts, ok := sorted[al.UID]; ok {
				return ts
			}
			ts := sortedTracks(al)
			sorted[al.UID] = ts
			return ts
		}
		window, total = pagedListing(idx.AlbumsOf(item.artist.UID), p,
			func(al *catalogv1.Album) int { return len(al.Status.Tracks) },
			func(al *catalogv1.Album, i int) Metadata {
				return buildTrackMetadata(root, u, idx, al, &tracksOf(al)[i])
			})
		return window, total, true
	case item.author != nil, item.synthetic != nil:
		abs := item.synthetic.booksOr(idx, item.author)
		window, total = pagedListing(abs, p, partCount,
			func(ab *catalogv1.Audiobook, i int) Metadata { return buildPartMetadata(root, u, idx, ab, int32(i+1)) })
		return window, total, true
	default:
		return nil, 0, false
	}
}

// booksOr is the synthetic artist's audiobooks, or the Author's when the
// item is an Author (s nil).
func (s *syntheticAuthor) booksOr(idx *projection.Index, author *catalogv1.Author) []*catalogv1.Audiobook {
	if s != nil {
		return s.audiobooks
	}
	return idx.AudiobooksOf(author.UID)
}

// pagedListing builds page p of a listing made of groups -- an artist's
// albums, each of count(g) items -- building only the window's items with
// build(g, i), and reports the listing's total. A group the window skips
// costs its count alone.
func pagedListing[G any](groups []G, p pageRequest, count func(G) int, build func(G, int) Metadata) ([]Metadata, int) {
	total := 0
	for _, g := range groups {
		total += count(g)
	}
	window := []Metadata{}
	pos := 0
	end := p.start + p.size
	for _, g := range groups {
		n := count(g)
		if pos+n <= p.start {
			pos += n
			continue
		}
		for i := range n {
			if pos >= end {
				return window, total
			}
			if pos >= p.start {
				window = append(window, build(g, i))
			}
			pos++
		}
	}
	return window, total
}
