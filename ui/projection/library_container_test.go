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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	catalogv1 "github.com/mediactl/clustarr/api/catalog/v1alpha1"
	commonv1 "github.com/mediactl/clustarr/api/common/v1alpha1"
	"github.com/mediactl/clustarr/ui/projection"
)

// A container's card -- an artist's, an author's, a comic's -- reads its
// status from the rollup of its children (2026-11-10 design §9.1), as a
// series' does from its episodes: Downloading while a child's grab is in
// flight, Missing while a monitored, released child has no file, Cutoff
// Unmet while a child's file is below the cutoff and nothing is missing,
// Downloaded once files are on disk and nothing is wanted, and Unmonitored
// with nothing on disk and nothing wanted. Before, the leaf rule read every
// monitored container as Missing, the way it read every series until
// 2026-09-30.
func TestContainerCardsReadTheirStatusFromTheirChildren(t *testing.T) {
	for _, kind := range []commonv1.MediaKind{commonv1.MediaKindArtist, commonv1.MediaKindAuthor, commonv1.MediaKindComic} {
		for _, tc := range []struct {
			name                              string
			hasFile                           bool
			missing, downloading, cutoffUnmet int32
			monitored                         bool
			want                              string
		}{
			{"complete", true, 0, 0, 0, true, "downloaded"},
			{"gaps", true, 3, 0, 0, true, "missing"},
			{"grabbing", true, 2, 1, 0, true, "downloading"},
			{"below cutoff", true, 0, 0, 2, true, "cutoff-unmet"},
			{"below cutoff but missing first", true, 1, 0, 2, true, "missing"},
			{"quiet", false, 0, 0, 0, true, "unmonitored"},
			{"nothing yet and unmonitored", false, 0, 0, 0, false, "unmonitored"},
			{"missing children of a monitored container", false, 4, 0, 0, true, "missing"},
		} {
			li := projection.LibraryItem{
				Ref: types.NamespacedName{Namespace: "media", Name: tc.name}, Kind: kind, Monitored: tc.monitored,
				Phase: "Ready", HasFile: tc.hasFile,
				MissingChildren: tc.missing, DownloadingChildren: tc.downloading, CutoffUnmetChildren: tc.cutoffUnmet,
			}
			require.Equal(t, tc.want, projection.LibraryStatus(li), "%s %s", kind, tc.name)
			// The presets read the same rollup, so Missing, Wanted,
			// Cutoff Unmet and Downloading match containers.
			require.Equal(t, tc.missing > 0 || tc.downloading > 0, projection.FilterMissing.Matches(li), "%s %s: Missing", kind, tc.name)
			require.Equal(t, tc.missing > 0 || tc.downloading > 0, projection.FilterWanted.Matches(li), "%s %s: Wanted", kind, tc.name)
			require.Equal(t, tc.want == "cutoff-unmet", projection.FilterCutoffUnmet.Matches(li), "%s %s: Cutoff Unmet", kind, tc.name)
			require.Equal(t, tc.want == "downloading", projection.FilterDownloading.Matches(li), "%s %s: Downloading", kind, tc.name)
		}
		// A pending delete still reads first.
		require.Equal(t, "deleting", projection.LibraryStatus(projection.LibraryItem{Kind: kind, DeleteMode: "files", MissingChildren: 1}))
	}
	// A leaf with the same counts set is not a container: its own rule holds.
	require.Equal(t, "downloaded", projection.LibraryStatus(projection.LibraryItem{Kind: commonv1.MediaKindMovie, HasFile: true, Phase: "Imported", MissingChildren: 3}))
	for _, kind := range []commonv1.MediaKind{commonv1.MediaKindMovie, commonv1.MediaKindSeries, commonv1.MediaKindBook, commonv1.MediaKindAudiobook, commonv1.MediaKindAlbum} {
		require.False(t, projection.IsContainer(kind), kind)
	}
}

// The rollup's counts reach the card from the real objects: a container's
// ContainerRollup is what describeLibraryItem reads, so an Artist with
// missing albums is Missing on the Music tab and a Comic with an issue
// downloading is Downloading on the Comics tab, while an Author whose every
// book is on disk is Downloaded.
func TestContainerRollupsReachTheLibraryCards(t *testing.T) {
	next := metav1.NewTime(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC))
	objs := []client.Object{
		&catalogv1.Artist{
			ObjectMeta: metav1.ObjectMeta{Name: "rh", Namespace: "media", UID: "rh-uid"},
			Spec:       catalogv1.ArtistSpec{MusicBrainzID: "a74b1b7f-71a5-4011-9441-d0b5e4122711", QualityProfileRef: "music"},
			Status: catalogv1.ArtistStatus{
				ContainerRollup: catalogv1.ContainerRollup{Phase: catalogv1.ContainerPhaseReady, MissingChildCount: 2, NextReleaseDate: &next},
				AlbumFileCount:  5,
				Metadata:        &catalogv1.ArtistMetadata{Name: "Radiohead", Status: catalogv1.ArtistRunStatusContinuing, Genres: []string{"Rock"}},
			},
		},
		&catalogv1.Author{
			ObjectMeta: metav1.ObjectMeta{Name: "ja", Namespace: "media", UID: "ja-uid"},
			Spec:       catalogv1.AuthorSpec{OpenLibraryID: "OL21594A", QualityProfileRef: "ebook"},
			Status: catalogv1.AuthorStatus{
				ContainerRollup: catalogv1.ContainerRollup{Phase: catalogv1.ContainerPhaseReady},
				BookFileCount:   6,
				Metadata:        &catalogv1.AuthorMetadata{Name: "Jane Austen"},
			},
		},
		&catalogv1.Comic{
			ObjectMeta: metav1.ObjectMeta{Name: "saga", Namespace: "media", UID: "saga-uid"},
			Spec:       catalogv1.ComicSpec{Source: catalogv1.ComicSourceComicVine, SourceID: "4050-18166", QualityProfileRef: "comic"},
			Status: catalogv1.ComicStatus{
				ContainerRollup: catalogv1.ContainerRollup{Phase: catalogv1.ContainerPhaseReady, MissingChildCount: 1, DownloadingChildCount: 1, CutoffUnmetChildCount: 3},
				IssueFileCount:  60,
				Metadata:        &catalogv1.ComicMetadata{Title: "Saga", Year: 2012, Publisher: "Image", AgeRating: "Mature", Status: catalogv1.ComicRunStatusContinuing},
			},
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
	proj := projection.New(fakeClient, time.Hour)
	ctx := t.Context()
	go func() { _ = proj.Run(ctx) }()

	var items []projection.LibraryItem
	require.Eventually(t, func() bool {
		items = proj.Library(ctx)
		return len(items) == len(objs)
	}, 2*time.Second, 10*time.Millisecond)
	byName := map[string]projection.LibraryItem{}
	for _, it := range items {
		byName[it.Ref.Name] = it
	}
	rh := byName["rh"]
	require.Equal(t, "missing", projection.LibraryStatus(rh))
	require.Equal(t, "Ready", rh.Phase)
	require.EqualValues(t, 2, rh.MissingChildren)
	require.True(t, rh.NextAiring.Equal(next.Time), "the rollup's next release stands where a series' next airing does: %s", rh.NextAiring)
	require.Equal(t, "continuing", rh.ReleaseStatus, "the Continuing Only preset reads an artist's run status")
	require.True(t, projection.FilterContinuing.Matches(rh))
	require.Equal(t, []string{"Rock"}, rh.Genres)
	require.Equal(t, "downloaded", projection.LibraryStatus(byName["ja"]))
	saga := byName["saga"]
	require.Equal(t, "downloading", projection.LibraryStatus(saga))
	require.EqualValues(t, 3, saga.CutoffUnmetChildren)
	require.Equal(t, []string{"Image"}, saga.Studios, "a publisher stands where a studio does")
	require.Equal(t, "Mature", saga.Certification, "an age rating stands where a certification does")
}

// The new tabs' menus (2026-11-10 design §9.1): each has the shared sorts
// and presets plus what its items carry -- a release date where the item
// has one, the rollup's next and previous release for a container, an
// audiobook's runtime, a run status for an artist or a comic -- and parses
// only its own.
func TestTheNonVideoTabsHaveTheirOwnSortsAndFilters(t *testing.T) {
	for tab, sorts := range map[projection.Tab][]projection.LibrarySort{
		projection.TabMusic:      {projection.SortNextRelease, projection.SortPreviousRelease, projection.SortRating},
		projection.TabBooks:      {projection.SortReleased, projection.SortNextRelease, projection.SortPreviousRelease, projection.SortRating},
		projection.TabComics:     {projection.SortNextRelease, projection.SortPreviousRelease, projection.SortRating},
		projection.TabAudiobooks: {projection.SortReleased, projection.SortRuntime, projection.SortRating},
	} {
		want := append([]projection.LibrarySort{
			projection.SortTitle, projection.SortYear, projection.SortProfile, projection.SortStatus, projection.SortAdded,
		}, sorts...)
		require.Equal(t, want, projection.LibrarySorts(tab), tab)
		for _, s := range sorts {
			got, ok := projection.ParseLibrarySort(tab, string(s))
			require.True(t, ok, "%s parses %s", tab, s)
			require.Equal(t, s, got)
		}
	}
	_, ok := projection.ParseLibrarySort(projection.TabMusic, string(projection.SortEpisodes))
	require.False(t, ok, "a music library has no episode progress")
	require.Equal(t, "Next Release", projection.SortNextRelease.Label())
	require.Equal(t, "Previous Release", projection.SortPreviousRelease.Label())

	for _, tab := range []projection.Tab{projection.TabMusic, projection.TabComics} {
		for _, f := range []projection.LibraryFilter{projection.FilterContinuing, projection.FilterEnded} {
			_, ok := projection.ParseLibraryFilter(tab, string(f))
			require.True(t, ok, "%s parses %s", tab, f)
		}
	}
	for _, tab := range []projection.Tab{projection.TabBooks, projection.TabAudiobooks} {
		_, ok := projection.ParseLibraryFilter(tab, string(projection.FilterEnded))
		require.False(t, ok, "%s has no run status", tab)
	}

	// A book's release date sorts as a movie's does, under the one key.
	released := time.Date(2020, 5, 1, 0, 0, 0, 0, time.UTC)
	books := []projection.LibraryItem{
		{Ref: types.NamespacedName{Name: "later"}, Kind: commonv1.MediaKindBook, Tab: projection.TabBooks, Title: "Later", Released: released.AddDate(1, 0, 0)},
		{Ref: types.NamespacedName{Name: "undated"}, Kind: commonv1.MediaKindBook, Tab: projection.TabBooks, Title: "Undated"},
		{Ref: types.NamespacedName{Name: "earlier"}, Kind: commonv1.MediaKindBook, Tab: projection.TabBooks, Title: "Earlier", Released: released},
	}
	got := projection.Arrange(books, projection.LibraryQuery{Tab: projection.TabBooks, Sort: projection.SortReleased, Filter: projection.FilterAll})
	require.Equal(t, []string{"Earlier", "Later", "Undated"}, titles(got), "an unknown date sorts last")
}

// The custom filter's fields are scoped to the tabs whose items carry them
// (2026-11-10 design §9.1): no video-only field leaks onto a non-video tab,
// and each new tab offers what its items have.
func TestCustomFilterFieldsAreScopedToTheirTabs(t *testing.T) {
	keys := func(tab projection.Tab) map[string]bool {
		out := map[string]bool{}
		for _, f := range projection.LibraryFields(tab) {
			out[f.Key] = true
		}
		return out
	}
	for _, tab := range []projection.Tab{projection.TabMusic, projection.TabBooks, projection.TabComics, projection.TabAudiobooks} {
		k := keys(tab)
		for _, videoOnly := range []string{"size", "videoCodec", "hdr", "audioLanguages", "transcoded", "ratingTmdb", "ratingImdb", "studio", "network", "seasonCount", "episodeProgress", "inCinemas", "seriesType"} {
			require.False(t, k[videoOnly], "%s offers %s", tab, videoOnly)
		}
		for _, shared := range []string{"monitored", "status", "hasFile", "title", "year", "added", "profile", "rootFolder", "path", "tags", "genres"} {
			require.True(t, k[shared], "%s lacks %s", tab, shared)
		}
	}
	require.True(t, keys(projection.TabMusic)["runStatus"])
	require.True(t, keys(projection.TabMusic)["missingChildren"])
	require.True(t, keys(projection.TabMusic)["countries"])
	require.True(t, keys(projection.TabBooks)["released"])
	require.True(t, keys(projection.TabBooks)["publisher"])
	require.True(t, keys(projection.TabBooks)["keywords"], "a book's subjects are its keywords")
	require.True(t, keys(projection.TabComics)["certification"], "a comic's age rating")
	require.True(t, keys(projection.TabComics)["runStatus"])
	require.True(t, keys(projection.TabAudiobooks)["runtime"])
	require.True(t, keys(projection.TabAudiobooks)["originalLanguage"])
	require.True(t, keys(projection.TabAudiobooks)["released"])
	require.False(t, keys(projection.TabAudiobooks)["missingChildren"], "an audiobook has no children")
	require.False(t, keys(projection.TabMovies)["publisher"])
	require.True(t, keys(projection.TabMovies)["studio"])

	// A container's missing-items condition reads the rollup.
	c, ok := projection.NewCondition(projection.TabMusic, "missingChildren", projection.OpGt, []string{"0"})
	require.True(t, ok)
	require.True(t, c.Matches(projection.TabMusic, projection.LibraryItem{Kind: commonv1.MediaKindArtist, MissingChildren: 2}, time.Now()))
	require.False(t, c.Matches(projection.TabMusic, projection.LibraryItem{Kind: commonv1.MediaKindArtist}, time.Now()))
}
