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
	require.True(t, ar.Spec.AddOptions.SearchForMissing)

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
	}
	var list catalogv1.MovieList
	require.NoError(t, c.List(t.Context(), &list))
	require.Empty(t, list.Items, "an invalid request writes nothing")
}
