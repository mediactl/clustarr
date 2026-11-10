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
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	"github.com/mediactl/clustarr/ui/projection"
)

// The music Metadata shapes (design §7.4) are inferred: Plex publishes no
// provider protocol for music, so the fields follow what
// metadata.provider.plex.tv answers for an artist, album and track and the
// video roots' own conventions. Every "Required" field of the video shapes
// is populated here too (an empty originallyAvailableAt when no date is
// known), and anything with no clustarr source is left out, never blanked.

// artistLevel is the artist level of a music hierarchy as every builder
// reads it -- an Artist, an Author (the audiobooks root maps an author as
// the artist) or a synthetic artist -- so an album's parent fields and a
// track's grandparent fields are built one way.
type artistLevel struct {
	key      string
	title    string
	sortName string
	summary  string
	genres   []string
	country  string
	guids    []GuidRef
	af       artworkFor

	// Exactly one of these is set, for the level's children.
	artist    *catalogv1.Artist
	author    *catalogv1.Author
	synthetic *syntheticAuthor
}

// artistLevelOf is an Artist as the artist level.
func artistLevelOf(a *catalogv1.Artist) artistLevel {
	lvl := artistLevel{key: RatingKey(a.UID), af: artistArtwork(a), artist: a}
	if md := a.Status.Metadata; md != nil {
		lvl.title, lvl.sortName, lvl.summary = md.Name, md.SortName, md.Overview
		lvl.genres, lvl.country = md.Genres, md.Country
	}
	if a.Spec.MusicBrainzID != "" {
		// mbid:// is the scheme Plex's own music items carry (inferred).
		lvl.guids = []GuidRef{{ID: "mbid://" + a.Spec.MusicBrainzID}}
	}
	return lvl
}

// authorLevelOf is an Author as the audiobooks root's artist (design §7.4:
// "author as artist"). An Open Library id has no Plex scheme, so no Guid.
func authorLevelOf(a *catalogv1.Author) artistLevel {
	lvl := artistLevel{key: RatingKey(a.UID), af: authorArtwork(a), author: a}
	if md := a.Status.Metadata; md != nil {
		lvl.title, lvl.sortName, lvl.summary, lvl.genres = md.Name, md.SortName, md.Overview, md.Genres
	}
	return lvl
}

// syntheticLevelOf is a synthetic artist as the artist level: the author's
// name and nothing else (design §7.4: "without an Author object,
// Metadata.authors[0].name and no summary").
func syntheticLevelOf(s syntheticAuthor) artistLevel {
	return artistLevel{key: s.key, title: s.title(), synthetic: &s}
}

// children is the level's albums or audiobooks as a Children block.
func (lvl artistLevel) children(root rootDef, u urls, idx *projection.Index) []Metadata {
	switch {
	case lvl.artist != nil:
		return albumChildren(root, u, idx, idx.AlbumsOf(lvl.artist.UID))
	case lvl.author != nil:
		return audiobookChildren(root, u, idx, idx.AudiobooksOf(lvl.author.UID))
	case lvl.synthetic != nil:
		return audiobookChildren(root, u, idx, lvl.synthetic.audiobooks)
	default:
		return nil
	}
}

// buildArtistLevel builds an artist's Metadata object (design §7.4,
// "artist", type "artist"): ratingKey, key, guid, title, titleSort
// (sortName), summary (overview), thumb/art and Image[] (coverPoster from
// the poster else the thumb, background from fanart, clearLogo), Genre[],
// Country[] (metadata.country, the ISO code as recorded), Guid[] mbid, and
// Children of albums when includeChildren. Role[] is not emitted (Plex
// shapes an artist's members as Similar); Similar[] rides the extended
// document ([handler.enrichExtended]).
func buildArtistLevel(root rootDef, u urls, idx *projection.Index, lvl artistLevel, includeChildren bool) Metadata {
	md := Metadata{
		RatingKey: lvl.key,
		Key:       metadataKey(lvl.key, true),
		Guid:      GUID(root.identifier, metadataTypeArtist, lvl.key),
		Type:      metadataTypeArtist,
		Title:     lvl.title,
		TitleSort: lvl.sortName,
		Summary:   lvl.summary,
		Genre:     genres(lvl.genres),
		Guids:     lvl.guids,
	}
	if lvl.country != "" {
		md.Country = []Tag{{Tag: lvl.country}}
	}
	af := withURLs(lvl.af, u)
	md.Thumb, md.Art = thumbAndArt(u.external, af)
	md.Image = withAlt(buildImages(u.external, af), md.Title)
	if includeChildren {
		md.Children = childrenContainer(lvl.children(root, u, idx))
	}
	return md
}

// setParent writes the artist level into an album's parent fields.
func setParent(md *Metadata, root rootDef, u urls, lvl artistLevel) {
	md.ParentRatingKey = lvl.key
	md.ParentKey = metadataKey(lvl.key, true)
	md.ParentGuid = GUID(root.identifier, metadataTypeArtist, lvl.key)
	md.ParentType = metadataTypeArtist
	md.ParentTitle = lvl.title
	md.ParentThumb, md.ParentArt = thumbAndArt(u.external, withURLs(lvl.af, u))
}

// setGrandparent writes the artist level into a track's grandparent
// fields.
func setGrandparent(md *Metadata, root rootDef, u urls, lvl artistLevel) {
	md.GrandparentRatingKey = lvl.key
	md.GrandparentKey = metadataKey(lvl.key, true)
	md.GrandparentGuid = GUID(root.identifier, metadataTypeArtist, lvl.key)
	md.GrandparentType = metadataTypeArtist
	md.GrandparentTitle = lvl.title
	md.GrandparentThumb, md.GrandparentArt = thumbAndArt(u.external, withURLs(lvl.af, u))
}

// albumArtistLevel is an Album's artist level, false for an Album whose
// spec.artistRef names no indexed Artist.
func albumArtistLevel(idx *projection.Index, al *catalogv1.Album) (artistLevel, bool) {
	a, ok := idx.ArtistOfAlbum(al.UID)
	if !ok {
		return artistLevel{}, false
	}
	return artistLevelOf(a), true
}

// audiobookArtistLevel is an Audiobook's artist level: its Author through
// spec.authorRef, else the synthetic artist of its first credited author
// (design §7.2). Every Audiobook has one.
func audiobookArtistLevel(idx *projection.Index, ab *catalogv1.Audiobook) artistLevel {
	if a, ok := idx.AuthorOfAudiobook(ab.UID); ok {
		return authorLevelOf(a)
	}
	return syntheticLevelOf(syntheticAuthorOf(idx, ab))
}

// dateString is a date as Plex's originallyAvailableAt spells it, "" for
// none.
func dateString(t *metav1.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format("2006-01-02")
}

// yearOf is a date's year, 0 for none; always UTC first (CLAUDE.md: a
// metav1.Time comes back in the replica's zone).
func yearOf(t *metav1.Time) int32 {
	if t == nil {
		return 0
	}
	return int32(t.UTC().Year())
}

// buildAlbumMetadata builds an album's Metadata object (design §7.4,
// "album", type "album"): title, titleSort, summary (overview), year and
// originallyAvailableAt (releaseDate), studio (label), thumb (cover),
// Image[], Genre[], Guid[] mbid (the release group), the artist as
// parent*, Rating[] from the MusicBrainz score (badge-less), and Children
// of tracks when includeChildren. It carries no index: Plex numbers no
// album.
func buildAlbumMetadata(root rootDef, u urls, idx *projection.Index, al *catalogv1.Album, includeChildren bool) Metadata {
	key := RatingKey(al.UID)
	md := Metadata{
		RatingKey: key,
		Key:       metadataKey(key, true),
		Guid:      GUID(root.identifier, metadataTypeAlbum, key),
		Type:      metadataTypeAlbum,
	}
	if meta := al.Status.Metadata; meta != nil {
		md.Title = meta.Title
		md.TitleSort = meta.Title
		md.Summary = meta.Overview
		md.Year = yearOf(meta.ReleaseDate)
		md.OriginallyAvailableAt = dateString(meta.ReleaseDate)
		md.Studio = meta.Label
		md.Genre = genres(meta.Genres)
		md.Rating = musicRatings(meta.Ratings)
	}
	if al.Spec.ReleaseGroupID != "" {
		md.Guids = []GuidRef{{ID: "mbid://" + al.Spec.ReleaseGroupID}}
	}
	af := withURLs(albumArtwork(al), u)
	md.Thumb, md.Art = thumbAndArt(u.external, af)
	md.Image = withAlt(buildImages(u.external, af), md.Title)
	if lvl, ok := albumArtistLevel(idx, al); ok {
		setParent(&md, root, u, lvl)
	}
	if includeChildren {
		md.Children = childrenContainer(trackChildren(root, u, idx, al))
	}
	return md
}

// buildTrackMetadata builds a track's Metadata object (design §7.4,
// "track", type "track"): title, index (track number), parentIndex
// (disc), duration (ms), Guid[] mbid (the recording), the album as
// parent* and the artist as grandparent*, and the album's cover as its
// thumb. originalTitle -- the track's own artist credit when it differs
// from the album artist -- waits on a credit per track, which
// Album.status.tracks does not record, so none is emitted. A track without
// a file is built like any other (Q4).
func buildTrackMetadata(root rootDef, u urls, idx *projection.Index, al *catalogv1.Album, tr *catalogv1.Track) Metadata {
	albumKey := RatingKey(al.UID)
	key := TrackKey(al.UID, tr.RecordingID)
	af := withURLs(albumArtwork(al), u)
	thumb, art := thumbAndArt(u.external, af)
	md := Metadata{
		RatingKey: key,
		Key:       metadataKey(key, false),
		Guid:      GUID(root.identifier, metadataTypeTrack, key),
		Type:      metadataTypeTrack,
		Title:     tr.Title,
		Thumb:     thumb,
		Art:       art,

		ParentRatingKey: albumKey,
		ParentKey:       metadataKey(albumKey, true),
		ParentGuid:      GUID(root.identifier, metadataTypeAlbum, albumKey),
		ParentType:      metadataTypeAlbum,
		ParentThumb:     thumb,
		ParentArt:       art,
	}
	if meta := al.Status.Metadata; meta != nil {
		md.ParentTitle = meta.Title
		md.Year = yearOf(meta.ReleaseDate)
		md.OriginallyAvailableAt = dateString(meta.ReleaseDate)
	}
	if tr.Number > 0 {
		md.Index = int32ptr(tr.Number)
	}
	if tr.Medium > 0 {
		md.ParentIndex = int32ptr(tr.Medium)
	}
	if tr.DurationMs > 0 {
		md.Duration = int64(tr.DurationMs)
	}
	if tr.RecordingID != "" {
		md.Guids = []GuidRef{{ID: "mbid://" + tr.RecordingID}}
	}
	if len(md.Image) == 0 {
		md.Image = withAlt(buildImages(u.external, af), md.Title)
	}
	if lvl, ok := albumArtistLevel(idx, al); ok {
		setGrandparent(&md, root, u, lvl)
	}
	return md
}

// buildAudiobookMetadata builds an audiobook as an album (design §7.4):
// title, titleSort, summary (overview), year and originallyAvailableAt
// (releaseDate), studio (publisher), duration (runtimeMinutes), Genre[],
// Role[] the narrators as "Narrator", Writer[] the authors, Guid[] asin://
// and isbn:// when known, Rating[] from the Audnexus score, the author (or
// synthetic artist) as parent*, and Children of parts when
// includeChildren.
func buildAudiobookMetadata(root rootDef, u urls, idx *projection.Index, ab *catalogv1.Audiobook, includeChildren bool) Metadata {
	key := RatingKey(ab.UID)
	md := Metadata{
		RatingKey: key,
		Key:       metadataKey(key, true),
		Guid:      GUID(root.identifier, metadataTypeAlbum, key),
		Type:      metadataTypeAlbum,
	}
	isbn := ""
	if meta := ab.Status.Metadata; meta != nil {
		md.Title = meta.Title
		md.TitleSort = meta.Title
		md.Summary = meta.Overview
		md.Year = yearOf(meta.ReleaseDate)
		md.OriginallyAvailableAt = dateString(meta.ReleaseDate)
		md.Studio = meta.Publisher
		if meta.RuntimeMinutes > 0 {
			md.Duration = int64(meta.RuntimeMinutes) * 60000
		}
		md.Genre = genres(meta.Genres)
		md.Rating = musicRatings(meta.Ratings)
		md.IsAdult = meta.IsAdult
		for i, n := range meta.Narrators {
			md.Role = append(md.Role, PersonTag{Tag: n, Role: "Narrator", Order: int32(i)})
		}
		for _, a := range meta.Authors {
			md.Writer = append(md.Writer, PersonTag{Tag: a.Name, Role: "Author"})
		}
		isbn = meta.ISBN
		if isbn == "" {
			isbn = meta.ExternalIDs["isbn13"]
		}
	}
	if ab.Spec.ASIN != "" {
		md.Guids = append(md.Guids, GuidRef{ID: "asin://" + ab.Spec.ASIN})
	}
	if isbn != "" {
		md.Guids = append(md.Guids, GuidRef{ID: "isbn://" + isbn})
	}
	af := withURLs(audiobookArtwork(ab), u)
	md.Thumb, md.Art = thumbAndArt(u.external, af)
	md.Image = withAlt(buildImages(u.external, af), md.Title)
	setParent(&md, root, u, audiobookArtistLevel(idx, ab))
	if includeChildren {
		md.Children = childrenContainer(partChildren(root, u, idx, ab))
	}
	return md
}

// partTitle is a part's title (design §7.4): the chapter's title when the
// parts and chapters align one to one, else "Part N".
func partTitle(ab *catalogv1.Audiobook, part int32) string {
	if meta := ab.Status.Metadata; meta != nil && len(meta.Chapters) == partCount(ab) && part >= 1 && int(part) <= len(meta.Chapters) {
		if t := meta.Chapters[part-1].Title; t != "" {
			return t
		}
	}
	return "Part " + strconv.Itoa(int(part))
}

// partDuration is a part's duration in milliseconds from its file's probe,
// 0 when the file is unknown or unprobed.
func partDuration(idx *projection.Index, ab *catalogv1.Audiobook, part int32) int64 {
	if part < 1 || int(part) > partCount(ab) {
		return 0
	}
	f, ok := idx.MediaFileByName(ab.Namespace, ab.Status.FileRefs[part-1])
	if !ok || f.Status.MediaInfo == nil {
		return 0
	}
	return f.Status.MediaInfo.RuntimeMillis
}

// buildPartMetadata builds an audiobook part as a track (design §7.4): title
// ([partTitle]), index (the part number), duration from the part's probe,
// the audiobook as parent*, the author as grandparent*, and the
// audiobook's cover as its thumb.
func buildPartMetadata(root rootDef, u urls, idx *projection.Index, ab *catalogv1.Audiobook, part int32) Metadata {
	bookKey := RatingKey(ab.UID)
	key := PartKey(ab.UID, part)
	af := withURLs(audiobookArtwork(ab), u)
	thumb, art := thumbAndArt(u.external, af)
	md := Metadata{
		RatingKey: key,
		Key:       metadataKey(key, false),
		Guid:      GUID(root.identifier, metadataTypeTrack, key),
		Type:      metadataTypeTrack,
		Title:     partTitle(ab, part),
		Thumb:     thumb,
		Art:       art,
		Index:     int32ptr(part),

		ParentRatingKey: bookKey,
		ParentKey:       metadataKey(bookKey, true),
		ParentGuid:      GUID(root.identifier, metadataTypeAlbum, bookKey),
		ParentType:      metadataTypeAlbum,
		ParentThumb:     thumb,
		ParentArt:       art,
	}
	if meta := ab.Status.Metadata; meta != nil {
		md.ParentTitle = meta.Title
		md.Year = yearOf(meta.ReleaseDate)
		md.OriginallyAvailableAt = dateString(meta.ReleaseDate)
	}
	md.Duration = partDuration(idx, ab, part)
	md.Image = withAlt(buildImages(u.external, af), md.Title)
	setGrandparent(&md, root, u, audiobookArtistLevel(idx, ab))
	return md
}
