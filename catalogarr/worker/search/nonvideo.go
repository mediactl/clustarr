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

package search

import (
	"context"
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/decision"
)

// The non-video half of the snapshot: an album, a book, an audiobook or a
// comic issue, each read with the container it belongs to (the Artist, the
// Author, the Comic), because that is where its creator's name -- and, when
// the item does not override it, its QualityProfile -- lives.
//
// None of these has an id an indexer takes, so each is searched by text
// (BuildSearchRequest's "<creator> <title>" and "<series> <issue>") and
// identified by what a release's title names (pkg/decision's
// identity_nonvideo.go). Every year is read in UTC: metav1.Time decodes into
// the local zone, and a year read locally is wrong by one near New Year
// (CLAUDE.md).

// AlbumIdentity is the decision.Identity of an Album of artist: the album
// title, the artist's name and sort name as its creators (a sort name like
// "Beatles, The" is keyed both ways round by the identity check), and the
// year of its first release. artist may be nil, which leaves Creators empty
// and fails every release closed on its artist.
func AlbumIdentity(a *catalogv1alpha1.Album, artist *catalogv1alpha1.Artist) decision.Identity {
	var id decision.Identity
	if md := a.Status.Metadata; md != nil {
		id.Titles = appendTitles(id.Titles, md.Title)
		id.Year = utcYear(md.ReleaseDate)
	}
	if artist != nil && artist.Status.Metadata != nil {
		id.Creators = appendTitles(id.Creators, artist.Status.Metadata.Name, artist.Status.Metadata.SortName)
	}
	return id
}

// BookIdentity is the decision.Identity of a Book by author: its title, the
// "Title: Subtitle" form a release often carries, every edition's own title
// (a translated or retitled edition is the same work), and the author's name
// and sort name as creators. author is nil for a standalone book, which
// leaves Creators empty: the identity check then fails every release closed
// on its author rather than accept one by title alone.
func BookIdentity(b *catalogv1alpha1.Book, author *catalogv1alpha1.Author) decision.Identity {
	var id decision.Identity
	if md := b.Status.Metadata; md != nil {
		id.Titles = appendTitles(id.Titles, md.Title, withSubtitle(md.Title, md.Subtitle))
		for _, e := range md.Editions {
			id.Titles = appendTitles(id.Titles, e.Title)
		}
		id.Year = utcYear(md.ReleaseDate)
	}
	if author != nil && author.Status.Metadata != nil {
		id.Creators = appendTitles(id.Creators, author.Status.Metadata.Name, author.Status.Metadata.SortName)
	}
	return id
}

// AudiobookIdentity is the decision.Identity of an Audiobook: its title and
// "Title: Subtitle" form, and every author Audible lists as its creators.
// Narrators are not creators: a release names the author, and matching a
// narrator would let a different book read by the same voice through.
func AudiobookIdentity(ab *catalogv1alpha1.Audiobook) decision.Identity {
	var id decision.Identity
	md := ab.Status.Metadata
	if md == nil {
		return id
	}
	id.Titles = appendTitles(id.Titles, md.Title, withSubtitle(md.Title, md.Subtitle))
	for _, a := range md.Authors {
		id.Creators = appendTitles(id.Creators, a.Name)
	}
	id.Year = utcYear(md.ReleaseDate)
	return id
}

// IssueIdentity is the decision.Identity of an Issue of comic: the COMIC's
// (volume's) title, because that is what a release names, the issue's own
// number, and the year of the issue's cover date. There is deliberately no
// fallback to the volume's start year when the issue has no date: a volume
// runs for years, and bounding issue 50 by the year issue 1 came out would
// reject every correct release of it.
func IssueIdentity(iss *catalogv1alpha1.Issue, comic *catalogv1alpha1.Comic) decision.Identity {
	id := decision.Identity{Issue: iss.Spec.Number, Year: utcYear(iss.Status.Date)}
	if comic != nil && comic.Status.Metadata != nil {
		id.Titles = appendTitles(id.Titles, comic.Status.Metadata.Title)
	}
	return id
}

// withSubtitle is the "Title: Subtitle" form, or "" when there is no
// subtitle (appendTitles drops it).
func withSubtitle(title, subtitle string) string {
	if title == "" || subtitle == "" {
		return ""
	}
	return title + ": " + subtitle
}

// utcYear is t's year in UTC, 0 when t is unknown.
func utcYear(t *metav1.Time) int {
	if t == nil || t.IsZero() {
		return 0
	}
	return t.UTC().Year()
}

// releasedBy reports whether an item dated d is out at now. An unknown date
// counts as released: unlike an unaired episode, whose fakes are common and
// whose air date is always known once it is scheduled, an album, book or
// issue with no date is almost always an old one a provider never dated,
// and Lidarr, Readarr and Mylar all decide a release for one without an
// availability rule.
func releasedBy(d *metav1.Time, now time.Time) bool {
	return d == nil || !d.After(now)
}

// firstCreator is the creator a text query names.
func firstCreator(id decision.Identity) string {
	if len(id.Creators) == 0 {
		return ""
	}
	return id.Creators[0]
}

// firstTitle is the title a text query names.
func firstTitle(id decision.Identity) string {
	if len(id.Titles) == 0 {
		return ""
	}
	return id.Titles[0]
}

// snapshotNonVideo fills snap for an album, book, audiobook or issue, and
// returns whether the item has a file and which MediaFile holds it (for the
// kinds whose current file is one MediaFile; an album's and an audiobook's
// current quality is their status rollup, set directly).
func (w *Worker) snapshotNonVideo(ctx context.Context, ns string, ref commonv1.MediaRef, snap *itemSnapshot) (bool, *string, error) {
	key := client.ObjectKey{Namespace: ns, Name: ref.Name}
	now := w.now()

	switch ref.Kind {
	case commonv1.MediaKindAlbum:
		var a catalogv1alpha1.Album
		if err := w.Client.Get(ctx, key, &a); err != nil {
			return false, nil, fmt.Errorf("get Album %s/%s: %w", ns, ref.Name, err)
		}
		var artist catalogv1alpha1.Artist
		if err := w.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: a.Spec.ArtistRef}, &artist); err != nil {
			return false, nil, fmt.Errorf("get Artist %s/%s: %w", ns, a.Spec.ArtistRef, err)
		}
		// An album ranks against its own override, else its artist's.
		snap.QualityProfileRef = ptr.Deref(a.Spec.QualityProfileRef, artist.Spec.QualityProfileRef)
		snap.Target.Monitored = ptr.Deref(a.Spec.Monitored, true)
		if md := a.Status.Metadata; md != nil {
			snap.Target.Available = releasedBy(md.ReleaseDate, now)
		} else {
			snap.Target.Available = true
		}
		snap.Target.Identity = AlbumIdentity(&a, &artist)
		// The album's quality is the lowest across its imported tracks
		// (status.quality), which is what an upgrade has to beat.
		if a.Status.TrackFileCount > 0 && a.Status.Quality != nil {
			snap.Target.Current = &decision.Current{Quality: *a.Status.Quality, FormatScore: int(a.Status.FormatScore)}
		}
		return false, nil, nil

	case commonv1.MediaKindBook:
		var b catalogv1alpha1.Book
		if err := w.Client.Get(ctx, key, &b); err != nil {
			return false, nil, fmt.Errorf("get Book %s/%s: %w", ns, ref.Name, err)
		}
		var author *catalogv1alpha1.Author
		profile := ptr.Deref(b.Spec.QualityProfileRef, "")
		if name := ptr.Deref(b.Spec.AuthorRef, ""); name != "" {
			author = &catalogv1alpha1.Author{}
			if err := w.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: name}, author); err != nil {
				return false, nil, fmt.Errorf("get Author %s/%s: %w", ns, name, err)
			}
			if profile == "" {
				profile = author.Spec.QualityProfileRef
			}
		}
		snap.QualityProfileRef = profile
		snap.Target.Monitored = ptr.Deref(b.Spec.Monitored, true)
		snap.Target.Available = b.Status.Metadata == nil || releasedBy(b.Status.Metadata.ReleaseDate, now)
		snap.Target.Identity = BookIdentity(&b, author)
		return b.Status.HasFile, b.Status.FileRef, nil

	case commonv1.MediaKindAudiobook:
		var ab catalogv1alpha1.Audiobook
		if err := w.Client.Get(ctx, key, &ab); err != nil {
			return false, nil, fmt.Errorf("get Audiobook %s/%s: %w", ns, ref.Name, err)
		}
		snap.QualityProfileRef = ab.Spec.QualityProfileRef
		snap.Target.Monitored = ptr.Deref(ab.Spec.Monitored, true)
		snap.Target.Available = ab.Status.Metadata == nil || releasedBy(ab.Status.Metadata.ReleaseDate, now)
		snap.Target.Identity = AudiobookIdentity(&ab)
		// An audiobook's parts are several MediaFiles; status.quality is
		// the recording's, which is what an upgrade has to beat.
		if ab.Status.HasFile && ab.Status.Quality != nil {
			snap.Target.Current = &decision.Current{Quality: *ab.Status.Quality}
		}
		return false, nil, nil

	case commonv1.MediaKindIssue:
		var iss catalogv1alpha1.Issue
		if err := w.Client.Get(ctx, key, &iss); err != nil {
			return false, nil, fmt.Errorf("get Issue %s/%s: %w", ns, ref.Name, err)
		}
		var comic catalogv1alpha1.Comic
		if err := w.Client.Get(ctx, client.ObjectKey{Namespace: ns, Name: iss.Spec.ComicRef}, &comic); err != nil {
			return false, nil, fmt.Errorf("get Comic %s/%s: %w", ns, iss.Spec.ComicRef, err)
		}
		// Issues are ranked against the owning Comic's profile; an Issue
		// carries none of its own.
		snap.QualityProfileRef = comic.Spec.QualityProfileRef
		snap.Target.Monitored = ptr.Deref(iss.Spec.Monitored, true)
		snap.Target.Available = releasedBy(iss.Status.Date, now)
		snap.Target.Identity = IssueIdentity(&iss, &comic)
		return iss.Status.HasFile, iss.Status.FileRef, nil
	}
	return false, nil, fmt.Errorf("unsupported media kind %q", ref.Kind)
}

// nonVideoIDs is the request half of a non-video snapshot: the title, the
// creator and the issue number the text query names. No year:
// schema.SearchRequest.Year narrows a movie search only.
func nonVideoIDs(id decision.Identity) TargetIDs {
	return TargetIDs{
		Title:   firstTitle(id),
		Creator: firstCreator(id),
		Issue:   id.Issue,
	}
}
