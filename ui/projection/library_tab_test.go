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

package projection_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/projection"
)

// artworkEntry is a minimal, valid status.artwork entry for t, standing in
// for what the metadata gateway would have written (app/catalog/metadata/artwork).
func artworkEntry(t catalogv1.ImageType, digest string) catalogv1.ArtworkEntry {
	return catalogv1.ArtworkEntry{
		Type: t, Source: catalogv1.ArtworkSourceProvider,
		SourceURL: "https://img.example/" + digest, Digest: digest, SizeBytes: 1,
		UpdatedAt: metav1.Now(),
	}
}

// The library page's cards (spec 2026-09-23-library-page-design): every
// parent kind lands in one tab, carries the [projection.ArtURL] its
// status.artwork poster entry resolves to (and nothing when there is none,
// or no artwork yet), its year and its quality profile -- all read off the
// real API objects, never off a hand-shaped LibraryItem.
func TestLibraryProjectionPutsEveryParentInATabWithArtYearAndProfile(t *testing.T) {
	movie := &catalogv1.Movie{
		ObjectMeta: metav1.ObjectMeta{Name: "arrival", Namespace: "default", UID: "arrival-uid"},
		Spec:       catalogv1.MovieSpec{TmdbID: 329865, QualityProfileRef: "hd-bluray-web", RootFolderRef: "movies"},
		Status: catalogv1.MovieStatus{
			Metadata: &catalogv1.MovieMetadata{Title: "Arrival", Year: 2016},
			Artwork: []catalogv1.ArtworkEntry{
				artworkEntry(catalogv1.ImageTypeFanart, "arrival-fanart"),
				artworkEntry(catalogv1.ImageTypePoster, "arrival-poster"),
			},
		},
	}
	series := &catalogv1.Series{
		ObjectMeta: metav1.ObjectMeta{Name: "andor", Namespace: "default", UID: "andor-uid"},
		Spec:       catalogv1.SeriesSpec{TvdbID: 393189, QualityProfileRef: "web-1080p", RootFolderRef: "tv"},
		Status: catalogv1.SeriesStatus{
			Metadata: &catalogv1.SeriesMetadata{Title: "Andor", Year: 2022},
			Artwork:  []catalogv1.ArtworkEntry{artworkEntry(catalogv1.ImageTypeFanart, "andor-fanart")},
		},
	}
	pending := &catalogv1.Series{ // no metadata yet: no poster, no year
		ObjectMeta: metav1.ObjectMeta{Name: "pending", Namespace: "default", UID: "pending-uid"},
		Spec:       catalogv1.SeriesSpec{TvdbID: 1, QualityProfileRef: "web-1080p", RootFolderRef: "tv"},
	}
	artist := &catalogv1.Artist{
		ObjectMeta: metav1.ObjectMeta{Name: "bjork", Namespace: "default", UID: "bjork-uid"},
		Spec:       catalogv1.ArtistSpec{QualityProfileRef: "music-lossless"},
		Status: catalogv1.ArtistStatus{
			Metadata: &catalogv1.ArtistMetadata{Name: "Björk"},
			Artwork:  []catalogv1.ArtworkEntry{artworkEntry(catalogv1.ImageTypePoster, "bjork-poster")},
		},
	}
	author := &catalogv1.Author{
		ObjectMeta: metav1.ObjectMeta{Name: "le-guin", Namespace: "default", UID: "le-guin-uid"},
		Spec:       catalogv1.AuthorSpec{QualityProfileRef: "ebook"},
		Status:     catalogv1.AuthorStatus{Metadata: &catalogv1.AuthorMetadata{Name: "Ursula K. Le Guin"}},
	}
	standalone := &catalogv1.Book{ // no authorRef: its own parent
		ObjectMeta: metav1.ObjectMeta{Name: "lone-book", Namespace: "default", UID: "lone-book-uid"},
		Spec:       catalogv1.BookSpec{QualityProfileRef: ptr.To("ebook")},
		Status:     catalogv1.BookStatus{Metadata: &catalogv1.BookMetadata{Title: "Lone Book"}},
	}
	owned := &catalogv1.Book{ // an author's child
		ObjectMeta: metav1.ObjectMeta{Name: "dispossessed", Namespace: "default", UID: "dispossessed-uid"},
		Spec:       catalogv1.BookSpec{AuthorRef: ptr.To("le-guin"), QualityProfileRef: ptr.To("ebook")},
		Status:     catalogv1.BookStatus{Metadata: &catalogv1.BookMetadata{Title: "The Dispossessed"}},
	}
	audiobook := &catalogv1.Audiobook{
		ObjectMeta: metav1.ObjectMeta{Name: "dune-audio", Namespace: "default", UID: "dune-audio-uid"},
		Spec:       catalogv1.AudiobookSpec{QualityProfileRef: "audiobook"},
		Status:     catalogv1.AudiobookStatus{Metadata: &catalogv1.AudiobookMetadata{Title: "Dune"}},
	}
	comic := &catalogv1.Comic{
		ObjectMeta: metav1.ObjectMeta{Name: "saga", Namespace: "default", UID: "saga-uid"},
		Spec:       catalogv1.ComicSpec{QualityProfileRef: "comic"},
		Status:     catalogv1.ComicStatus{Metadata: &catalogv1.ComicMetadata{Title: "Saga", Year: 2012}},
	}
	episode := &catalogv1.Episode{
		ObjectMeta: metav1.ObjectMeta{Name: "andor-s01e01", Namespace: "default", UID: "andor-s01e01-uid"},
		Spec:       catalogv1.EpisodeSpec{SeriesRef: "andor", SeasonNumber: 1, EpisodeNumber: 1},
	}

	fakeClient := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(movie, series, pending, artist, author, standalone, owned, audiobook, comic, episode).Build()
	proj := projection.New(fakeClient, time.Hour)
	ctx := t.Context()
	go func() { _ = proj.Run(ctx) }()

	var items []projection.LibraryItem
	require.Eventually(t, func() bool {
		items = proj.Library(ctx)
		return len(items) == 8
	}, 2*time.Second, 10*time.Millisecond, "eight parents: the owned book and the episode are children")
	byName := map[string]projection.LibraryItem{}
	for _, it := range items {
		byName[it.Ref.Name] = it
	}

	m := byName["arrival"]
	require.Equal(t, projection.TabMovies, m.Tab)
	require.Equal(t, projection.ArtURL(commonv1.MediaKindMovie, "arrival-uid", catalogv1.ImageTypePoster, "arrival-poster"),
		m.Poster, "the poster entry, not the fanart one")
	require.NotContains(t, m.Poster, "http://")
	require.NotContains(t, m.Poster, "https://")
	require.EqualValues(t, 2016, m.Year)
	require.Equal(t, "hd-bluray-web", m.QualityProfileRef)

	s := byName["andor"]
	require.Equal(t, projection.TabTV, s.Tab)
	require.Empty(t, s.Poster, "fanart is not a poster")
	require.EqualValues(t, 2022, s.Year)
	require.Equal(t, "web-1080p", s.QualityProfileRef)
	require.Empty(t, byName["pending"].Poster)
	require.Zero(t, byName["pending"].Year)

	require.Equal(t, projection.TabMusic, byName["bjork"].Tab)
	require.Equal(t, projection.ArtURL(commonv1.MediaKindArtist, "bjork-uid", catalogv1.ImageTypePoster, "bjork-poster"),
		byName["bjork"].Poster)
	require.Equal(t, "music-lossless", byName["bjork"].QualityProfileRef)
	for _, name := range []string{"le-guin", "lone-book", "dune-audio", "saga"} {
		require.Equal(t, projection.TabBooks, byName[name].Tab, name)
	}
	require.EqualValues(t, 2012, byName["saga"].Year)

	tv := projection.ForTab(items, projection.TabTV)
	require.Len(t, tv, 2)
	for _, it := range tv {
		require.Equal(t, projection.TabTV, it.Tab)
	}
	require.Len(t, projection.ForTab(items, projection.TabBooks), 4)
}

// The tab in a URL is untrusted input: the four tabs parse, in the order
// the page lists them, and anything else is refused rather than treated as
// an empty tab.
func TestParseTabAcceptsTheFourTabsOnly(t *testing.T) {
	require.Equal(t, []projection.Tab{projection.TabMovies, projection.TabTV, projection.TabMusic, projection.TabBooks},
		projection.Tabs())
	for _, tab := range projection.Tabs() {
		got, ok := projection.ParseTab(string(tab))
		require.True(t, ok, tab)
		require.Equal(t, tab, got)
	}
	for _, bad := range []string{"", "series", "TV", "movies/"} {
		_, ok := projection.ParseTab(bad)
		require.False(t, ok, bad)
	}
}
