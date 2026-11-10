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

package actions_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/pkg/names"
	"github.com/mediactl/clustarr/ui/actions"
)

func addClient(t *testing.T) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, catalogv1.AddToScheme(scheme))
	return fake.NewClientBuilder().WithScheme(scheme).Build()
}

func TestAddItemBuildsEachKindsSpec(t *testing.T) {
	c := addClient(t)
	ctx := t.Context()

	name, existed, err := actions.AddItem(ctx, c, actions.AddRequest{
		Kind: commonv1.MediaKindMovie, Namespace: "media", Title: "Heat", ProviderID: "949",
		RootFolderRef: "movies", QualityProfileRef: "hd", Monitored: true, Monitor: "movieOnly",
		SearchOnAdd: true, MinimumAvailability: "released",
	})
	require.NoError(t, err)
	require.False(t, existed)
	require.Equal(t, names.Movie("Heat", 949), name)
	var m catalogv1.Movie
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "media", Name: name}, &m))
	require.Equal(t, int64(949), m.Spec.TmdbID)
	require.Equal(t, "movies", m.Spec.RootFolderRef)
	require.Equal(t, "hd", m.Spec.QualityProfileRef)
	require.True(t, *m.Spec.Monitored)
	require.Equal(t, catalogv1.MovieMonitorMode("movieOnly"), m.Spec.AddOptions.Monitor)
	require.True(t, *m.Spec.AddOptions.SearchForMovie)
	require.Equal(t, catalogv1.MinimumAvailability("released"), m.Spec.MinimumAvailability)
	require.NotNil(t, m.Spec.Source)
	require.Empty(t, m.Spec.Source.ImportListRef, "added by hand")
	require.Equal(t, catalogv1.MovieStatus{}, m.Status, "the UI never writes status")

	name, _, err = actions.AddItem(ctx, c, actions.AddRequest{
		Kind: commonv1.MediaKindSeries, Namespace: "media", Title: "Breaking Bad", ProviderID: "81189",
		RootFolderRef: "tv", QualityProfileRef: "hd", Monitored: true, Monitor: "future",
		SearchOnAdd: true, SeriesType: "standard", SeasonFolder: true, SearchCutoffUnmet: true, MonitorNewItems: "all",
	})
	require.NoError(t, err)
	var s catalogv1.Series
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "media", Name: name}, &s))
	require.Equal(t, int64(81189), s.Spec.TvdbID)
	require.Equal(t, catalogv1.SeriesMonitorMode("future"), s.Spec.AddOptions.Monitor)
	require.True(t, *s.Spec.AddOptions.SearchForMissing)
	require.True(t, *s.Spec.AddOptions.SearchForCutoffUnmet)
	require.True(t, *s.Spec.SeasonFolder)
	require.Equal(t, catalogv1.SeriesType("standard"), s.Spec.SeriesType)
	require.Equal(t, catalogv1.MonitorNewChildrenMode("all"), s.Spec.MonitorNewItems)

	name, _, err = actions.AddItem(ctx, c, actions.AddRequest{
		Kind: commonv1.MediaKindArtist, Namespace: "media", Title: "Radiohead", ProviderID: "a74b1b7f-71a5-4011-9441-d0b5e4122711",
		RootFolderRef: "music", QualityProfileRef: "lossless", Monitored: true, Monitor: "all", SearchOnAdd: true, MonitorNewItems: "new",
	})
	require.NoError(t, err)
	var ar catalogv1.Artist
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "media", Name: name}, &ar))
	require.Equal(t, "a74b1b7f-71a5-4011-9441-d0b5e4122711", ar.Spec.MusicBrainzID)
	require.Equal(t, catalogv1.MonitorNewItemsMode("new"), ar.Spec.MonitorNewItems)
	require.True(t, *ar.Spec.AddOptions.SearchForMissing)

	name, _, err = actions.AddItem(ctx, c, actions.AddRequest{
		Kind: commonv1.MediaKindAuthor, Namespace: "media", Title: "Jane Austen", ProviderID: "OL21594A",
		RootFolderRef: "books", QualityProfileRef: "ebook", Monitored: false, Monitor: "none", MonitorNewItems: "none",
	})
	require.NoError(t, err)
	var au catalogv1.Author
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "media", Name: name}, &au))
	require.Equal(t, "OL21594A", au.Spec.OpenLibraryID)
	require.False(t, *au.Spec.Monitored, "false is sent, not dropped")
	require.Equal(t, catalogv1.AuthorMonitorMode("none"), au.Spec.AddOptions.Monitor)
	require.Equal(t, []string{"en"}, au.Spec.MetadataProfile.AllowedLanguages,
		"an added author lists only works with an English edition, not every translation catalogued as its own work")
}

// TestAddItemLandsOnTheExistingItem: adding what is already there -- twice
// from the UI, or after an import list added it -- is not an error and
// never a second object.
func TestAddItemLandsOnTheExistingItem(t *testing.T) {
	c := addClient(t)
	req := actions.AddRequest{Kind: commonv1.MediaKindMovie, Namespace: "media", Title: "Heat", ProviderID: "949", RootFolderRef: "movies", QualityProfileRef: "hd", Monitored: true, Monitor: "movieOnly"}
	first, existed, err := actions.AddItem(t.Context(), c, req)
	require.NoError(t, err)
	require.False(t, existed)
	second, existed, err := actions.AddItem(t.Context(), c, req)
	require.NoError(t, err)
	require.True(t, existed)
	require.Equal(t, first, second)
	var list catalogv1.MovieList
	require.NoError(t, c.List(t.Context(), &list))
	require.Len(t, list.Items, 1)
}

func TestAddItemRefusesAnIncompleteRequest(t *testing.T) {
	c := addClient(t)
	ok := actions.AddRequest{Kind: commonv1.MediaKindMovie, Namespace: "media", Title: "Heat", ProviderID: "949", RootFolderRef: "movies", QualityProfileRef: "hd"}
	for name, mutate := range map[string]func(*actions.AddRequest){
		"kind":        func(r *actions.AddRequest) { r.Kind = commonv1.MediaKindEpisode },
		"namespace":   func(r *actions.AddRequest) { r.Namespace = "" },
		"id":          func(r *actions.AddRequest) { r.ProviderID = "" },
		"numeric id":  func(r *actions.AddRequest) { r.ProviderID = "tt0113277" },
		"zero id":     func(r *actions.AddRequest) { r.ProviderID = "0" },
		"root folder": func(r *actions.AddRequest) { r.RootFolderRef = "" },
		"profile":     func(r *actions.AddRequest) { r.QualityProfileRef = "" },
	} {
		r := ok
		mutate(&r)
		_, _, err := actions.AddItem(t.Context(), c, r)
		require.ErrorIs(t, err, actions.ErrInvalid, name)
		require.ErrorIs(t, actions.Validate(r), actions.ErrInvalid, "Validate refuses what AddItem refuses: %s", name)
	}
	require.NoError(t, actions.Validate(ok), "Validate admits what AddItem adds")
	var list catalogv1.MovieList
	require.NoError(t, c.List(t.Context(), &list))
	require.Empty(t, list.Items, "an invalid request writes nothing, and Validate never writes")
}

// TestAddItemBuildsTheNonVideoKinds: the Comics and Audiobooks tabs' Add
// New and the Books tab's standalone Book (2026-11-10 design §9.1) create
// each kind with spec only, named as pkg/names names it: a comic by its
// source and id (both immutable), an audiobook by its ASIN in the root's
// region, a book by its work key with its own profile and root and no
// author.
func TestAddItemBuildsTheNonVideoKinds(t *testing.T) {
	c := addClient(t)
	ctx := t.Context()

	name, existed, err := actions.AddItem(ctx, c, actions.AddRequest{
		Kind: commonv1.MediaKindComic, Namespace: "media", Title: "Saga", ProviderID: "56907", Source: "comicvine",
		RootFolderRef: "comics", QualityProfileRef: "cbz", Monitored: true, Monitor: "missing", SearchOnAdd: true,
		ComicKind: "comic", MonitorNewItems: "none",
	})
	require.NoError(t, err)
	require.False(t, existed)
	require.Equal(t, names.Comic("Saga", "comicvine", "56907"), name)
	var co catalogv1.Comic
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "media", Name: name}, &co))
	require.Equal(t, catalogv1.ComicSourceComicVine, co.Spec.Source)
	require.Equal(t, "56907", co.Spec.SourceID)
	require.Equal(t, catalogv1.ComicKindComic, co.Spec.Kind)
	require.Equal(t, catalogv1.ComicMonitorMissing, co.Spec.AddOptions.Monitor)
	require.True(t, *co.Spec.AddOptions.SearchForMissing)
	require.NotNil(t, co.Spec.MonitorNewIssues)
	require.False(t, *co.Spec.MonitorNewIssues, "the form's monitor-new none is the flag off")
	require.Equal(t, "comics", co.Spec.RootFolderRef)
	require.Equal(t, "cbz", co.Spec.QualityProfileRef)
	require.NotNil(t, co.Spec.AddSource)
	require.Empty(t, co.Spec.AddSource.ImportListRef, "added by hand")
	require.Equal(t, catalogv1.ComicStatus{}, co.Status, "the UI never writes status")

	name, _, err = actions.AddItem(ctx, c, actions.AddRequest{
		Kind: commonv1.MediaKindComic, Namespace: "media", Title: "Berserk", ProviderID: "801513ba-a712-498c-8f57-cae55b38cc92", Source: "mangadex",
		RootFolderRef: "comics", QualityProfileRef: "cbz", Monitored: true, Monitor: "all", ComicKind: "manga",
	})
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "media", Name: name}, &co))
	require.Equal(t, catalogv1.ComicSourceMangaDex, co.Spec.Source)
	require.Equal(t, catalogv1.ComicKindManga, co.Spec.Kind)
	require.Nil(t, co.Spec.MonitorNewIssues, "unsaid, the CRD default")
	require.NotEqual(t, names.Comic("Berserk", "comicvine", "801513ba-a712-498c-8f57-cae55b38cc92"), name, "the source is part of the identity")

	name, _, err = actions.AddItem(ctx, c, actions.AddRequest{
		Kind: commonv1.MediaKindAudiobook, Namespace: "media", Title: "Dune", ProviderID: "B002V1OF70", Region: "uk",
		RootFolderRef: "audiobooks", QualityProfileRef: "m4b", Monitored: false, SearchOnAdd: true,
	})
	require.NoError(t, err)
	require.Equal(t, names.Audiobook("Dune", "B002V1OF70"), name)
	var ab catalogv1.Audiobook
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "media", Name: name}, &ab))
	require.Equal(t, "B002V1OF70", ab.Spec.ASIN)
	require.Equal(t, catalogv1.AudiobookRegionUK, ab.Spec.Region)
	require.False(t, *ab.Spec.Monitored, "false is sent, not dropped")
	require.True(t, *ab.Spec.AddOptions.SearchForMissing)
	require.Nil(t, ab.Spec.AuthorRef, "no author resolved: no authorRef at all, not an empty one")
	require.Equal(t, "audiobooks", ab.Spec.RootFolderRef)

	// The author link (design D11, §5.6): the Author the hit's first author
	// resolved to, which ui/add.go passes as AuthorRef.
	name, _, err = actions.AddItem(ctx, c, actions.AddRequest{
		Kind: commonv1.MediaKindAudiobook, Namespace: "media", Title: "Children of Dune", ProviderID: "B002V0QK4C", Region: "us",
		RootFolderRef: "audiobooks", QualityProfileRef: "m4b", Monitored: true, SearchOnAdd: false, AuthorRef: "frank-herbert-x",
	})
	require.NoError(t, err)
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "media", Name: name}, &ab))
	require.NotNil(t, ab.Spec.AuthorRef)
	require.Equal(t, "frank-herbert-x", *ab.Spec.AuthorRef)
	require.False(t, *ab.Spec.AddOptions.SearchForMissing, "false is sent, not dropped")

	name, _, err = actions.AddItem(ctx, c, actions.AddRequest{
		Kind: commonv1.MediaKindBook, Namespace: "media", Title: "Dune", ProviderID: "OL893415W",
		RootFolderRef: "books", QualityProfileRef: "ebook", Monitored: true, SearchOnAdd: true,
	})
	require.NoError(t, err)
	require.Equal(t, names.Book("Dune", "OL893415W"), name)
	var bk catalogv1.Book
	require.NoError(t, c.Get(ctx, client.ObjectKey{Namespace: "media", Name: name}, &bk))
	require.Equal(t, "OL893415W", bk.Spec.WorkID)
	require.Nil(t, bk.Spec.AuthorRef, "a standalone book")
	require.Equal(t, "ebook", *bk.Spec.QualityProfileRef)
	require.Equal(t, "books", *bk.Spec.RootFolderRef)
	require.True(t, *bk.Spec.Monitored)
}

// A comic without a known source, or a book whose id is no work key, is
// ErrInvalid before any request: the apiserver would refuse them anyway,
// and the page should say which field.
func TestAddItemRefusesAComicWithoutASourceAndABookWithoutAWorkKey(t *testing.T) {
	c := addClient(t)
	for name, req := range map[string]actions.AddRequest{
		"no source":      {Kind: commonv1.MediaKindComic, Namespace: "media", Title: "Saga", ProviderID: "1", RootFolderRef: "r", QualityProfileRef: "p"},
		"unknown source": {Kind: commonv1.MediaKindComic, Namespace: "media", Title: "Saga", ProviderID: "1", Source: "metron", RootFolderRef: "r", QualityProfileRef: "p"},
		"an author key":  {Kind: commonv1.MediaKindBook, Namespace: "media", Title: "Dune", ProviderID: "OL21594A", RootFolderRef: "r", QualityProfileRef: "p"},
	} {
		_, _, err := actions.AddItem(t.Context(), c, req)
		require.ErrorIs(t, err, actions.ErrInvalid, name)
	}
	var comics catalogv1.ComicList
	require.NoError(t, c.List(t.Context(), &comics))
	require.Empty(t, comics.Items)
	var books catalogv1.BookList
	require.NoError(t, c.List(t.Context(), &books))
	require.Empty(t, books.Items)
}

// AddableKinds is what Grants derives the create verbs from: every kind an
// Add New page creates, and each one AddItem builds.
func TestAddableKindsAreTheOnesAddNewCreates(t *testing.T) {
	require.ElementsMatch(t, []commonv1.MediaKind{
		commonv1.MediaKindMovie, commonv1.MediaKindSeries, commonv1.MediaKindArtist, commonv1.MediaKindAuthor,
		commonv1.MediaKindComic, commonv1.MediaKindAudiobook, commonv1.MediaKindBook,
	}, actions.AddableKinds)
	for _, g := range []actions.Grant{
		{Group: "catalog.clustarr.io", Resource: "comics", Verb: "create"},
		{Group: "catalog.clustarr.io", Resource: "audiobooks", Verb: "create"},
		{Group: "catalog.clustarr.io", Resource: "books", Verb: "create"},
	} {
		require.Contains(t, actions.Grants(), g)
	}
}
