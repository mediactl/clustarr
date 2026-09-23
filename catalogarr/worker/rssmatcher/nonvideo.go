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
package rssmatcher

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	catalogv1alpha1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/catalogarr/worker/search"
	"github.com/mediactl/clustarr/pkg/events/schema"
	"github.com/mediactl/clustarr/pkg/obs/logging"
	"github.com/mediactl/clustarr/pkg/release"
)

// The non-video half of the matcher: an album, a book, an audiobook or a
// comic issue, found by the names indexarr puts on the release
// (schema.Release's Artist, Album, Author and Issue, with ParsedTitle as the
// book title and the comic series). None of these items has an id a release
// carries, so each is found by name, as its *arr finds it from RSS:
//
//   - an album as Lidarr does -- the artist by clean name, then the album by
//     title within that artist (ParsingService.GetArtist, GetAlbums);
//   - a book as Readarr does -- the author by clean name, then the book by
//     title within that author (ParsingService.GetAuthor, GetBooks);
//   - an audiobook, which has no container, by author and title together;
//   - an issue as Mylar does -- the comic (volume) by clean title, then the
//     issue by number within it.
//
// A container that is not monitored hides its items, as Lidarr's and
// Readarr's RSS MonitoredAlbumSpecification / MonitoredBookSpecification
// reject "Artist is not monitored" / "Author is not monitored" before the
// item's own flag is looked at. A release whose names resolve to more than
// one item matches none: Sonarr refuses an ambiguous series title the same
// way (SeriesRepository.FindByTitle -> ReturnSingleSeriesOrThrow), and
// grabbing one release for two items is wrong for one of them. The decision
// engine then checks every match against the item's identity
// (pkg/decision's identity_nonvideo.go) anyway.

// nameKeys keys a creator's name for lookup: release.CleanTitle of it, and of
// its "First Last" form when it is written "Last, First" -- how library
// catalogues and many book releases file a name ("Herbert, Frank") and how a
// sort name reads ("Cave, Nick"). pkg/decision's creatorKeys reads a name the
// same two ways.
func nameKeys(name string) []string {
	keys := []string{release.CleanTitle(name)}
	if last, first, ok := strings.Cut(name, ","); ok && !strings.Contains(first, ",") {
		keys = append(keys, release.CleanTitle(first+" "+last))
	}
	return dedupeNonEmpty(keys...)
}

// coCredit splits a release's credit into the people it names: "Stephen King
// & Peter Straub", "Artist feat. Guest". It is pkg/decision's own splitter
// (identity_nonvideo.go), restated because that one is unexported: the
// matcher must find every item the identity check would accept a release
// for, or the check never sees it.
var coCredit = regexp.MustCompile(`(?i)\s+(?:&|and|feat\.?|ft\.?|featuring)\s+|\s*[;,]\s*`)

// creditKeys keys a release's credit whole and each co-credited name in it,
// so a co-written book or a featured-artist album is found through any one
// of its creators. Only the release side is split; an item's creators are
// already separate names.
func creditKeys(credit string) []string {
	keys := nameKeys(credit)
	for _, part := range coCredit.Split(credit, -1) {
		keys = append(keys, nameKeys(part)...)
	}
	return dedupeNonEmpty(keys...)
}

// issueNumberKey is an issue number's comparison form: a plain number loses
// its padding and trailing fractional zeros ("050", "50" and "50.0" are one
// issue), anything else ("Annual 1") keeps only its lower-cased letters and
// digits. It is pkg/decision's issueNumberKey (identity_nonvideo.go, Mylar's
// helpers.issuedigits), restated because that one is unexported; the two
// must agree or the check refuses what the matcher found.
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

// pairKey is "<left>|<right>", or "" when either half is empty.
func pairKey(left, right string) string {
	if left == "" || right == "" {
		return ""
	}
	return left + "|" + right
}

// artistNameKeys indexes an Artist under its name and sort name.
func artistNameKeys(o client.Object) []string {
	a, ok := o.(*catalogv1alpha1.Artist)
	if !ok || a.Status.Metadata == nil {
		return nil
	}
	return dedupeNonEmpty(append(nameKeys(a.Status.Metadata.Name), nameKeys(a.Status.Metadata.SortName)...)...)
}

// authorNameKeys indexes an Author under its name and sort name.
func authorNameKeys(o client.Object) []string {
	a, ok := o.(*catalogv1alpha1.Author)
	if !ok || a.Status.Metadata == nil {
		return nil
	}
	return dedupeNonEmpty(append(nameKeys(a.Status.Metadata.Name), nameKeys(a.Status.Metadata.SortName)...)...)
}

// albumKeys indexes an Album under "<artistRef>|<clean title>" for every
// title its identity knows (search.AlbumIdentity).
func albumKeys(o client.Object) []string {
	a, ok := o.(*catalogv1alpha1.Album)
	if !ok || a.Spec.ArtistRef == "" {
		return nil
	}
	var keys []string
	for _, t := range search.AlbumIdentity(a, nil).Titles {
		keys = append(keys, pairKey(a.Spec.ArtistRef, release.CleanTitle(t)))
	}
	return dedupeNonEmpty(keys...)
}

// bookKeys indexes a Book under "<authorRef>|<clean title>" for its title,
// its "Title: Subtitle" form and every edition's title
// (search.BookIdentity). A standalone Book -- no authorRef -- is not
// indexed: the identity check fails every release for it closed on its
// author, so matching it would only cost a decision.
func bookKeys(o client.Object) []string {
	b, ok := o.(*catalogv1alpha1.Book)
	if !ok {
		return nil
	}
	author := ptr.Deref(b.Spec.AuthorRef, "")
	if author == "" {
		return nil
	}
	var keys []string
	for _, t := range search.BookIdentity(b, nil).Titles {
		keys = append(keys, pairKey(author, release.CleanTitle(t)))
	}
	return dedupeNonEmpty(keys...)
}

// audiobookKeys indexes an Audiobook under "<clean author>|<clean title>"
// for every credited author and every title (search.AudiobookIdentity):
// it has no container to look the author up in first.
func audiobookKeys(o client.Object) []string {
	ab, ok := o.(*catalogv1alpha1.Audiobook)
	if !ok {
		return nil
	}
	id := search.AudiobookIdentity(ab)
	var keys []string
	for _, creator := range id.Creators {
		for _, ck := range nameKeys(creator) {
			for _, t := range id.Titles {
				keys = append(keys, pairKey(ck, release.CleanTitle(t)))
			}
		}
	}
	return dedupeNonEmpty(keys...)
}

// comicTitleKeys indexes a Comic under its clean title.
func comicTitleKeys(o client.Object) []string {
	c, ok := o.(*catalogv1alpha1.Comic)
	if !ok || c.Status.Metadata == nil {
		return nil
	}
	return dedupeNonEmpty(release.CleanTitle(c.Status.Metadata.Title))
}

// issueKeys indexes an Issue under "<comicRef>#<issue number key>".
func issueKeys(o client.Object) []string {
	iss, ok := o.(*catalogv1alpha1.Issue)
	if !ok || iss.Spec.ComicRef == "" {
		return nil
	}
	if k := issueNumberKey(iss.Spec.Number); k != "" {
		return []string{iss.Spec.ComicRef + "#" + k}
	}
	return nil
}

// listByKeys lists every object of list's kind indexed under any of keys,
// once each, in first-seen order.
func listByKeys[T any, PT interface {
	*T
	client.Object
}](ctx context.Context, c client.Client, namespace, index string, keys []string, items func(client.ObjectList) []T, newList func() client.ObjectList) ([]PT, error) {
	seen := map[string]bool{}
	var out []PT
	for _, k := range dedupeNonEmpty(keys...) {
		list := newList()
		if err := c.List(ctx, list, client.InNamespace(namespace), client.MatchingFields{index: k}); err != nil {
			return nil, fmt.Errorf("rssmatcher: list by %s: %w", index, err)
		}
		for _, item := range items(list) {
			p := PT(&item)
			if seen[p.GetName()] {
				continue
			}
			seen[p.GetName()] = true
			out = append(out, p)
		}
	}
	return out, nil
}

// singleMonitored applies the matcher's two rules to the items a release's
// names resolved to: more than one is ambiguous and matches none (counted
// before the monitored filter, as Sonarr counts every series), and an
// unmonitored item is never returned.
func singleMonitored(ctx context.Context, kind commonv1.MediaKind, names []string, monitored []bool) []commonv1.MediaRef {
	switch len(names) {
	case 0:
		return nil
	case 1:
		if !monitored[0] {
			return nil
		}
		return []commonv1.MediaRef{{Kind: kind, Name: names[0]}}
	default:
		logging.FromContext(ctx).Debug("rssmatcher: release names several items; not guessing",
			"kind", string(kind), "items", len(names))
		return nil
	}
}

// matchNonVideo dispatches a non-video release to its kind's lookup. A
// comic release -- the classifier's kind for one -- is an issue's.
func matchNonVideo(ctx context.Context, c client.Client, namespace string, rel schema.Release) ([]commonv1.MediaRef, error) {
	switch rel.Kind {
	case commonv1.MediaKindAlbum:
		return matchAlbum(ctx, c, namespace, rel)
	case commonv1.MediaKindBook:
		return matchBook(ctx, c, namespace, rel)
	case commonv1.MediaKindAudiobook:
		return matchAudiobook(ctx, c, namespace, rel)
	case commonv1.MediaKindComic, commonv1.MediaKindIssue:
		return matchIssue(ctx, c, namespace, rel)
	}
	return nil, nil
}

func matchAlbum(ctx context.Context, c client.Client, namespace string, rel schema.Release) ([]commonv1.MediaRef, error) {
	title := release.CleanTitle(rel.Album)
	if title == "" {
		return nil, nil
	}
	artists, err := listByKeys[catalogv1alpha1.Artist](ctx, c, namespace, IndexArtistName, creditKeys(rel.Artist),
		func(l client.ObjectList) []catalogv1alpha1.Artist { return l.(*catalogv1alpha1.ArtistList).Items },
		func() client.ObjectList { return &catalogv1alpha1.ArtistList{} })
	if err != nil {
		return nil, err
	}
	var names []string
	var monitored []bool
	for _, artist := range artists {
		albums, err := listByKeys[catalogv1alpha1.Album](ctx, c, namespace, IndexAlbumArtistTitle, []string{pairKey(artist.Name, title)},
			func(l client.ObjectList) []catalogv1alpha1.Album { return l.(*catalogv1alpha1.AlbumList).Items },
			func() client.ObjectList { return &catalogv1alpha1.AlbumList{} })
		if err != nil {
			return nil, err
		}
		for _, a := range albums {
			names = append(names, a.Name)
			monitored = append(monitored, ptr.Deref(artist.Spec.Monitored, true) && ptr.Deref(a.Spec.Monitored, true))
		}
	}
	return singleMonitored(ctx, commonv1.MediaKindAlbum, names, monitored), nil
}

func matchBook(ctx context.Context, c client.Client, namespace string, rel schema.Release) ([]commonv1.MediaRef, error) {
	title := release.CleanTitle(rel.ParsedTitle)
	if title == "" {
		return nil, nil
	}
	authors, err := listByKeys[catalogv1alpha1.Author](ctx, c, namespace, IndexAuthorName, creditKeys(rel.Author),
		func(l client.ObjectList) []catalogv1alpha1.Author { return l.(*catalogv1alpha1.AuthorList).Items },
		func() client.ObjectList { return &catalogv1alpha1.AuthorList{} })
	if err != nil {
		return nil, err
	}
	var names []string
	var monitored []bool
	for _, author := range authors {
		books, err := listByKeys[catalogv1alpha1.Book](ctx, c, namespace, IndexBookAuthorTitle, []string{pairKey(author.Name, title)},
			func(l client.ObjectList) []catalogv1alpha1.Book { return l.(*catalogv1alpha1.BookList).Items },
			func() client.ObjectList { return &catalogv1alpha1.BookList{} })
		if err != nil {
			return nil, err
		}
		for _, b := range books {
			names = append(names, b.Name)
			monitored = append(monitored, ptr.Deref(author.Spec.Monitored, true) && ptr.Deref(b.Spec.Monitored, true))
		}
	}
	return singleMonitored(ctx, commonv1.MediaKindBook, names, monitored), nil
}

func matchAudiobook(ctx context.Context, c client.Client, namespace string, rel schema.Release) ([]commonv1.MediaRef, error) {
	title := release.CleanTitle(rel.ParsedTitle)
	if title == "" {
		return nil, nil
	}
	var keys []string
	for _, ck := range creditKeys(rel.Author) {
		keys = append(keys, pairKey(ck, title))
	}
	books, err := listByKeys[catalogv1alpha1.Audiobook](ctx, c, namespace, IndexAudiobookAuthorTitle, keys,
		func(l client.ObjectList) []catalogv1alpha1.Audiobook { return l.(*catalogv1alpha1.AudiobookList).Items },
		func() client.ObjectList { return &catalogv1alpha1.AudiobookList{} })
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(books))
	monitored := make([]bool, 0, len(books))
	for _, ab := range books {
		names = append(names, ab.Name)
		monitored = append(monitored, ptr.Deref(ab.Spec.Monitored, true))
	}
	return singleMonitored(ctx, commonv1.MediaKindAudiobook, names, monitored), nil
}

func matchIssue(ctx context.Context, c client.Client, namespace string, rel schema.Release) ([]commonv1.MediaRef, error) {
	number := issueNumberKey(rel.Issue)
	if number == "" {
		return nil, nil
	}
	comics, err := listByKeys[catalogv1alpha1.Comic](ctx, c, namespace, IndexComicTitle, []string{release.CleanTitle(rel.ParsedTitle)},
		func(l client.ObjectList) []catalogv1alpha1.Comic { return l.(*catalogv1alpha1.ComicList).Items },
		func() client.ObjectList { return &catalogv1alpha1.ComicList{} })
	if err != nil {
		return nil, err
	}
	var names []string
	var monitored []bool
	for _, comic := range comics {
		issues, err := listByKeys[catalogv1alpha1.Issue](ctx, c, namespace, IndexIssueComicNumber, []string{comic.Name + "#" + number},
			func(l client.ObjectList) []catalogv1alpha1.Issue { return l.(*catalogv1alpha1.IssueList).Items },
			func() client.ObjectList { return &catalogv1alpha1.IssueList{} })
		if err != nil {
			return nil, err
		}
		for _, iss := range issues {
			names = append(names, iss.Name)
			monitored = append(monitored, ptr.Deref(comic.Spec.Monitored, true) && ptr.Deref(iss.Spec.Monitored, true))
		}
	}
	return singleMonitored(ctx, commonv1.MediaKindIssue, names, monitored), nil
}
