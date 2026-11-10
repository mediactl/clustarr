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

package ui_test

import (
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui"
	"github.com/mediactl/clustarr/ui/actions"
	"github.com/mediactl/clustarr/ui/projection"
)

// The Edit dialog's kind block (2026-11-10 design §9.2-§9.5): the
// non-video kinds' own fields are fetched as the dialog opens from the
// item's edit/fields route and saved with the dialog's form, which carries
// the block's "fields" marker; without the marker a save leaves every
// kind field alone.

// editFixture is one of each editable non-video kind with its own fields
// set, the Authors and Books an audiobook's links choose from, the book
// roots and profiles a standalone book's selects offer (and a root of
// another namespace and a comic profile, which they must not), an album
// of the artist with two releases and no override of its own (the music
// profiles its select offers), and a series whose episodes hold two files,
// for its Delete dialog. The fake client is the reader and the writer,
// with the ui's children indexes.
func editFixture(t *testing.T) (*ui.Server, client.Client) {
	t.Helper()
	artist := &catalogv1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "radiohead", Namespace: "default"},
		Spec: catalogv1.ArtistSpec{
			MusicBrainzID: "a74b1b7f", QualityProfileRef: "lossless", RootFolderRef: "music", MonitorNewItems: catalogv1.MonitorNewItemsAll,
			MetadataProfile: catalogv1.MusicMetadataProfile{PrimaryTypes: []string{"album", "single"}, SecondaryTypes: []string{"studio", "live"}, ReleaseStatuses: []string{"official"}},
		},
	}
	released := metav1.NewTime(time.Date(1997, time.May, 21, 0, 0, 0, 0, time.UTC))
	okComputer := &catalogv1.Album{
		ObjectMeta: metav1.ObjectMeta{Name: "ok-computer", Namespace: "default"},
		Spec:       catalogv1.AlbumSpec{ArtistRef: "radiohead", ReleaseGroupID: "rg-ok", Monitored: new(true)},
		Status: catalogv1.AlbumStatus{Metadata: &catalogv1.AlbumMetadata{
			Title: "OK Computer", SelectedReleaseID: "rel-1",
			Releases: []catalogv1.ReleaseSummary{
				{ID: "rel-1", Status: "Official", Country: "GB", Label: "Parlophone", ReleaseDate: &released, TrackCount: 12},
				{ID: "rel-2", Status: "Official", Country: "US", Label: "Capitol", Title: "OK Computer", Disambiguation: "Collector's Edition", TrackCount: 24},
			},
		}},
	}
	leGuin := &catalogv1.Author{
		ObjectMeta: metav1.ObjectMeta{Name: "le-guin", Namespace: "default"},
		Spec: catalogv1.AuthorSpec{
			OpenLibraryID: "OL1A", QualityProfileRef: "ebook", RootFolderRef: "books", MonitorNewItems: catalogv1.MonitorNewChildrenNone,
			MetadataProfile: catalogv1.BookMetadataProfile{AllowedLanguages: []string{"en", "fr"}, SkipMissingDate: true, MinPages: 50},
		},
		Status: catalogv1.AuthorStatus{Metadata: &catalogv1.AuthorMetadata{Name: "Ursula K. Le Guin"}},
	}
	gibson := &catalogv1.Author{
		ObjectMeta: metav1.ObjectMeta{Name: "gibson", Namespace: "default"},
		Spec:       catalogv1.AuthorSpec{OpenLibraryID: "OL2A", QualityProfileRef: "ebook", RootFolderRef: "books"},
	}
	comic := &catalogv1.Comic{
		ObjectMeta: metav1.ObjectMeta{Name: "saga", Namespace: "default"},
		Spec: catalogv1.ComicSpec{
			Source: catalogv1.ComicSourceComicVine, SourceID: "1", Kind: catalogv1.ComicKindManga, SpecialVersion: catalogv1.ComicSpecialVersionTPB,
			MonitorNewIssues: new(false), QualityProfileRef: "cbz", RootFolderRef: "comics",
		},
	}
	audiobook := &catalogv1.Audiobook{
		ObjectMeta: metav1.ObjectMeta{Name: "earthsea-ab", Namespace: "default"},
		Spec: catalogv1.AudiobookSpec{
			ASIN: "B00ABC1234", Region: catalogv1.AudiobookRegionUK, AuthorRef: new("le-guin"), BookRef: new("wizard"),
			QualityProfileRef: "m4b", RootFolderRef: "audiobooks",
		},
	}
	book := func(name, author, title string) *catalogv1.Book {
		b := &catalogv1.Book{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       catalogv1.BookSpec{WorkID: "OL" + name + "W", Monitored: new(true)},
			Status:     catalogv1.BookStatus{Metadata: &catalogv1.BookMetadata{Title: title}},
		}
		if author != "" {
			b.Spec.AuthorRef = new(author)
		}
		return b
	}
	standalone := book("standalone", "", "A Standalone Book")
	standalone.Spec.QualityProfileRef, standalone.Spec.RootFolderRef = new("ebook"), new("books")
	root := func(name, ns string, kind catalogv1.RootFolderKind) *catalogv1.RootFolder {
		return &catalogv1.RootFolder{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: catalogv1.RootFolderSpec{Path: "/data/" + name, Kind: kind}}
	}
	profile := func(name string, kind catalogv1.ProfileMediaKind) *catalogv1.QualityProfile {
		return &catalogv1.QualityProfile{ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: catalogv1.QualityProfileSpec{MediaKind: kind}}
	}
	series := &catalogv1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "andor", Namespace: "default"},
		Spec:       catalogv1.SeriesSpec{TvdbID: 393187, QualityProfileRef: "hd", RootFolderRef: "tv", Monitored: new(true)},
		Status:     catalogv1.SeriesStatus{Path: "/data/tv/Andor"},
	}
	episode := func(name, fileRef string) *catalogv1.Episode {
		ep := &catalogv1.Episode{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       catalogv1.EpisodeSpec{SeriesRef: "andor", SeasonNumber: 1, EpisodeNumber: 1},
		}
		if fileRef != "" {
			ep.Status.FileRef, ep.Status.HasFile = new(fileRef), true
		}
		return ep
	}
	file := func(name, path string, size int64) *catalogv1.MediaFile {
		return &catalogv1.MediaFile{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
			Spec:       catalogv1.MediaFileSpec{MediaRef: commonv1.MediaRef{Kind: commonv1.MediaKindEpisode, Name: name}, Path: path, SizeBytes: size},
		}
	}
	c := fake.NewClientBuilder().WithScheme(libraryTestScheme(t)).
		WithIndex(&catalogv1.Book{}, ui.BookAuthorRefField, ui.IndexBookByAuthorRef).
		WithIndex(&catalogv1.Episode{}, ui.EpisodeSeriesRefField, ui.IndexEpisodeBySeriesRef).
		WithIndex(&catalogv1.Album{}, ui.AlbumArtistRefField, ui.IndexAlbumByArtistRef).
		WithIndex(&catalogv1.Issue{}, ui.IssueComicRefField, ui.IndexIssueByComicRef).
		WithObjects(
			artist, okComputer, leGuin, gibson, comic, audiobook, standalone,
			book("wizard", "le-guin", "A Wizard of Earthsea"), book("tombs", "le-guin", "The Tombs of Atuan"), book("neuromancer", "gibson", "Neuromancer"),
			root("books", "default", catalogv1.RootFolderKindBook), root("books-2", "default", catalogv1.RootFolderKindBook),
			root("books-other", "other", catalogv1.RootFolderKindBook), root("comics", "default", catalogv1.RootFolderKindComic),
			profile("ebook", catalogv1.ProfileMediaKindBook), profile("ebook-2", catalogv1.ProfileMediaKindBook), profile("cbz", catalogv1.ProfileMediaKindComic),
			profile("hd", catalogv1.ProfileMediaKindVideo), profile("lossless", catalogv1.ProfileMediaKindMusic), profile("mp3", catalogv1.ProfileMediaKindMusic),
			series, episode("andor-s01e01", "andor-f1"), episode("andor-s01e02", "andor-f2"), episode("andor-s01e03", ""),
			file("andor-f1", "/data/tv/Andor/Season 01/Andor - S01E01.mkv", 3<<30), file("andor-f2", "/data/tv/Andor/Season 01/Andor - S01E02.mkv", 2<<30),
		).Build()
	items := []projection.LibraryItem{
		{Ref: types.NamespacedName{Namespace: "default", Name: "andor"}, Kind: commonv1.MediaKindSeries, Tab: projection.TabTV, Title: "Andor", Monitored: true, QualityProfileRef: "hd"},
		{Ref: types.NamespacedName{Namespace: "default", Name: "saga"}, Kind: commonv1.MediaKindComic, Tab: projection.TabComics, Title: "Saga", Monitored: true, QualityProfileRef: "cbz"},
	}
	srv := ui.NewServer(t.Context(), ui.Options{
		Reader: c, Actions: actions.New(c),
		Library: func(context.Context) []projection.LibraryItem { return items },
	})
	return srv, c
}

// editChecked is whether the control with id renders checked: the
// checkbox's and switch's hidden native input, which owns the id
// (checkedAttr, the toolbar test's).
func editChecked(t *testing.T, body, id string) bool {
	t.Helper()
	return checkedAttr.MatchString(requireTag(t, body, `id="`+id+`"`))
}

func TestEditFieldsRouteServesEachKindsBlock(t *testing.T) {
	srv, _ := editFixture(t)
	fields := func(path string) string {
		rec := getPath(t, srv, path, true)
		require.Equal(t, http.StatusOK, rec.Code, path)
		return rec.Body.String()
	}

	artist := fields("/library/default/artist/radiohead/edit/fields")
	requireTag(t, artist, `data-edit-fields="artist"`, `data-edit-fields-loaded`)
	requireTag(t, artist, `name="fields"`, `value="artist"`)
	requireTag(t, artist, `name="monitorNewItems"`, `value="all"`)
	require.Contains(t, artist, `data-templ-value="new"`, "an artist's monitor-new has Lidarr's third mode")
	for _, group := range []string{"primaryTypes", "secondaryTypes", "releaseStatuses"} {
		requireTag(t, artist, `data-edit-group="`+group+`"`, `data-slot="field-set"`)
	}
	require.True(t, editChecked(t, artist, "edit-primaryTypes-album-radiohead"), "a type the profile lists is ticked")
	require.True(t, editChecked(t, artist, "edit-primaryTypes-single-radiohead"))
	require.False(t, editChecked(t, artist, "edit-primaryTypes-ep-radiohead"), "one it does not is not")
	require.True(t, editChecked(t, artist, "edit-secondaryTypes-live-radiohead"))
	require.False(t, editChecked(t, artist, "edit-releaseStatuses-bootleg-radiohead"))
	require.Contains(t, artist, "Field recording", "every MusicBrainz secondary type is offered")

	author := fields("/library/default/author/le-guin/edit/fields")
	requireTag(t, author, `name="fields"`, `value="author"`)
	requireTag(t, author, `name="monitorNewItems"`, `value="none"`)
	require.NotContains(t, author, `data-templ-value="new"`, "an author's monitor-new is all or none")
	requireTag(t, author, `name="allowedLanguages"`, `value="en, fr"`)
	requireTag(t, author, `name="minPages"`, `type="number"`, `value="50"`)
	require.True(t, editChecked(t, author, "edit-skipMissingDate-le-guin"))
	require.False(t, editChecked(t, author, "edit-skipMissingISBN-le-guin"))
	for _, name := range []string{"skipMissingISBN", "skipPartsAndSets", "skipSeriesSecondary"} {
		requireTag(t, author, `name="`+name+`"`, `value="false"`)
	}

	comic := fields("/library/default/comic/saga/edit/fields")
	requireTag(t, comic, `name="fields"`, `value="comic"`)
	requireTag(t, comic, `name="comicKind"`, `value="manga"`)
	requireTag(t, comic, `name="specialVersion"`, `value="tpb"`)
	require.Contains(t, comic, `data-templ-value="volumeAsIssue"`)
	require.False(t, editChecked(t, comic, "edit-monitorNewIssues-saga"), "new issues are not monitored")
	requireTag(t, comic, `name="monitorNewIssues"`, `value="false"`)

	audiobook := fields("/library/default/audiobook/earthsea-ab/edit/fields")
	requireTag(t, audiobook, `name="fields"`, `value="audiobook"`)
	requireTag(t, audiobook, `name="region"`, `value="uk"`)
	require.Contains(t, audiobook, `data-templ-value="jp"`)
	requireTag(t, audiobook, `name="authorRef"`, `value="le-guin"`)
	require.Contains(t, audiobook, `data-templ-value="__none__"`, "a link can be cleared")
	require.Contains(t, audiobook, `data-templ-value="gibson"`, "every Author of the namespace is offered")
	require.Contains(t, audiobook, "Ursula K. Le Guin", "an author by its gathered name")
	requireTag(t, audiobook, `name="bookRef"`, `value="wizard"`)
	require.Contains(t, audiobook, `data-templ-value="tombs"`, "the linked author's books")
	require.Contains(t, audiobook, "The Tombs of Atuan")
	require.NotContains(t, audiobook, `data-templ-value="neuromancer"`, "another author's book is not offered")

	standalone := fields("/library/default/book/standalone/edit/fields")
	requireTag(t, standalone, `name="fields"`, `value="book"`)
	requireTag(t, standalone, `name="qualityProfile"`, `value="ebook"`)
	require.Contains(t, standalone, `data-templ-value="ebook-2"`)
	require.NotContains(t, standalone, `data-templ-value="cbz"`, "a comic profile is not offered for a book")
	requireTag(t, standalone, `name="rootFolder"`, `value="books"`)
	require.Contains(t, standalone, `data-templ-value="books-2"`)
	require.NotContains(t, standalone, `books-other`, "a root of another namespace is not offered")
	require.NotContains(t, standalone, `data-templ-value="comics"`, "a comic root is not offered")
	require.NotContains(t, standalone, `data-edit-inherits`)

	authors := fields("/library/default/book/wizard/edit/fields")
	requireTag(t, authors, `data-edit-inherits`)
	require.NotContains(t, authors, `name="qualityProfile"`, "an author's book inherits its profile and root")
	require.NotContains(t, authors, `name="rootFolder"`)

	// An album's block (design §9.2): its profile override over the music
	// profiles behind the artist's, which applies without one and is the
	// choice selected here; the release choice -- anyReleaseOk on by its
	// CRD default, and the pin over the group's releases, Automatic selected
	// here, each release named by what the metadata knows of it, the
	// selected one said.
	album := fields("/library/default/album/ok-computer/edit/fields")
	requireTag(t, album, `name="fields"`, `value="album"`)
	requireTag(t, album, `name="qualityProfile"`, `value="__none__"`)
	require.Contains(t, album, "(lossless)", "the artist's profile is named as the inherited choice")
	require.Contains(t, album, `data-templ-value="mp3"`, "every music profile is offered")
	require.NotContains(t, album, `data-templ-value="cbz"`, "a comic profile is not")
	require.NotContains(t, album, `data-templ-value="hd"`, "nor a video one")
	requireTag(t, album, `name="anyReleaseOk"`, `value="false"`)
	require.True(t, editChecked(t, album, "edit-anyReleaseOk-ok-computer"), "anyReleaseOk defaults on")
	requireTag(t, album, `name="releaseID"`, `value="__none__"`)
	require.Contains(t, album, "Automatic (the best release of the group)")
	require.Contains(t, album, `data-templ-value="rel-1"`)
	require.Contains(t, album, "GB · 1997-05-21 · 12 tracks · Parlophone (selected)")
	require.Contains(t, album, `data-templ-value="rel-2"`)
	require.Contains(t, album, "Collector&#39;s Edition · US · 24 tracks · Capitol")
	require.NotContains(t, album, `name="rootFolder"`, "an album's folder is its artist's")

	require.Equal(t, http.StatusNotFound, getPath(t, srv, "/library/default/movie/andor/edit/fields", true).Code, "a movie's fields render with the page")
	require.Equal(t, http.StatusNotFound, getPath(t, srv, "/library/default/comic/nope/edit/fields", true).Code)
}

func TestEditDialogSavesEachKindsFieldsOnlyWhenItsBlockWasLoaded(t *testing.T) {
	srv, c := editFixture(t)
	ctx := t.Context()
	save := func(path string, form url.Values) int {
		form.Set("return", "/library")
		return postForm(t, srv, path, form, false).Code
	}

	require.Equal(t, http.StatusSeeOther, save("/library/default/artist/radiohead/edit", url.Values{
		"fields": {"artist"}, "qualityProfile": {"lossless"}, "monitored": {"false", "true"}, "monitorNewItems": {"new"},
		"primaryTypes": {"album", "ep"}, "secondaryTypes": {"studio"}, "releaseStatuses": {"official", "bootleg"},
	}))
	var ar catalogv1.Artist
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "radiohead"}, &ar))
	require.Equal(t, catalogv1.MonitorNewItemsNew, ar.Spec.MonitorNewItems)
	require.Equal(t, []string{"album", "ep"}, ar.Spec.MetadataProfile.PrimaryTypes)
	require.Equal(t, []string{"studio"}, ar.Spec.MetadataProfile.SecondaryTypes, "an unticked type is dropped")
	require.Equal(t, []string{"official", "bootleg"}, ar.Spec.MetadataProfile.ReleaseStatuses)
	require.Equal(t, "a74b1b7f", ar.Spec.MusicBrainzID, "a merge patch leaves the rest")
	require.Equal(t, http.StatusBadRequest, save("/library/default/artist/radiohead/edit", url.Values{
		"fields": {"artist"}, "qualityProfile": {"lossless"}, "primaryTypes": {}, "secondaryTypes": {"studio"}, "releaseStatuses": {"official"},
	}), "no primary type at all would admit nothing")

	require.Equal(t, http.StatusSeeOther, save("/library/default/author/le-guin/edit", url.Values{
		"fields": {"author"}, "qualityProfile": {"ebook"}, "monitored": {"false", "true"}, "monitorNewItems": {"all"},
		"allowedLanguages": {"de"}, "minPages": {"100"}, "skipMissingDate": {"false"}, "skipMissingISBN": {"false", "true"},
		"skipPartsAndSets": {"false"}, "skipSeriesSecondary": {"false"},
	}))
	var au catalogv1.Author
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "le-guin"}, &au))
	require.Equal(t, catalogv1.MonitorNewChildrenAll, au.Spec.MonitorNewItems)
	require.Equal(t, []string{"de"}, au.Spec.MetadataProfile.AllowedLanguages)
	require.Equal(t, int32(100), au.Spec.MetadataProfile.MinPages)
	require.False(t, au.Spec.MetadataProfile.SkipMissingDate, "a switch turned off is written off")
	require.True(t, au.Spec.MetadataProfile.SkipMissingISBN)
	require.Equal(t, http.StatusBadRequest, save("/library/default/author/le-guin/edit", url.Values{
		"fields": {"author"}, "qualityProfile": {"ebook"}, "minPages": {"many"},
	}), "a page count that is no number")
	require.Equal(t, http.StatusSeeOther, save("/library/default/author/le-guin/edit", url.Values{
		"fields": {"author"}, "qualityProfile": {"ebook"}, "allowedLanguages": {""}, "minPages": {""},
	}))
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "le-guin"}, &au))
	require.Empty(t, au.Spec.MetadataProfile.AllowedLanguages, "no language is every language")

	require.Equal(t, http.StatusSeeOther, save("/library/default/comic/saga/edit", url.Values{
		"fields": {"comic"}, "qualityProfile": {"cbz"}, "monitored": {"false", "true"}, "comicKind": {"comic"}, "specialVersion": {"omnibus"}, "monitorNewIssues": {"false", "true"},
	}))
	var co catalogv1.Comic
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "saga"}, &co))
	require.Equal(t, catalogv1.ComicKindComic, co.Spec.Kind)
	require.Equal(t, catalogv1.ComicSpecialVersionOmnibus, co.Spec.SpecialVersion)
	require.True(t, *co.Spec.MonitorNewIssues)
	// Without the block's marker the kind fields are left as they are: a
	// save before the block loaded cannot write a default over them.
	require.Equal(t, http.StatusSeeOther, save("/library/default/comic/saga/edit", url.Values{
		"qualityProfile": {"cbz"}, "monitored": {"false"}, "comicKind": {"manga"}, "specialVersion": {"tpb"},
	}))
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "saga"}, &co))
	require.Equal(t, catalogv1.ComicKindComic, co.Spec.Kind, "the kind field posted without the marker is ignored")
	require.Equal(t, catalogv1.ComicSpecialVersionOmnibus, co.Spec.SpecialVersion)
	require.False(t, *co.Spec.Monitored, "the generic fields are saved as before")

	require.Equal(t, http.StatusSeeOther, save("/library/default/audiobook/earthsea-ab/edit", url.Values{
		"fields": {"audiobook"}, "qualityProfile": {"m4b"}, "monitored": {"false", "true"}, "region": {"us"}, "authorRef": {"__none__"}, "bookRef": {"tombs"},
	}))
	var ab catalogv1.Audiobook
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "earthsea-ab"}, &ab))
	require.Equal(t, catalogv1.AudiobookRegionUS, ab.Spec.Region)
	require.Nil(t, ab.Spec.AuthorRef, "None clears the link")
	require.Equal(t, "tombs", *ab.Spec.BookRef)
	require.Equal(t, "B00ABC1234", ab.Spec.ASIN)

	require.Equal(t, http.StatusSeeOther, save("/library/default/book/standalone/edit", url.Values{
		"fields": {"book"}, "qualityProfile": {"ebook-2"}, "rootFolder": {"books-2"}, "monitored": {"false"},
	}))
	var bk catalogv1.Book
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "standalone"}, &bk))
	require.Equal(t, "ebook-2", *bk.Spec.QualityProfileRef)
	require.Equal(t, "books-2", *bk.Spec.RootFolderRef)
	require.False(t, *bk.Spec.Monitored)
	require.Nil(t, bk.Spec.AuthorRef)

	// An author's book saves monitored alone: no profile is posted, and
	// none is required of it.
	require.Equal(t, http.StatusSeeOther, save("/library/default/book/wizard/edit", url.Values{"fields": {"book"}, "monitored": {"false"}}))
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "wizard"}, &bk))
	require.False(t, *bk.Spec.Monitored)
	require.Nil(t, bk.Spec.QualityProfileRef, "the author's profile still applies")
	require.Equal(t, "le-guin", *bk.Spec.AuthorRef)

	// An album (design §9.2): a profile override, anyReleaseOk off and a
	// pinned release; then the artist's profile again (None clears the
	// override) and no pin; and, without the block's marker, monitored
	// alone, the release choice left as it is.
	require.Equal(t, http.StatusSeeOther, save("/library/default/album/ok-computer/edit", url.Values{
		"fields": {"album"}, "qualityProfile": {"mp3"}, "monitored": {"false"}, "anyReleaseOk": {"false"}, "releaseID": {"rel-2"},
	}))
	var al catalogv1.Album
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "ok-computer"}, &al))
	require.Equal(t, "mp3", *al.Spec.QualityProfileRef)
	require.False(t, *al.Spec.Monitored)
	require.False(t, *al.Spec.AnyReleaseOk)
	require.Equal(t, "rel-2", *al.Spec.ReleaseID)
	require.Equal(t, "rg-ok", al.Spec.ReleaseGroupID, "a merge patch leaves the rest")
	require.Equal(t, http.StatusSeeOther, save("/library/default/album/ok-computer/edit", url.Values{
		"fields": {"album"}, "qualityProfile": {"__none__"}, "monitored": {"false", "true"}, "anyReleaseOk": {"false", "true"}, "releaseID": {"__none__"},
	}))
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "ok-computer"}, &al))
	require.Nil(t, al.Spec.QualityProfileRef, "the artist's profile applies again")
	require.True(t, *al.Spec.Monitored)
	require.True(t, *al.Spec.AnyReleaseOk)
	require.Nil(t, al.Spec.ReleaseID, "Automatic clears the pin")
	require.Equal(t, http.StatusSeeOther, save("/library/default/album/ok-computer/edit", url.Values{
		"qualityProfile": {"mp3"}, "monitored": {"false"}, "anyReleaseOk": {"false"}, "releaseID": {"rel-1"},
	}))
	require.NoError(t, c.Get(ctx, types.NamespacedName{Namespace: "default", Name: "ok-computer"}, &al))
	require.Equal(t, "mp3", *al.Spec.QualityProfileRef, "the generic field is saved as before")
	require.False(t, *al.Spec.Monitored)
	require.True(t, *al.Spec.AnyReleaseOk, "the release choice posted without the marker is ignored")
	require.Nil(t, al.Spec.ReleaseID)
}

// Each non-video item page's Edit dialog renders the generic fields with
// the page and a placeholder that fetches the kind block as the dialog
// opens; a movie's dialog has no block to fetch.
func TestEditDialogFetchesTheKindBlockWhenItOpens(t *testing.T) {
	comicSrv, _ := comicFixture(t)
	page := detailPage(t, comicSrv, "/library/default/comic/saga")
	dlg := section(t, page, `data-edit-dialog="saga"`)
	require.Contains(t, dlg, `name="qualityProfile"`)
	requireTag(t, dlg, `data-edit-fields="comic"`, `hx-get="/library/default/comic/saga/edit/fields"`, `hx-trigger="intersect once"`, `hx-swap="outerHTML"`)
	require.NotContains(t, dlg, `name="comicKind"`, "the kind block comes when the dialog opens")

	audiobookSrv, _ := audiobookFixture(t)
	page = detailPage(t, audiobookSrv, "/library/default/audiobook/earthsea-ab")
	requireTag(t, section(t, page, `data-edit-dialog="earthsea-ab"`), `data-edit-fields="audiobook"`, `hx-get="/library/default/audiobook/earthsea-ab/edit/fields"`)

	bookSrv, _ := bookFixture(t)
	page = detailPage(t, bookSrv, "/library/default/book/standalone")
	dlg = section(t, page, `data-edit-dialog="standalone"`)
	requireTag(t, dlg, `data-edit-fields="book"`, `hx-get="/library/default/book/standalone/edit/fields"`)
	require.NotContains(t, dlg, `name="qualityProfile"`, "a book's profile is in its block, since an author's book inherits it")
	require.Contains(t, dlg, `name="monitored"`)

	movieSrv, _ := movieFixture(t, false)
	page = detailPage(t, movieSrv, "/library/default/movie/nerve")
	require.NotContains(t, section(t, page, `data-edit-dialog="nerve"`), `data-edit-fields=`)
}

// A container's Delete dialog counts the files the delete removes with the
// folder -- a series' episodes', a comic's issues' -- read through the
// reader's children indexes, and the hero shows their size.
func TestDeleteDialogCountsAContainersFiles(t *testing.T) {
	srv, _ := editFixture(t)
	page := detailPage(t, srv, "/library/default/series/andor")
	require.Regexp(t, `data-delete-files>[^<]*2 file\(s\)`, page)
	require.Contains(t, section(t, page, `data-fact="size"`), "5.00 GiB")

	comicSrv, _ := comicFixture(t)
	page = detailPage(t, comicSrv, "/library/default/comic/saga")
	require.Regexp(t, `data-delete-files>[^<]*1 file\(s\)`, page)
}
